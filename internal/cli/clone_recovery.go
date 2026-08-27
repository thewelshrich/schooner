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
		return repository.MutationResult{}, withCloneSourceGuidance(err, target.BoxName())
	}
	identity, network, identityErr := source.RepositoryIdentityFor(request.Source)
	if identityErr != nil || !network || !identity.IsGitHub() {
		return repository.MutationResult{}, err
	}
	if cloneErrorReason(err) == "github_saml_sso" || cloneErrorReason(err) == "host_runtime_update_required" {
		return repository.MutationResult{}, withCloneSourceGuidance(err, target.BoxName())
	}
	if !interactionAllowed(streams, global) {
		return repository.MutationResult{}, withCloneSourceGuidance(err, target.BoxName())
	}

	confirmed, promptErr := prompts.Confirm(ctx, promptOptions(streams, global), "Connect this Box to GitHub and retry the clone?", "Connect and retry", "Cancel")
	if errors.Is(promptErr, prompts.ErrAborted) {
		return repository.MutationResult{}, abortError{cause: promptErr}
	}
	if promptErr != nil {
		return repository.MutationResult{}, promptErr
	}
	if !confirmed {
		return repository.MutationResult{}, withCloneSourceGuidance(err, target.BoxName())
	}
	if connector == nil {
		connector = func(connectCtx context.Context, connectTarget source.Target, repositoryURL string) (source.ConnectResult, error) {
			services, closeServices, openErr := openApplication(connectCtx, streams, global.build)
			if openErr != nil {
				return source.ConnectResult{}, openErr
			}
			defer closeServices()
			return services.sources.Connect(connectCtx, source.ConnectRequest{Target: connectTarget, AllowAuthorization: true, Repository: repositoryURL})
		}
	}
	connected, connectErr := connector(ctx, target, identity.CanonicalSSH())
	if connectErr != nil {
		return repository.MutationResult{}, withSourceGuidance(connectErr, target.BoxName())
	}
	if connected.Warning != "" && global.output != "json" {
		_ = writeWarningLine(streams.Err, terminalTheme(global, streams), connected.Warning)
	}
	result, err = cloneAttempt(ctx, streams, global, target, request, waitLabel)
	return result, withCloneSourceGuidance(ensureCloneAuthenticationReason(err), target.BoxName())
}

func cloneAttempt(ctx context.Context, streams Streams, global *globalOptions, target cloneExecutionTarget, request repository.CloneRequest, waitLabel string) (result repository.MutationResult, err error) {
	if waitLabel == "" || !interactionAllowed(streams, global) {
		return target.CloneRepository(ctx, request)
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

func withCloneSourceGuidance(err error, boxName string) error {
	if err == nil {
		return nil
	}
	switch cloneErrorReason(err) {
	case "github_saml_sso":
		organization := "your GitHub organization"
		if value := sourceReasonContext(err)["organization"]; value != "" {
			organization = "the " + value + " GitHub organization"
		}
		return guidanceError{cause: err, guidance: "authorize the `Schooner / " + firstNonEmpty(boxName, "Box") + "` SSH key for " + organization + "'s SAML SSO, then retry"}
	case "host_key_changed":
		return guidanceError{cause: err, guidance: "run `schooner source connect github --box " + firstNonEmpty(boxName, "<box>") + "` to refresh managed GitHub host trust"}
	case "host_runtime_update_required":
		return guidanceError{cause: err, guidance: "run `schooner box update " + firstNonEmpty(boxName, "<box>") + "` to add managed GitHub clone support, then retry"}
	case "credentials_missing":
		return guidanceError{cause: err, guidance: "run `schooner source connect github --box " + firstNonEmpty(boxName, "<box>") + "`, then retry"}
	default:
		return err
	}
}
