package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/spf13/cobra"
	"github.com/thewelshrich/schooner/internal/box"
	"github.com/thewelshrich/schooner/internal/ui/prompts"
	uitheme "github.com/thewelshrich/schooner/internal/ui/theme"
)

func newBoxCommand(streams Streams, global *globalOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "box", Short: "Add and operate development boxes", Args: cobra.NoArgs, RunE: helpRun}
	cmd.AddCommand(newBoxAddCommand(streams, global), newBoxStatusCommand(streams, global), newBoxRemoveCommand(streams, global))
	return cmd
}

func newBoxAddCommand(streams Streams, global *globalOptions) *cobra.Command {
	var destination, projectRoot string
	var yes, acceptNew bool
	cmd := &cobra.Command{
		Use:   "add [name]",
		Short: "Adopt an existing machine over SSH",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := ""
			if len(args) == 1 {
				name = args[0]
			}
			interactive := interactionAllowed(streams, global)
			nameSet := name != ""
			sshSet := cmd.Flags().Changed("ssh")
			rootSet := cmd.Flags().Changed("project-root")
			if (!nameSet || !sshSet) && !interactive {
				return usageError{cause: fmt.Errorf("name and --ssh are required when prompts are unavailable")}
			}
			if nameSet {
				if err := box.ValidateName(name); err != nil {
					return usageError{cause: err}
				}
			}
			if sshSet {
				if err := box.ValidateSSHDestination(destination); err != nil {
					return usageError{cause: err}
				}
			}
			if rootSet {
				if err := box.ValidateProjectRoot(projectRoot); err != nil {
					return usageError{cause: err}
				}
			}
			if !rootSet {
				projectRoot = box.DefaultProjectRoot
			}
			if interactive && (!yes || !nameSet || !sshSet) {
				draft, confirmed, err := prompts.Add(cmd.Context(), promptOptions(streams, global), prompts.AddDraft{Name: name, SSHDestination: destination, ProjectRoot: projectRoot}, nameSet, sshSet, rootSet, yes)
				if errors.Is(err, prompts.ErrAborted) {
					return abortError{cause: err}
				}
				if err != nil {
					return executionError{cause: err}
				}
				if !confirmed {
					_, _ = fmt.Fprintln(streams.Out, "Cancelled. No changes made.")
					return nil
				}
				name, destination, projectRoot = draft.Name, draft.SSHDestination, draft.ProjectRoot
			} else if !yes {
				return usageError{cause: fmt.Errorf("--yes is required when prompts are unavailable")}
			}
			service, closeService, err := openBoxService(cmd.Context(), streams)
			if err != nil {
				return executionError{cause: err}
			}
			defer closeService()
			var progress box.Progress
			if global.output == "human" {
				renderer := newProgressRenderer(cmd.Context(), streams.Err, streams.ErrIsTerminal && !global.accessible, terminalTheme(global, streams))
				defer renderer.Close()
				progress = renderer.Event
			}
			result, err := service.Add(cmd.Context(), box.AddRequest{Name: name, SSHDestination: destination, ProjectRoot: projectRoot, AcceptNewHostKey: acceptNew, BatchMode: !interactive, Progress: progress})
			if err != nil {
				return executionError{cause: err}
			}
			if err = writeAddResult(streams.Out, global.output, result); err != nil {
				return executionError{cause: err}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&destination, "ssh", "", "OpenSSH destination (alias or user@host)")
	cmd.Flags().StringVar(&projectRoot, "project-root", "", "remote project root")
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm the remote setup")
	cmd.Flags().BoolVar(&acceptNew, "accept-new-host-key", false, "allow OpenSSH to trust a new host key (never a changed key)")
	return cmd
}

func newBoxStatusCommand(streams Streams, global *globalOptions) *cobra.Command {
	return &cobra.Command{
		Use: "status [name]", Short: "Show live box status", Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			service, closeService, err := openBoxService(cmd.Context(), streams)
			if err != nil {
				return executionError{cause: err}
			}
			defer closeService()
			name := ""
			if len(args) == 1 {
				name = args[0]
			}
			if name == "" {
				if !interactionAllowed(streams, global) {
					return usageError{cause: fmt.Errorf("box name is required when prompts are unavailable")}
				}
				records, listErr := service.List(cmd.Context())
				if listErr != nil {
					return executionError{cause: listErr}
				}
				name, err = prompts.PickBox(cmd.Context(), promptOptions(streams, global), "Choose a box", records)
				if errors.Is(err, prompts.ErrAborted) {
					return abortError{cause: err}
				}
				if err != nil {
					return executionError{cause: err}
				}
			}
			if err := box.ValidateName(name); err != nil {
				return usageError{cause: err}
			}
			var progress box.Progress
			if global.output == "human" {
				renderer := newProgressRenderer(cmd.Context(), streams.Err, streams.ErrIsTerminal && !global.accessible, terminalTheme(global, streams))
				defer renderer.Close()
				progress = renderer.Event
			}
			result, err := service.Status(cmd.Context(), box.StatusRequest{Name: name, BatchMode: !interactionAllowed(streams, global), Progress: progress})
			if err != nil {
				return executionError{cause: err}
			}
			if err = writeStatusResult(streams.Out, global.output, result); err != nil {
				return executionError{cause: err}
			}
			return nil
		},
	}
}

