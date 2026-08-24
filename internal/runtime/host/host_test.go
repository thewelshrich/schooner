package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	hostruntime "github.com/thewelshrich/schooner/internal/runtime"
)

func TestRuntimeHelloInspectAndDoctor(t *testing.T) {
	home := t.TempDir()
	identity := "11111111-1111-4111-8111-111111111111"
	identityPath := filepath.Join(home, ".local", "state", "schooner")
	if err := os.MkdirAll(identityPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(identityPath, "identity"), []byte(identity+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(home, "schooner")
	if err := os.Mkdir(workspace, 0o700); err != nil {
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
	expectedWorkspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := runtime.Inspect(t.Context(), hostruntime.NewInspectRequest("~/schooner"))
	if err != nil || inspection.OSID != "ubuntu" || inspection.WorkspaceRoot != expectedWorkspace || !inspection.Git.Available || !inspection.PasswordlessSudo {
		t.Fatalf("Inspect() = %+v, %v", inspection, err)
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
