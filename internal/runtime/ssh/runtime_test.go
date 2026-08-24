package ssh

import (
	"os/exec"
	"strings"
	"testing"

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
