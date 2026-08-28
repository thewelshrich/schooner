package cli

import (
	"context"
	"errors"

	"github.com/thewelshrich/schooner/internal/box"
	"github.com/thewelshrich/schooner/internal/repository"
	"github.com/thewelshrich/schooner/internal/source"
	"github.com/thewelshrich/schooner/internal/ui/prompts"
)

type cloneExecutionTarget interface {
	source.Target
	CloneRepository(context.Context, repository.CloneRequest) (repository.MutationResult, error)
}

type cloneSourceConnector func(context.Context, source.Target, string) (source.ConnectResult, error)

func cloneWithRecovery(ctx context.Context, streams Streams, global *globalOptions, target cloneExecutionTarget, request repository.CloneRequest, waitLabel string, connector cloneSourceConnector) (repository.MutationResult, error) {
	result, err := cloneAttempt(ctx, streams, global, target, request, waitLabel)
	if err == nil {
		return result, nil
	}
	err = ensureCloneAuthenticationReason(err)
	if box.ErrorCode(err) != "authentication_required" {
		return repository.MutationResult{}, withCloneSourceGuidance(err, target.BoxName(), request.Source)
	}
	identity, network, identityErr := source.RepositoryIdentityFor(request.Source)
	if identityErr == nil && !network {
		identity, network = source.GitHubIdentityForShorthand(request.Source)
	}
	if identityErr != nil || !network || !identity.IsGitHub() {
		return repository.MutationResult{}, err
	}
	if cloneErrorReason(err) == "github_saml_sso" || cloneErrorReason(err) == "host_runtime_update_required" {
		return repository.MutationResult{}, withCloneSourceGuidance(err, target.BoxName(), request.Source)
	}
	if !interactionAllowed(streams, global) {
		return repository.MutationResult{}, withCloneSourceGuidance(err, target.BoxName(), request.Source)
	}

	repositoryLabel := identity.Owner + "/" + identity.Repository
	added, promptErr := prompts.ConfirmGitHubCloneRecovery(ctx, promptOptions(streams, global), target.BoxName(), repositoryLabel)
	if errors.Is(promptErr, prompts.ErrAborted) {
		return repository.MutationResult{}, abortError{cause: promptErr}
	}
	if promptErr != nil {
		return repository.MutationResult{}, promptErr
	}
	if !added {
		return repository.MutationResult{}, guidanceError{
			cause:    err,
			guidance: "configure Git or SSH on the Box, then retry. To add a dedicated Box key later, run `schooner source connect github --box " + firstNonEmpty(target.BoxName(), "<box>") + "`",
		}
	}

	draft := prompts.GitHubConnectDraft{BoxName: target.BoxName(), NeedsDeviceFlow: true}
	var beforeAuthorization func(context.Context, source.RemoteAccount) error
	if connector == nil {
		services, closeServices, openErr := openApplication(ctx, streams, global)
		if openErr != nil {
			return repository.MutationResult{}, openErr
		}
		defer closeServices()
		if state, stateErr := services.sources.AuthorizationState(ctx); stateErr == nil {
			draft.NeedsDeviceFlow = state.NeedsDeviceFlow
			draft.AccountLogin = state.Account.Login
		}
		connector = func(connectCtx context.Context, connectTarget source.Target, repositoryURL string) (source.ConnectResult, error) {
			return services.sources.Connect(connectCtx, source.ConnectRequest{
				Target: connectTarget, AllowAuthorization: true, Repository: repositoryURL,
				BeforeAuthorization: beforeAuthorization,
				RunPhase:            githubConnectPhaseRunner(connectCtx, streams, global, connectTarget.BoxName()),
			})
		}
	}
	confirmed, confirmErr := prompts.ConfirmGitHubConnect(ctx, promptOptions(streams, global), draft)
	if errors.Is(confirmErr, prompts.ErrAborted) {
		return repository.MutationResult{}, abortError{cause: confirmErr}
	}
	if confirmErr != nil {
		return repository.MutationResult{}, confirmErr
	}
	if !confirmed {
		return repository.MutationResult{}, abortError{cause: prompts.ErrAborted}
	}
	beforeAuthorization = githubAuthorizationConfirmation(streams, global, target.BoxName(), draft.NeedsDeviceFlow)

	connected, connectErr := connector(ctx, target, identity.CanonicalSSH())
	if errors.Is(connectErr, prompts.ErrAborted) {
		return repository.MutationResult{}, abortError{cause: connectErr}
	}
	if connectErr != nil {
		return repository.MutationResult{}, withSourceGuidance(connectErr, target.BoxName())
	}
	if connected.Warning != "" && global.output != "json" {
		_ = writeWarningLine(streams.Err, terminalTheme(global, streams), connected.Warning)
	}
	result, err = cloneAttempt(ctx, streams, global, target, request, waitLabel)
	return result, withCloneSourceGuidance(ensureCloneAuthenticationReason(err), target.BoxName(), request.Source)
}

