package process

import (
	"context"
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
