package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"
	"github.com/thewelshrich/schooner/internal/box"
	"github.com/thewelshrich/schooner/internal/boxtarget"
	"github.com/thewelshrich/schooner/internal/source"
	"github.com/thewelshrich/schooner/internal/ui/prompts"
	uitheme "github.com/thewelshrich/schooner/internal/ui/theme"
)

func newSourceCommand(streams Streams, global *globalOptions, targets *boxtarget.Resolver) *cobra.Command {
	command := &cobra.Command{Use: "source", Short: "Manage source-host access for Boxes", Args: cobra.NoArgs, RunE: helpRun}
	command.AddCommand(
		newSourceConnectCommand(streams, global, targets),
		newSourceStatusCommand(streams, global, targets),
		newSourceDisconnectCommand(streams, global, targets),
	)
	return command
}

func newSourceConnectCommand(streams Streams, global *globalOptions, targets *boxtarget.Resolver) *cobra.Command {
	var explicitBox string
	command := &cobra.Command{Use: "connect github", Short: "Connect a Box to private GitHub repositories", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if args[0] != source.GitHub {
			return usageError{cause: fmt.Errorf("unsupported source provider %q", args[0])}
		}
		target, err := resolveBoxExecutionTarget(cmd.Context(), streams, global, targets, explicitBox)
		if err != nil {
			return executionError{cause: err}
		}
		services, closeServices, err := openApplication(cmd.Context(), streams, global)
		if err != nil {
			return executionError{cause: err}
		}
		defer closeServices()
		interactive := interactionAllowed(streams, global)
		authorizationConfirmed := false
		if interactive {
			state, stateErr := services.sources.AuthorizationState(cmd.Context())
			if stateErr != nil {
				return executionError{cause: stateErr}
			}
			confirmed, confirmErr := prompts.ConfirmGitHubConnect(cmd.Context(), promptOptions(streams, global), prompts.GitHubConnectDraft{
				BoxName: target.BoxName(), AccountLogin: state.Account.Login, NeedsDeviceFlow: state.NeedsDeviceFlow,
			})
			if errors.Is(confirmErr, prompts.ErrAborted) {
				return abortError{cause: confirmErr}
			}
			if confirmErr != nil {
				return executionError{cause: confirmErr}
			}
			if !confirmed {
				writeCancelled(streams.Out)
				return nil
			}
			authorizationConfirmed = state.NeedsDeviceFlow
		}
		result, err := services.sources.Connect(cmd.Context(), source.ConnectRequest{
			Target: target, AllowAuthorization: interactive,
			BeforeAuthorization: githubAuthorizationConfirmation(streams, global, target.BoxName(), authorizationConfirmed),
			RunPhase:            githubConnectPhaseRunner(cmd.Context(), streams, global, target.BoxName()),
		})
		if errors.Is(err, prompts.ErrAborted) {
			return abortError{cause: err}
		}
		if err != nil {
			return executionError{cause: withSourceGuidance(err, target.BoxName())}
		}
		if result.Warning != "" && global.output != "json" {
			_ = writeWarningLine(streams.Err, terminalTheme(global, streams), result.Warning)
		}
		return writeSourceConnect(streams.Out, global.output, result, outputTheme(global, streams))
	}}
	command.Flags().StringVar(&explicitBox, "box", "", "box name (always uses OpenSSH)")
	return command
}

func newSourceStatusCommand(streams Streams, global *globalOptions, targets *boxtarget.Resolver) *cobra.Command {
	var explicitBox string
	command := &cobra.Command{Use: "status", Short: "Inspect GitHub source access for a Box", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		target, targetErr := resolveSourceTarget(cmd.Context(), streams, global, targets, explicitBox, true)
		if targetErr != nil {
			return executionError{cause: targetErr}
		}
		services, closeServices, err := openApplication(cmd.Context(), streams, global)
		if err != nil {
			return executionError{cause: err}
		}
		defer closeServices()
		result, err := services.sources.Status(cmd.Context(), source.StatusRequest{Target: target, BoxName: explicitBox})
		if err != nil {
			return executionError{cause: err}
		}
		return writeSourceStatus(streams.Out, global.output, result, outputTheme(global, streams))
	}}
	command.Flags().StringVar(&explicitBox, "box", "", "box name (always uses OpenSSH)")
	return command
}

