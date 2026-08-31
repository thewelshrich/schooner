package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/spf13/cobra"
	"github.com/thewelshrich/schooner/internal/box"
	"github.com/thewelshrich/schooner/internal/boxtarget"
	"github.com/thewelshrich/schooner/internal/link"
	"github.com/thewelshrich/schooner/internal/repository"
	"github.com/thewelshrich/schooner/internal/session"
	"github.com/thewelshrich/schooner/internal/ui/prompts"
	uitheme "github.com/thewelshrich/schooner/internal/ui/theme"
	"github.com/thewelshrich/schooner/internal/workcontext"
)

func newSessionCommands(streams Streams, global *globalOptions, targets *boxtarget.Resolver) []*cobra.Command {
	return []*cobra.Command{
		newStartSessionCommand(streams, global, targets),
		newResumeSessionCommand(streams, global, targets),
		newSessionsCommand(streams, global, targets),
		newSessionLogsCommand(streams, global, targets),
		newStopSessionCommand(streams, global, targets),
		newWorktreeShellCommand(streams, global, targets),
	}
}

func newStartSessionCommand(streams Streams, global *globalOptions, targets *boxtarget.Resolver) *cobra.Command {
	var explicitBox string
	command := &cobra.Command{Use: "start [worktree-path]", Short: "Open persistent work for a Repository", Long: "Open persistent work for a Repository.\n\nSchooner uses the current local Repository as context. With an explicit Worktree path, it opens that exact Worktree. Without one, it uses a matching remote Repository or offers to clone the local origin. Starting is idempotent: if the Worktree already has a managed live Session, Schooner reuses it.", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireInteractiveTerminal(streams, global, "start"); err != nil {
			return err
		}
		var local *repository.LocalCheckout
		var linkedValue link.LocalLink
		var hasLink bool
		var err error
		if sessionSelectorOmitted(args) {
			local, err = inspectCurrentCheckout(cmd.Context())
			if err == nil {
				linkedValue, hasLink, err = currentLocalLink(cmd.Context(), local)
			}
			if err != nil {
				return executionError{cause: normalizePushError(err)}
			}
		}
		linkedBoxID := ""
		if hasLink {
			linkedBoxID = linkedValue.BoxID
		}
		target, err := resolveBoxExecutionTargetLinked(cmd.Context(), streams, global, targets, explicitBox, linkedBoxID)
		if err != nil {
			if hasLink && explicitBox == "" && box.ErrorCode(err) == "not_found" {
				err = normalizePushError(&link.Error{Code: link.CodeStale, Message: "the Local Link refers to a Box that is no longer configured"})
			}
			return executionError{cause: err}
		}
		selector := firstArgument(args)
		if sessionSelectorOmitted(args) {
			if hasLink && target.BoxID() == linkedValue.BoxID {
				selector, err = validateLinkedContext(cmd.Context(), target, linkedValue)
			} else {
				selector, err = resolveContextualStart(cmd.Context(), streams, global, target)
			}
			if err != nil {
				return err
			}
		}
		result, err := startSessionOnTarget(cmd.Context(), target, selector)
		if err != nil {
			return executionError{cause: err}
		}
		worktreeLabel := defaultString(result.Session.WorktreeRelativePath, result.Session.WorktreePath)
		_ = writeReadySummary(streams.Err, terminalTheme(global, streams), "Session ready", []summaryRow{
			{Label: "Box", Value: targetBoxLabel(target)},
			{Label: "Worktree", Value: worktreeLabel},
			{Label: "Session", Value: result.Session.ID},
		})
		attachResult, err := resumeSessionOnTarget(cmd.Context(), streams, target, result.Session.ID)
		if err != nil {
			message := fmt.Errorf("Session %s remains running; resume it after fixing the connection: %w", result.Session.ID, err)
			if attachResult.DiagnosticsReported {
				_, _ = fmt.Fprintf(streams.Err, "Session %s remains running; resume it after fixing the connection.\n", result.Session.ID)
				return reportedExecutionError{cause: message}
			}
			return executionError{cause: message}
		}
		if attachResult.ExitCode != 0 {
			_, _ = fmt.Fprintf(streams.Err, "Session %s remains running; resume it after fixing the terminal handoff.\n", result.Session.ID)
			return exitStatusError{code: attachResult.ExitCode}
		}
		return nil
	}}
	command.Flags().StringVar(&explicitBox, "box", "", "box name (always uses OpenSSH)")
	return command
}

