package process

import (
	"context"
	"strings"
	"testing"
)

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