func newSourceDisconnectCommand(streams Streams, global *globalOptions, targets *boxtarget.Resolver) *cobra.Command {
	var explicitBox string
	var yes bool
	command := &cobra.Command{Use: "disconnect github", Short: "Revoke a Box GitHub key and remove its private key", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if args[0] != source.GitHub {
			return usageError{cause: fmt.Errorf("unsupported source provider %q", args[0])}
		}
		interactive := interactionAllowed(streams, global)
		if !yes && !interactive {
			return usageError{cause: fmt.Errorf("--yes is required when prompts are unavailable")}
		}
		target, targetErr := resolveSourceTarget(cmd.Context(), streams, global, targets, explicitBox, true)
		if targetErr != nil {
			return executionError{cause: targetErr}
		}
		services, closeServices, err := openApplication(cmd.Context(), streams, global)
		if err != nil {
			return executionError{cause: err}
		}
		defer closeServices()
		if !yes {
			draft := prompts.GitHubDisconnectDraft{BoxName: firstNonEmpty(explicitBox, boxNameOf(target))}
			if preview, previewErr := services.sources.PreviewDisconnect(cmd.Context(), source.StatusRequest{Target: target, BoxName: explicitBox}); previewErr == nil {
				draft.BoxName = firstNonEmpty(preview.BoxName, draft.BoxName)
				draft.AccountLogin = preview.Account.Login
				draft.KeyTitle = preview.RemoteKeyTitle
				draft.LastBox = preview.LastBox
			}
			confirmed, confirmErr := prompts.ConfirmGitHubDisconnect(cmd.Context(), promptOptions(streams, global), draft)
			if errors.Is(confirmErr, prompts.ErrAborted) {
				return abortError{cause: confirmErr}
			}
			if confirmErr != nil {
				return executionError{cause: confirmErr}
			}
			if !confirmed {
				writeCancelled(streams.Out)
				return nil
			}
		}
		result, err := services.sources.Disconnect(cmd.Context(), source.DisconnectRequest{
			Target: target, BoxName: explicitBox, AllowAuthorization: interactive,
			BeforeAuthorization: githubAuthorizationConfirmation(streams, global, firstNonEmpty(explicitBox, boxNameOf(target)), false),
			RunPhase:            githubDisconnectPhaseRunner(cmd.Context(), streams, global, firstNonEmpty(explicitBox, boxNameOf(target))),
		})
		if errors.Is(err, prompts.ErrAborted) {
			return abortError{cause: err}
		}
		if err != nil {
			return executionError{cause: err}
		}
		if result.Warning != "" && global.output != "json" {
			_ = writeWarningLine(streams.Err, terminalTheme(global, streams), result.Warning)
		}
		return writeSourceDisconnect(streams.Out, global.output, result, outputTheme(global, streams))
	}}
	command.Flags().StringVar(&explicitBox, "box", "", "box name (always uses OpenSSH)")
	command.Flags().BoolVar(&yes, "yes", false, "confirm GitHub revocation and Box key removal")
	return command
}

func boxNameOf(target source.Target) string {
	if target == nil {
		return ""
	}
	return target.BoxName()
}

func resolveSourceTarget(ctx context.Context, streams Streams, global *globalOptions, targets *boxtarget.Resolver, explicit string, allowFormer bool) (source.Target, error) {
	target, err := resolveBoxExecutionTarget(ctx, streams, global, targets, explicit)
	if err == nil {
		return target, nil
	}
	if allowFormer && explicit != "" && box.ErrorCode(err) == "not_found" {
		return nil, nil
	}
	return nil, err
}

func withSourceGuidance(err error, boxName string) error {
	contextValues := sourceReasonContext(err)
	reason := contextValues["reason"]
	switch reason {
	case "github_saml_sso":
		organization := "your GitHub organization"
		if value := contextValues["organization"]; value != "" {
			organization = "the " + value + " organization"
		}
		return guidanceError{cause: err, guidance: "in GitHub, authorize the SSH key titled `" + githubKeyTitle(boxName) + "` for " + organization + ", then run source connect again"}
	case "host_key_changed":
		return guidanceError{cause: err, guidance: "retry source connect to refresh GitHub host trust; Schooner will not bypass strict host-key checking"}
	case "credentials_missing":
		return guidanceError{cause: err, guidance: "authorize the Schooner GitHub App in an interactive terminal so Schooner can add this Box's public SSH key"}
	case "authorization_denied":
		return guidanceError{cause: err, guidance: "authorize the Schooner GitHub App, then run source connect again"}
	case "device_code_expired":
		return guidanceError{cause: err, guidance: "run source connect again and enter the new device code before it expires"}
	default:
		return err
	}
}

func githubKeyTitle(boxName string) string {
	return "Schooner / " + firstNonEmpty(boxName, "Box")
}

func githubAuthorizationConfirmation(streams Streams, global *globalOptions, boxName string, alreadyConfirmed bool) func(context.Context, source.RemoteAccount) error {
	if global == nil || !interactionAllowed(streams, global) {
		return nil
	}
	confirmed := alreadyConfirmed
	return func(ctx context.Context, account source.RemoteAccount) error {
		if confirmed {
			return nil
		}
		approved, err := prompts.ConfirmGitHubConnect(ctx, promptOptions(streams, global), prompts.GitHubConnectDraft{
			BoxName: boxName, AccountLogin: account.Login, NeedsDeviceFlow: true,
		})
		if err != nil {
			return err
		}
		if !approved {
			return prompts.ErrAborted
		}
		confirmed = true
		return nil
	}
}