func newResumeSessionCommand(streams Streams, global *globalOptions, targets *boxtarget.Resolver) *cobra.Command {
	var explicitBox string
	command := &cobra.Command{Use: "resume [worktree-path-or-session-id]", Short: "Return to an existing live Session", Long: "Return to an existing live Session using the current local Repository as context.\n\nWith an explicit Worktree path or Session ID, Schooner resumes that exact Session. Without one, it resumes only a Session matching the current local Repository; outside a local Repository, it resumes the newest managed live Session on the Box. Resume never creates a Session.", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireInteractiveTerminal(streams, global, "resume"); err != nil {
			return err
		}
		var local *repository.LocalCheckout
		var linkedValue link.LocalLink
		var hasLink bool
		var err error
		if sessionSelectorOmitted(args) {
			local, err = inspectCurrentCheckout(cmd.Context())
			if err == nil {
				linkedValue, hasLink, err = currentLocalLink(cmd.Context(), local)
			}
			if err != nil {
				return executionError{cause: normalizePushError(err)}
			}
		}
		linkedBoxID := ""
		if hasLink {
			linkedBoxID = linkedValue.BoxID
		}
		target, err := resolveBoxExecutionTargetLinked(cmd.Context(), streams, global, targets, explicitBox, linkedBoxID)
		if err != nil {
			if hasLink && explicitBox == "" && box.ErrorCode(err) == "not_found" {
				err = normalizePushError(&link.Error{Code: link.CodeStale, Message: "the Local Link refers to a Box that is no longer configured"})
			}
			return executionError{cause: err}
		}
		selector := firstArgument(args)
		if sessionSelectorOmitted(args) {
			if hasLink && target.BoxID() == linkedValue.BoxID {
				selector, err = resolveLinkedResume(cmd.Context(), target, linkedValue)
			} else {
				selector, err = resolveContextualResume(cmd.Context(), streams, global, target)
			}
			if err != nil {
				return err
			}
		}
		result, err := resumeSessionOnTarget(cmd.Context(), streams, target, selector)
		if err != nil {
			if result.DiagnosticsReported {
				return reportedExecutionError{cause: err}
			}
			return executionError{cause: err}
		}
		if result.ExitCode != 0 {
			return exitStatusError{code: result.ExitCode}
		}
		return nil
	}}
	command.Flags().StringVar(&explicitBox, "box", "", "box name (always uses OpenSSH)")
	return command
}

func validateLinkedContext(ctx context.Context, target boxtarget.Target, value link.LocalLink) (string, error) {
	if target.BoxIdentity() != value.ExpectedBoxIdentity {
		return "", executionError{cause: normalizePushError(&link.Error{Code: link.CodeStale, Message: "the Local Link's Box identity no longer matches the selected Box"})}
	}
	inspection, err := target.InspectWorktree(ctx, value.RemoteWorktree)
	if err != nil {
		if box.ErrorCode(err) == "not_found" {
			return "", executionError{cause: normalizePushError(&link.Error{Code: link.CodeStale, Message: "the linked remote Worktree no longer exists"})}
		}
		return "", executionError{cause: err}
	}
	observedIdentity := repository.OriginKey(inspection.Repository.Origin)
	if value.RepositoryIdentity != "" && observedIdentity != value.RepositoryIdentity {
		return "", executionError{cause: normalizePushError(&link.Error{Code: link.CodeStale, Message: "the linked remote Worktree now belongs to a different Repository"})}
	}
	return inspection.Worktree.Path, nil
}

func resolveLinkedResume(ctx context.Context, target boxtarget.Target, value link.LocalLink) (string, error) {
	worktree, err := validateLinkedContext(ctx, target, value)
	if err != nil {
		return "", err
	}
	catalog, err := target.ListSessions(ctx)
	if err != nil {
		return "", executionError{cause: err}
	}
	for _, candidate := range catalog.Sessions {
		if candidate.Ownership == session.Managed && candidate.Association == session.AssociationLive && candidate.WorktreePath == worktree && candidate.ID != "" {
			return candidate.ID, nil
		}
	}
	return "", contextualUnavailable("no managed live Session exists for the linked remote Worktree", "run `schooner start` to open persistent work in the linked Worktree")
}

