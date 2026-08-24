// Package cli adapts Schooner's command-line interface to application behavior.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	"github.com/thewelshrich/schooner/internal/box"
	invsqlite "github.com/thewelshrich/schooner/internal/inventory/sqlite"
	sshRuntime "github.com/thewelshrich/schooner/internal/runtime/ssh"
)

const (
	exitSuccess = 0
	exitFailure = 1
	exitUsage   = 2
	exitAbort   = 130
)

type Streams struct {
	In            io.Reader
	Out           io.Writer
	Err           io.Writer
	InIsTerminal  bool
	OutIsTerminal bool
	ErrIsTerminal bool
}

type BuildInfo struct {
	Version   string
	Commit    string
	BuiltAt   string
	GoVersion string
	OS        string
	Arch      string
}

type globalOptions struct {
	output     string
	noInput    bool
	color      string
	theme      string
	accessible bool
}

func Run(ctx context.Context, args []string, streams Streams, build BuildInfo) int {
	streams = normalizedStreams(streams)
	if err := ctx.Err(); err != nil {
		return exitAbort
	}
	options := &globalOptions{}
	root := newRootCommand(build, streams, options)
	root.SetArgs(args)
	root.SetIn(streams.In)
	root.SetOut(streams.Out)
	root.SetErr(streams.Err)
	if err := root.ExecuteContext(ctx); err != nil {
		if ctx.Err() != nil {
			return exitAbort
		}
		var aborted abortError
		if errors.As(err, &aborted) {
			return exitAbort
		}
		printError(streams.Err, err, options.output)
		var failure executionError
		if errors.As(err, &failure) {
			return exitFailure
		}
		return exitUsage
	}
	return exitSuccess
}

func newRootCommand(build BuildInfo, streams Streams, options *globalOptions) *cobra.Command {
	root := &cobra.Command{
		Use:           "schooner",
		Short:         "Create and operate persistent development machines",
		SilenceErrors: true,
		SilenceUsage:  true,
		Version:       defaultString(build.Version, "dev"),
		Args:          cobra.NoArgs,
		PersistentPreRunE: func(*cobra.Command, []string) error {
			if options.output != "human" && options.output != "json" {
				return usageError{cause: fmt.Errorf("unsupported output format %q (expected human or json)", options.output)}
			}
			if options.color != "auto" && options.color != "always" && options.color != "never" {
				return usageError{cause: fmt.Errorf("unsupported color mode %q (expected auto, always, or never)", options.color)}
			}
			if options.theme != "auto" && options.theme != "light" && options.theme != "dark" {
				return usageError{cause: fmt.Errorf("unsupported theme mode %q (expected auto, light, or dark)", options.theme)}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := cmd.Help(); err != nil {
				return executionError{cause: err}
			}
			return nil
		},
	}
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return usageError{cause: err} })
	root.PersistentFlags().StringVar(&options.output, "output", "human", "output format: human or json")
	root.PersistentFlags().BoolVar(&options.noInput, "no-input", false, "disable interactive prompts")
	root.PersistentFlags().StringVar(&options.color, "color", "auto", "color mode: auto, always, or never")
	root.PersistentFlags().StringVar(&options.theme, "theme", "auto", "terminal theme: auto, light, or dark")
	root.PersistentFlags().BoolVar(&options.accessible, "accessible", false, "use screen-reader-friendly prompts and progress")
	root.AddCommand(newVersionCommand(build, options))
	root.AddCommand(newBoxCommand(streams, options))
	return root
}

func openBoxService(ctx context.Context, streams Streams) (*box.Service, func(), error) {
	path, err := invsqlite.DefaultPath()
	if err != nil {
		return nil, nil, err
	}
	store, err := invsqlite.Open(ctx, path)
	if err != nil {
		return nil, nil, err
	}
	runtime := sshRuntime.New("", streams.Err)
	return box.New(runtime, store), func() { _ = store.Close() }, nil
}

type executionError struct{ cause error }

func (e executionError) Error() string { return e.cause.Error() }
func (e executionError) Unwrap() error { return e.cause }

type usageError struct{ cause error }

func (e usageError) Error() string { return e.cause.Error() }
func (e usageError) Unwrap() error { return e.cause }

type abortError struct{ cause error }

func (e abortError) Error() string { return e.cause.Error() }
func (e abortError) Unwrap() error { return e.cause }

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

func printError(w io.Writer, err error, output string) {
	if output == "json" {
		code := box.ErrorCode(err)
		var usage usageError
		if errors.As(err, &usage) {
			code = "invalid_input"
		}
		var domain *box.Error
		contextValues := map[string]string{}
		if errors.As(err, &domain) && domain.Context != nil {
			contextValues = domain.Context
		}
		document := struct {
			SchemaVersion string `json:"schema_version"`
			Error         struct {
				Code    string            `json:"code"`
				Message string            `json:"message"`
				Context map[string]string `json:"context,omitempty"`
			} `json:"error"`
		}{SchemaVersion: "1"}
		document.Error.Code, document.Error.Message, document.Error.Context = code, err.Error(), contextValues
		_ = json.NewEncoder(w).Encode(document)
		return
	}
	printHumanError(w, err)
	var domain *box.Error
	if errors.As(err, &domain) {
		if observed := domain.Context["last_observed_at"]; observed != "" {
			_, _ = fmt.Fprintf(w, "Last known observation: %s (stale)\n", observed)
			_, _ = fmt.Fprintf(w, "Last known capabilities: %s, %s; %s; %s\n", domain.Context["last_os"], domain.Context["last_architecture"], domain.Context["last_git"], domain.Context["last_tmux"])
		}
	}
}

func printHumanError(w io.Writer, err error) { _, _ = fmt.Fprintf(w, "Error: %v\n", err) }
func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

type emptyReader struct{}

func (emptyReader) Read([]byte) (int, error) { return 0, io.EOF }

func colorDisabled(options *globalOptions, streams Streams) bool {
	return options.color == "never" || (options.color == "auto" && (!streams.ErrIsTerminal || os.Getenv("NO_COLOR") != ""))
}
