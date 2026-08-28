package boxgit

import (
	"context"
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/thewelshrich/schooner/internal/process"
	"github.com/thewelshrich/schooner/internal/source"
)

type cloneCall struct {
	environment []string
	arguments   []string
}

type cloneResponse struct {
	result process.Result
	err    error
}

type cloneScriptRunner struct {
	delegate  commandRunner
	responses []cloneResponse
	calls     []cloneCall
}

func (runner *cloneScriptRunner) Run(ctx context.Context, environment []string, name string, arguments ...string) (process.Result, error) {
	if name == "ssh-keygen" && runner.delegate != nil {
		return runner.delegate.Run(ctx, environment, name, arguments...)
	}
	runner.calls = append(runner.calls, cloneCall{environment: append([]string(nil), environment...), arguments: append([]string(nil), arguments...)})
	if len(runner.responses) == 0 {
		return process.Result{}, nil
	}
	response := runner.responses[0]
	runner.responses = runner.responses[1:]
	return response.result, response.err
}

func TestCloneTriesGitHubTransportsInOrderAndDisablesPrompts(t *testing.T) {
	manager, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runner := &cloneScriptRunner{responses: []cloneResponse{
		{result: process.Result{Stderr: []byte("fatal: Authentication failed")}, err: errors.New("exit 128")},
		{result: process.Result{Stderr: []byte("git@github.com: Permission denied (publickey).")}, err: errors.New("exit 128")},
		{},
	}}
	manager.run = runner
	prepareCalls := 0
	request := cloneExecution(t, "https://github.com/Owner/Repo.git")
	if err = manager.Clone(t.Context(), request, func() error { prepareCalls++; return nil }); err != nil {
		t.Fatal(err)
	}
	if prepareCalls != 3 || len(runner.calls) != 3 {
		t.Fatalf("prepare calls = %d, Git calls = %d", prepareCalls, len(runner.calls))
	}
	for _, call := range runner.calls {
		for _, required := range []string{"GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never", "SSH_ASKPASS_REQUIRE=never"} {
			if !slices.Contains(call.environment, required) {
				t.Fatalf("environment missing %q: %v", required, call.environment)
			}
		}
	}
	second := strings.Join(runner.calls[1].arguments, "\n")
	if !strings.Contains(second, "url.git@github.com:owner/repo.git.insteadOf=https://github.com/Owner/Repo.git") {
		t.Fatalf("ambient SSH arguments = %v", runner.calls[1].arguments)
	}
	last := strings.Join(runner.calls[2].arguments, "\n")
	if !strings.Contains(last, "credential.helper=") || !strings.Contains(last, "credential.interactive=false") || !strings.Contains(last, "url.https://github.com/owner/repo.git.insteadOf=https://github.com/Owner/Repo.git") {
		t.Fatalf("anonymous HTTPS arguments = %v", runner.calls[2].arguments)
	}
	if environment := strings.Join(runner.calls[2].environment, "\n"); !strings.Contains(environment, "GIT_CONFIG_GLOBAL=/dev/null") || !strings.Contains(environment, "GIT_CONFIG_SYSTEM=/dev/null") || !strings.Contains(environment, "HOME=/dev/null") || !strings.Contains(environment, "XDG_CONFIG_HOME=/dev/null") {
		t.Fatalf("anonymous HTTPS environment = %v", runner.calls[2].environment)
	}
}

func TestCloneStopsImmediatelyForNetworkFailure(t *testing.T) {
	manager, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runner := &cloneScriptRunner{responses: []cloneResponse{{result: process.Result{Stderr: []byte("Could not resolve host: github.com")}, err: errors.New("exit 128")}}}
	manager.run = runner
	prepareCalls := 0
	err = manager.Clone(t.Context(), cloneExecution(t, "git@github.com:owner/repo.git"), func() error { prepareCalls++; return nil })
	if source.ErrorCode(err) != source.CodeSourceUnavailable || prepareCalls != 1 || len(runner.calls) != 1 {
		t.Fatalf("error = %v, prepare calls = %d, Git calls = %d", err, prepareCalls, len(runner.calls))
	}
}