func newSessionsCommand(streams Streams, global *globalOptions, targets *boxtarget.Resolver) *cobra.Command {
	var explicitBox string
	command := &cobra.Command{Use: "sessions", Short: "List live managed and unmanaged tmux Sessions", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		target, err := resolveBoxExecutionTarget(cmd.Context(), streams, global, targets, explicitBox)
		if err != nil {
			return executionError{cause: err}
		}
		catalog, err := listSessionsOnTarget(cmd.Context(), target)
		if err != nil {
			return executionError{cause: err}
		}
		return writeSessions(cmd.OutOrStdout(), global.output, catalog, outputTheme(global, streams))
	}}
	command.Flags().StringVar(&explicitBox, "box", "", "box name (always uses OpenSSH)")
	return command
}

func newSessionLogsCommand(streams Streams, global *globalOptions, targets *boxtarget.Resolver) *cobra.Command {
	var explicitBox string
	var lines int
	command := &cobra.Command{Use: "logs [session-id]", Short: "Capture bounded history from a managed Session", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		target, err := resolveBoxExecutionTarget(cmd.Context(), streams, global, targets, explicitBox)
		if err != nil {
			return executionError{cause: err}
		}
		id := firstArgument(args)
		if sessionSelectorOmitted(args) {
			catalog, listErr := listSessionsOnTarget(cmd.Context(), target)
			if listErr != nil {
				return executionError{cause: listErr}
			}
			id, err = chooseSession(cmd.Context(), streams, global, catalog, sessionChoiceManaged, "Choose a Session for logs")
			if err != nil {
				return err
			}
		}
		result, err := logsOnTarget(cmd.Context(), target, id, lines)
		if err != nil {
			return executionError{cause: err}
		}
		return writeSessionLogs(cmd.OutOrStdout(), global.output, result)
	}}
	command.Flags().StringVar(&explicitBox, "box", "", "box name (always uses OpenSSH)")
	command.Flags().IntVar(&lines, "lines", session.DefaultLogLines, "history lines to capture (1-2000)")
	return command
}

func newStopSessionCommand(streams Streams, global *globalOptions, targets *boxtarget.Resolver) *cobra.Command {
	var explicitBox string
	command := &cobra.Command{Use: "stop [session-id]", Short: "Stop a managed Session without changing its Worktree", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		target, err := resolveBoxExecutionTarget(cmd.Context(), streams, global, targets, explicitBox)
		if err != nil {
			return executionError{cause: err}
		}
		id := firstArgument(args)
		if sessionSelectorOmitted(args) {
			if !interactionAllowed(streams, global) {
				return usageError{cause: fmt.Errorf("a managed Session ID is required when prompts are unavailable")}
			}
			catalog, listErr := listSessionsOnTarget(cmd.Context(), target)
			if listErr != nil {
				return executionError{cause: listErr}
			}
			id, err = chooseSession(cmd.Context(), streams, global, catalog, sessionChoiceManaged, "Choose a Session to stop")
			if err != nil {
				return err
			}
			confirmed, confirmErr := prompts.Confirm(cmd.Context(), promptOptions(streams, global), "Stop this Session?", "Stop", "Keep running")
			if errors.Is(confirmErr, prompts.ErrAborted) {
				return abortError{cause: confirmErr}
			}
			if confirmErr != nil {
				return executionError{cause: confirmErr}
			}
			if !confirmed {
				return abortError{cause: prompts.ErrAborted}
			}
		}
		result, err := stopOnTarget(cmd.Context(), target, id)
		if err != nil {
			return executionError{cause: err}
		}
		return writeSessionStop(cmd.OutOrStdout(), global.output, result, outputTheme(global, streams))
	}}
	command.Flags().StringVar(&explicitBox, "box", "", "box name (always uses OpenSSH)")
	return command
}

func newWorktreeShellCommand(streams Streams, global *globalOptions, targets *boxtarget.Resolver) *cobra.Command {
	var explicitBox string
	command := &cobra.Command{Use: "shell [worktree-path]", Short: "Open an ephemeral shell in a live Worktree", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireInteractiveTerminal(streams, global, "shell"); err != nil {
			return err
		}
		target, err := resolveBoxExecutionTarget(cmd.Context(), streams, global, targets, explicitBox)
		if err != nil {
			return executionError{cause: err}
		}
		selector := firstArgument(args)
		if sessionSelectorOmitted(args) {
			selector, err = chooseWorktree(cmd.Context(), streams, global, target, "Choose a Worktree for the shell")
			if err != nil {
				return err
			}
		}
		result, err := shellOnTarget(cmd.Context(), streams, target, selector)
		if err != nil {
			if result.DiagnosticsReported {
				return reportedExecutionError{cause: err}
			}
			return executionError{cause: err}
		}
		if result.ExitCode != 0 {
			return exitStatusError{code: result.ExitCode}
		}
		return nil
	}}
	command.Flags().StringVar(&explicitBox, "box", "", "box name (always uses OpenSSH)")
	return command
}