func githubConnectPhaseRunner(ctx context.Context, streams Streams, global *globalOptions, boxName string) func(source.ConnectPhase, func() error) error {
	if global == nil || global.output == "json" || !interactionAllowed(streams, global) {
		return nil
	}
	return func(phase source.ConnectPhase, fn func() error) error {
		label := ""
		switch phase {
		case source.ConnectPhaseCreatingKey:
			label = "Creating an SSH key on " + firstNonEmpty(boxName, "the Box") + "…"
		case source.ConnectPhaseRegisteringKey:
			label = "Registering the public key with GitHub…"
		case source.ConnectPhaseVerifying:
			label = "Checking that " + firstNonEmpty(boxName, "the Box") + " can reach GitHub…"
		}
		if label == "" {
			return fn()
		}
		err := prompts.Wait(ctx, promptOptions(streams, global), label, func(context.Context) error { return fn() })
		if errors.Is(err, prompts.ErrAborted) {
			return abortError{cause: err}
		}
		return err
	}
}

func githubDisconnectPhaseRunner(ctx context.Context, streams Streams, global *globalOptions, boxName string) func(source.DisconnectPhase, func() error) error {
	if global == nil || global.output == "json" || !interactionAllowed(streams, global) {
		return nil
	}
	return func(phase source.DisconnectPhase, fn func() error) error {
		label := ""
		switch phase {
		case source.DisconnectPhaseRevokingKey:
			label = "Revoking the Box key from GitHub…"
		case source.DisconnectPhaseRemovingKey:
			label = "Removing the SSH key from " + firstNonEmpty(boxName, "the Box") + "…"
		}
		if label == "" {
			return fn()
		}
		err := prompts.Wait(ctx, promptOptions(streams, global), label, func(context.Context) error { return fn() })
		if errors.Is(err, prompts.ErrAborted) {
			return abortError{cause: err}
		}
		return err
	}
}

func sourceReasonContext(err error) map[string]string {
	var sourceDomain *source.Error
	if errors.As(err, &sourceDomain) {
		return sourceDomain.Context
	}
	var boxDomain *box.Error
	if errors.As(err, &boxDomain) {
		return boxDomain.Context
	}
	return nil
}

type sourceAccountDocument struct {
	ID    string `json:"id,omitempty"`
	Login string `json:"login,omitempty"`
}

