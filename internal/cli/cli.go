// Package cli adapts Schooner's command-line interface to application behavior.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

const (
	exitSuccess = 0
	exitFailure = 1
	exitUsage   = 2
)

// Streams are the process streams used by the command-line interface.
type Streams struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer
}

// BuildInfo describes the Schooner executable being run.
type BuildInfo struct {
	Version string
	Commit  string
	// BuiltAt is empty for a development build or an RFC3339 timestamp.
	BuiltAt   string
	GoVersion string
	OS        string
	Arch      string
}

// Run executes Schooner with the supplied process inputs and returns a process
// exit status. Results are written to Out; diagnostics are written to Err.
func Run(ctx context.Context, args []string, streams Streams, build BuildInfo) int {
	streams = normalizedStreams(streams)

	if err := ctx.Err(); err != nil {
		printError(streams.Err, err)
		return exitFailure
	}

	root := newRootCommand(build)
	root.SetArgs(args)
	root.SetIn(streams.In)
	root.SetOut(streams.Out)
	root.SetErr(streams.Err)

	if err := root.ExecuteContext(ctx); err != nil {
		printError(streams.Err, err)

		var failure executionError
		if errors.As(err, &failure) {
			return exitFailure
		}

		return exitUsage
	}

	return exitSuccess
}

func newRootCommand(build BuildInfo) *cobra.Command {
	root := &cobra.Command{
		Use:           "schooner",
		Short:         "Create and operate persistent development machines",
		SilenceErrors: true,
		SilenceUsage:  true,
		Version:       defaultString(build.Version, "dev"),
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := cmd.Help(); err != nil {
				return executionError{cause: err}
			}

			return nil
		},
	}

	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return usageError{cause: err}
	})
	root.AddCommand(newVersionCommand(build))

	return root
}

type executionError struct {
	cause error
}

func (e executionError) Error() string {
	return e.cause.Error()
}

func (e executionError) Unwrap() error {
	return e.cause
}

type usageError struct {
	cause error
}

func (e usageError) Error() string {
	return e.cause.Error()
}

func (e usageError) Unwrap() error {
	return e.cause
}

func normalizedStreams(streams Streams) Streams {
	if streams.In == nil {
		streams.In = emptyReader{}
	}
	if streams.Out == nil {
		streams.Out = io.Discard
	}
	if streams.Err == nil {
		streams.Err = io.Discard
	}

	return streams
}

func printError(w io.Writer, err error) {
	_, _ = fmt.Fprintf(w, "Error: %v\n", err)
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}

	return value
}

type emptyReader struct{}

func (emptyReader) Read([]byte) (int, error) {
	return 0, io.EOF
}