func resolveContextualStart(ctx context.Context, streams Streams, global *globalOptions, target boxtarget.Target) (string, error) {
	local, err := inspectCurrentCheckout(ctx)
	if err != nil {
		return "", executionError{cause: err}
	}
	var catalog repository.Catalog
	err = prompts.Wait(ctx, promptOptions(streams, global), "Finding remote work", func(waitCtx context.Context) error {
		var listErr error
		catalog, listErr = listWorktreesForContext(waitCtx, target)
		return listErr
	})
	if errors.Is(err, prompts.ErrAborted) {
		return "", abortError{cause: err}
	}
	if err != nil {
		return "", executionError{cause: err}
	}
	plan := workcontext.PlanStart(local, catalog)
	if plan.Incomplete {
		_ = writeMutedNotice(streams.Err, terminalTheme(global, streams), "Remote Repository discovery was incomplete; choose known work explicitly instead of cloning.")
	}
	switch plan.Mode {
	case workcontext.StartUse:
		return plan.Preferred.Worktree.Path, nil
	case workcontext.StartChoose:
		if plan.Incomplete {
			return pickWorktreeChoices(ctx, streams, global, "Choose a known Worktree to start", plan.Choices)
		}
		return chooseWorktreeChoices(ctx, streams, global, "Choose a Worktree to start", plan.Choices)
	case workcontext.StartClone:
		return confirmCloneForStart(ctx, streams, global, target, local, plan)
	default:
		if plan.Incomplete {
			return "", contextualUnavailable("remote Repository discovery was incomplete", "resolve the Box discovery warnings, or specify a known remote Worktree explicitly")
		}
		return "", contextualUnavailable("no remote Repository is available to start", "add a network origin to this local Repository, or run `schooner clone <origin> --box <box>`")
	}
}

func confirmCloneForStart(ctx context.Context, streams Streams, global *globalOptions, target cloneExecutionTarget, local *repository.LocalCheckout, plan workcontext.StartPlan) (string, error) {
	interactive := interactionAllowed(streams, global)
	if interactive {
		theme := terminalTheme(global, streams)
		repositoryName := filepath.Base(local.TopLevel)
		if err := writeActionSummary(streams.Err, theme, "Create remote checkout", []summaryRow{
			{Label: "Repository", Value: repositoryName},
			{Label: "Origin", Value: plan.CloneSource},
			{Label: "Box", Value: targetBoxLabel(target)},
			{Label: "Remote branch", Value: "origin default"},
		}); err != nil {
			return "", executionError{cause: err}
		}
		for _, warning := range cloneStartWarnings(local) {
			if err := writeWarningLine(streams.Err, theme, warning); err != nil {
				return "", executionError{cause: err}
			}
		}
		negative := "Cancel"
		if len(plan.Choices) != 0 {
			negative = "Choose existing"
		}
		confirmed, err := prompts.Confirm(ctx, promptOptions(streams, global), "Clone this Repository and start?", "Clone and start", negative)
		if errors.Is(err, prompts.ErrAborted) {
			return "", abortError{cause: err}
		}
		if err != nil {
			return "", executionError{cause: err}
		}
		if !confirmed {
			if len(plan.Choices) != 0 {
				return chooseWorktreeChoices(ctx, streams, global, "Choose an existing Worktree", plan.Choices)
			}
			writeCancelled(streams.Err)
			return "", abortError{cause: prompts.ErrAborted}
		}
	}
	result, err := cloneWithRecovery(ctx, streams, global, target, repository.CloneRequest{Source: plan.CloneSource}, "Cloning Repository on "+targetBoxLabel(target), nil)
	if err != nil {
		return "", executionError{cause: err}
	}
	path := result.Path
	if result.Inspection != nil {
		path = result.Inspection.Worktree.Path
	}
	if path == "" {
		return "", executionError{cause: fmt.Errorf("clone completed without a Worktree path")}
	}
	return path, nil
}