func newBoxRemoveCommand(streams Streams, global *globalOptions) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use: "remove [name]", Short: "Forget a box without changing its machine", Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			service, closeService, err := openBoxService(cmd.Context(), streams)
			if err != nil {
				return executionError{cause: err}
			}
			defer closeService()
			interactive := interactionAllowed(streams, global)
			name := ""
			if len(args) == 1 {
				name = args[0]
			}
			if name != "" {
				if err := box.ValidateName(name); err != nil {
					return usageError{cause: err}
				}
			}
			records, err := service.List(cmd.Context())
			if err != nil {
				return executionError{cause: err}
			}
			if name == "" {
				if !interactive {
					return usageError{cause: fmt.Errorf("box name is required when prompts are unavailable")}
				}
				name, err = prompts.PickBox(cmd.Context(), promptOptions(streams, global), "Choose a box to remove", records)
				if errors.Is(err, prompts.ErrAborted) {
					return abortError{cause: err}
				}
				if err != nil {
					return executionError{cause: err}
				}
			}
			if !yes {
				if !interactive {
					return usageError{cause: fmt.Errorf("--yes is required when prompts are unavailable")}
				}
				index := slices.IndexFunc(records, func(record box.Record) bool { return record.Name == name })
				if index < 0 {
					return executionError{cause: box.NotFound(name)}
				}
				confirmed, confirmErr := prompts.ConfirmRemove(cmd.Context(), promptOptions(streams, global), records[index])
				if errors.Is(confirmErr, prompts.ErrAborted) {
					return abortError{cause: confirmErr}
				}
				if confirmErr != nil {
					return executionError{cause: confirmErr}
				}
				if !confirmed {
					_, _ = fmt.Fprintln(streams.Out, "Cancelled. No changes made.")
					return nil
				}
			}
			result, err := service.Remove(cmd.Context(), name)
			if err != nil {
				return executionError{cause: err}
			}
			if err = writeRemoveResult(streams.Out, global.output, result); err != nil {
				return executionError{cause: err}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm local removal")
	return cmd
}

func interactionAllowed(streams Streams, options *globalOptions) bool {
	return streams.InIsTerminal && streams.ErrIsTerminal && !options.noInput && options.output == "human"
}
func promptOptions(streams Streams, options *globalOptions) prompts.Options {
	return prompts.Options{Input: streams.In, Output: streams.Err, Accessible: options.accessible, Theme: terminalTheme(options, streams)}
}
func terminalTheme(options *globalOptions, streams Streams) *uitheme.Theme {
	return uitheme.New(uitheme.Mode(options.theme), !colorDisabled(options, streams))
}
func helpRun(cmd *cobra.Command, _ []string) error { return cmd.Help() }

type boxDocument struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Acquisition    string `json:"acquisition"`
	SSHDestination string `json:"ssh_destination"`
	RemoteIdentity string `json:"remote_identity"`
	ProjectRoot    string `json:"project_root"`
}
type capabilitiesDocument struct {
	OS struct {
		ID      string `json:"id"`
		Version string `json:"version"`
	} `json:"os"`
	Architecture      string   `json:"architecture"`
	ProjectRoot       string   `json:"project_root"`
	ProjectRootExists bool     `json:"project_root_exists"`
	Git               box.Tool `json:"git"`
	Tmux              box.Tool `json:"tmux"`
}