func cloneAttempt(ctx context.Context, streams Streams, global *globalOptions, target cloneExecutionTarget, request repository.CloneRequest, waitLabel string) (result repository.MutationResult, err error) {
	if !interactionAllowed(streams, global) {
		return target.CloneRepository(ctx, request)
	}
	if waitLabel == "" {
		waitLabel = "Cloning onto " + firstNonEmpty(target.BoxName(), "the Box")
	}
	err = prompts.Wait(ctx, promptOptions(streams, global), waitLabel, func(waitCtx context.Context) error {
		result, err = target.CloneRepository(waitCtx, request)
		return err
	})
	if errors.Is(err, prompts.ErrAborted) {
		return repository.MutationResult{}, abortError{cause: err}
	}
	return result, err
}

func ensureCloneAuthenticationReason(err error) error {
	if err == nil || box.ErrorCode(err) != "authentication_required" {
		return err
	}
	var domain *box.Error
	if !errors.As(err, &domain) {
		return err
	}
	contextValues := make(map[string]string, len(domain.Context)+1)
	for key, value := range domain.Context {
		contextValues[key] = value
	}
	if contextValues["reason"] == "" {
		contextValues["reason"] = "credentials_missing"
	}
	return &box.Error{Code: domain.Code, Message: domain.Message, Context: contextValues, Cause: err}
}

func cloneErrorReason(err error) string {
	return sourceReasonContext(err)["reason"]
}

func withCloneSourceGuidance(err error, boxName, repositoryURL string) error {
	if err == nil {
		return nil
	}
	switch cloneErrorReason(err) {
	case "github_saml_sso":
		organization := "your GitHub organization"
		if value := sourceReasonContext(err)["organization"]; value != "" {
			organization = "the " + value + " organization"
		}
		return guidanceError{cause: err, guidance: "in GitHub, authorize the SSH key titled `" + githubKeyTitle(boxName) + "` for " + organization + ", then retry"}
	case "host_key_changed":
		return guidanceError{cause: err, guidance: "run `schooner source connect github --box " + firstNonEmpty(boxName, "<box>") + "` to refresh managed GitHub host trust"}
	case "ambient_host_key_changed":
		host := "the repository host"
		if identity, network, identityErr := source.RepositoryIdentityFor(repositoryURL); identityErr == nil && network && identity.Host != "" {
			host = identity.Host
		}
		return guidanceError{cause: err, guidance: "inspect and repair the Box user's SSH known_hosts entry for " + host + ", then retry"}
	case "host_runtime_update_required":
		return guidanceError{cause: err, guidance: "run `schooner box update " + firstNonEmpty(boxName, "<box>") + "` to add managed GitHub clone support, then retry"}
	case "credentials_missing":
		return guidanceError{cause: err, guidance: "run `schooner source connect github --box " + firstNonEmpty(boxName, "<box>") + "` in an interactive terminal to authorize the Schooner GitHub App, then retry"}
	default:
		return err
	}
}
