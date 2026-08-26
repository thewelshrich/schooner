package runtime

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/thewelshrich/schooner/internal/repository"
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
