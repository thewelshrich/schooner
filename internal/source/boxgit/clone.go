package boxgit

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/thewelshrich/schooner/internal/process"
	"github.com/thewelshrich/schooner/internal/source"
)

type cloneTransport struct {
	url       string
	managed   bool
	anonymous bool
}

// Clone tries transport candidates in provider-defined order. It never owns
// the destination: prepare is supplied by the repository lifecycle and resets
// only that lifecycle's operation-owned staging area.
func (m *Manager) Clone(ctx context.Context, request source.CloneExecution, prepare source.PrepareCloneAttempt) error {
	if err := source.ValidateCloneExecution(request); err != nil || prepare == nil || !filepath.IsAbs(request.WorktreeRoot) || filepath.Clean(request.WorktreeRoot) != request.WorktreeRoot || !filepath.IsAbs(request.Destination) || filepath.Clean(request.Destination) != request.Destination {
		return source.NewError("invalid_input", "source clone execution is invalid", err)
	}
	candidates := []cloneTransport{{url: request.SuppliedOrigin}}
	if request.Repository.IsGitHub() {
		sshURL := request.Repository.CanonicalSSH()
		if sshURL != request.SuppliedOrigin {
			candidates = append(candidates, cloneTransport{url: sshURL})
		}
		candidates = append(candidates,
			cloneTransport{url: sshURL, managed: true},
			cloneTransport{url: request.Repository.CanonicalHTTPS(), anonymous: true},
		)
	}

	var lastAuthentication error
	var samlAuthentication error
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return err
		}
		environment := ambientCloneEnvironment()
		if candidate.managed {
			identity, err := m.Inspect(ctx, source.GitHub)
			if err != nil {
				return err
			}
			if !identity.Exists {
				continue
			}
			if !identity.TrustConfigured {
				return &source.Error{Code: "conflict", Message: "managed GitHub SSH host trust is missing", Context: map[string]string{"reason": "host_key_changed"}}
			}
			environment = managedCloneEnvironment(m.paths())
		} else if candidate.anonymous {
			environment = anonymousCloneEnvironment()
		}
		if err := prepare(); err != nil {
			return err
		}
		arguments := cloneArguments(request, candidate)
		result, runErr := m.run.Run(ctx, environment, "git", arguments...)
		if runErr == nil {
			return nil
		}
		classified := classifyCloneFailure(result, runErr, request.Repository.IsGitHub())
		if source.ErrorCode(classified) != "authentication_required" {
			return classified
		}
		lastAuthentication = classified
		var domain *source.Error
		if candidate.managed && errors.As(classified, &domain) && domain.Context["reason"] == "github_saml_sso" {
			samlAuthentication = classified
		}
	}
	if samlAuthentication != nil {
		return samlAuthentication
	}
	if lastAuthentication != nil {
		var domain *source.Error
		if errors.As(lastAuthentication, &domain) && domain.Context["reason"] != "" {
			return lastAuthentication
		}
	}
	return &source.Error{Code: "authentication_required", Message: "repository authentication is required", Context: map[string]string{"reason": "credentials_missing"}, Cause: lastAuthentication}
}

func cloneArguments(request source.CloneExecution, candidate cloneTransport) []string {
	arguments := []string{"--no-optional-locks", "-c", "core.fsmonitor=false"}
	if candidate.url != request.SuppliedOrigin {
		arguments = append(arguments, "-c", "url."+candidate.url+".insteadOf="+request.SuppliedOrigin)
	}
	if candidate.anonymous {
		arguments = append(arguments, "-c", "credential.helper=", "-c", "credential.interactive=false")
	}
	arguments = append(arguments, "-C", request.WorktreeRoot, "clone", "-c", "remote.origin.url="+request.SuppliedOrigin)
	if request.Branch != "" {
		arguments = append(arguments, "--branch", request.Branch)
	}
	return append(arguments, "--", request.SuppliedOrigin, request.Destination)
}

