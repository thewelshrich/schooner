package cli

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/thewelshrich/schooner/internal/box"
	"github.com/thewelshrich/schooner/internal/repository"
	hostruntime "github.com/thewelshrich/schooner/internal/runtime"
	"github.com/thewelshrich/schooner/internal/runtime/host"
	uitheme "github.com/thewelshrich/schooner/internal/ui/theme"
)

func newHostCommand(streams Streams, options *globalOptions) *cobra.Command {
	runtime := options.hostRuntime()
	cmd := &cobra.Command{
		Use:    "host",
		Short:  "Run private host operations",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE:   helpRun,
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:          "hello",
			Args:         cobra.NoArgs,
			SilenceUsage: true,
			RunE: func(cmd *cobra.Command, _ []string) error {
				result, err := runtime.Hello()
				if err != nil {
					return executionError{cause: err}
				}
				return encodeHostResult(cmd.OutOrStdout(), result)
			},
		},
		&cobra.Command{
			Use:          "inspect",
			Args:         cobra.NoArgs,
			SilenceUsage: true,
			RunE: func(cmd *cobra.Command, _ []string) error {
				request, err := readHostRequest(streams, box.DefaultWorktreeRoot)
				if err != nil {
					return executionError{cause: err}
				}
				result, err := runtime.Inspect(cmd.Context(), request)
				if err != nil {
					return executionError{cause: err}
				}
				return encodeHostResult(cmd.OutOrStdout(), result)
			},
		},
		&cobra.Command{
			Use:          "doctor",
			Args:         cobra.NoArgs,
			SilenceUsage: true,
			RunE: func(cmd *cobra.Command, _ []string) error {
				request, err := readHostRequest(streams, box.DefaultWorktreeRoot)
				if err != nil {
					return executionError{cause: err}
				}
				result, err := runtime.Doctor(cmd.Context(), request)
				if err != nil {
					return executionError{cause: err}
				}
				return encodeHostResult(cmd.OutOrStdout(), result)
			},
		},
		&cobra.Command{
			Use:          "configure",
			Args:         cobra.NoArgs,
			SilenceUsage: true,
			RunE: func(cmd *cobra.Command, _ []string) error {
				operation := hostruntime.ConfigureOperation()
				request, err := readHostOperationRequest(streams, operation)
				if err != nil {
					return executionError{cause: err}
				}
				result, err := runtime.Configure(request)
				if err != nil {
					return executionError{cause: err}
				}
				return encodeHostOperationResult(cmd.OutOrStdout(), operation, request, result)
			},
		},
	)
	worktree := &cobra.Command{Use: "worktree", Hidden: true, Args: cobra.NoArgs, RunE: helpRun}
	worktree.AddCommand(
		&cobra.Command{
			Use: "list", Args: cobra.NoArgs, SilenceUsage: true,
			RunE: func(cmd *cobra.Command, _ []string) error {
				operation := hostruntime.WorktreeListOperation()
				request, err := readHostOperationRequest(streams, operation)
				if err != nil {
					return executionError{cause: err}
				}
				result, err := runtime.ListWorktrees(cmd.Context(), request)
				if err != nil {
					return executionError{cause: err}
				}
				return encodeHostOperationResult(cmd.OutOrStdout(), operation, request, result)
			},
		},
		&cobra.Command{
			Use: "inspect", Args: cobra.NoArgs, SilenceUsage: true,
			RunE: func(cmd *cobra.Command, _ []string) error {
				operation := hostruntime.WorktreeInspectOperation()
				request, err := readHostOperationRequest(streams, operation)
				if err != nil {
					return executionError{cause: err}
				}
				result, err := runtime.InspectWorktree(cmd.Context(), request)
				if err != nil {
					switch repository.ErrorCode(err) {
					case repository.CodeNotFound:
						return encodeHostResult(cmd.OutOrStdout(), hostruntime.NewOperationError(request.BoxIdentity, hostruntime.CodeNotFound, err.Error()))
					case repository.CodeInvalidInput:
						return encodeHostResult(cmd.OutOrStdout(), hostruntime.NewOperationError(request.BoxIdentity, hostruntime.CodeInvalidInput, err.Error()))
					}
					if hostruntime.ErrorCode(err) == hostruntime.CodeInvalidInput {
						return encodeHostResult(cmd.OutOrStdout(), hostruntime.NewOperationError(request.BoxIdentity, hostruntime.CodeInvalidInput, err.Error()))
					}
					return executionError{cause: err}
				}
				return encodeHostOperationResult(cmd.OutOrStdout(), operation, request, result)
			},
		},
	)
	worktree.AddCommand(&cobra.Command{
		Use: "shell <request>", Args: cobra.ExactArgs(1), SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			var request hostruntime.WorktreeShellRequest
			if err := decodeInteractiveHostRequest(args[0], &request); err != nil {
				return usageError{cause: err}
			}
			result, err := runtime.OpenWorktreeShell(cmd.Context(), request, host.TerminalIO{In: streams.In, Out: streams.Out, Err: streams.Err, DisableJobControl: true})
			if err != nil {
				return executionError{cause: err}
			}
			if result.ExitCode != 0 {
				return exitStatusError{code: result.ExitCode}
			}
			return nil
		},
	})
	sessions := &cobra.Command{Use: "session", Hidden: true, Args: cobra.NoArgs, RunE: helpRun}
	sessions.AddCommand(
		&cobra.Command{Use: "list", Args: cobra.NoArgs, SilenceUsage: true, RunE: func(cmd *cobra.Command, _ []string) error {
			operation := hostruntime.SessionListOperation()
			request, err := readHostOperationRequest(streams, operation)
			if err != nil {
				return executionError{cause: err}
			}
			result, err := runtime.ListSessions(cmd.Context(), request)
			if err != nil {
				return encodeLifecycleError(cmd.OutOrStdout(), request.BoxIdentity, err)
			}
			return encodeHostOperationResult(cmd.OutOrStdout(), operation, request, result)
		}},
		&cobra.Command{Use: "start", Args: cobra.NoArgs, SilenceUsage: true, RunE: func(cmd *cobra.Command, _ []string) error {
			operation := hostruntime.SessionStartOperation()
			request, err := readHostOperationRequest(streams, operation)
			if err != nil {
				return executionError{cause: err}
			}
			result, err := runtime.StartSession(cmd.Context(), request)
			if err != nil {
				return encodeLifecycleError(cmd.OutOrStdout(), request.BoxIdentity, err)
			}
			return encodeHostOperationResult(cmd.OutOrStdout(), operation, request, result)
		}},
		&cobra.Command{Use: "logs", Args: cobra.NoArgs, SilenceUsage: true, RunE: func(cmd *cobra.Command, _ []string) error {
			operation := hostruntime.SessionLogsOperation()
			request, err := readHostOperationRequest(streams, operation)
			if err != nil {
				return executionError{cause: err}
			}
			result, err := runtime.SessionLogs(cmd.Context(), request)
			if err != nil {
				return encodeLifecycleError(cmd.OutOrStdout(), request.BoxIdentity, err)
			}
			return encodeHostOperationResult(cmd.OutOrStdout(), operation, request, result)
		}},
		&cobra.Command{Use: "stop", Args: cobra.NoArgs, SilenceUsage: true, RunE: func(cmd *cobra.Command, _ []string) error {
			operation := hostruntime.SessionStopOperation()
			request, err := readHostOperationRequest(streams, operation)
			if err != nil {
				return executionError{cause: err}
			}
			result, err := runtime.StopSession(cmd.Context(), request)
			if err != nil {
				return encodeLifecycleError(cmd.OutOrStdout(), request.BoxIdentity, err)
			}
			return encodeHostOperationResult(cmd.OutOrStdout(), operation, request, result)
		}},
		&cobra.Command{Use: "resume <request>", Args: cobra.ExactArgs(1), SilenceUsage: true, RunE: func(cmd *cobra.Command, args []string) error {
			var request hostruntime.SessionTargetRequest
			if err := decodeInteractiveHostRequest(args[0], &request); err != nil {
				return usageError{cause: err}
			}
			result, err := runtime.ResumeSession(cmd.Context(), request, host.TerminalIO{In: streams.In, Out: streams.Out, Err: streams.Err, DisableJobControl: true})
			if err != nil {
				return executionError{cause: err}
			}
			if result.ExitCode != 0 {
				return exitStatusError{code: result.ExitCode}
			}
			return nil
		}},
	)
	cmd.AddCommand(sessions)
	cmd.AddCommand(worktree)
	repositoryCommand := &cobra.Command{Use: "repository", Hidden: true, Args: cobra.NoArgs, RunE: helpRun}
	repositoryCommand.AddCommand(&cobra.Command{
		Use: "clone", Args: cobra.NoArgs, SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			operation := hostruntime.RepositoryCloneOperation()
			request, err := readHostOperationRequest(streams, operation)
			if err != nil {
				return executionError{cause: err}
			}
			request.NonInteractive = options.noInput
			result, err := runtime.CloneRepository(cmd.Context(), request)
			if err != nil {
				return encodeLifecycleError(cmd.OutOrStdout(), request.BoxIdentity, err)
			}
			return encodeHostOperationResult(cmd.OutOrStdout(), operation, request, result)
		},
	})
	cmd.AddCommand(repositoryCommand)
	worktree.AddCommand(
		&cobra.Command{
			Use: "add", Args: cobra.NoArgs, SilenceUsage: true,
			RunE: func(cmd *cobra.Command, _ []string) error {
				operation := hostruntime.WorktreeAddOperation()
				request, err := readHostOperationRequest(streams, operation)
				if err != nil {
					return executionError{cause: err}
				}
				request.NonInteractive = options.noInput
				result, err := runtime.AddWorktree(cmd.Context(), request)
				if err != nil {
					return encodeLifecycleError(cmd.OutOrStdout(), request.BoxIdentity, err)
				}
				return encodeHostOperationResult(cmd.OutOrStdout(), operation, request, result)
			},
		},
		&cobra.Command{
			Use: "remove", Args: cobra.NoArgs, SilenceUsage: true,
			RunE: func(cmd *cobra.Command, _ []string) error {
				operation := hostruntime.WorktreeRemoveOperation()
				request, err := readHostOperationRequest(streams, operation)
				if err != nil {
					return executionError{cause: err}
				}
				request.NonInteractive = options.noInput
				result, err := runtime.RemoveWorktree(cmd.Context(), request)
				if err != nil {
					return encodeLifecycleError(cmd.OutOrStdout(), request.BoxIdentity, err)
				}
				return encodeHostOperationResult(cmd.OutOrStdout(), operation, request, result)
			},
		},
		&cobra.Command{
			Use: "prune", Args: cobra.NoArgs, SilenceUsage: true,
			RunE: func(cmd *cobra.Command, _ []string) error {
				operation := hostruntime.WorktreePruneOperation()
				request, err := readHostOperationRequest(streams, operation)
				if err != nil {
					return executionError{cause: err}
				}
				request.NonInteractive = options.noInput
				result, err := runtime.PruneWorktrees(cmd.Context(), request)
				if err != nil {
					return encodeLifecycleError(cmd.OutOrStdout(), request.BoxIdentity, err)
				}
				return encodeHostOperationResult(cmd.OutOrStdout(), operation, request, result)
			},
		},
	)
	return cmd
}