func TestCloneRetainsSAMLClassificationAcrossFallbacks(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen is required")
	}
	manager, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Ensure(t.Context(), testEnsureRequest(t)); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("exit 128")
	manager.run = &cloneScriptRunner{delegate: osRunner{}, responses: []cloneResponse{
		{result: process.Result{Stderr: []byte("Authentication failed")}, err: failure},
		{result: process.Result{Stderr: []byte("Permission denied (publickey)")}, err: failure},
		{result: process.Result{Stderr: []byte("The 'acme-tools' organization has enabled SAML single sign-on")}, err: failure},
		{result: process.Result{Stderr: []byte("Repository not found")}, err: failure},
	}}
	err = manager.Clone(t.Context(), cloneExecution(t, "https://github.com/owner/repo.git"), func() error { return nil })
	var domain *source.Error
	if !errors.As(err, &domain) || domain.Code != "authentication_required" || domain.Context["reason"] != "github_saml_sso" || domain.Context["organization"] != "acme-tools" {
		t.Fatalf("error = %#v", err)
	}
}

func TestCloneDoesNotAttributeAmbientSAMLToManagedKey(t *testing.T) {
	manager, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("exit 128")
	manager.run = &cloneScriptRunner{responses: []cloneResponse{
		{result: process.Result{Stderr: []byte("The 'acme-tools' organization has enabled SAML single sign-on")}, err: failure},
		{result: process.Result{Stderr: []byte("Permission denied (publickey)")}, err: failure},
		{result: process.Result{Stderr: []byte("Repository not found")}, err: failure},
	}}
	err = manager.Clone(t.Context(), cloneExecution(t, "https://github.com/owner/repo.git"), func() error { return nil })
	var domain *source.Error
	if !errors.As(err, &domain) || domain.Code != "authentication_required" || domain.Context["reason"] != "credentials_missing" {
		t.Fatalf("error = %#v", err)
	}
}

func TestCloneDoesNotInferSAMLFromRepositoryName(t *testing.T) {
	for _, repository := range []string{"saml-client", "saml-sso"} {
		t.Run(repository, func(t *testing.T) {
			failure := errors.New("exit 128")
			err := classifyCloneFailure(process.Result{Stderr: []byte("Repository not found: https://github.com/owner/" + repository + ".git")}, failure, true, false)
			var domain *source.Error
			if !errors.As(err, &domain) || domain.Code != "authentication_required" || domain.Context["reason"] == "github_saml_sso" {
				t.Fatalf("error = %#v", err)
			}
		})
	}
}

