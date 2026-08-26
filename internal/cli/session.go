package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/thewelshrich/schooner/internal/box"
	"github.com/thewelshrich/schooner/internal/repository"
	hostruntime "github.com/thewelshrich/schooner/internal/runtime"
	"github.com/thewelshrich/schooner/internal/runtime/host"
	sshruntime "github.com/thewelshrich/schooner/internal/runtime/ssh"
	"github.com/thewelshrich/schooner/internal/session"
	"github.com/thewelshrich/schooner/internal/ui/prompts"
)

func newSessionCommands(streams Streams, global *globalOptions) []*cobra.Command {
	return []*cobra.Command{
		newStartSessionCommand(streams, global),
		newResumeSessionCommand(streams, global),
		newSessionsCommand(streams, global),
		newSessionLogsCommand(streams, global),
		newStopSessionCommand(streams, global),
		newWorktreeShellCommand(streams, global),
	}
}

func newStartSessionCommand(streams Streams, global *globalOptions) *cobra.Command {
	var explicitBox string
	command := &cobra.Command{Use: "start [worktree-path]", Short: "Start or reuse a persistent Worktree Session", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireInteractiveTerminal(streams, global, "start"); err != nil {
			return err
		}
		target, err := resolveWorktreeTarget(cmd.Context(), streams, global, explicitBox)
		if err != nil {
			return executionError{cause: err}
		}
		selector := firstArgument(args)
		if sessionSelectorOmitted(args) {
			selector, err = chooseWorktree(cmd.Context(), streams, global, target, "Choose a Worktree to start")
			if err != nil {
				closeWorktreeTarget(&target)
				return err
			}
		}
		result, err := startSessionOnTarget(cmd.Context(), streams, global, target, selector)
		if err != nil {
			closeWorktreeTarget(&target)
			return executionError{cause: publicRepositoryError(err)}
		}
		closeWorktreeTarget(&target)
		attachResult, err := resumeSessionOnTarget(cmd.Context(), streams, global, target, result.Session.ID)
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

func newResumeSessionCommand(streams Streams, global *globalOptions) *cobra.Command {
	var explicitBox string
	command := &cobra.Command{Use: "resume [worktree-path-or-session-id]", Short: "Resume an existing persistent Session", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireInteractiveTerminal(streams, global, "resume"); err != nil {
			return err
		}
		target, err := resolveWorktreeTarget(cmd.Context(), streams, global, explicitBox)
		if err != nil {
			return executionError{cause: err}
		}
		selector := firstArgument(args)
		if sessionSelectorOmitted(args) {
			catalog, listErr := listSessionsOnTarget(cmd.Context(), streams, global, target)
			if listErr != nil {
				closeWorktreeTarget(&target)
				return executionError{cause: publicRepositoryError(listErr)}
			}
			selector, err = chooseSession(cmd.Context(), streams, global, catalog, sessionChoiceResume, "Choose a Session to resume")
			if err != nil {
				closeWorktreeTarget(&target)
				return err
			}
		}
		closeWorktreeTarget(&target)
		result, err := resumeSessionOnTarget(cmd.Context(), streams, global, target, selector)
		if err != nil {
			if result.DiagnosticsReported {
				return reportedExecutionError{cause: err}
			}
			return executionError{cause: publicRepositoryError(err)}
		}
		if result.ExitCode != 0 {
			return exitStatusError{code: result.ExitCode}
		}
		return nil
	}}
	command.Flags().StringVar(&explicitBox, "box", "", "box name (always uses OpenSSH)")
	return command
}

func newSessionsCommand(streams Streams, global *globalOptions) *cobra.Command {
	var explicitBox string
	command := &cobra.Command{Use: "sessions", Short: "List live managed and unmanaged tmux Sessions", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		target, err := resolveWorktreeTarget(cmd.Context(), streams, global, explicitBox)
		if err != nil {
			return executionError{cause: err}
		}
		defer closeWorktreeTarget(&target)
		catalog, err := listSessionsOnTarget(cmd.Context(), streams, global, target)
		if err != nil {
			return executionError{cause: publicRepositoryError(err)}
		}
		return writeSessions(cmd.OutOrStdout(), global.output, catalog)
	}}
	command.Flags().StringVar(&explicitBox, "box", "", "box name (always uses OpenSSH)")
	return command
}

