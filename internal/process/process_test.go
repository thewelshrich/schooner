package process

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunInteractiveAttachesStreamsDirectoryAndExitStatus(t *testing.T) {
	directory := t.TempDir()
	var stdout, stderr bytes.Buffer
	exitCode, err := RunInteractive(t.Context(), directory, "/bin/sh", []string{"-c", `read value; printf '%s:%s' "$PWD" "$value"; printf warning >&2; exit 7`}, strings.NewReader("input\n"), &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if exitCode != 7 {
		t.Fatalf("exit code = %d", exitCode)
	}
	if stdout.String() != directory+":input" || stderr.String() != "warning" {
		t.Fatalf("stdout = %q, stderr = %q", stdout.String(), stderr.String())
	}
}

func TestRunInteractiveWithoutEnvironmentRemovesSelectedVariables(t *testing.T) {
	t.Setenv("SCHOONER_TEST_REMOVE", "secret")
	t.Setenv("SCHOONER_TEST_KEEP", "visible")
	var stdout bytes.Buffer
	exitCode, err := RunInteractiveWithoutEnvironment(t.Context(), "", "/bin/sh", []string{"-c", `printf '%s:%s' "${SCHOONER_TEST_REMOVE-unset}" "$SCHOONER_TEST_KEEP"`}, []string{"SCHOONER_TEST_REMOVE"}, nil, &stdout, io.Discard)
	if err != nil || exitCode != 0 {
		t.Fatalf("exit code = %d, error = %v", exitCode, err)
	}
	if stdout.String() != "unset:visible" {
		t.Fatalf("environment = %q", stdout.String())
	}
}

func TestInteractiveTerminalRejectsNonTTYStreams(t *testing.T) {
	if interactiveTerminal(strings.NewReader("not a terminal")) {
		t.Fatal("ordinary reader was treated as an interactive terminal")
	}
}

func TestRunInteractiveCancellationTerminatesDescendants(t *testing.T) {
	output := filepath.Join(t.TempDir(), "descendant-output")
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, err := RunInteractive(ctx, "", "/bin/sh", []string{"-c", `(sleep 0.3; printf leaked > "$1") & wait`, "sh", output}, nil, io.Discard, io.Discard)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation error = %v", err)
	}
	time.Sleep(350 * time.Millisecond)
	if _, err = os.Stat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("descendant survived cancellation: %v", err)
	}
}

func TestRunBoundsOutputAndHonorsCancellation(t *testing.T) {
	if _, err := Run(t.Context(), 4, "/bin/sh", "-c", "printf 12345"); err == nil {
		t.Fatal("oversized output succeeded")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Run(ctx, 4, "/usr/bin/true"); err != context.Canceled {
		t.Fatalf("cancel error = %v", err)
	}
}

func TestRunStopsCommandWhenOutputLimitIsReached(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	_, err := Run(ctx, 4, "/bin/sh", "-c", "while :; do printf 12345; done")
	if err == nil || errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "command output exceeded") {
		t.Fatalf("error = %v", err)
	}
}

func TestRunWithoutEnvironmentRemovesOnlySelectedVariables(t *testing.T) {
	t.Setenv("SCHOONER_PROCESS_REMOVE", "secret")
	t.Setenv("SCHOONER_PROCESS_KEEP", "visible")
	output, err := RunWithoutEnvironment(t.Context(), 64, []string{"SCHOONER_PROCESS_REMOVE"}, "/bin/sh", "-c", `printf '%s:%s' "$SCHOONER_PROCESS_REMOVE" "$SCHOONER_PROCESS_KEEP"`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(output)) != ":visible" {
		t.Fatalf("output = %q", output)
	}
}

func TestRunCapturedBoundsWithoutCancellingCommand(t *testing.T) {
	t.Setenv("SCHOONER_PROCESS_OVERRIDE", "old")
	result, err := RunCapturedWithoutEnvironment(t.Context(), 4, nil, nil, "/bin/sh", "-c", "printf 123456; printf abcdef >&2; exit 7")
	if ExitCode(err) != 7 {
		t.Fatalf("exit error = %v", err)
	}
	if string(result.Stdout) != "1234" || string(result.Stderr) != "abcd" || !result.Truncated {
		t.Fatalf("result = %+v", result)
	}
	overridden, err := RunCapturedWithoutEnvironment(t.Context(), 16, nil, []string{"SCHOONER_PROCESS_OVERRIDE=new"}, "/bin/sh", "-c", `printf %s "$SCHOONER_PROCESS_OVERRIDE"`)
	if err != nil || string(overridden.Stdout) != "new" {
		t.Fatalf("override = %+v, %v", overridden, err)
	}
}

func TestRunCapturedBoundsInheritedPipeWait(t *testing.T) {
	started := time.Now()
	result, err := RunCapturedWithoutEnvironment(t.Context(), 64, nil, nil, "/bin/sh", "-c", "(sleep 2) & printf done")
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Stdout) != "done" {
		t.Fatalf("stdout = %q", result.Stdout)
	}
	if elapsed := time.Since(started); elapsed > 1500*time.Millisecond {
		t.Fatalf("inherited pipe kept command blocked for %s", elapsed)
	}
}

func TestRunCapturedCancellationTerminatesDescendants(t *testing.T) {
	output := filepath.Join(t.TempDir(), "descendant-output")
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := RunCapturedWithoutEnvironment(ctx, 64, nil, nil, "/bin/sh", "-c", `(sleep 0.3; printf leaked > "$1") & wait`, "sh", output)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("cancellation waited for descendant: %v", elapsed)
	}
	time.Sleep(350 * time.Millisecond)
	if _, err = os.Stat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("descendant survived cancellation: %v", err)
	}
}