func resolveContextualResume(ctx context.Context, streams Streams, global *globalOptions, target boxtarget.Target) (string, error) {
	local, err := inspectCurrentCheckout(ctx)
	if err != nil {
		return "", executionError{cause: err}
	}
	if local != nil && local.OriginKey == "" {
		return "", contextualUnavailable("this local Repository has no matchable network origin", "run `schooner start` to choose or create persistent work for this Repository")
	}
	var repositories repository.Catalog
	var sessions session.Catalog
	err = prompts.Wait(ctx, promptOptions(streams, global), "Finding live Sessions", func(waitCtx context.Context) error {
		var listErr error
		if local != nil {
			repositories, listErr = listWorktreesForContext(waitCtx, target)
			if listErr != nil {
				return listErr
			}
		}
		sessions, listErr = target.ListSessions(waitCtx)
		return listErr
	})
	if errors.Is(err, prompts.ErrAborted) {
		return "", abortError{cause: err}
	}
	if err != nil {
		return "", executionError{cause: err}
	}
	plan := workcontext.PlanResume(local, repositories, sessions)
	if plan.Incomplete && plan.Mode != workcontext.ResumeUnavailable {
		_ = writeMutedNotice(streams.Err, terminalTheme(global, streams), "Remote Repository discovery was incomplete; choose a known matching Session explicitly.")
	}
	switch plan.Mode {
	case workcontext.ResumeUse:
		writeResumeSummary(streams, global, target, plan.Preferred)
		return plan.Preferred.ID, nil
	case workcontext.ResumeChoose:
		return pickSession(ctx, streams, global, plan.Choices, "Choose a Session to resume")
	default:
		if local != nil {
			message := "no managed live Session matches this local Repository"
			guidance := "run `schooner start` to open persistent work for this Repository"
			if plan.Incomplete {
				message = "no matching managed live Session could be selected because remote Repository discovery was incomplete"
				guidance = "resolve the Box discovery warnings, or run `schooner start` to open persistent work for this Repository"
			}
			return "", contextualUnavailable(message, guidance)
		}
		return "", contextualUnavailable("no managed live Session is available to resume", "run `schooner start` to open persistent work for a Repository")
	}
}

func listWorktreesForContext(ctx context.Context, target boxtarget.Target) (repository.Catalog, error) {
	return target.ListContextWorktrees(ctx)
}

func contextualUnavailable(message, guidance string) error {
	return guidanceError{
		cause:    executionError{cause: box.NewError("not_found", message, nil)},
		guidance: guidance,
	}
}

func inspectCurrentCheckout(ctx context.Context) (*repository.LocalCheckout, error) {
	directory, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("read current working directory: %w", err)
	}
	return repository.InspectLocal(ctx, directory)
}

func chooseWorktreeChoices(ctx context.Context, streams Streams, global *globalOptions, title string, values []workcontext.WorktreeChoice) (string, error) {
	choices := make([]prompts.Choice, 0, len(values))
	for _, value := range values {
		branch := value.Worktree.Branch
		if value.Worktree.Detached {
			branch = "detached"
		}
		choices = append(choices, prompts.Choice{Label: value.Worktree.RelativePath + "  " + branch, Value: value.Worktree.Path})
	}
	return chooseValue(ctx, streams, global, title, choices, "multiple Worktrees are available; specify an exact Worktree path")
}

func pickWorktreeChoices(ctx context.Context, streams Streams, global *globalOptions, title string, values []workcontext.WorktreeChoice) (string, error) {
	if !interactionAllowed(streams, global) {
		return "", usageError{cause: fmt.Errorf("a Worktree selector is required when remote Repository discovery is incomplete")}
	}
	choices := make([]prompts.Choice, 0, len(values))
	for _, value := range values {
		branch := value.Worktree.Branch
		if value.Worktree.Detached {
			branch = "detached"
		}
		choices = append(choices, prompts.Choice{Label: value.Worktree.RelativePath + "  " + branch, Value: value.Worktree.Path})
	}
	selected, err := prompts.Pick(ctx, promptOptions(streams, global), title, choices)
	if errors.Is(err, prompts.ErrAborted) {
		return "", abortError{cause: err}
	}
	if err != nil {
		return "", executionError{cause: err}
	}
	return selected, nil
}