type sourceLayerDocument struct {
	State       string `json:"state"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

func writeSourceConnect(w io.Writer, output string, result source.ConnectResult, theme *uitheme.Theme) error {
	if output == "json" {
		document := struct {
			SchemaVersion  string                `json:"schema_version"`
			Provider       string                `json:"provider"`
			Account        sourceAccountDocument `json:"account"`
			BoxName        string                `json:"box_name"`
			BoxIdentity    string                `json:"box_identity"`
			Fingerprint    string                `json:"fingerprint"`
			RemoteKeyID    int64                 `json:"remote_key_id"`
			RemoteKeyTitle string                `json:"remote_key_title"`
			State          string                `json:"state"`
			Local          sourceLayerDocument   `json:"local"`
			Box            sourceLayerDocument   `json:"box"`
			GitHub         sourceLayerDocument   `json:"github"`
			Recovered      bool                  `json:"recovered"`
			Warnings       []string              `json:"warnings"`
		}{
			SchemaVersion: "1", Provider: result.Provider, Account: sourceAccountDocument{result.Account.ID, result.Account.Login},
			BoxName: result.BoxName, BoxIdentity: result.BoxIdentity, Fingerprint: result.Fingerprint,
			RemoteKeyID: result.RemoteKeyID, RemoteKeyTitle: result.RemoteKeyTitle, State: string(result.State),
			Local:     sourceLayerDocument{State: string(result.State), Fingerprint: result.Fingerprint},
			Box:       sourceLayerDocument{State: "present", Fingerprint: result.Fingerprint},
			GitHub:    sourceLayerDocument{State: "present", Fingerprint: result.Fingerprint},
			Recovered: result.Recovered, Warnings: warningList(result.Warning),
		}
		return json.NewEncoder(w).Encode(document)
	}
	if err := writeReadySummary(w, theme, firstNonEmpty(result.BoxName, "Box")+" can use GitHub as "+firstNonEmpty(result.Account.Login, "your account"), []summaryRow{
		{Label: "Box", Value: result.BoxName}, {Label: "Account", Value: result.Account.Login},
		{Label: "SSH key", Value: result.RemoteKeyTitle}, {Label: "Fingerprint", Value: result.Fingerprint},
	}); err != nil {
		return err
	}
	if err := writeExplanation(w, theme, "The private key is on the Box. The GitHub App token is on this machine. GitHub may email you that a new SSH key was added; that is this Box key."); err != nil {
		return err
	}
	return writeMutedNotice(w, theme, "Disconnect later with: schooner source disconnect github --box "+firstNonEmpty(result.BoxName, "<box>"))
}

func writeSourceStatus(w io.Writer, output string, result source.StatusResult, theme *uitheme.Theme) error {
	if output == "json" {
		return json.NewEncoder(w).Encode(struct {
			SchemaVersion  string                `json:"schema_version"`
			Provider       string                `json:"provider"`
			BoxName        string                `json:"box_name,omitempty"`
			BoxIdentity    string                `json:"box_identity,omitempty"`
			State          string                `json:"state"`
			Account        sourceAccountDocument `json:"account"`
			Fingerprint    string                `json:"fingerprint,omitempty"`
			RemoteKeyID    int64                 `json:"remote_key_id,omitempty"`
			RemoteKeyTitle string                `json:"remote_key_title,omitempty"`
			Local          sourceLayerDocument   `json:"local"`
			Box            sourceLayerDocument   `json:"box"`
			GitHub         sourceLayerDocument   `json:"github"`
			Warnings       []string              `json:"warnings"`
		}{"1", result.Provider, result.BoxName, result.BoxIdentity, string(result.State), sourceAccountDocument{result.Account.ID, result.Account.Login}, result.Fingerprint, result.RemoteKeyID, result.RemoteKeyTitle, layerDocument(result.Local), layerDocument(result.Box), layerDocument(result.Remote), result.Warnings})
	}
	rows := []summaryRow{{Label: "Box", Value: firstNonEmpty(result.BoxName, "—")}, {Label: "Status", Value: string(result.State)}, {Label: "Account", Value: firstNonEmpty(result.Account.Login, "—")}}
	if result.Fingerprint != "" {
		rows = append(rows, summaryRow{Label: "Fingerprint", Value: result.Fingerprint})
	}
	if err := writeActionSummary(w, theme, "GitHub source access", rows); err != nil {
		return err
	}
	for _, warning := range result.Warnings {
		if err := writeWarningLine(w, theme, warning); err != nil {
			return err
		}
	}
	return nil
}

func writeSourceDisconnect(w io.Writer, output string, result source.DisconnectResult, theme *uitheme.Theme) error {
	if output == "json" {
		document := struct {
			SchemaVersion   string                `json:"schema_version"`
			Provider        string                `json:"provider"`
			Account         sourceAccountDocument `json:"account"`
			BoxName         string                `json:"box_name"`
			BoxIdentity     string                `json:"box_identity"`
			State           string                `json:"state"`
			Fingerprint     string                `json:"fingerprint,omitempty"`
			RemoteKeyID     int64                 `json:"remote_key_id,omitempty"`
			RemoteKeyTitle  string                `json:"remote_key_title,omitempty"`
			Local           sourceLayerDocument   `json:"local"`
			Box             sourceLayerDocument   `json:"box"`
			GitHub          sourceLayerDocument   `json:"github"`
			Revoked         bool                  `json:"revoked"`
			BoxFilesRemoved bool                  `json:"box_files_removed"`
			CleanupPending  bool                  `json:"cleanup_pending"`
			AccountRemoved  bool                  `json:"account_removed"`
			Warnings        []string              `json:"warnings"`
		}{
			SchemaVersion: "1", Provider: result.Provider, Account: sourceAccountDocument{result.Account.ID, result.Account.Login},
			BoxName: result.BoxName, BoxIdentity: result.BoxIdentity, State: string(result.State), Fingerprint: result.Fingerprint,
			RemoteKeyID: result.RemoteKeyID, RemoteKeyTitle: result.RemoteKeyTitle,
			Local: layerDocument(result.Local), Box: layerDocument(result.Box), GitHub: layerDocument(result.Remote),
			Revoked: result.Revoked, BoxFilesRemoved: result.BoxFilesRemoved, CleanupPending: result.CleanupPending,
			AccountRemoved: result.AccountRemoved, Warnings: warningList(result.Warning),
		}
		return json.NewEncoder(w).Encode(document)
	}
	status := "Disconnected"
	if result.CleanupPending {
		status = "GitHub revoked; Box cleanup pending"
	}
	return writeReadySummary(w, theme, "GitHub source access", []summaryRow{{Label: "Box", Value: firstNonEmpty(result.BoxName, "—")}, {Label: "Status", Value: status}})
}

func layerDocument(value source.LayerObservation) sourceLayerDocument {
	return sourceLayerDocument{State: value.State, Fingerprint: value.Fingerprint}
}

func warningList(value string) []string {
	if value == "" {
		return []string{}
	}
	return []string{value}
}
