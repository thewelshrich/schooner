package runtime

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/thewelshrich/schooner/internal/repository"
	"github.com/thewelshrich/schooner/internal/source"
)

func TestBoundedOperationContractsAreExplicitUniqueAndAdvertised(t *testing.T) {
	commands := map[string]struct{}{}
	capabilities := map[string]struct{}{}
	advertised := Capabilities()
	for _, operation := range boundedOperationContracts() {
		if !strings.HasPrefix(operation.Command(), "host ") {
			t.Fatalf("operation command %q is not private host command", operation.Command())
		}
		if _, exists := commands[operation.Command()]; exists {
			t.Fatalf("duplicate operation command %q", operation.Command())
		}
		commands[operation.Command()] = struct{}{}
		if _, exists := capabilities[operation.capabilityName()]; exists {
			t.Fatalf("duplicate operation capability %q", operation.capabilityName())
		}
		capabilities[operation.capabilityName()] = struct{}{}
		if !slices.Contains(advertised, operation.capabilityName()) {
			t.Fatalf("operation capability %q is not advertised", operation.capabilityName())
		}
	}
}

func TestOperationKeepsStrictDecodingAndSemanticValidationSeparate(t *testing.T) {
	operation := WorktreeInspectOperation()
	encoded, err := json.Marshal(NewWorktreeRequest("", testIdentity))
	if err != nil {
		t.Fatal(err)
	}
	request, err := operation.DecodeRequest(encoded)
	if err != nil {
		t.Fatalf("DecodeRequest() error = %v", err)
	}
	if err = operation.ValidateRequest(request); ErrorCode(err) != CodeInvalidInput {
		t.Fatalf("ValidateRequest() error = %v", err)
	}
}

func TestWorkspacePushInspectSeparatesObservationFromValidatedPreflight(t *testing.T) {
	operation := WorkspacePushInspectOperation()
	observation := NewWorkspacePushInspectRequest("/worktrees/repo", testIdentity)
	if err := operation.ValidateRequest(observation); err != nil {
		t.Fatal(err)
	}

	digest := strings.Repeat("a", 64)
	preflight := observation
	preflight.IncomingStateDigest = digest
	preflight.IncomingBranch = "main"
	preflight.PreflightBranch = true
	preflight.IncomingFiles = []repository.CheckoutFile{{Path: "file.txt", Kind: "file", Size: 4, SHA256: digest, Tracked: true}}
	if err := operation.ValidateRequest(preflight); err != nil {
		t.Fatal(err)
	}
	absent := preflight
	absent.IncomingFiles = []repository.CheckoutFile{{Path: "staged.txt", Kind: "absent", Tracked: true}}
	if err := operation.ValidateRequest(absent); err != nil {
		t.Fatalf("valid absent path error = %v", err)
	}
	invalidAbsent := absent
	invalidAbsent.IncomingFiles[0].Tracked = false
	if err := operation.ValidateRequest(invalidAbsent); ErrorCode(err) != CodeInvalidInput {
		t.Fatalf("untracked absent path error = %v", err)
	}

	invalidObservation := observation
	invalidObservation.IncomingBranch = "main"
	if err := operation.ValidateRequest(invalidObservation); ErrorCode(err) != CodeInvalidInput {
		t.Fatalf("observation metadata error = %v", err)
	}
	invalidManifest := preflight
	invalidManifest.IncomingFiles = append(invalidManifest.IncomingFiles, invalidManifest.IncomingFiles[0])
	if err := operation.ValidateRequest(invalidManifest); ErrorCode(err) != CodeInvalidInput {
		t.Fatalf("duplicate manifest error = %v", err)
	}
	invalidManifest = preflight
	invalidManifest.IncomingFiles[0].SHA256 = "not-a-digest"
	if err := operation.ValidateRequest(invalidManifest); ErrorCode(err) != CodeInvalidInput {
		t.Fatalf("manifest digest error = %v", err)
	}
	invalidManifest = preflight
	invalidManifest.IncomingFiles = []repository.CheckoutFile{{Path: "nested/.git/config", Kind: "file", Size: 4, SHA256: digest}}
	if err := operation.ValidateRequest(invalidManifest); ErrorCode(err) != CodeInvalidInput {
		t.Fatalf("Git metadata path error = %v", err)
	}
}

func TestContextWorktreeListRequiresOriginIdentityCapability(t *testing.T) {
	request := NewWorktreeRequest("", testIdentity)
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
	if err := ContextWorktreeListOperation().ValidateHello(request, hello); err != nil {
		t.Fatal(err)
	}
	hello.Capabilities = slices.DeleteFunc(hello.Capabilities, func(value string) bool { return value == CapabilityOriginIdentityV1 })
	if err := ContextWorktreeListOperation().ValidateHello(request, hello); ErrorCode(err) != CodeCapabilityUnavailable {
		t.Fatalf("context capability error = %v", err)
	}
	if err := WorktreeListOperation().ValidateHello(request, hello); err != nil {
		t.Fatalf("ordinary list should remain v1 compatible: %v", err)
	}
}