func pickSession(ctx context.Context, streams Streams, global *globalOptions, values []session.Session, title string) (string, error) {
	if !interactionAllowed(streams, global) {
		return "", usageError{cause: fmt.Errorf("a Session selector is required when prompts are unavailable")}
	}
	choices := make([]prompts.Choice, 0, len(values))
	for _, value := range values {
		selector := value.ID
		if value.Ownership == session.Unmanaged {
			selector = "tmux:" + value.TmuxID
		}
		label := value.Name + "  " + string(value.Ownership) + "  " + string(value.Association)
		if value.WorktreeRelativePath != "" {
			label += "  " + value.WorktreeRelativePath
		}
		choices = append(choices, prompts.Choice{Label: label, Value: selector})
	}
	selected, err := prompts.Pick(ctx, promptOptions(streams, global), title, choices)
	if errors.Is(err, prompts.ErrAborted) {
		return "", abortError{cause: err}
	}
	if err != nil {
		return "", executionError{cause: err}
	}
	return selected, nil
}

func cloneStartWarnings(local *repository.LocalCheckout) []string {
	warnings := []string{"Only commits available from the origin will be cloned; local files and unpushed commits are not copied."}
	dirty := local.Status.Staged + local.Status.Unstaged + local.Status.Untracked + local.Status.Conflicted
	if dirty != 0 {
		warnings = append(warnings, fmt.Sprintf("The local checkout has %d changed or untracked item(s).", dirty))
	}
	if local.Detached {
		warnings = append(warnings, "The local checkout has a detached HEAD; the remote clone will use the origin default branch.")
	} else if local.Upstream == "" {
		warnings = append(warnings, "The local branch has no upstream; the remote clone will use the origin default branch.")
	} else if local.Ahead != 0 {
		warnings = append(warnings, fmt.Sprintf("The local branch is %d commit(s) ahead of its upstream.", local.Ahead))
	}
	return warnings
}

func writeResumeSummary(streams Streams, global *globalOptions, target boxtarget.Target, value session.Session) {
	rows := []summaryRow{{Label: "Box", Value: targetBoxLabel(target)}, {Label: "Session", Value: value.Name}}
	if value.WorktreeRelativePath != "" {
		rows = append(rows, summaryRow{Label: "Worktree", Value: value.WorktreeRelativePath})
	}
	if !value.ActivityAt.IsZero() {
		rows = append(rows, summaryRow{Label: "Last active", Value: value.ActivityAt.Local().Format("2006-01-02 15:04 MST")})
	}
	_ = writeActionSummary(streams.Err, terminalTheme(global, streams), "Resuming work", rows)
}

func targetBoxLabel(target interface{ BoxName() string }) string {
	return defaultString(target.BoxName(), "this box")
}

func listSessionsOnTarget(ctx context.Context, target boxtarget.Target) (session.Catalog, error) {
	return target.ListSessions(ctx)
}

func startSessionOnTarget(ctx context.Context, target boxtarget.Target, worktree string) (session.StartResult, error) {
	return target.StartSession(ctx, worktree)
}

func resumeSessionOnTarget(ctx context.Context, streams Streams, target boxtarget.Target, selector string) (boxtarget.HandoffResult, error) {
	return target.ResumeSession(ctx, selector, boxtarget.Terminal{In: streams.In, Out: streams.Out, Err: streams.Err})
}

func logsOnTarget(ctx context.Context, target boxtarget.Target, id string, lines int) (session.LogsResult, error) {
	return target.SessionLogs(ctx, id, lines)
}

func stopOnTarget(ctx context.Context, target boxtarget.Target, id string) (session.StopResult, error) {
	return target.StopSession(ctx, id)
}

func shellOnTarget(ctx context.Context, streams Streams, target boxtarget.Target, worktree string) (boxtarget.HandoffResult, error) {
	return target.OpenWorktreeShell(ctx, worktree, boxtarget.Terminal{In: streams.In, Out: streams.Out, Err: streams.Err})
}

func chooseWorktree(ctx context.Context, streams Streams, global *globalOptions, target boxtarget.Target, title string) (string, error) {
	catalog, err := target.ListWorktrees(ctx)
	if err != nil {
		return "", executionError{cause: err}
	}
	choices := make([]prompts.Choice, 0)
	for _, relation := range catalog.Repositories {
		if relation.Primary != nil {
			choices = append(choices, prompts.Choice{Label: relation.Primary.RelativePath + "  " + relation.Primary.Branch, Value: relation.Primary.Path})
		}
		for _, worktree := range relation.Linked {
			choices = append(choices, prompts.Choice{Label: worktree.RelativePath + "  " + worktree.Branch, Value: worktree.Path})
		}
	}
	return chooseValue(ctx, streams, global, title, choices, "multiple Worktrees are available; specify an exact Worktree path")
}

