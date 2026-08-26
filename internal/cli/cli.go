// Package cli adapts Schooner's command-line interface to application behavior.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/thewelshrich/schooner/internal/acquisition"
	"github.com/thewelshrich/schooner/internal/artifact"
	"github.com/thewelshrich/schooner/internal/box"
	"github.com/thewelshrich/schooner/internal/boxtarget"
	"github.com/thewelshrich/schooner/internal/credentials"
	invsqlite "github.com/thewelshrich/schooner/internal/inventory/sqlite"
	digitalOcean "github.com/thewelshrich/schooner/internal/provider/digitalocean"
	localHost "github.com/thewelshrich/schooner/internal/runtime/host"
	sshRuntime "github.com/thewelshrich/schooner/internal/runtime/ssh"
	"github.com/thewelshrich/schooner/internal/ui/prompts"
	uitheme "github.com/thewelshrich/schooner/internal/ui/theme"
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
	build         BuildInfo
	output        string
	noInput       bool
	color         string
	theme         string
	accessible    bool
	choiceSummary *prompts.ChoiceSummary
	hostRuntime   func() *localHost.Runtime
}

func Run(ctx context.Context, args []string, streams Streams, build BuildInfo) int {
	return runWithHostRuntime(ctx, args, streams, build, func() *localHost.Runtime { return localHost.New(hostBuildInfo(build)) })
}

func RunAtHostHome(ctx context.Context, args []string, streams Streams, build BuildInfo, home string) int {
	return runWithHostRuntime(ctx, args, streams, build, func() *localHost.Runtime { return localHost.NewAtHome(hostBuildInfo(build), home) })
}

func runWithHostRuntime(ctx context.Context, args []string, streams Streams, build BuildInfo, newHostRuntime func() *localHost.Runtime) int {
	streams = normalizedStreams(streams)
	if err := ctx.Err(); err != nil {
		return exitAbort
	}
	options := &globalOptions{build: build, hostRuntime: newHostRuntime}
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
		var status exitStatusError
		if errors.As(err, &status) {
			return status.code
		}
		var reported reportedExecutionError
		if errors.As(err, &reported) {
			return exitFailure
		}
		printError(streams.Err, err, options.output, terminalTheme(options, streams))
		var failure executionError
		if errors.As(err, &failure) {
			return exitFailure
		}
		return exitUsage
	}
	return exitSuccess
}

func newRootCommand(build BuildInfo, streams Streams, options *globalOptions) *cobra.Command {
	targets := newBoxTargetResolver(streams, options)
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
	root.AddCommand(newVersionCommand(streams, build, options))
	if defaultString(build.Version, "dev") == "dev" {
		root.AddCommand(newDevelopmentCommand(options, artifact.BuildDevelopment))
	}
	root.AddCommand(newDoctorCommand(streams, options))
	root.AddCommand(newHostCommand(streams, options))
	root.AddCommand(newDatabaseCommand(streams, options))
	root.AddCommand(newProviderCommand(streams, options))
	root.AddCommand(newBoxCommand(streams, options))
	root.AddCommand(newCloneCommand(streams, options, targets))
	root.AddCommand(newWorktreeCommand(streams, options, targets))
	root.AddCommand(newSessionCommands(streams, options, targets)...)
	return root
}

func newBoxTargetResolver(streams Streams, options *globalOptions) *boxtarget.Resolver {
	remote := sshRuntime.NewHost("", streams.Err, defaultString(options.build.Version, "dev"), artifact.NewDeferredDefault())
	return boxtarget.NewResolver(boxtarget.Options{
		Direct: options.hostRuntime,
		Remote: remote,
		OpenInventory: func(ctx context.Context) (boxtarget.Inventory, error) {
			path, err := invsqlite.DefaultPath()
			if err != nil {
				return nil, err
			}
			return invsqlite.Open(ctx, path)
		},
		OpenExistingInventory: func(ctx context.Context) (boxtarget.Inventory, bool, error) {
			path, err := invsqlite.DefaultPath()
			if err != nil {
				return nil, false, err
			}
			if _, err = os.Stat(path); errors.Is(err, os.ErrNotExist) {
				return nil, false, nil
			} else if err != nil {
				return nil, false, err
			}
			store, err := invsqlite.Open(ctx, path)
			return store, err == nil, err
		},
	})
}