func ambientCloneEnvironment() []string {
	return []string{
		"LC_ALL=C", "LANG=C", "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never", "SSH_ASKPASS_REQUIRE=never",
		"GIT_SSH_COMMAND=ssh -o BatchMode=yes", "GIT_SSH_VARIANT=ssh",
	}
}

func managedCloneEnvironment(paths identityPaths) []string {
	return []string{
		"LC_ALL=C", "LANG=C", "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never", "SSH_ASKPASS_REQUIRE=never",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_SSH_COMMAND=" + managedSSHCommand(paths), "GIT_SSH_VARIANT=ssh",
	}
}

func anonymousCloneEnvironment() []string {
	return append(ambientCloneEnvironment(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"HOME=/dev/null", "XDG_CONFIG_HOME=/dev/null",
	)
}

func classifyCloneFailure(result process.Result, cause error, github bool) error {
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		return source.NewError("outcome_unknown", "Git clone was interrupted; retry to reconcile it", cause)
	}
	message := strings.ToLower(string(result.Stdout) + "\n" + string(result.Stderr))
	if github && githubSAMLDiagnostic(message) {
		contextValues := map[string]string{"reason": "github_saml_sso"}
		if organization := githubOrganization(message); organization != "" {
			contextValues["organization"] = organization
		}
		return &source.Error{Code: "authentication_required", Message: "GitHub organization SAML SSO must authorize this Box SSH key", Context: contextValues, Cause: cause}
	}
	if strings.Contains(message, "host key verification failed") || strings.Contains(message, "remote host identification has changed") || strings.Contains(message, "offending") && strings.Contains(message, "known_hosts") {
		safe := "repository SSH host-key verification failed"
		if github {
			safe = "GitHub SSH host-key verification failed"
		}
		return &source.Error{Code: "conflict", Message: safe, Context: map[string]string{"reason": "host_key_changed"}, Cause: cause}
	}
	if strings.Contains(message, "remote branch") && strings.Contains(message, "not found") || strings.Contains(message, "invalid refspec") {
		return source.NewError("invalid_input", "Git branch or tag was not found at the source", cause)
	}
	if authenticationShaped(message, github) {
		return source.NewError("authentication_required", "repository authentication failed using available Box credentials", cause)
	}
	if networkShaped(message) {
		return source.NewError(source.CodeSourceUnavailable, "repository source could not be reached", cause)
	}
	if filesystemShaped(message) {
		return source.NewError("permission_denied", "Git clone was denied by the Box filesystem", cause)
	}
	if result.Truncated {
		return source.NewError("outcome_unknown", "Git clone failed after producing bounded diagnostics; retry to reconcile it", cause)
	}
	return source.NewError("conflict", "Git clone failed for a non-authentication reason", cause)
}

func authenticationShaped(message string, github bool) bool {
	for _, fragment := range []string{
		"authentication failed", "permission denied (publickey", "could not read username", "terminal prompts disabled",
		"invalid username or password", "access denied", "http 401", "http 403", "returned error: 401", "returned error: 403",
	} {
		if strings.Contains(message, fragment) {
			return true
		}
	}
	return github && (strings.Contains(message, "repository not found") || strings.Contains(message, "not found") && strings.Contains(message, "github.com"))
}

func networkShaped(message string) bool {
	for _, fragment := range []string{
		"could not resolve host", "could not resolve hostname", "network is unreachable", "connection timed out",
		"connection refused", "failed to connect", "tls handshake", "tls connect error", "tlsv1 alert",
		"gnutls_handshake", "ssl_connect", "ssl certificate", "certificate verify failed", "connection reset", "no route to host",
	} {
		if strings.Contains(message, fragment) {
			return true
		}
	}
	return false
}

func filesystemShaped(message string) bool {
	for _, fragment := range []string{"permission denied", "no space left", "read-only file system", "could not create work tree dir", "unable to create directory"} {
		if strings.Contains(message, fragment) {
			return true
		}
	}
	return false
}