func TestCloneManagedSSHUsesDedicatedStrictConfiguration(t *testing.T) {
	home := t.TempDir()
	manager, err := New(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Ensure(t.Context(), testEnsureRequest(t)); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("exit 128")
	runner := &cloneScriptRunner{delegate: osRunner{}, responses: []cloneResponse{
		{result: process.Result{Stderr: []byte("Authentication failed")}, err: failure},
		{result: process.Result{Stderr: []byte("Permission denied (publickey)")}, err: failure},
		{},
	}}
	manager.run = runner
	if err = manager.Clone(t.Context(), cloneExecution(t, "https://github.com/owner/repo.git"), func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 3 {
		t.Fatalf("Git calls = %d", len(runner.calls))
	}
	managedEnvironment := strings.Join(runner.calls[2].environment, "\n")
	for _, required := range []string{"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "-F", "'/dev/null'", "IdentitiesOnly=yes", "BatchMode=yes", "StrictHostKeyChecking=yes", "GlobalKnownHostsFile=/dev/null"} {
		if !strings.Contains(managedEnvironment, required) {
			t.Fatalf("managed environment missing %q: %s", required, managedEnvironment)
		}
	}
}

func TestCloneAttributesAmbientHostKeyFailureToBoxSSHConfig(t *testing.T) {
	manager, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("exit 128")
	runner := &cloneScriptRunner{responses: []cloneResponse{
		{result: process.Result{Stderr: []byte("Authentication failed")}, err: failure},
		{result: process.Result{Stderr: []byte("WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!")}, err: failure},
	}}
	manager.run = runner

	err = manager.Clone(t.Context(), cloneExecution(t, "https://github.com/owner/repo.git"), func() error { return nil })
	var domain *source.Error
	if !errors.As(err, &domain) || domain.Code != "conflict" || domain.Context["reason"] != "ambient_host_key_changed" || len(runner.calls) != 2 {
		t.Fatalf("error = %#v, Git calls = %d", err, len(runner.calls))
	}
}

func TestCloneTreatsMissingAmbientGitHubHostTrustAsRecoverableAuthentication(t *testing.T) {
	manager, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("exit 128")
	runner := &cloneScriptRunner{responses: []cloneResponse{
		{result: process.Result{Stderr: []byte("Authentication failed")}, err: failure},
		{result: process.Result{Stderr: []byte("Host key verification failed.")}, err: failure},
		{result: process.Result{Stderr: []byte("fatal: unable to get password from user")}, err: failure},
	}}
	manager.run = runner

	err = manager.Clone(t.Context(), cloneExecution(t, "https://github.com/owner/private-repo.git"), func() error { return nil })
	var domain *source.Error
	if !errors.As(err, &domain) || domain.Code != "authentication_required" || domain.Context["reason"] != "credentials_missing" || len(runner.calls) != 3 {
		t.Fatalf("error = %#v, Git calls = %d", err, len(runner.calls))
	}
}

func TestCloneFailureClassificationStopsForNonAuthenticationCauses(t *testing.T) {
	tests := []struct {
		name    string
		message string
		code    string
		reason  string
		managed bool
	}{
		{name: "filesystem", message: "fatal: could not create work tree dir: Permission denied", code: "permission_denied"},
		{name: "filesystem path containing tls", message: "fatal: could not create work tree dir '/worktrees/tls-client': Permission denied", code: "permission_denied"},
		{name: "filesystem path containing network diagnostic", message: "fatal: could not create work tree dir '/worktrees/failed to connect': Permission denied", code: "permission_denied"},
		{name: "filesystem path containing authentication diagnostic", message: "fatal: could not create work tree dir '/worktrees/access denied': Permission denied", code: "permission_denied"},
		{name: "invalid branch", message: "fatal: Remote branch missing not found in upstream origin", code: "invalid_input"},
		{name: "changed managed host key", message: "WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!", code: "conflict", reason: "host_key_changed", managed: true},
		{name: "missing managed host trust", message: "Host key verification failed.", code: "conflict", reason: "host_key_changed", managed: true},
		{name: "TLS handshake", message: "fatal: unable to access source: TLS handshake timeout", code: source.CodeSourceUnavailable},
		{name: "integrity", message: "fatal: index-pack failed", code: "conflict"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := classifyCloneFailure(process.Result{Stderr: []byte(test.message)}, errors.New("exit 128"), true, test.managed)
			var domain *source.Error
			if !errors.As(err, &domain) || domain.Code != test.code || domain.Context["reason"] != test.reason {
				t.Fatalf("error = %#v", err)
			}
		})
	}
	interrupted := classifyCloneFailure(process.Result{}, context.Canceled, true, false)
	if source.ErrorCode(interrupted) != "outcome_unknown" || !errors.Is(interrupted, context.Canceled) {
		t.Fatalf("cancellation error = %v", interrupted)
	}

	missingAmbientGitHubTrust := classifyCloneFailure(process.Result{Stderr: []byte("Host key verification failed.")}, errors.New("exit 128"), true, false)
	if source.ErrorCode(missingAmbientGitHubTrust) != "authentication_required" {
		t.Fatalf("missing ambient GitHub trust error = %v", missingAmbientGitHubTrust)
	}

	missingNonGitHubAmbientTrust := classifyCloneFailure(process.Result{Stderr: []byte("Host key verification failed.")}, errors.New("exit 128"), false, false)
	var domain *source.Error
	if !errors.As(missingNonGitHubAmbientTrust, &domain) || domain.Code != "conflict" || domain.Context["reason"] != "ambient_host_key_changed" {
		t.Fatalf("missing non-GitHub ambient trust error = %#v", missingNonGitHubAmbientTrust)
	}
}

func cloneExecution(t *testing.T, supplied string) source.CloneExecution {
	t.Helper()
	identity, network, err := source.RepositoryIdentityFor(supplied)
	if err != nil || !network {
		t.Fatalf("identity = %+v, %t, %v", identity, network, err)
	}
	root := t.TempDir()
	return source.CloneExecution{Repository: identity, SuppliedOrigin: supplied, WorktreeRoot: root, Destination: root + "/stage/repo"}
}