func decodeInteractiveHostRequest(value string, target any) error {
	if value == "" || len(value) > 16<<10 {
		return fmt.Errorf("interactive host request is invalid")
	}
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return fmt.Errorf("decode interactive host request: %w", err)
	}
	if err = hostruntime.DecodeStrict(decoded, target); err != nil {
		return fmt.Errorf("decode interactive host request: %w", err)
	}
	return nil
}

func encodeLifecycleError(writer io.Writer, identity string, err error) error {
	code := repository.ErrorCode(err)
	switch code {
	case repository.CodeNotFound, repository.CodeInvalidInput, repository.CodeConflict, repository.CodeAuthentication, repository.CodePermissionDenied, repository.CodeOperationInProgress, repository.CodeOutcomeUnknown:
		return encodeHostResult(writer, hostruntime.NewOperationError(identity, hostruntime.Code(code), err.Error()))
	default:
		hostCode := hostruntime.ErrorCode(err)
		if slices.Contains([]hostruntime.Code{hostruntime.CodeInvalidInput, hostruntime.CodeConflict, hostruntime.CodeAuthentication, hostruntime.CodePermissionDenied, hostruntime.CodeOperationInProgress, hostruntime.CodeOutcomeUnknown}, hostCode) {
			return encodeHostResult(writer, hostruntime.NewOperationError(identity, hostCode, err.Error()))
		}
		return executionError{cause: err}
	}
}

