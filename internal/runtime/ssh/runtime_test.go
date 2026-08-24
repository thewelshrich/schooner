package ssh

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thewelshrich/schooner/internal/box"
)

func TestShellQuoteRoundTrip(t *testing.T) {
	for _, value := range []string{"", "plain", "space value", "$(touch nope)", "'; echo injected; '"} {
		command := "printf %s " + shellQuote(value)
		output, err := exec.Command("sh", "-c", command).Output()
		if err != nil {
			t.Fatalf("shell round trip for %q: %v", value, err)
		}
		if string(output) != value {
			t.Fatalf("round trip = %q, want %q", output, value)
		}
	}
}

func FuzzShellQuote(f *testing.F) {
	for _, seed := range []string{"hello", "a b", "'", "$()", "semi;colon"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		if strings.ContainsRune(value, 0) || strings.ContainsAny(value, "\r\n") {
			t.Skip()
		}
		command := "printf %s " + shellQuote(value)
		output, err := exec.Command("sh", "-c", command).Output()
		if err != nil {
			t.Fatalf("round trip: %v", err)
		}
		if string(output) != value {
			t.Fatalf("got %q, want %q", output, value)
		}
	})
}

func TestClassifyHostIdentityFailures(t *testing.T) {
	err := classify(exec.ErrNotFound, "WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!")
	if got := box.ErrorCode(err); got != "host_identity_changed" {
		t.Fatalf("code = %q", got)
	}
}

func TestConnectionPolicyOptions(t *testing.T) {
	runtime := New("ssh", nil)
	options := strings.Join(runtime.options(box.Connection{AcceptNewHostKey: true, BatchMode: true}), " ")
	if !strings.Contains(options, "BatchMode=yes") || !strings.Contains(options, "StrictHostKeyChecking=accept-new") {
		t.Fatalf("options = %q", options)
	}
	defaultOptions := strings.Join(runtime.options(box.Connection{}), " ")
	if strings.Contains(defaultOptions, "StrictHostKeyChecking") || strings.Contains(defaultOptions, "BatchMode") {
		t.Fatalf("default options override native OpenSSH policy: %q", defaultOptions)
	}
}

func TestOpenShellAttachesTerminalWithoutRemoteCommand(t *testing.T) {
	path := writeExecutable(t, `#!/bin/sh
IFS= read -r input
printf 'input=%s args=%s' "$input" "$*"
printf 'native diagnostic' >&2
`)
	runtime := New(path, nil)
	var stdout, stderr bytes.Buffer
	result, err := runtime.OpenShell(t.Context(), box.Connection{
		Destination:  "root@203.0.113.8",
		IdentityFile: "/state/ssh/id_ed25519",
		BatchMode:    true,
	}, TerminalIO{In: strings.NewReader("hello\n"), Out: &stdout, Err: &stderr})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	want := "input=hello args=-i /state/ssh/id_ed25519 -o IdentitiesOnly=yes -o BatchMode=yes root@203.0.113.8"
	if stdout.String() != want {
		t.Fatalf("stdout=%q, want %q", stdout.String(), want)
	}
	if stderr.String() != "native diagnostic" {
		t.Fatalf("stderr=%q", stderr.String())
	}
	if strings.Contains(stdout.String(), "LogLevel") || strings.Contains(stdout.String(), "StrictHostKeyChecking") {
		t.Fatalf("interactive arguments override native OpenSSH policy: %q", stdout.String())
	}
}

func TestOpenShellReturnsRemoteAndConnectionExitStatuses(t *testing.T) {
	t.Run("remote shell", func(t *testing.T) {
		runtime := New(writeExecutable(t, "#!/bin/sh\nexit 42\n"), nil)
		result, err := runtime.OpenShell(t.Context(), box.Connection{Destination: "work"}, TerminalIO{})
		if err != nil || result.ExitCode != 42 || result.DiagnosticsReported {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})

	t.Run("OpenSSH connection failure", func(t *testing.T) {
		runtime := New(writeExecutable(t, "#!/bin/sh\nprintf 'Permission denied (publickey).\\n' >&2\nexit 255\n"), nil)
		var stderr bytes.Buffer
		result, err := runtime.OpenShell(t.Context(), box.Connection{Destination: "work"}, TerminalIO{Err: &stderr})
		if box.ErrorCode(err) != "authentication_required" || result.ExitCode != 255 || !result.DiagnosticsReported {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		if stderr.String() != "Permission denied (publickey).\n" {
			t.Fatalf("stderr=%q", stderr.String())
		}
	})
}

func TestOpenShellRespectsCancellationAndStartupFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	runtime := New(writeExecutable(t, "#!/bin/sh\nexit 0\n"), nil)
	if _, err := runtime.OpenShell(ctx, box.Connection{Destination: "work"}, TerminalIO{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}

	runtime = New(filepath.Join(t.TempDir(), "missing-ssh"), nil)
	if _, err := runtime.OpenShell(t.Context(), box.Connection{Destination: "work"}, TerminalIO{}); err == nil {
		t.Fatal("missing executable succeeded")
	}
}

func writeExecutable(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ssh")
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestWaitReadyRetriesOnlyConnectionFailures(t *testing.T) {
	runtime := New("ssh", nil)
	attempts := 0
	runtime.probe = func(context.Context, box.Connection) error {
		attempts++
		if attempts < 3 {
			return box.NewError("connection_failed", "connection refused", nil)
		}
		return nil
	}
	runtime.wait = func(context.Context, time.Duration) error { return nil }
	if err := runtime.WaitReady(t.Context(), box.Connection{Destination: "root@host"}); err != nil || attempts != 3 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}

	attempts = 0
	runtime.probe = func(context.Context, box.Connection) error {
		attempts++
		return box.NewError("authentication_required", "permission denied", nil)
	}
	if err := runtime.WaitReady(t.Context(), box.Connection{Destination: "root@host"}); box.ErrorCode(err) != "authentication_required" || attempts != 1 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
}
