package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/thewelshrich/schooner/internal/box"
	"github.com/thewelshrich/schooner/internal/repository"
	"github.com/thewelshrich/schooner/internal/source"
)

type cloneRecoveryTarget struct {
	name         string
	identity     string
	cloneErrors  []error
	cloneResults []repository.MutationResult
	cloneCalls   int
}

func (target *cloneRecoveryTarget) BoxName() string     { return target.name }
func (target *cloneRecoveryTarget) BoxIdentity() string { return target.identity }
func (target *cloneRecoveryTarget) CloneRepository(_ context.Context, _ repository.CloneRequest) (repository.MutationResult, error) {
	index := target.cloneCalls
	target.cloneCalls++
	var result repository.MutationResult
	if index < len(target.cloneResults) {
		result = target.cloneResults[index]
	}
	if index < len(target.cloneErrors) {
		return result, target.cloneErrors[index]
	}
	return result, nil
}
func (*cloneRecoveryTarget) InspectSourceIdentity(context.Context, string) (source.HostIdentity, error) {
	return source.HostIdentity{}, nil
}
func (*cloneRecoveryTarget) EnsureSourceIdentity(context.Context, source.EnsureIdentityRequest) (source.HostIdentity, error) {
	return source.HostIdentity{}, nil
}
func (*cloneRecoveryTarget) RemoveSourceIdentity(context.Context, source.RemoveIdentityRequest) (source.RemoveIdentityResult, error) {
	return source.RemoveIdentityResult{}, nil
}
func (*cloneRecoveryTarget) VerifySourceRepository(context.Context, source.VerifyRequest) (source.VerifyResult, error) {
	return source.VerifyResult{}, nil
}

func TestCloneWithRecoveryConnectsVerifiesAndRetriesExactlyOnce(t *testing.T) {
	authentication := box.NewError("authentication_required", "GitHub authentication failed", nil)
	target := &cloneRecoveryTarget{
		name: "work", identity: "11111111-1111-4111-8111-111111111111",
		cloneErrors:  []error{authentication, nil},
		cloneResults: []repository.MutationResult{{}, {Action: "clone", Path: "/worktrees/repo"}},
	}
	var output bytes.Buffer
	streams := Streams{In: strings.NewReader("y\n"), Out: &output, Err: &output, InIsTerminal: true, OutIsTerminal: true, ErrIsTerminal: true}
	global := &globalOptions{output: "human", accessible: true}
	connectCalls := 0
	result, err := cloneWithRecovery(t.Context(), streams, global, target, repository.CloneRequest{Source: "https://github.com/Owner/Repo.git"}, "", func(_ context.Context, connectTarget source.Target, repositoryURL string) (source.ConnectResult, error) {
		connectCalls++
		if connectTarget.BoxName() != "work" || repositoryURL != "git@github.com:owner/repo.git" {
			t.Fatalf("connect target = %q, repository = %q", connectTarget.BoxName(), repositoryURL)
		}
		return source.ConnectResult{State: source.StateConnected}, nil
	})
	if err != nil || result.Path != "/worktrees/repo" || target.cloneCalls != 2 || connectCalls != 1 {
		t.Fatalf("result = %+v, clone calls = %d, connect calls = %d, err = %v", result, target.cloneCalls, connectCalls, err)
	}
}

func TestCloneWithRecoveryNeverConnectsWithoutInteraction(t *testing.T) {
	target := &cloneRecoveryTarget{name: "work", identity: "11111111-1111-4111-8111-111111111111", cloneErrors: []error{box.NewError("authentication_required", "authentication failed", nil)}}
	connectCalls := 0
	_, err := cloneWithRecovery(t.Context(), Streams{}, &globalOptions{output: "json", noInput: true}, target, repository.CloneRequest{Source: "git@github.com:owner/repo.git"}, "", func(context.Context, source.Target, string) (source.ConnectResult, error) {
		connectCalls++
		return source.ConnectResult{}, nil
	})
	var domain *box.Error
	if !errors.As(err, &domain) || domain.Code != "authentication_required" || domain.Context["reason"] != "credentials_missing" || target.cloneCalls != 1 || connectCalls != 0 {
		t.Fatalf("error = %#v, clone calls = %d, connect calls = %d", err, target.cloneCalls, connectCalls)
	}
	var structured bytes.Buffer
	printError(&structured, err, "json", nil)
	if want := sourceGolden(t, "clone-authentication.json"); structured.String() != want {
		t.Fatalf("structured error = %s, want %s", structured.String(), want)
	}
}

