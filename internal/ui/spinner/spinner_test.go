package spinner

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	uitheme "github.com/thewelshrich/schooner/internal/ui/theme"
)

func TestRunDoesNotProbeTerminal(t *testing.T) {
	var out bytes.Buffer
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		Run(ctx, &out, uitheme.New(uitheme.Auto, true), "Loading catalog")
	}()
	time.Sleep(120 * time.Millisecond)
	cancel()
	<-done
	got := out.String()
	for _, leak := range []string{"\x1b]11;", "\x1b[?2026", "\x1b[?2027", "RequestBackground"} {
		if strings.Contains(got, leak) {
			t.Fatalf("spinner probed terminal with %q in %q", leak, got)
		}
	}
	if !strings.Contains(got, "Loading catalog") {
		t.Fatalf("title missing from %q", got)
	}
}

func TestWhileKeepsCompletedStep(t *testing.T) {
	var out bytes.Buffer
	called := false
	err := While(t.Context(), &out, uitheme.New(uitheme.Auto, false), "Creating an SSH key on work…", false, func(context.Context) error {
		called = true
		return nil
	})
	if err != nil || !called {
		t.Fatalf("err=%v called=%t", err, called)
	}
	if out.String() != "… Creating an SSH key on work\n✓ Creating an SSH key on work\n" {
		t.Fatalf("output = %q", out.String())
	}
}

func TestWhileKeepsFailedStep(t *testing.T) {
	var out bytes.Buffer
	err := While(t.Context(), &out, nil, "Cloning onto work", false, func(context.Context) error {
		return context.Canceled
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if out.String() != "… Cloning onto work\n✗ Cloning onto work\n" {
		t.Fatalf("output = %q", out.String())
	}
}
