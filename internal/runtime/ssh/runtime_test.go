package ssh

import (
	"bytes"
	"context"
	"errors"
	"io"
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

func TestFixedShellCommandRunsThroughNonPOSIXLoginShell(t *testing.T) {
	command := fixedShellCommand(`value=$(printf %s "$1" | base64 -d) || exit 64; printf %s "$value"`, "path with ' quotes")
	if !strings.HasPrefix(command, "/bin/sh -c ") {
		t.Fatalf("command does not enter through /bin/sh: %q", command)
	}
	shell, err := exec.LookPath("csh")
	if err != nil {
		t.Skip("csh is not installed")
	}
	output, err := exec.Command(shell, "-f", "-c", command).CombinedOutput()
	if err != nil || string(output) != "path with ' quotes" {
		t.Fatalf("csh command output=%q err=%v", output, err)
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

func TestInteractiveHostCommandAllocatesTTYAndPreservesTerminal(t *testing.T) {
	path := writeExecutable(t, `#!/bin/sh
IFS= read -r input
printf 'input=%s args=%s' "$input" "$*"
`)
	runtime := New(path, nil)
	var stdout bytes.Buffer
	result, err := runtime.openInteractiveCommand(t.Context(), box.Connection{Destination: "work", BatchMode: true}, "fixed-command", TerminalIO{In: strings.NewReader("hello\n"), Out: &stdout})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if got := stdout.String(); got != "input=hello args=-o BatchMode=yes -t work fixed-command" {
		t.Fatalf("stdout=%q", got)
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

func TestRunRemotePreservesRemoteExitAndRejectsOversizedOutput(t *testing.T) {
	runtime := New(writeExecutable(t, "#!/bin/sh\nprintf 'remote diagnostic' >&2\nexit 42\n"), nil)
	result, err := runtime.runRemote(t.Context(), box.Connection{Destination: "work"}, "fixed operation", nil)
	if err != nil || result.ExitCode != 42 || string(result.Stderr) != "remote diagnostic" {
		t.Fatalf("result=%+v err=%v", result, err)
	}

	runtime = New(writeExecutable(t, "#!/bin/sh\ndd if=/dev/zero bs=1048577 count=1 2>/dev/null\n"), nil)
	if _, err = runtime.runRemote(t.Context(), box.Connection{Destination: "work"}, "fixed operation", nil); box.ErrorCode(err) != "internal" {
		t.Fatalf("oversized output error = %v", err)
	}
}

func TestRunRemoteStreamConsumesUnboundedStdoutWithBoundedDiagnostics(t *testing.T) {
	runtime := New(writeExecutable(t, "#!/bin/sh\nprintf 'header\\npayload'\n"), nil)
	var output bytes.Buffer
	result, err := runtime.runRemoteStream(t.Context(), box.Connection{Destination: "work"}, "fixed operation", nil, func(reader io.Reader) error {
		_, copyErr := io.Copy(&output, reader)
		return copyErr
	})
	if err != nil || result.ExitCode != 0 || output.String() != "header\npayload" {
		t.Fatalf("result=%+v output=%q err=%v", result, output.String(), err)
	}

	want := errors.New("invalid stream")
	runtime = New(writeExecutable(t, "#!/bin/sh\nprintf 'bad'\n"), nil)
	if _, err = runtime.runRemoteStream(t.Context(), box.Connection{Destination: "work"}, "fixed operation", nil, func(io.Reader) error { return want }); !errors.Is(err, want) {
		t.Fatalf("consumer error = %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	runtime = New(writeExecutable(t, "#!/bin/sh\nexec sleep 30\n"), nil)
	if _, err = runtime.runRemoteStream(ctx, box.Connection{Destination: "work"}, "fixed operation", nil, func(reader io.Reader) error {
		_, copyErr := io.Copy(io.Discard, reader)
		return copyErr
	}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stream cancellation error = %v", err)
	}
}

func TestInstallScriptKeepsPackageOutputOffStdout(t *testing.T) {
	program, err := scripts.ReadFile("scripts/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	// Stub sudo so the real script cannot install packages on the test host.
	stub := `#!/bin/sh
printf 'package progress: %s\n' "$*"
case " $* " in
  *" $SCHOONER_TEST_APT_FAILURE "*) printf 'E: package operation failed\n' >&2; exit 100 ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(directory, "sudo"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, step := range []string{"none", "update", "install"} {
		t.Run(step, func(t *testing.T) {
			t.Setenv("SCHOONER_TEST_APT_FAILURE", step)
			command := exec.CommandContext(t.Context(), "sh", "-s", "--", "git", "tmux")
			command.Stdin = bytes.NewReader(program)
			var stderr bytes.Buffer
			command.Stderr = &stderr
			stdout, err := command.Output()
			wantStdout := ""
			if step == "none" {
				wantStdout = "{\"schema_version\":\"1\",\"installed\":true}\n"
				if err != nil {
					t.Fatalf("install: %v; stderr=%s", err, &stderr)
				}
			} else {
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) || exitErr.ExitCode() != 100 {
					t.Fatalf("install error = %v, want exit 100", err)
				}
			}
			if string(stdout) != wantStdout {
				t.Fatalf("stdout = %q, want %q", stdout, wantStdout)
			}
			wantStderr := ""
			if step != "none" {
				wantStderr = "E: package operation failed\n"
			}
			if stderr.String() != wantStderr {
				t.Fatalf("stderr = %q, want %q", &stderr, wantStderr)
			}
			err = New(testSSHExecutable(t), nil).InstallTools(t.Context(), box.Connection{Destination: "test-host"}, []string{"git", "tmux"})
			if step == "none" {
				if err != nil {
					t.Fatalf("InstallTools: %v", err)
				}
			} else if box.ErrorCode(err) != "remote_operation_failed" || !strings.Contains(err.Error(), strings.TrimSpace(wantStderr)) {
				t.Fatalf("InstallTools error = %v, want native apt diagnostic", err)
			}
		})
	}
}