func newSessionLogsCommand(streams Streams, global *globalOptions) *cobra.Command {
	var explicitBox string
	var lines int
	command := &cobra.Command{Use: "logs [session-id]", Short: "Capture bounded history from a managed Session", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		target, err := resolveWorktreeTarget(cmd.Context(), streams, global, explicitBox)
		if err != nil {
			return executionError{cause: err}
		}
		defer closeWorktreeTarget(&target)
		id := firstArgument(args)
		if sessionSelectorOmitted(args) {
			catalog, listErr := listSessionsOnTarget(cmd.Context(), streams, global, target)
			if listErr != nil {
				return executionError{cause: publicRepositoryError(listErr)}
			}
			id, err = chooseSession(cmd.Context(), streams, global, catalog, sessionChoiceManaged, "Choose a Session for logs")
			if err != nil {
				return err
			}
		}
		result, err := logsOnTarget(cmd.Context(), streams, global, target, id, lines)
		if err != nil {
			return executionError{cause: publicRepositoryError(err)}
		}
		return writeSessionLogs(cmd.OutOrStdout(), global.output, result)
	}}
	command.Flags().StringVar(&explicitBox, "box", "", "box name (always uses OpenSSH)")
	command.Flags().IntVar(&lines, "lines", session.DefaultLogLines, "history lines to capture (1-2000)")
	return command
}

func newStopSessionCommand(streams Streams, global *globalOptions) *cobra.Command {
	var explicitBox string
	command := &cobra.Command{Use: "stop [session-id]", Short: "Stop a managed Session without changing its Worktree", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		target, err := resolveWorktreeTarget(cmd.Context(), streams, global, explicitBox)
		if err != nil {
			return executionError{cause: err}
		}
		defer closeWorktreeTarget(&target)
		id := firstArgument(args)
		if sessionSelectorOmitted(args) {
			if !interactionAllowed(streams, global) {
				return usageError{cause: fmt.Errorf("a managed Session ID is required when prompts are unavailable")}
			}
			catalog, listErr := listSessionsOnTarget(cmd.Context(), streams, global, target)
			if listErr != nil {
				return executionError{cause: publicRepositoryError(listErr)}
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
		result, err := stopOnTarget(cmd.Context(), streams, global, target, id)
		if err != nil {
			return executionError{cause: publicRepositoryError(err)}
		}
		return writeSessionStop(cmd.OutOrStdout(), global.output, result)
	}}
	command.Flags().StringVar(&explicitBox, "box", "", "box name (always uses OpenSSH)")
	return command
}

func newWorktreeShellCommand(streams Streams, global *globalOptions) *cobra.Command {
	var explicitBox string
	command := &cobra.Command{Use: "shell [worktree-path]", Short: "Open an ephemeral shell in a live Worktree", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireInteractiveTerminal(streams, global, "shell"); err != nil {
			return err
		}
		target, err := resolveWorktreeTarget(cmd.Context(), streams, global, explicitBox)
		if err != nil {
			return executionError{cause: err}
		}
		selector := firstArgument(args)
		if sessionSelectorOmitted(args) {
			selector, err = chooseWorktree(cmd.Context(), streams, global, target, "Choose a Worktree for the shell")
			if err != nil {
				closeWorktreeTarget(&target)
				return err
			}
		}
		closeWorktreeTarget(&target)
		result, err := shellOnTarget(cmd.Context(), streams, global, target, selector)
		if err != nil {
			if result.DiagnosticsReported {
				return reportedExecutionError{cause: err}
			}
			return executionError{cause: publicRepositoryError(err)}
		}
		if result.ExitCode != 0 {
			return exitStatusError{code: result.ExitCode}
		}
		return nil
	}}
	command.Flags().StringVar(&explicitBox, "box", "", "box name (always uses OpenSSH)")
	return command
}

func listSessionsOnTarget(ctx context.Context, streams Streams, global *globalOptions, target worktreeTarget) (session.Catalog, error) {
	if target.direct != nil {
		result, err := target.direct.ListSessions(ctx, hostruntime.NewSessionListRequest(target.configured.WorktreeRoot, target.identity))
		return result.Catalog, err
	}
	return target.remote.ssh.ListSessions(ctx, worktreeConnection(target.record, streams, global), box.HostRuntime{Path: target.record.RuntimePath}, target.record.RemoteIdentity, target.record.WorktreeRoot)
}

func startSessionOnTarget(ctx context.Context, streams Streams, global *globalOptions, target worktreeTarget, worktree string) (session.StartResult, error) {
	if target.direct != nil {
		result, err := target.direct.StartSession(ctx, hostruntime.NewSessionStartRequest(target.configured.WorktreeRoot, target.identity, worktree))
		return result.StartResult, err
	}
	return target.remote.ssh.StartSession(ctx, worktreeConnection(target.record, streams, global), box.HostRuntime{Path: target.record.RuntimePath}, target.record.RemoteIdentity, target.record.WorktreeRoot, worktree)
}