func documentBox(record box.Record) boxDocument {
	return boxDocument{record.ID, record.Name, record.Acquisition, record.SSHDestination, record.RemoteIdentity, record.ProjectRoot}
}
func documentCapabilities(value box.Capabilities) capabilitiesDocument {
	result := capabilitiesDocument{Architecture: value.Architecture, ProjectRoot: value.ProjectRoot, ProjectRootExists: value.ProjectRootExists, Git: value.Git, Tmux: value.Tmux}
	result.OS.ID, result.OS.Version = value.OSID, value.OSVersion
	return result
}

func writeAddResult(w interface{ Write([]byte) (int, error) }, output string, result box.AddResult) error {
	if output == "json" {
		doc := struct {
			SchemaVersion string               `json:"schema_version"`
			Box           boxDocument          `json:"box"`
			Capabilities  capabilitiesDocument `json:"capabilities"`
			Setup         struct {
				Installed []string `json:"installed"`
				Verified  []string `json:"verified"`
			} `json:"setup"`
		}{SchemaVersion: "1", Box: documentBox(result.Box), Capabilities: documentCapabilities(result.Capabilities)}
		doc.Setup.Installed, doc.Setup.Verified = result.Installed, result.Verified
		if doc.Setup.Installed == nil {
			doc.Setup.Installed = []string{}
		}
		return json.NewEncoder(w).Encode(doc)
	}
	_, err := fmt.Fprintf(w, "Added box %s\nSSH: %s\nProject root: %s\nUbuntu: %s (%s)\nGit: %s\ntmux: %s\n", result.Box.Name, result.Box.SSHDestination, result.Box.ProjectRoot, result.Capabilities.OSVersion, result.Capabilities.Architecture, result.Capabilities.Git.Version, result.Capabilities.Tmux.Version)
	return err
}

func writeStatusResult(w interface{ Write([]byte) (int, error) }, output string, result box.StatusResult) error {
	if output == "json" {
		doc := struct {
			SchemaVersion string      `json:"schema_version"`
			Box           boxDocument `json:"box"`
			Status        struct {
				Reachable        bool                 `json:"reachable"`
				IdentityVerified bool                 `json:"identity_verified"`
				ObservedAt       string               `json:"observed_at"`
				Capabilities     capabilitiesDocument `json:"capabilities"`
			} `json:"status"`
		}{SchemaVersion: "1", Box: documentBox(result.Box)}
		doc.Status.Reachable, doc.Status.IdentityVerified = true, true
		doc.Status.ObservedAt = result.Observation.ObservedAt.Format(time.RFC3339)
		doc.Status.Capabilities = documentCapabilities(result.Observation.Capabilities)
		return json.NewEncoder(w).Encode(doc)
	}
	c := result.Observation.Capabilities
	projectRoot := c.ProjectRoot
	if !c.ProjectRootExists {
		projectRoot += " (missing)"
	}
	_, err := fmt.Fprintf(w, "%s is reachable\nSSH: %s\nObserved: %s\nUbuntu: %s (%s)\nProject root: %s\nGit: %s\ntmux: %s\n", result.Box.Name, result.Box.SSHDestination, result.Observation.ObservedAt.Format(time.RFC3339), c.OSVersion, c.Architecture, projectRoot, c.Git.Version, c.Tmux.Version)
	return err
}

func writeRemoveResult(w interface{ Write([]byte) (int, error) }, output string, result box.RemoveResult) error {
	if output == "json" {
		return json.NewEncoder(w).Encode(struct {
			SchemaVersion   string      `json:"schema_version"`
			Box             boxDocument `json:"box"`
			RemoteUnchanged bool        `json:"remote_unchanged"`
		}{"1", documentBox(result.Box), result.RemoteUnchanged})
	}
	_, err := fmt.Fprintf(w, "Removed box %s from Schooner. The remote machine was not changed.\n", result.Box.Name)
	return err
}