type sessionChoiceMode int

const (
	sessionChoiceResume sessionChoiceMode = iota
	sessionChoiceManaged
)

func chooseSession(ctx context.Context, streams Streams, global *globalOptions, catalog session.Catalog, mode sessionChoiceMode, title string) (string, error) {
	choices := make([]prompts.Choice, 0)
	for _, value := range catalog.Sessions {
		selector := value.ID
		if value.Ownership == session.Unmanaged && mode == sessionChoiceResume {
			selector = "tmux:" + value.TmuxID
		}
		if selector == "" || value.Ownership == session.Invalid || mode == sessionChoiceManaged && value.Ownership != session.Managed {
			continue
		}
		label := value.Name + "  " + string(value.Ownership) + "  " + string(value.Association)
		if value.WorktreeRelativePath != "" {
			label += "  " + value.WorktreeRelativePath
		}
		choices = append(choices, prompts.Choice{Label: label, Value: selector})
	}
	return chooseValue(ctx, streams, global, title, choices, "multiple Sessions are available; specify a Session selector")
}

func chooseValue(ctx context.Context, streams Streams, global *globalOptions, title string, choices []prompts.Choice, ambiguous string) (string, error) {
	if len(choices) == 0 {
		return "", executionError{cause: box.NewError("not_found", "no matching live resources are available", nil)}
	}
	if len(choices) == 1 {
		return choices[0].Value, nil
	}
	if !interactionAllowed(streams, global) {
		return "", usageError{cause: fmt.Errorf("%s", ambiguous)}
	}
	value, err := prompts.Pick(ctx, promptOptions(streams, global), title, choices)
	if errors.Is(err, prompts.ErrAborted) {
		return "", abortError{cause: err}
	}
	if err != nil {
		return "", executionError{cause: err}
	}
	return value, nil
}

func writeSessions(writer io.Writer, output string, catalog session.Catalog, theme *uitheme.Theme) error {
	if output == "json" {
		return json.NewEncoder(writer).Encode(struct {
			SchemaVersion string `json:"schema_version"`
			session.Catalog
		}{SchemaVersion: "1", Catalog: catalog})
	}
	rows := make([][]string, 0, len(catalog.Sessions))
	for _, value := range catalog.Sessions {
		identifier := value.ID
		if identifier == "" {
			identifier = "tmux:" + value.TmuxID
		}
		worktree := value.WorktreeRelativePath
		if worktree == "" {
			worktree = "-"
		}
		name := value.Name
		if value.Ownership == session.Invalid {
			name = strconv.QuoteToASCII(name)
		}
		rows = append(rows, []string{identifier, name, string(value.Ownership), string(value.Association), worktree, strconv.Itoa(value.AttachedClients)})
	}
	return writeTable(writer, theme, []string{"SESSION", "NAME", "OWNERSHIP", "STATE", "WORKTREE", "ATTACHED"}, rows)
}

func writeSessionLogs(writer io.Writer, output string, result session.LogsResult) error {
	if output == "json" {
		return json.NewEncoder(writer).Encode(struct {
			SchemaVersion string `json:"schema_version"`
			session.LogsResult
		}{SchemaVersion: "1", LogsResult: result})
	}
	_, err := io.WriteString(writer, result.Content)
	return err
}

func writeSessionStop(writer io.Writer, output string, result session.StopResult, theme *uitheme.Theme) error {
	if output == "json" {
		return json.NewEncoder(writer).Encode(struct {
			SchemaVersion string `json:"schema_version"`
			session.StopResult
		}{SchemaVersion: "1", StopResult: result})
	}
	return writeReadySummary(writer, theme, "Stopped Session "+result.SessionID, []summaryRow{
		{Label: "Worktree", Value: "unchanged"},
	})
}

func requireInteractiveTerminal(streams Streams, global *globalOptions, command string) error {
	if global.output != "human" {
		return usageError{cause: fmt.Errorf("%s supports human output only", command)}
	}
	if !streams.InIsTerminal || !streams.OutIsTerminal {
		return usageError{cause: fmt.Errorf("%s requires an interactive terminal", command)}
	}
	return nil
}

func firstArgument(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

func sessionSelectorOmitted(args []string) bool {
	return len(args) == 0
}