func TestRepositoryCloneCapabilitiesNegotiateIndependently(t *testing.T) {
	request := NewCloneRequest("https://github.com/owner/repo.git", "", "/worktrees", testIdentity)
	hello := Hello{
		SchemaVersion: SchemaVersion, ProtocolVersion: ProtocolVersion, SchoonerVersion: "v1.2.3", Commit: "abc123",
		BoxIdentity: testIdentity, OS: "linux", Architecture: "amd64", Capabilities: Capabilities(),
	}
	withoutV2 := hello
	withoutV2.Capabilities = slices.DeleteFunc(append([]string(nil), hello.Capabilities...), func(value string) bool { return value == CapabilityRepositoryCloneV2 })
	if err := RepositoryCloneOperation().ValidateHello(request, withoutV2); err != nil {
		t.Fatalf("v1 clone rejected legacy runtime: %v", err)
	}
	if err := RepositoryCloneV2Operation().ValidateHello(request, withoutV2); ErrorCode(err) != CodeCapabilityUnavailable {
		t.Fatalf("v2 clone capability error = %v", err)
	}
	withoutV1 := hello
	withoutV1.Capabilities = slices.DeleteFunc(append([]string(nil), hello.Capabilities...), func(value string) bool { return value == CapabilityRepositoryCloneV1 })
	if err := RepositoryCloneV2Operation().ValidateHello(request, withoutV1); err != nil {
		t.Fatalf("v2 clone unexpectedly depends on v1: %v", err)
	}
}

func TestRepositoryCloneOperationPreservesExactDestination(t *testing.T) {
	request := NewCloneRequest("https://github.com/owner/repo.git", "main", "/worktrees", testIdentity)
	request.Destination = "/worktrees/chosen-destination"
	operation := RepositoryCloneV2Operation()

	encoded, err := operation.EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"destination":"/worktrees/chosen-destination"`) {
		t.Fatalf("encoded request = %s", encoded)
	}
	decoded, err := operation.DecodeRequest(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if err = operation.ValidateRequest(decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Destination != request.Destination {
		t.Fatalf("destination = %q, want %q", decoded.Destination, request.Destination)
	}

	outside := request
	outside.Destination = "/elsewhere/repo"
	if err = operation.ValidateRequest(outside); ErrorCode(err) != CodeInvalidInput {
		t.Fatalf("outside destination error = %v", err)
	}
}

func TestWorkspacePushApplyOperationCarriesCreatedDestinationProof(t *testing.T) {
	digest := strings.Repeat("a", 64)
	request := NewWorkspacePushApplyRequest(strings.Repeat("b", 32), "/worktrees/repo", digest, digest, 3, digest, testIdentity)
	request.OperationCreatedDestination = true
	operation := WorkspacePushApplyOperation()
	if err := operation.ValidateRequest(request); err != nil {
		t.Fatal(err)
	}
	encoded, err := operation.EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"operation_created_destination":true`) {
		t.Fatalf("encoded request = %s", encoded)
	}
	request.ExpectedStateDigest = ""
	if err = operation.ValidateRequest(request); ErrorCode(err) != CodeInvalidInput {
		t.Fatalf("created destination without expected state error = %v", err)
	}
}

func TestOperationDecodeResultAppliesEnvelopeAndResultInvariant(t *testing.T) {
	operation := RepositoryCloneOperation()
	request := NewCloneRequest("git@example.com:owner/repo.git", "main", "/worktrees", testIdentity)
	want := LifecycleResult{
		SchemaVersion:   SchemaVersion,
		ProtocolVersion: ProtocolVersion,
		BoxIdentity:     testIdentity,
		MutationResult: repository.MutationResult{
			Action:       "clone",
			WorktreeRoot: "/worktrees",
			Path:         "/worktrees/repo",
		},
	}
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	got, failure, err := operation.DecodeResult(encoded, request)
	if err != nil || failure != nil || got.Action != want.Action || got.Path != want.Path {
		t.Fatalf("DecodeResult() = %+v, failure=%+v, err=%v", got, failure, err)
	}

	want.Action = "worktree_add"
	encoded, err = json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = operation.DecodeResult(encoded, request); ErrorCode(err) != CodeInvalidMessage {
		t.Fatalf("mismatched action error = %v", err)
	}
}