func TestCloneWithRecoveryGuidesHostKeyFailureWithoutRetrying(t *testing.T) {
	target := &cloneRecoveryTarget{name: "work", identity: "11111111-1111-4111-8111-111111111111", cloneErrors: []error{&box.Error{Code: "conflict", Message: "host key changed", Context: map[string]string{"reason": "host_key_changed"}}}}
	_, err := cloneWithRecovery(t.Context(), Streams{}, &globalOptions{output: "human", noInput: true}, target, repository.CloneRequest{Source: "https://github.com/owner/repo.git"}, "", nil)
	var guidance guidanceError
	if !errors.As(err, &guidance) || !strings.Contains(guidance.guidance, "source connect github --box work") || target.cloneCalls != 1 {
		t.Fatalf("error = %v, clone calls = %d", err, target.cloneCalls)
	}
}

func TestCloneWithRecoveryRequiresV2BeforeConnecting(t *testing.T) {
	runtimeUpdate := &box.Error{Code: "authentication_required", Message: "managed recovery requires clone v2", Context: map[string]string{"reason": "host_runtime_update_required"}}
	target := &cloneRecoveryTarget{name: "work", identity: "11111111-1111-4111-8111-111111111111", cloneErrors: []error{runtimeUpdate}}
	connectCalls := 0
	streams := Streams{In: strings.NewReader("y\n"), Out: &bytes.Buffer{}, Err: &bytes.Buffer{}, InIsTerminal: true, OutIsTerminal: true, ErrIsTerminal: true}
	_, err := cloneWithRecovery(t.Context(), streams, &globalOptions{output: "human", accessible: true}, target, repository.CloneRequest{Source: "https://github.com/owner/repo.git"}, "", func(context.Context, source.Target, string) (source.ConnectResult, error) {
		connectCalls++
		return source.ConnectResult{}, nil
	})
	var guidance guidanceError
	if !errors.As(err, &guidance) || !strings.Contains(guidance.guidance, "schooner box update work") || target.cloneCalls != 1 || connectCalls != 0 {
		t.Fatalf("error = %v, clone calls = %d, connect calls = %d", err, target.cloneCalls, connectCalls)
	}
}

func TestCloneWithRecoveryDoesNotPromptForSAMLOrExistingSuccess(t *testing.T) {
	saml := &box.Error{Code: "authentication_required", Message: "SAML required", Context: map[string]string{"reason": "github_saml_sso", "organization": "acme-tools"}}
	for name, target := range map[string]*cloneRecoveryTarget{
		"saml":    {name: "work", identity: "11111111-1111-4111-8111-111111111111", cloneErrors: []error{saml}},
		"success": {name: "work", identity: "11111111-1111-4111-8111-111111111111", cloneResults: []repository.MutationResult{{Action: "clone"}}},
	} {
		t.Run(name, func(t *testing.T) {
			connectCalls := 0
			_, err := cloneWithRecovery(t.Context(), Streams{In: strings.NewReader(""), InIsTerminal: true, OutIsTerminal: true, ErrIsTerminal: true}, &globalOptions{output: "human", accessible: true}, target, repository.CloneRequest{Source: "https://github.com/owner/repo.git"}, "", func(context.Context, source.Target, string) (source.ConnectResult, error) {
				connectCalls++
				return source.ConnectResult{}, nil
			})
			if name == "success" && err != nil {
				t.Fatalf("existing access error = %v", err)
			}
			if name == "saml" && box.ErrorCode(err) != "authentication_required" {
				t.Fatalf("SAML error = %v", err)
			}
			if target.cloneCalls != 1 || connectCalls != 0 {
				t.Fatalf("clone calls = %d, connect calls = %d", target.cloneCalls, connectCalls)
			}
		})
	}
}

func TestCloneWithRecoveryRetriesOnlyOnceAfterConnection(t *testing.T) {
	authentication := box.NewError("authentication_required", "authentication failed", nil)
	target := &cloneRecoveryTarget{name: "work", identity: "11111111-1111-4111-8111-111111111111", cloneErrors: []error{authentication, authentication, nil}}
	streams := Streams{In: strings.NewReader("y\n"), Out: &bytes.Buffer{}, Err: &bytes.Buffer{}, InIsTerminal: true, OutIsTerminal: true, ErrIsTerminal: true}
	_, err := cloneWithRecovery(t.Context(), streams, &globalOptions{output: "human", accessible: true}, target, repository.CloneRequest{Source: "https://github.com/owner/repo.git"}, "", func(context.Context, source.Target, string) (source.ConnectResult, error) {
		return source.ConnectResult{}, nil
	})
	if box.ErrorCode(err) != "authentication_required" || target.cloneCalls != 2 {
		t.Fatalf("error = %v, clone calls = %d", err, target.cloneCalls)
	}
}
