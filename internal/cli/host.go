package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"github.com/thewelshrich/schooner/internal/box"
	hostruntime "github.com/thewelshrich/schooner/internal/runtime"
	"github.com/thewelshrich/schooner/internal/runtime/host"
)

func newHostCommand(build BuildInfo, streams Streams) *cobra.Command {
	runtime := host.New(hostBuildInfo(build))
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
				var request hostruntime.ConfigureRequest
				if err := readRequiredHostRequest(streams, &request); err != nil {
					return executionError{cause: err}
				}
				result, err := runtime.Configure(request)
				if err != nil {
					return executionError{cause: err}
				}
				return encodeHostResult(cmd.OutOrStdout(), result)
			},
		},
	)
	worktree := &cobra.Command{Use: "worktree", Hidden: true, Args: cobra.NoArgs, RunE: helpRun}
	worktree.AddCommand(
		&cobra.Command{
			Use: "list", Args: cobra.NoArgs, SilenceUsage: true,
			RunE: func(cmd *cobra.Command, _ []string) error {
				var request hostruntime.WorktreeRequest
				if err := readRequiredHostRequest(streams, &request); err != nil {
					return executionError{cause: err}
				}
				result, err := runtime.ListWorktrees(cmd.Context(), request)
				if err != nil {
					return executionError{cause: err}
				}
				return encodeHostResult(cmd.OutOrStdout(), result)
			},
		},
		&cobra.Command{
			Use: "inspect", Args: cobra.NoArgs, SilenceUsage: true,
			RunE: func(cmd *cobra.Command, _ []string) error {
				var request hostruntime.WorktreeRequest
				if err := readRequiredHostRequest(streams, &request); err != nil {
					return executionError{cause: err}
				}
				result, err := runtime.InspectWorktree(cmd.Context(), request)
				if err != nil {
					return executionError{cause: err}
				}
				return encodeHostResult(cmd.OutOrStdout(), result)
			},
		},
	)
	cmd.AddCommand(worktree)
	return cmd
}

func newDoctorCommand(build BuildInfo, streams Streams, options *globalOptions) *cobra.Command {
	return &cobra.Command{
		Use:          "doctor",
		Short:        "Check this machine for Schooner readiness",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			runtime := host.New(hostBuildInfo(build))
			report, err := runtime.Doctor(cmd.Context(), hostruntime.NewInspectRequest(box.DefaultWorktreeRoot))
			if err != nil {
				return executionError{cause: err}
			}
			return writeDoctorResult(cmd.OutOrStdout(), options.output, report)
		},
	}
}

func writeDoctorResult(w io.Writer, output string, report hostruntime.DoctorReport) error {
	var err error
	switch output {
	case "json":
		err = encodeHostResult(w, report)
	case "human":
		err = writeDoctorReport(w, report)
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

func readRequiredHostRequest(streams Streams, target any) error {
	contents, err := io.ReadAll(io.LimitReader(streams.In, hostruntime.MaxMessageBytes+1))
	if err != nil {
		return fmt.Errorf("read host request: %w", err)
	}
	if len(strings.TrimSpace(string(contents))) == 0 {
		return fmt.Errorf("host request is required")
	}
	return hostruntime.DecodeStrict(contents, target)
}

func writeDoctorReport(w io.Writer, report hostruntime.DoctorReport) error {
	status := "ready"
	if !report.Healthy {
		status = "needs attention"
	}
	if _, err := fmt.Fprintf(w, "Schooner doctor: %s\n", status); err != nil {
		return err
	}
	for _, check := range report.Checks {
		mark := "✓"
		if !check.OK {
			mark = "!"
		}
		if _, err := fmt.Fprintf(w, "%s %s\n", mark, check.Message); err != nil {
			return err
		}
	}
	return nil
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
