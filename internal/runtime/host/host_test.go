package host

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thewelshrich/schooner/internal/config"
	"github.com/thewelshrich/schooner/internal/repository"
	hostruntime "github.com/thewelshrich/schooner/internal/runtime"
)

func TestRuntimeHelloInspectAndDoctor(t *testing.T) {
	home := t.TempDir()
	configurationPath := filepath.Join(home, "config.toml")
	t.Setenv("SCHOONER_CONFIG", configurationPath)
	identity := "11111111-1111-4111-8111-111111111111"
	identityPath := filepath.Join(home, ".local", "state", "schooner")
	if err := os.MkdirAll(identityPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(identityPath, "identity"), []byte(identity+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(home, "schooner")
	if err := os.Mkdir(worktree, 0o700); err != nil {
		t.Fatal(err)
	}

	runtime := New(hostruntime.BuildInfo{Version: "v1.2.3", Commit: "abc123"})
	runtime.operatingSystem = "linux"
	runtime.architecture = "arm64"
	runtime.home = func() (string, error) { return home, nil }
	runtime.readFile = func(name string) ([]byte, error) {
		if name == "/etc/os-release" {
			return []byte("ID=ubuntu\nVERSION_ID=\"24.04\"\n"), nil
		}
		return os.ReadFile(name)
	}
	runtime.lookPath = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	runtime.run = func(_ context.Context, executable string, arguments ...string) (string, error) {
		switch filepath.Base(executable) {
		case "git":
			return "git version 2.43.0\n", nil
		case "tmux":
			return "tmux 3.4\n", nil
		default:
			return "", nil
		}
	}

	hello, err := runtime.Hello()
	if err != nil || hello.BoxIdentity != identity || hello.OS != "linux" || hello.Architecture != "arm64" {
		t.Fatalf("Hello() = %+v, %v", hello, err)
	}
	if got, identityErr := runtime.operationIdentity(identity); identityErr != nil || got != identity {
		t.Fatalf("operationIdentity() = %q, %v", got, identityErr)
	}
	if _, identityErr := runtime.operationIdentity("22222222-2222-4222-8222-222222222222"); hostruntime.ErrorCode(identityErr) != hostruntime.CodeInvalidIdentity {
		t.Fatalf("operation identity mismatch = %v", identityErr)
	}
	expectedWorktree, err := filepath.EvalSymlinks(worktree)
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := runtime.Inspect(t.Context(), hostruntime.NewInspectRequest("~/schooner"))
	if err != nil || inspection.OSID != "ubuntu" || inspection.WorktreeRoot != expectedWorktree || !inspection.Git.Available || !inspection.PasswordlessSudo {
		t.Fatalf("Inspect() = %+v, %v", inspection, err)
	}
	configuredRoot := filepath.Join(home, "configured-worktrees")
	if err = os.Mkdir(configuredRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = config.Write(configurationPath, config.Host{WorktreeRoot: configuredRoot}); err != nil {
		t.Fatal(err)
	}
	configuredRoot, err = filepath.EvalSymlinks(configuredRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, lifecycleErr := runtime.lifecycle(identity, expectedWorktree, false, nil); hostruntime.ErrorCode(lifecycleErr) != hostruntime.CodeConflict {
		t.Fatalf("lifecycle Worktree Root drift error = %v", lifecycleErr)
	}
	inspection, err = runtime.Inspect(t.Context(), hostruntime.NewInspectRequest(expectedWorktree))
	if err != nil || inspection.WorktreeRoot != configuredRoot || !inspection.WorktreeRootExists {
		t.Fatalf("configured Inspect() = %+v, %v", inspection, err)
	}
	report, err := runtime.Doctor(t.Context(), hostruntime.NewInspectRequest("~/schooner"))
	if err != nil || !report.Healthy || len(report.Checks) != 6 {
		t.Fatalf("Doctor() = %+v, %v", report, err)
	}

	for canceledProbe := 1; canceledProbe <= 3; canceledProbe++ {
		ctx, cancel := context.WithCancel(t.Context())
		calls := 0
		runtime.run = func(ctx context.Context, executable string, arguments ...string) (string, error) {
			calls++
			if calls == canceledProbe {
				cancel()
				return "", ctx.Err()
			}
			switch filepath.Base(executable) {
			case "git":
				return "git version 2.43.0\n", nil
			case "tmux":
				return "tmux 3.4\n", nil
			default:
				return "", nil
			}
		}
		if _, err = runtime.Inspect(ctx, hostruntime.NewInspectRequest("~/schooner")); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation during probe %d returned %v", canceledProbe, err)
		}
	}

	ctx, cancel := context.WithCancel(t.Context())
	runtime.run = func(ctx context.Context, _ string, _ ...string) (string, error) {
		cancel()
		return "", ctx.Err()
	}
	if _, err = runtime.Doctor(ctx, hostruntime.NewInspectRequest("~/schooner")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Doctor cancellation returned %v", err)
	}
}

func TestWorkspacePullManifestContinuationRejectsSourceDrift(t *testing.T) {
	home := t.TempDir()
	configurationPath := filepath.Join(home, "config.toml")
	t.Setenv("SCHOONER_CONFIG", configurationPath)
	identity := "11111111-1111-4111-8111-111111111111"
	identityDirectory := filepath.Join(home, ".local", "state", "schooner")
	if err := os.MkdirAll(identityDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(identityDirectory, "identity"), []byte(identity+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(home, "worktrees")
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, arguments := range [][]string{{"init", repo}, {"-C", repo, "config", "user.name", "Test"}, {"-C", repo, "config", "user.email", "test@example.com"}} {
		if output, err := exec.Command("git", arguments...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", arguments, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, arguments := range [][]string{{"-C", repo, "add", "file.txt"}, {"-C", repo, "commit", "-m", "base"}} {
		if output, err := exec.Command("git", arguments...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", arguments, err, output)
		}
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	canonicalRepo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err = config.Write(configurationPath, config.Host{WorktreeRoot: canonicalRoot}); err != nil {
		t.Fatal(err)
	}
	runtime := New(hostruntime.BuildInfo{Version: "dev"})
	runtime.home = func() (string, error) { return home, nil }
	localHead := strings.Repeat("a", 40)
	first, err := runtime.InspectWorkspacePull(t.Context(), hostruntime.NewWorkspacePullInspectRequest(canonicalRepo, localHead, identity, true))
	if err != nil || len(first.Manifest) == 0 {
		t.Fatalf("first inspection = %+v, err=%v", first, err)
	}
	if err = os.WriteFile(filepath.Join(repo, "file.txt"), []byte("after\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	continuation := hostruntime.NewWorkspacePullInspectRequest(canonicalRepo, localHead, identity, true)
	continuation.ManifestOffset = 1
	continuation.ExpectedSourceRevalidationDigest = first.State.RevalidationDigest
	if _, err = runtime.InspectWorkspacePull(t.Context(), continuation); repository.ErrorCode(err) != repository.CodeConflict {
		t.Fatalf("source drift error = %v", err)
	}
}

func TestCleanupPullStagingRemovesOnlyOldOwnedPayloads(t *testing.T) {
	directory := t.TempDir()
	old := filepath.Join(directory, ".pull-"+strings.Repeat("a", 32)+".tar")
	recent := filepath.Join(directory, ".pull-"+strings.Repeat("b", 32)+".tar")
	unrelated := filepath.Join(directory, "keep.tar")
	for _, path := range []string{old, recent, unrelated} {
		if err := os.WriteFile(path, []byte("payload"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	if err := os.Chtimes(old, now.Add(-48*time.Hour), now.Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	cleanupPullStaging(directory, now.Add(-24*time.Hour))
	if _, err := os.Stat(old); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old payload remains: %v", err)
	}
	for _, path := range []string{recent, unrelated} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("preserved file %q: %v", path, err)
		}
	}
}

func TestWorkspacePushRejectsDestinationThatAppearedAfterPreflight(t *testing.T) {
	home := t.TempDir()
	identity := "11111111-1111-4111-8111-111111111111"
	identityDirectory := filepath.Join(home, ".local", "state", "schooner")
	if err := os.MkdirAll(identityDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(identityDirectory, "identity"), []byte(identity+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(home, "worktrees")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(home, "source")
	for _, arguments := range [][]string{{"init", source}, {"-C", source, "config", "user.name", "Test"}, {"-C", source, "config", "user.email", "test@example.com"}} {
		if output, err := exec.Command("git", arguments...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", arguments, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(source, "file.txt"), []byte("source\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, arguments := range [][]string{{"-C", source, "add", "file.txt"}, {"-C", source, "commit", "-m", "base"}} {
		if output, err := exec.Command("git", arguments...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", arguments, err, output)
		}
	}
	canonicalSource, err := filepath.EvalSymlinks(source)
	if err != nil {
		t.Fatal(err)
	}
	capture, err := repository.CaptureCheckout(t.Context(), canonicalSource, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer capture.Release()
	target := filepath.Join(root, "repo")
	if output, cloneErr := exec.Command("git", "clone", canonicalSource, target).CombinedOutput(); cloneErr != nil {
		t.Fatalf("git clone: %v\n%s", cloneErr, output)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	canonicalTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	configurationPath := filepath.Join(home, "config.toml")
	t.Setenv("SCHOONER_CONFIG", configurationPath)
	if err = config.Write(configurationPath, config.Host{WorktreeRoot: canonicalRoot}); err != nil {
		t.Fatal(err)
	}
	runtime := New(hostruntime.BuildInfo{Version: "dev"})
	runtime.home = func() (string, error) { return home, nil }
	payload, err := os.Open(capture.PayloadPath)
	if err != nil {
		t.Fatal(err)
	}
	defer payload.Close()
	request := hostruntime.NewWorkspacePushApplyRequest(strings.Repeat("c", 32), canonicalTarget, "", capture.PayloadSHA256, capture.PayloadSize, capture.State.Digest, identity)
	if _, err = runtime.ApplyWorkspacePush(t.Context(), request, payload); repository.ErrorCode(err) != repository.CodeConflict {
		t.Fatalf("appeared destination error = %v", err)
	}
}

func TestCurrentHomeIgnoresOverriddenHOME(t *testing.T) {
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	home, err := currentHome()
	if err != nil {
		t.Fatal(err)
	}
	if home != filepath.Clean(current.HomeDir) {
		t.Fatalf("home = %q, account home = %q", home, current.HomeDir)
	}
}
