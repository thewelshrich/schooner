package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/thewelshrich/schooner/internal/selfupdate"
	"github.com/thewelshrich/schooner/internal/semver"
	uitheme "github.com/thewelshrich/schooner/internal/ui/theme"
)

type selfUpdateRunner func(context.Context, selfupdate.Mode) (selfupdate.Result, error)

type updateDocument struct {
	SchemaVersion      string `json:"schema_version"`
	InstalledVersion   string `json:"installed_version"`
	AvailableVersion   string `json:"available_version"`
	InstallationMethod string `json:"installation_method"`
	Action             string `json:"action"`
	Guidance           string `json:"guidance"`
}

func defaultSelfUpdateRunner(build BuildInfo) selfUpdateRunner {
	return func(ctx context.Context, mode selfupdate.Mode) (selfupdate.Result, error) {
		updater, err := selfupdate.NewDefault(selfupdate.Current{Version: defaultString(build.Version, "dev"), OS: build.OS, Arch: build.Arch, ExecutablePath: build.ExecutablePath, InvocationPath: build.InvocationPath})
		if err != nil {
			return selfupdate.Result{}, err
		}
		return updater.Run(ctx, mode)
	}
}

func newUpdateCommand(streams Streams, options *globalOptions) *cobra.Command {
	var check bool
	command := &cobra.Command{
		Use:   "update",
		Short: "Check or update the local Schooner executable",
		Long:  "Check or update the local Schooner executable. Direct installations may be replaced after ownership and release verification. Homebrew and source installations remain owned by their installer.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			mode := selfupdate.ModeApply
			if check {
				mode = selfupdate.ModeCheck
			}
			result, err := options.selfUpdate(cmd.Context(), mode)
			if err != nil {
				return executionError{cause: err}
			}
			if err = writeLocalUpdateResult(cmd.OutOrStdout(), options.output, result, outputTheme(options, streams)); err != nil {
				return executionError{cause: err}
			}
			return nil
		},
	}
	command.Flags().BoolVar(&check, "check", false, "check for an update without changing the executable")
	command.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return usageError{cause: err} })
	return command
}

func writeLocalUpdateResult(w io.Writer, output string, result selfupdate.Result, theme *uitheme.Theme) error {
	switch output {
	case "json":
		document := updateDocument{
			SchemaVersion: selfupdate.SchemaVersion, InstalledVersion: result.InstalledVersion,
			AvailableVersion: result.AvailableVersion, InstallationMethod: result.InstallationMethod,
			Action: result.Action, Guidance: result.Guidance,
		}
		encoder := json.NewEncoder(w)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(document)
	case "human":
		return writeHumanUpdate(w, result, theme)
	default:
		return fmt.Errorf("unsupported output format %q (expected human or json)", output)
	}
}

func writeHumanUpdate(w io.Writer, result selfupdate.Result, theme *uitheme.Theme) error {
	title := "Schooner update"
	switch result.Action {
	case selfupdate.ActionUpdated:
		title = "Schooner updated"
	case selfupdate.ActionUpdateAvailable:
		title = "Schooner update available"
	case selfupdate.ActionUpToDate:
		title = "Schooner is up to date"
	case selfupdate.ActionUsePackageManager:
		title = "Homebrew owns this installation"
	case selfupdate.ActionReinstallSource:
		title = "Source installation detected"
	}
	rows := []summaryRow{{Label: "Installed", Value: emptyUpdateValue(result.InstalledVersion)}, {Label: "Available", Value: emptyUpdateValue(result.AvailableVersion)}, {Label: "Installation", Value: result.InstallationMethod}, {Label: "Action", Value: result.Action}}
	if err := writeReadySummary(w, theme, title, rows); err != nil {
		return err
	}
	return writeExplanation(w, theme, result.Guidance)
}

func emptyUpdateValue(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}

func maybeWriteAutomaticUpdateNotice(ctx context.Context, args []string, command *cobra.Command, streams Streams, options *globalOptions) {
	if !automaticUpdateEligible(args, command, streams, options) {
		return
	}
	checkContext, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	result, err := options.selfUpdate(checkContext, selfupdate.ModeAutomatic)
	if err != nil || result.Action != selfupdate.ActionUpdateAvailable {
		return
	}
	_, _ = fmt.Fprintf(streams.Err, "\nUpdate available: %s → %s\n%s\n", result.InstalledVersion, result.AvailableVersion, result.Guidance)
}

func automaticUpdateEligible(args []string, command *cobra.Command, streams Streams, options *globalOptions) bool {
	if command == nil || command.Parent() == nil || options.selfUpdate == nil || options.output != "human" || options.noInput || !streams.OutIsTerminal || !streams.ErrIsTerminal || !semver.Stable(options.build.Version) {
		return false
	}
	if os.Getenv("CI") != "" || os.Getenv("SCHOONER_NO_UPDATE_CHECK") == "1" {
		return false
	}
	for _, argument := range args {
		if argument == "help" || argument == "--help" || argument == "-h" {
			return false
		}
	}
	for current := command; current != nil; current = current.Parent() {
		if current.Hidden {
			return false
		}
	}
	name := command.Name()
	if name == "version" || name == "update" || name == "completion" || strings.HasPrefix(command.CommandPath(), "schooner completion ") {
		return false
	}
	return true
}