func TestOperationDecodeResultSeparatesRemoteFailureFromProtocolFailure(t *testing.T) {
	operation := WorktreeInspectOperation()
	request := NewWorktreeRequest("missing", testIdentity)
	encoded, err := json.Marshal(NewOperationError(testIdentity, CodeNotFound, `worktree "missing" was not found`))
	if err != nil {
		t.Fatal(err)
	}
	_, failure, err := operation.DecodeResult(encoded, request)
	if err != nil || failure == nil || failure.Error.Code != CodeNotFound {
		t.Fatalf("DecodeResult() failure=%+v, err=%v", failure, err)
	}

	encoded, err = json.Marshal(NewOperationError("22222222-2222-4222-8222-222222222222", CodeNotFound, "missing"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = operation.DecodeResult(encoded, request); ErrorCode(err) != CodeInvalidIdentity {
		t.Fatalf("mismatched identity error = %v", err)
	}
}

func TestSessionOperationCorrelatesResultToRequest(t *testing.T) {
	operation := SessionStopOperation()
	request := NewSessionTargetRequest("/worktrees", testIdentity, "session-one")
	result := SessionStopResult{
		SchemaVersion:   SchemaVersion,
		ProtocolVersion: ProtocolVersion,
		BoxIdentity:     testIdentity,
		WorktreeRoot:    request.WorktreeRoot,
	}
	result.Stopped = true
	result.SessionID = "session-two"
	if err := operation.ValidateResult(request, result); ErrorCode(err) != CodeInvalidMessage {
		t.Fatalf("mismatched Session result error = %v", err)
	}
}

func TestSourceIdentityOperationsValidateKeysAndNeverCarryPrivateMaterial(t *testing.T) {
	publicKey := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAABAgMEBQYHCAkKCwwNDg8QERITFBUWFxgZGhscHR4f"
	fingerprint, err := source.PublicKeyFingerprint(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	hostKey := source.HostKey{Key: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAICAhIiMkJSYnKCkqKywtLi8wMTIzNDU2Nzg5Ojs8PT4/", Fingerprint: ""}
	hostKey.Fingerprint, err = source.PublicKeyFingerprint(hostKey.Key)
	if err != nil {
		t.Fatal(err)
	}
	request := NewSourceIdentityEnsureRequest(source.GitHub, testIdentity, []source.HostKey{hostKey})
	if err = SourceIdentityEnsureOperation().ValidateRequest(request); err != nil {
		t.Fatal(err)
	}
	result := SourceIdentityResult{SchemaVersion: SchemaVersion, ProtocolVersion: ProtocolVersion, BoxIdentity: testIdentity, HostIdentity: source.HostIdentity{Provider: source.GitHub, Exists: true, PublicKey: publicKey, Fingerprint: fingerprint, TrustConfigured: true, HostFingerprints: []string{hostKey.Fingerprint}}}
	if err = SourceIdentityEnsureOperation().ValidateResult(request, result); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(encoded)), "private") || strings.Contains(string(encoded), "/.local/state/") {
		t.Fatalf("source identity result leaked private state: %s", encoded)
	}
	request.HostKeys[0].Fingerprint = "SHA256:wrong"
	if err = SourceIdentityEnsureOperation().ValidateRequest(request); ErrorCode(err) != CodeInvalidInput {
		t.Fatalf("invalid host key error=%v", err)
	}
}

func TestOperationErrorCarriesBoundedSafeReasonContext(t *testing.T) {
	document := NewOperationErrorWithContext(testIdentity, CodeAuthentication, "SAML authorization is required", map[string]string{"reason": "github_saml_sso"})
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	decoded, present, err := DecodeOperationError(encoded, testIdentity)
	if err != nil || !present || decoded.Error.Context["reason"] != "github_saml_sso" {
		t.Fatalf("decoded=%+v present=%v err=%v", decoded, present, err)
	}
}

func TestSourceOperationStrictlyRejectsUnknownRequestFields(t *testing.T) {
	request := NewSourceIdentityRequest(source.GitHub, testIdentity)
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded[:len(encoded)-1], []byte(`,"private_key":"must-not-cross"}`)...)
	if _, err = SourceIdentityInspectOperation().DecodeRequest(encoded); ErrorCode(err) != CodeInvalidMessage {
		t.Fatalf("err=%v", err)
	}
}

func TestOperationErrorRejectsUnboundedContextAndMessages(t *testing.T) {
	for _, document := range []OperationError{
		NewOperationErrorWithContext(testIdentity, CodeAuthentication, "failure", map[string]string{"reason": strings.Repeat("x", 257)}),
		NewOperationErrorWithContext(testIdentity, CodeAuthentication, "failure", map[string]string{"managed_path": "/home/alice/.local/state/schooner"}),
		NewOperationError(testIdentity, CodeAuthentication, strings.Repeat("x", 4097)),
	} {
		encoded, err := json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		if _, present, decodeErr := DecodeOperationError(encoded, testIdentity); !present || ErrorCode(decodeErr) != CodeInvalidMessage {
			t.Fatalf("present=%v err=%v", present, decodeErr)
		}
	}
}
