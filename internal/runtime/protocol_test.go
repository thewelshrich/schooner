package runtime

import (
	"bytes"
	"strings"
	"testing"
)

const testIdentity = "11111111-1111-4111-8111-111111111111"

func TestValidateHelloNegotiatesProtocolCapabilitiesAndIdentity(t *testing.T) {
	hello := Hello{
		SchemaVersion:   SchemaVersion,
		ProtocolVersion: ProtocolVersion,
		SchoonerVersion: "v1.2.3",
		Commit:          "abc123",
		BoxIdentity:     testIdentity,
		OS:              "linux",
		Architecture:    "amd64",
		Capabilities:    Capabilities(),
	}
	if err := ValidateHello(hello, testIdentity, CapabilityHelloV1, CapabilityInspectV1); err != nil {
		t.Fatal(err)
	}

	hello.ProtocolVersion = "2"
	if err := ValidateHello(hello, testIdentity); ErrorCode(err) != CodeUnsupportedProtocol {
		t.Fatalf("protocol error = %v", err)
	}
	hello.ProtocolVersion = ProtocolVersion
	hello.Capabilities = []string{CapabilityHelloV1}
	if err := ValidateHello(hello, testIdentity, CapabilityInspectV1); ErrorCode(err) != CodeCapabilityUnavailable {
		t.Fatalf("capability error = %v", err)
	}
	hello.Capabilities = Capabilities()
	if err := ValidateHello(hello, "22222222-2222-4222-8222-222222222222"); ErrorCode(err) != CodeInvalidIdentity {
		t.Fatalf("identity error = %v", err)
	}
}

func TestDecodeStrictRejectsUnknownTrailingAndOversizedMessages(t *testing.T) {
	for _, input := range [][]byte{
		[]byte(`{"schema_version":"1","protocol_version":"1","worktree_root":"~/schooner","unknown":true}`),
		[]byte(`{"schema_version":"1","protocol_version":"1","worktree_root":"~/schooner"} {}`),
		bytes.Repeat([]byte("x"), MaxMessageBytes+1),
	} {
		var request InspectRequest
		if err := DecodeStrict(input, &request); ErrorCode(err) != CodeInvalidMessage {
			t.Fatalf("DecodeStrict(%q) error = %v", string(input[:min(len(input), 100)]), err)
		}
	}
}

func TestInspectRequestAndInstallPathRejectHostilePaths(t *testing.T) {
	for _, root := range []string{"relative/path", "~/bad\npath", ""} {
		if err := ValidateInspectRequest(NewInspectRequest(root)); ErrorCode(err) != CodeInvalidMessage {
			t.Fatalf("worktree root %q error = %v", root, err)
		}
	}
	if got, err := InstallPath("/home/alice"); err != nil || got != "/home/alice/.local/bin/schooner" {
		t.Fatalf("InstallPath() = %q, %v", got, err)
	}
	for _, home := range []string{"home/alice", "/home/alice\nother", strings.Repeat("x", 2)} {
		if _, err := InstallPath(home); ErrorCode(err) != CodeInvalidMessage {
			t.Fatalf("home %q error = %v", home, err)
		}
	}
}

func TestConfigureAndWorktreeRequestsAreStrict(t *testing.T) {
	if err := ValidateConfigureRequest(NewConfigureRequest("/home/alice/schooner")); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{"~/schooner", "/home/alice/../alice/schooner", "/bad\nroot"} {
		if err := ValidateConfigureRequest(NewConfigureRequest(root)); ErrorCode(err) != CodeInvalidMessage {
			t.Fatalf("root %q error = %v", root, err)
		}
	}
	if err := ValidateWorktreeRequest(NewWorktreeRequest(""), false); err != nil {
		t.Fatal(err)
	}
	if err := ValidateWorktreeRequest(NewWorktreeRequest("owner/repo"), true); err != nil {
		t.Fatal(err)
	}
	if err := ValidateWorktreeRequest(NewWorktreeRequest(""), true); ErrorCode(err) != CodeInvalidMessage {
		t.Fatalf("missing selector error = %v", err)
	}
	if err := ValidateWorktreeRequest(NewWorktreeRequest("unexpected"), false); ErrorCode(err) != CodeInvalidMessage {
		t.Fatalf("list selector error = %v", err)
	}
}