func resumeSessionOnTarget(ctx context.Context, streams Streams, global *globalOptions, target worktreeTarget, selector string) (sshruntime.ShellResult, error) {
	terminal := sshruntime.TerminalIO{In: streams.In, Out: streams.Out, Err: streams.Err}
	if target.direct != nil {
		result, err := target.direct.ResumeSession(ctx, hostruntime.NewSessionTargetRequest(target.configured.WorktreeRoot, target.identity, selector), host.TerminalIO{In: streams.In, Out: streams.Out, Err: streams.Err})
		return sshruntime.ShellResult{ExitCode: result.ExitCode}, err
	}
	connection := worktreeConnection(target.record, streams, global)
	connection.BatchMode = global.noInput
	return target.remote.ssh.ResumeSession(ctx, connection, box.HostRuntime{Path: target.record.RuntimePath}, target.record.RemoteIdentity, target.record.WorktreeRoot, selector, terminal)
}

func logsOnTarget(ctx context.Context, streams Streams, global *globalOptions, target worktreeTarget, id string, lines int) (session.LogsResult, error) {
	if target.direct != nil {
		result, err := target.direct.SessionLogs(ctx, hostruntime.NewSessionLogsRequest(target.configured.WorktreeRoot, target.identity, id, lines))
		return result.LogsResult, err
	}
	return target.remote.ssh.SessionLogs(ctx, worktreeConnection(target.record, streams, global), box.HostRuntime{Path: target.record.RuntimePath}, target.record.RemoteIdentity, target.record.WorktreeRoot, id, lines)
}

func stopOnTarget(ctx context.Context, streams Streams, global *globalOptions, target worktreeTarget, id string) (session.StopResult, error) {
	if target.direct != nil {
		result, err := target.direct.StopSession(ctx, hostruntime.NewSessionTargetRequest(target.configured.WorktreeRoot, target.identity, id))
		return result.StopResult, err
	}
	return target.remote.ssh.StopSession(ctx, worktreeConnection(target.record, streams, global), box.HostRuntime{Path: target.record.RuntimePath}, target.record.RemoteIdentity, target.record.WorktreeRoot, id)
}

func shellOnTarget(ctx context.Context, streams Streams, global *globalOptions, target worktreeTarget, worktree string) (sshruntime.ShellResult, error) {
	if target.direct != nil {
		result, err := target.direct.OpenWorktreeShell(ctx, hostruntime.NewWorktreeShellRequest(target.configured.WorktreeRoot, target.identity, worktree), host.TerminalIO{In: streams.In, Out: streams.Out, Err: streams.Err})
		return sshruntime.ShellResult{ExitCode: result.ExitCode}, err
	}
	connection := worktreeConnection(target.record, streams, global)
	connection.BatchMode = global.noInput
	return target.remote.ssh.OpenWorktreeShell(ctx, connection, box.HostRuntime{Path: target.record.RuntimePath}, target.record.RemoteIdentity, target.record.WorktreeRoot, worktree, sshruntime.TerminalIO{In: streams.In, Out: streams.Out, Err: streams.Err})
}

func chooseWorktree(ctx context.Context, streams Streams, global *globalOptions, target worktreeTarget, title string) (string, error) {
	var catalog repository.Catalog
	var err error
	if target.direct != nil {
		result, callErr := target.direct.ListWorktrees(ctx, hostruntime.NewWorktreeRequest("", target.identity))
		catalog, err = result.Catalog, callErr
	} else {
		catalog, err = target.remote.ssh.ListWorktrees(ctx, worktreeConnection(target.record, streams, global), box.HostRuntime{Path: target.record.RuntimePath}, target.record.RemoteIdentity)
	}
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

func writeSessions(writer io.Writer, output string, catalog session.Catalog) error {
	if output == "json" {
		return json.NewEncoder(writer).Encode(struct {
			SchemaVersion string `json:"schema_version"`
			session.Catalog
		}{SchemaVersion: "1", Catalog: catalog})
	}
	table := tabwriter.NewWriter(writer, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(table, "SESSION\tNAME\tOWNERSHIP\tSTATE\tWORKTREE\tATTACHED")
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
		_, _ = fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%d\n", identifier, name, value.Ownership, value.Association, worktree, value.AttachedClients)
	}
	return table.Flush()
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

func writeSessionStop(writer io.Writer, output string, result session.StopResult) error {
	if output == "json" {
		return json.NewEncoder(writer).Encode(struct {
			SchemaVersion string `json:"schema_version"`
			session.StopResult
		}{SchemaVersion: "1", StopResult: result})
	}
	_, err := fmt.Fprintf(writer, "Stopped Session %s. Its Worktree was not changed.\n", result.SessionID)
	return err
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

func closeWorktreeTarget(target *worktreeTarget) {
	if target.close != nil {
		target.close()
		target.close = nil
	}
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