func newDoctorCommand(streams Streams, options *globalOptions) *cobra.Command {
	return &cobra.Command{
		Use:          "doctor",
		Short:        "Check this machine for Schooner readiness",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			runtime := options.hostRuntime()
			report, err := runtime.Doctor(cmd.Context(), hostruntime.NewInspectRequest(box.DefaultWorktreeRoot))
			if err != nil {
				return executionError{cause: err}
			}
			return writeDoctorResult(cmd.OutOrStdout(), options.output, report, terminalTheme(options, streams))
		},
	}
}

func writeDoctorResult(w io.Writer, output string, report hostruntime.DoctorReport, theme *uitheme.Theme) error {
	var err error
	switch output {
	case "json":
		err = encodeHostResult(w, report)
	case "human":
		err = writeDoctorReport(w, report, theme)
	default:
		return usageError{cause: fmt.Errorf("unsupported output format %q (expected human or json)", output)}
	}
	if err != nil {
		return err
	}
	if !report.Healthy {
		return exitStatusError{code: exitFailure}
	}
	return nil
}

func readHostRequest(streams Streams, defaultWorktreeRoot string) (hostruntime.InspectRequest, error) {
	if streams.InIsTerminal {
		return hostruntime.NewInspectRequest(defaultWorktreeRoot), nil
	}
	contents, err := io.ReadAll(io.LimitReader(streams.In, hostruntime.MaxMessageBytes+1))
	if err != nil {
		return hostruntime.InspectRequest{}, fmt.Errorf("read host request: %w", err)
	}
	if len(strings.TrimSpace(string(contents))) == 0 {
		return hostruntime.NewInspectRequest(defaultWorktreeRoot), nil
	}
	var request hostruntime.InspectRequest
	if err := hostruntime.DecodeStrict(contents, &request); err != nil {
		return hostruntime.InspectRequest{}, err
	}
	if err := hostruntime.ValidateInspectRequest(request); err != nil {
		return hostruntime.InspectRequest{}, err
	}
	return request, nil
}

