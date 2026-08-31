package boxtarget

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thewelshrich/schooner/internal/box"
	"github.com/thewelshrich/schooner/internal/config"
	"github.com/thewelshrich/schooner/internal/repository"
	hostruntime "github.com/thewelshrich/schooner/internal/runtime"
	"github.com/thewelshrich/schooner/internal/runtime/host"
	"github.com/thewelshrich/schooner/internal/runtime/ssh"
	"github.com/thewelshrich/schooner/internal/source"
	"github.com/thewelshrich/schooner/internal/workspacetransfer"
)

const conformanceSourceKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAABAgMEBQYHCAkKCwwNDg8QERITFBUWFxgZGhscHR4f"

func TestDirectAdapterWorkspacePushRoundTrip(t *testing.T) {
	target := directConformanceTarget(t)
	sourcePath := filepath.Join(t.TempDir(), "source")
	if output, err := exec.Command("git", "init", sourcePath).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, output)
	}
	for _, arguments := range [][]string{{"-C", sourcePath, "config", "user.name", "Test"}, {"-C", sourcePath, "config", "user.email", "test@example.com"}} {
		if output, err := exec.Command("git", arguments...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", arguments, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(sourcePath, "file.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, arguments := range [][]string{{"-C", sourcePath, "add", "file.txt"}, {"-C", sourcePath, "commit", "-m", "base"}} {
		if output, err := exec.Command("git", arguments...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", arguments, err, output)
		}
	}
	canonical, err := filepath.EvalSymlinks(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(target.WorktreeRoot(), "pushed")
	result, err := workspacetransfer.Push(t.Context(), workspacetransfer.PushRequest{LocalWorktree: canonical, RemoteWorktree: destination, Staging: t.TempDir(), Remote: target})
	if err != nil || result.Action != workspacetransfer.ActionPushed {
		t.Fatalf("push = %+v, %v", result, err)
	}
	observed, err := repository.ObserveCheckout(t.Context(), destination)
	if err != nil || observed.Digest != result.Source.Digest {
		t.Fatalf("destination = %+v, %v", observed, err)
	}
	if err = os.WriteFile(filepath.Join(canonical, "file.txt"), []byte("updated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err = workspacetransfer.Push(t.Context(), workspacetransfer.PushRequest{LocalWorktree: canonical, RemoteWorktree: destination, Staging: t.TempDir(), Remote: target})
	if err != nil || result.Action != workspacetransfer.ActionPushed || result.FilesChanged != 1 {
		t.Fatalf("subsequent push = %+v, %v", result, err)
	}
	result, err = workspacetransfer.Push(t.Context(), workspacetransfer.PushRequest{LocalWorktree: canonical, RemoteWorktree: destination, Staging: t.TempDir(), Remote: target})
	if err != nil || result.Action != workspacetransfer.ActionNoChange {
		t.Fatalf("no-op push = %+v, %v", result, err)
	}
}

func TestDirectAdapterWorkspacePullRoundTrip(t *testing.T) {
	target := directConformanceTarget(t)
	remote := filepath.Join(target.WorktreeRoot(), "repo")
	for _, arguments := range [][]string{{"-C", remote, "config", "user.name", "Test"}, {"-C", remote, "config", "user.email", "test@example.com"}} {
		if output, err := exec.Command("git", arguments...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", arguments, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(remote, "file.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, arguments := range [][]string{{"-C", remote, "add", "file.txt"}, {"-C", remote, "commit", "-m", "base"}} {
		if output, err := exec.Command("git", arguments...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", arguments, err, output)
		}
	}
	local := filepath.Join(t.TempDir(), "local")
	if output, err := exec.Command("git", "clone", remote, local).CombinedOutput(); err != nil {
		t.Fatalf("git clone: %v\n%s", err, output)
	}
	local, err := filepath.EvalSymlinks(local)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(remote, "file.txt"), []byte("remote committed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(remote, "untracked.txt"), []byte("remote dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, arguments := range [][]string{{"-C", remote, "add", "file.txt"}, {"-C", remote, "commit", "-m", "remote"}} {
		if output, err := exec.Command("git", arguments...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", arguments, err, output)
		}
	}
	result, err := workspacetransfer.Pull(t.Context(), workspacetransfer.PullRequest{
		LocalWorktree: local, RemoteWorktree: remote, Staging: t.TempDir(), LockStateDirectory: t.TempDir(), Source: target,
	})
	if err != nil || result.Action != workspacetransfer.ActionPulled || result.BytesTransferred == 0 {
		t.Fatalf("pull = %+v, %v", result, err)
	}
	remoteState, err := repository.ObserveCheckout(t.Context(), remote)
	if err != nil {
		t.Fatal(err)
	}
	localState, err := repository.ObserveCheckout(t.Context(), local)
	if err != nil || localState.Digest != remoteState.Digest {
		t.Fatalf("local = %+v, remote = %+v, err = %v", localState, remoteState, err)
	}
	dryRun, err := workspacetransfer.Pull(t.Context(), workspacetransfer.PullRequest{
		LocalWorktree: local, RemoteWorktree: remote, Staging: filepath.Join(t.TempDir(), "unused"), DryRun: true, Source: target,
	})
	if err != nil || dryRun.Action != workspacetransfer.ActionNoChange {
		t.Fatalf("no-op pull = %+v, %v", dryRun, err)
	}
}

func TestDirectAdapterOperationCreatedCloneCanOverlayBehindSource(t *testing.T) {
	target := directConformanceTarget(t)
	sourcePath := filepath.Join(t.TempDir(), "source")
	if output, err := exec.Command("git", "init", sourcePath).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, output)
	}
	for _, arguments := range [][]string{{"-C", sourcePath, "config", "user.name", "Test"}, {"-C", sourcePath, "config", "user.email", "test@example.com"}} {
		if output, err := exec.Command("git", arguments...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", arguments, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(sourcePath, "file.txt"), []byte("local base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, arguments := range [][]string{{"-C", sourcePath, "add", "file.txt"}, {"-C", sourcePath, "commit", "-m", "local base"}} {
		if output, err := exec.Command("git", arguments...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", arguments, err, output)
		}
	}
	sourcePath, err := filepath.EvalSymlinks(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(target.WorktreeRoot(), "operation-created")
	if output, err := exec.Command("git", "clone", sourcePath, destination).CombinedOutput(); err != nil {
		t.Fatalf("git clone: %v\n%s", err, output)
	}
	for _, arguments := range [][]string{{"-C", destination, "config", "user.name", "Test"}, {"-C", destination, "config", "user.email", "test@example.com"}} {
		if output, err := exec.Command("git", arguments...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", arguments, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(destination, "published.txt"), []byte("newer clone seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, arguments := range [][]string{{"-C", destination, "add", "published.txt"}, {"-C", destination, "commit", "-m", "newer clone seed"}} {
		if output, err := exec.Command("git", arguments...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", arguments, err, output)
		}
	}
	seed, err := repository.ObserveCheckout(t.Context(), destination)
	if err != nil {
		t.Fatal(err)
	}
	result, err := workspacetransfer.Push(t.Context(), workspacetransfer.PushRequest{
		LocalWorktree: sourcePath, RemoteWorktree: destination, Staging: t.TempDir(), Remote: target, CreatedDestination: &seed,
	})
	if err != nil || result.Action != workspacetransfer.ActionPushed || !result.Created {
		t.Fatalf("operation-created clone push = %+v, %v", result, err)
	}
	observed, err := repository.ObserveCheckout(t.Context(), destination)
	if err != nil || observed.Digest != result.Source.Digest {
		t.Fatalf("destination = %+v, %v", observed, err)
	}
}

func TestDirectAndSSHAdaptersConformForUnsupportedCheckout(t *testing.T) {
	direct := directConformanceTarget(t)
	remote := sshConformanceTarget(t)
	for _, harness := range []struct {
		name   string
		target Target
	}{{name: "direct", target: direct}, {name: "ssh", target: remote}} {
		t.Run(harness.name, func(t *testing.T) {
			_, err := harness.target.ObservePushDestination(t.Context(), filepath.Join(harness.target.WorktreeRoot(), "repo"))
			if code := box.ErrorCode(err); code != string(repository.CodeUnsupported) {
				t.Fatalf("error = %v, code = %q", err, code)
			}
		})
	}
}

func TestDirectAndSSHAdaptersConformForWorktreeObservation(t *testing.T) {
	direct := directConformanceTarget(t)
	remote := sshConformanceTarget(t)
	for _, harness := range []struct {
		name string
		root string
		make func() Target
	}{
		{name: "direct", root: direct.state.worktreeRoot, make: func() Target { return direct }},
		{name: "ssh", root: remote.state.worktreeRoot, make: func() Target { return remote }},
	} {
		t.Run(harness.name, func(t *testing.T) {
			target := harness.make()
			catalog, err := target.ListWorktrees(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if catalog.WorktreeRoot != harness.root || len(catalog.Repositories) != 1 || catalog.Repositories[0].Primary == nil || catalog.Repositories[0].Primary.RelativePath != "repo" {
				t.Fatalf("catalog = %+v", catalog)
			}
			inspection, err := target.InspectWorktree(t.Context(), "repo")
			if err != nil || inspection.Worktree.RelativePath != "repo" || inspection.WorktreeRoot != harness.root {
				t.Fatalf("inspection = %+v, error = %v", inspection, err)
			}
			if _, err = target.InspectWorktree(t.Context(), "../outside"); box.ErrorCode(err) != "invalid_input" {
				t.Fatalf("invalid selector error = %v, code = %s", err, box.ErrorCode(err))
			}
			sessions, err := target.ListSessions(t.Context())
			if err != nil || sessions.WorktreeRoot != harness.root || len(sessions.Sessions) != 0 {
				t.Fatalf("Sessions = %+v, error = %v", sessions, err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if _, err = target.ListWorktrees(ctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation error = %v", err)
			}
			changed := *target.state
			changed.worktreeRoot += "-changed"
			if _, err = (Target{state: &changed}).ListWorktrees(t.Context()); box.ErrorCode(err) != "conflict" {
				t.Fatalf("root drift error = %v, code = %s", err, box.ErrorCode(err))
			}
		})
	}
}

func TestDirectAndSSHAdaptersConformForSourceIdentityLifecycle(t *testing.T) {
	direct := directConformanceTarget(t)
	bin := filepath.Clean(filepath.SplitList(os.Getenv("PATH"))[0])
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte("#!/bin/sh\nprintf 'Hi user! You have successfully authenticated, but GitHub does not provide shell access.\\n' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	remote := sshConformanceTarget(t)
	fingerprint, err := source.PublicKeyFingerprint(conformanceSourceKey)
	if err != nil {
		t.Fatal(err)
	}
	request := source.EnsureIdentityRequest{Provider: source.GitHub, HostKeys: []source.HostKey{{Key: conformanceSourceKey, Fingerprint: fingerprint}}}
	for _, harness := range []struct {
		name   string
		target Target
	}{{name: "direct", target: direct}, {name: "ssh", target: remote}} {
		t.Run(harness.name, func(t *testing.T) {
			before, inspectErr := harness.target.InspectSourceIdentity(t.Context(), source.GitHub)
			if inspectErr != nil || before.Exists {
				t.Fatalf("initial identity=%+v err=%v", before, inspectErr)
			}
			identity, ensureErr := harness.target.EnsureSourceIdentity(t.Context(), request)
			if ensureErr != nil || !identity.Exists || !identity.TrustConfigured || identity.Fingerprint == "" || identity.PublicKey == "" || strings.Contains(identity.PublicKey, "PRIVATE") {
				t.Fatalf("ensured identity=%+v err=%v", identity, ensureErr)
			}
			observed, inspectErr := harness.target.InspectSourceIdentity(t.Context(), source.GitHub)
			if inspectErr != nil || observed.Fingerprint != identity.Fingerprint {
				t.Fatalf("observed identity=%+v err=%v", observed, inspectErr)
			}
			verified, verifyErr := harness.target.VerifySourceRepository(t.Context(), source.VerifyRequest{Provider: source.GitHub})
			if verifyErr != nil || !verified.Authenticated {
				t.Fatalf("verified=%+v err=%v", verified, verifyErr)
			}
			removed, removeErr := harness.target.RemoveSourceIdentity(t.Context(), source.RemoveIdentityRequest{Provider: source.GitHub, ExpectedFingerprint: identity.Fingerprint})
			if removeErr != nil || !removed.Removed {
				t.Fatalf("removed=%+v err=%v", removed, removeErr)
			}
			after, inspectErr := harness.target.InspectSourceIdentity(t.Context(), source.GitHub)
			if inspectErr != nil || after.Exists {
				t.Fatalf("final identity=%+v err=%v", after, inspectErr)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if _, cancelErr := harness.target.EnsureSourceIdentity(ctx, request); !errors.Is(cancelErr, context.Canceled) {
				t.Fatalf("cancellation error=%v", cancelErr)
			}
		})
	}
}

func TestDirectAndSSHAdaptersConformForRepositoryCloneV2(t *testing.T) {
	supplied := "https://example.test/owner/clone.git"
	bare := filepath.Join(t.TempDir(), "clone.git")
	if output, err := exec.Command("git", "init", "--bare", bare).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, output)
	}
	candidate := (&url.URL{Scheme: "file", Path: bare}).String()
	gitConfig := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(gitConfig, []byte(fmt.Sprintf("[url %q]\n\tinsteadOf = %s\n", candidate, supplied)), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", gitConfig)

	direct := directConformanceTarget(t)
	remote := sshConformanceTarget(t)
	for _, harness := range []struct {
		name   string
		target Target
	}{{name: "direct", target: direct}, {name: "ssh", target: remote}} {
		t.Run(harness.name, func(t *testing.T) {
			destination := filepath.Join(harness.target.state.worktreeRoot, "chosen-destination")
			result, err := harness.target.CloneRepository(t.Context(), repository.CloneRequest{Source: supplied, Destination: destination})
			if err != nil || result.Action != "clone" || result.Path != destination {
				t.Fatalf("clone = %+v, error = %v", result, err)
			}
			if harness.name == "direct" {
				output, configErr := exec.Command("git", "-C", result.Path, "config", "--get-all", "remote.origin.url").CombinedOutput()
				if configErr != nil || strings.TrimSpace(string(output)) != supplied {
					t.Fatalf("stored origin = %q, error = %v", strings.TrimSpace(string(output)), configErr)
				}
			}
		})
	}
}

func directConformanceTarget(t *testing.T) Target {
	t.Helper()
	home := t.TempDir()
	root := filepath.Join(home, "worktrees")
	repositoryPath := filepath.Join(root, "repo")
	if output, err := exec.Command("git", "init", repositoryPath).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, output)
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	writeIdentity(t, home, testIdentity)
	configuration := filepath.Join(home, "config.toml")
	if err = config.Write(configuration, config.Host{WorktreeRoot: root}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SCHOONER_CONFIG", configuration)
	bin := filepath.Join(home, "bin")
	if err = os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(bin, "tmux"), []byte("#!/bin/sh\nprintf 'no server running\\n' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	state := &targetState{boxIdentity: testIdentity, worktreeRoot: root, direct: true}
	state.run = directAdapter{runtime: host.NewAtHome(hostruntime.BuildInfo{}, home), state: state, nonInteractive: true}
	return Target{state: state}
}

func sshConformanceTarget(t *testing.T) Target {
	t.Helper()
	root := "/remote/worktrees"
	sourceState := filepath.Join(t.TempDir(), "source-connected")
	sourceFingerprint, err := source.PublicKeyFingerprint(conformanceSourceKey)
	if err != nil {
		t.Fatal(err)
	}
	repositoryValue := fmt.Sprintf(`{"common_directory":%q,"primary":{"path":%q,"relative_path":"repo","git_directory":%q,"kind":"primary","branch":"main","detached":false,"status":{"staged":0,"unstaged":0,"untracked":0,"conflicted":0}},"linked":[]}`, root+"/repo/.git", root+"/repo", root+"/repo/.git")
	worktreeValue := fmt.Sprintf(`{"path":%q,"relative_path":"repo","git_directory":%q,"kind":"primary","branch":"main","detached":false,"status":{"staged":0,"unstaged":0,"untracked":0,"conflicted":0}}`, root+"/repo", root+"/repo/.git")
	hello := fmt.Sprintf(`{"schema_version":"1","protocol_version":"1","schooner_version":"dev","commit":"test","box_identity":%q,"os":"linux","architecture":"amd64","capabilities":["repository.clone.v2","session.list.v1","source.identity.ensure.v1","source.identity.inspect.v1","source.identity.remove.v1","source.repository.verify.v1","workspace.push.inspect.v1","worktree.inspect.v1","worktree.list.v1"]}`, testIdentity)
	catalog := fmt.Sprintf(`{"schema_version":"1","protocol_version":"1","box_identity":%q,"worktree_root":%q,"repositories":[%s],"warnings":[]}`, testIdentity, root, repositoryValue)
	inspection := fmt.Sprintf(`{"schema_version":"1","protocol_version":"1","box_identity":%q,"worktree_root":%q,"repository":%s,"worktree":%s,"warnings":[]}`, testIdentity, root, repositoryValue, worktreeValue)
	invalid := fmt.Sprintf(`{"schema_version":"1","protocol_version":"1","box_identity":%q,"error":{"code":"invalid_input","message":"worktree selector is invalid"}}`, testIdentity)
	sessions := fmt.Sprintf(`{"schema_version":"1","protocol_version":"1","box_identity":%q,"worktree_root":%q,"sessions":[]}`, testIdentity, root)
	cloneDestination := root + "/chosen-destination"
	cloneResult := fmt.Sprintf(`{"schema_version":"1","protocol_version":"1","box_identity":%q,"action":"clone","recovered":false,"worktree_root":%q,"path":%q}`, testIdentity, root, cloneDestination)
	sourcePresent := fmt.Sprintf(`{"schema_version":"1","protocol_version":"1","box_identity":%q,"provider":"github","exists":true,"public_key":%q,"fingerprint":%q,"trust_configured":true,"host_fingerprints":[%q]}`, testIdentity, conformanceSourceKey, sourceFingerprint, sourceFingerprint)
	sourceAbsent := fmt.Sprintf(`{"schema_version":"1","protocol_version":"1","box_identity":%q,"provider":"github","exists":false,"trust_configured":false}`, testIdentity)
	sourceRemoved := fmt.Sprintf(`{"schema_version":"1","protocol_version":"1","box_identity":%q,"provider":"github","removed":true}`, testIdentity)
	sourceVerified := fmt.Sprintf(`{"schema_version":"1","protocol_version":"1","box_identity":%q,"provider":"github","authenticated":true}`, testIdentity)
	checkoutUnsupported := fmt.Sprintf(`{"schema_version":"1","protocol_version":"1","box_identity":%q,"error":{"code":"capability_unavailable","message":"workspace transfer does not support this checkout"}}`, testIdentity)
	script := fmt.Sprintf(`#!/bin/sh
case " $* " in
  *"host hello"*) printf '%%s\n' '%s' ;;
  *"host workspace push-inspect"*) cat >/dev/null; printf '%%s\n' '%s' ;;
  *"host worktree list"*) cat >/dev/null; printf '%%s\n' '%s' ;;
  *"host worktree inspect"*) payload=$(cat); case "$payload" in *"../outside"*) printf '%%s\n' '%s' ;; *) printf '%%s\n' '%s' ;; esac ;;
  *"host session list"*) cat >/dev/null; printf '%%s\n' '%s' ;;
  *"host repository clone-v2"*) payload=$(cat); case "$payload" in *'"destination":"%s"'*) printf '%%s\n' '%s' ;; *) exit 65 ;; esac ;;
  *"host source identity ensure"*) cat >/dev/null; : > '%s'; printf '%%s\n' '%s' ;;
  *"host source identity inspect"*) cat >/dev/null; if test -f '%s'; then printf '%%s\n' '%s'; else printf '%%s\n' '%s'; fi ;;
  *"host source identity remove"*) cat >/dev/null; rm -f '%s'; printf '%%s\n' '%s' ;;
  *"host source repository verify"*) cat >/dev/null; printf '%%s\n' '%s' ;;
  *) exit 64 ;;
esac
`, hello, checkoutUnsupported, catalog, invalid, inspection, sessions, cloneDestination, cloneResult, sourceState, sourcePresent, sourceState, sourcePresent, sourceAbsent, sourceState, sourceRemoved, sourceVerified)
	path := filepath.Join(t.TempDir(), "ssh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	state := &targetState{boxName: "work", boxIdentity: testIdentity, worktreeRoot: root}
	state.run = sshAdapter{
		runtime:    ssh.NewHost(path, nil, "dev", nil),
		state:      state,
		connection: box.Connection{Destination: "work-host", BatchMode: true},
		installed:  box.HostRuntime{Path: "/home/alice/.local/bin/schooner"},
	}
	return Target{state: state}
}