func openBoxService(ctx context.Context, streams Streams, build BuildInfo) (*box.Service, func(), error) {
	services, closeServices, err := openApplication(ctx, streams, build)
	if err != nil {
		return nil, nil, err
	}
	return services.boxes, closeServices, nil
}

type application struct {
	boxes       *box.Service
	boxResolver *box.Resolver
	credentials *credentials.Manager
	acquisition *acquisition.Service
	ssh         *sshRuntime.Runtime
}

type sshIdentitySource struct{ stateDirectory string }

func (s sshIdentitySource) Ensure(ctx context.Context) (acquisition.Identity, error) {
	identity, err := sshRuntime.EnsureIdentity(ctx, s.stateDirectory)
	return acquisition.Identity{PublicKey: identity.PublicKey, PrivateKey: identity.PrivateKey}, err
}

func openApplication(ctx context.Context, streams Streams, build BuildInfo) (*application, func(), error) {
	path, err := invsqlite.DefaultPath()
	if err != nil {
		return nil, nil, err
	}
	store, err := invsqlite.Open(ctx, path)
	if err != nil {
		return nil, nil, err
	}
	artifacts := artifact.NewDeferredDefault()
	runtime := sshRuntime.NewHost("", streams.Err, defaultString(build.Version, "dev"), artifacts)
	boxes := box.New(runtime, store)
	cloud := digitalOcean.New()
	credentialManager := credentials.New(store, credentials.KeyringStore{}, cloud)
	acquisitionService := acquisition.New(boxes, store, credentialManager, cloud, sshIdentitySource{stateDirectory: filepath.Dir(path)}, runtime)
	return &application{boxes: boxes, boxResolver: box.NewResolver(store), credentials: credentialManager, acquisition: acquisitionService, ssh: runtime}, func() { _ = store.Close() }, nil
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

type exitStatusError struct{ code int }

func (e exitStatusError) Error() string {
	return fmt.Sprintf("command exited with status %d", e.code)
}

type reportedExecutionError struct{ cause error }

func (e reportedExecutionError) Error() string { return e.cause.Error() }
func (e reportedExecutionError) Unwrap() error { return e.cause }

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

func printError(w io.Writer, err error, output string, theme *uitheme.Theme) {
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
	printHumanError(w, err, theme)
	var domain *box.Error
	if errors.As(err, &domain) {
		if observed := domain.Context["last_observed_at"]; observed != "" {
			_ = writeMutedNotice(w, theme, "Last known observation: "+observed+" (stale)")
			_ = writeMutedNotice(w, theme, "Last known capabilities: "+domain.Context["last_os"]+", "+domain.Context["last_architecture"]+"; "+domain.Context["last_git"]+"; "+domain.Context["last_tmux"])
		}
	}
}

func printHumanError(w io.Writer, err error, theme *uitheme.Theme) {
	prefix, body := "Error:", err.Error()
	if theme != nil && theme.HasColor() {
		prefix = theme.Style(uitheme.Error).Render(prefix)
		body = theme.Style(uitheme.Text).Render(body)
	}
	_, _ = fmt.Fprintf(w, "%s %s\n", prefix, body)
}
func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

type emptyReader struct{}

func (emptyReader) Read([]byte) (int, error) { return 0, io.EOF }

func colorDisabled(options *globalOptions, streams Streams) bool {
	return colorDisabledForTerminal(options, streams.ErrIsTerminal)
}

func colorDisabledForTerminal(options *globalOptions, isTerminal bool) bool {
	return options.color == "never" || (options.color == "auto" && (!isTerminal || os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb"))
}