func readHostOperationRequest[Request, Result any](streams Streams, operation hostruntime.Operation[Request, Result]) (Request, error) {
	var zero Request
	contents, err := io.ReadAll(io.LimitReader(streams.In, hostruntime.MaxMessageBytes+1))
	if err != nil {
		return zero, fmt.Errorf("read host request: %w", err)
	}
	if len(strings.TrimSpace(string(contents))) == 0 {
		return zero, fmt.Errorf("host request is required")
	}
	return operation.DecodeRequest(contents)
}

func encodeHostOperationResult[Request, Result any](writer io.Writer, operation hostruntime.Operation[Request, Result], request Request, result Result) error {
	if err := operation.ValidateResult(request, result); err != nil {
		return executionError{cause: err}
	}
	return encodeHostResult(writer, result)
}

func writeDoctorReport(w io.Writer, report hostruntime.DoctorReport, theme *uitheme.Theme) error {
	status := "ready"
	if !report.Healthy {
		status = "needs attention"
	}
	heading := "Schooner doctor: " + status
	if theme != nil && theme.HasColor() {
		role := uitheme.Success
		if !report.Healthy {
			role = uitheme.Warning
		}
		heading = theme.Style(role).Bold(true).Render(heading)
	}
	if _, err := fmt.Fprintln(w, heading); err != nil {
		return err
	}
	for _, check := range report.Checks {
		mark := "✓"
		role := uitheme.Success
		if !check.OK {
			mark = "!"
			role = uitheme.Warning
		}
		message := check.Message
		if theme != nil && theme.HasColor() {
			mark = theme.Style(role).Render(mark)
			message = theme.Style(uitheme.Text).Render(message)
		}
		if _, err := fmt.Fprintf(w, "%s %s\n", mark, message); err != nil {
			return err
		}
	}
	if doctorIsUnsupportedLocalClient(report) {
		if _, err := fmt.Fprintln(w); err != nil {
			return err
		}
		return writeMutedNotice(w, theme, "This client can still manage remote boxes. Next: run `schooner box add` with a supported Ubuntu SSH destination.")
	}
	return nil
}

func doctorIsUnsupportedLocalClient(report hostruntime.DoctorReport) bool {
	for _, check := range report.Checks {
		if (check.ID == "platform" || check.ID == "operating_system") && !check.OK {
			return true
		}
	}
	return false
}

func encodeHostResult(w io.Writer, value any) error {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return executionError{cause: err}
	}
	return nil
}

func hostBuildInfo(build BuildInfo) hostruntime.BuildInfo {
	return hostruntime.BuildInfo{Version: defaultString(build.Version, "dev"), Commit: defaultString(build.Commit, "unknown")}
}
