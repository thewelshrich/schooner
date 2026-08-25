package runtime

import (
	"bytes"
	"encoding/json"
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
	if err := ValidateHello(hello, testIdentity, CapabilityHelloV1, CapabilityInspectV2); err != nil {
		t.Fatal(err)
	}

	hello.ProtocolVersion = "2"
	if err := ValidateHello(hello, testIdentity); ErrorCode(err) != CodeUnsupportedProtocol {
		t.Fatalf("protocol error = %v", err)
	}
	hello.ProtocolVersion = ProtocolVersion
	hello.Capabilities = []string{CapabilityHelloV1}
	if err := ValidateHello(hello, testIdentity, CapabilityInspectV2); ErrorCode(err) != CodeCapabilityUnavailable {
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
	if err := ValidateConfigureRequest(NewConfigureRequest("/home/alice/schooner", testIdentity)); err != nil {
		t.Fatal(err)
	}
	if err := ValidateConfigureRequest(NewConfigureRequest("/home/alice/schooner", "wrong")); ErrorCode(err) != CodeInvalidIdentity {
		t.Fatalf("configure identity error = %v", err)
	}
	for _, root := range []string{"~/schooner", "/home/alice/../alice/schooner", "/bad\nroot"} {
		if err := ValidateConfigureRequest(NewConfigureRequest(root, testIdentity)); ErrorCode(err) != CodeInvalidMessage {
			t.Fatalf("root %q error = %v", root, err)
		}
	}
	if err := ValidateWorktreeRequest(NewWorktreeRequest("", testIdentity), false); err != nil {
		t.Fatal(err)
	}
	if err := ValidateWorktreeRequest(NewWorktreeRequest("owner/repo", testIdentity), true); err != nil {
		t.Fatal(err)
	}
	if err := ValidateWorktreeRequest(NewWorktreeRequest("", testIdentity), true); ErrorCode(err) != CodeInvalidInput {
		t.Fatalf("missing selector error = %v", err)
	}
	if err := ValidateWorktreeRequest(NewWorktreeRequest("unexpected", testIdentity), false); ErrorCode(err) != CodeInvalidInput {
		t.Fatalf("list selector error = %v", err)
	}
}

func TestGitLifecycleRequestsAreStrict(t *testing.T) {
	if err := ValidateCloneRequest(NewCloneRequest("git@example.com:owner/repo.git", "main", testIdentity)); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCloneRequest(NewCloneRequest("", "", testIdentity)); ErrorCode(err) != CodeInvalidInput {
		t.Fatalf("empty clone source error = %v", err)
	}
	if err := ValidateWorktreeMutationRequest(NewWorktreeMutationRequest("owner/repo", "owner/feature", "feature", testIdentity), "add"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateWorktreeMutationRequest(NewWorktreeMutationRequest("", "owner/feature", "", testIdentity), "remove"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateWorktreeMutationRequest(NewWorktreeMutationRequest("", "", "", testIdentity), "prune"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateWorktreeMutationRequest(NewWorktreeMutationRequest("unexpected", "", "", testIdentity), "prune"); ErrorCode(err) != CodeInvalidInput {
		t.Fatalf("invalid prune error = %v", err)
	}
}

func TestDecodeOperationErrorPreservesTypedNotFound(t *testing.T) {
	document := NewOperationError(testIdentity, CodeNotFound, `worktree "missing" was not found`)
	var encoded bytes.Buffer
	if err := json.NewEncoder(&encoded).Encode(document); err != nil {
		t.Fatal(err)
	}
	decoded, present, err := DecodeOperationError(encoded.Bytes(), testIdentity)
	if err != nil || !present || decoded.Error.Code != CodeNotFound || decoded.Error.Message != document.Error.Message {
		t.Fatalf("decoded = %+v, present = %t, err = %v", decoded, present, err)
	}
	if _, present, err = DecodeOperationError([]byte(`{"schema_version":"1"}`), testIdentity); err != nil || present {
		t.Fatalf("success probe present = %t, err = %v", present, err)
	}
}

func TestDecodeOperationErrorPreservesInvalidInput(t *testing.T) {
	document := NewOperationError(testIdentity, CodeInvalidInput, "worktree selector must be canonical")
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	decoded, present, err := DecodeOperationError(encoded, testIdentity)
	if err != nil || !present || decoded.Error.Code != CodeInvalidInput {
		t.Fatalf("decoded = %+v, present = %t, err = %v", decoded, present, err)
	}
}

func TestDecodeOperationErrorPreservesLifecycleCodes(t *testing.T) {
	for _, code := range []Code{CodeConflict, CodeAuthentication, CodePermissionDenied, CodeOperationInProgress, CodeOutcomeUnknown} {
		encoded, err := json.Marshal(NewOperationError(testIdentity, code, "safe failure"))
		if err != nil {
			t.Fatal(err)
		}
		decoded, present, err := DecodeOperationError(encoded, testIdentity)
		if err != nil || !present || decoded.Error.Code != code {
			t.Fatalf("code %s decoded=%+v present=%t err=%v", code, decoded, present, err)
		}
	}
}
