package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"
	"github.com/thewelshrich/schooner/internal/acquisition"
	"github.com/thewelshrich/schooner/internal/box"
	"github.com/thewelshrich/schooner/internal/credentials"
	providerdomain "github.com/thewelshrich/schooner/internal/provider"
	sshRuntime "github.com/thewelshrich/schooner/internal/runtime/ssh"
	"github.com/thewelshrich/schooner/internal/ui/intro"
	"github.com/thewelshrich/schooner/internal/ui/prompts"
	uitheme "github.com/thewelshrich/schooner/internal/ui/theme"
)

func newBoxCommand(streams Streams, global *globalOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "box", Short: "Add and operate development boxes", Args: cobra.NoArgs, RunE: helpRun}
	cmd.AddCommand(newBoxAddCommand(streams, global), newBoxListCommand(streams, global), newBoxStatusCommand(streams, global), newBoxSSHCommand(streams, global), newBoxRemoveCommand(streams, global), newBoxDestroyCommand(streams, global))
	return cmd
}

func newBoxAddCommand(streams Streams, global *globalOptions) *cobra.Command {
	var destination, workspaceRoot, providerID, profileName, region, size, image, vpc string
	var accountKeys []string
	var yes, acceptNew, noAccountKeys, backups, ipv6 bool
	ipv6 = true
	cmd := &cobra.Command{
		Use:   "add [name]",
		Short: "Adopt an SSH machine or provision one with a provider",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			global.choiceSummary = prompts.NewChoiceSummary()
			name := ""
			if len(args) == 1 {
				name = args[0]
			}
			interactive := interactionAllowed(streams, global)
			if name != "" && providerID == "" && !cmd.Flags().Changed("ssh") {
				if op, resumeErr := interruptedDigitalOceanAdd(cmd.Context(), streams, global.build, name); resumeErr == nil {
					if interactive {
						clearBoxAddScreen(streams, global)
					}
					return runDigitalOceanAdd(cmd, streams, global, digitalOceanOptionsFromInterrupted(op, digitalOceanAddOptions{Yes: yes, AcceptNew: acceptNew, Interactive: interactive, Resume: true}))
				} else if !box.IsNotFound(resumeErr) {
					return executionError{cause: resumeErr}
				}
			}
			if interactive && (!yes || (providerID == "" && destination == "")) {
				clearBoxAddScreen(streams, global)
			}
			introShown := false
			acquisitionShown := false
			if providerID == "" && destination == "" && interactive {
				if err := showInteractiveIntro(cmd.Context(), streams, global); err != nil {
					return err
				}
				introShown = true
				method, err := prompts.ChooseAcquisition(cmd.Context(), promptOptions(streams, global))
				if errors.Is(err, prompts.ErrAborted) {
					return abortError{cause: err}
				}
				if err != nil {
					return executionError{cause: err}
				}
				if method == "digitalocean" {
					providerID = method
				}
				acquisitionShown = true
			}
			if providerID != "" && providerID != string(providerdomain.DigitalOcean) {
				return usageError{cause: fmt.Errorf("unsupported provider %q", providerID)}
			}
			if providerID != "" && cmd.Flags().Changed("ssh") {
				return usageError{cause: fmt.Errorf("--provider and --ssh are mutually exclusive")}
			}
			if providerID == string(providerdomain.DigitalOcean) {
				return runDigitalOceanAdd(cmd, streams, global, digitalOceanAddOptions{Name: name, WorkspaceRoot: workspaceRoot, Profile: profileName, Region: region, Size: size, Image: image, VPC: vpc, AccountKeys: accountKeys, NoAccountKeys: noAccountKeys, Backups: backups, IPv6: ipv6, Yes: yes, AcceptNew: acceptNew, Interactive: interactive, IntroShown: introShown, AcquisitionShown: acquisitionShown})
			}
			nameSet := name != ""
			sshSet := cmd.Flags().Changed("ssh")
			rootSet := cmd.Flags().Changed("workspace-root")
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
				if err := box.ValidateWorkspaceRoot(workspaceRoot); err != nil {
					return usageError{cause: err}
				}
			}
			if !rootSet {
				workspaceRoot = box.DefaultWorkspaceRoot
			}
			if interactive && (!yes || !nameSet || !sshSet) {
				if !introShown {
					if err := showInteractiveIntro(cmd.Context(), streams, global); err != nil {
						return err
					}
				}
				if !acquisitionShown {
					prompts.ShowAcquisition(promptOptions(streams, global), "ssh")
				}
				draft, confirmed, err := prompts.Add(cmd.Context(), promptOptions(streams, global), prompts.AddDraft{Name: name, SSHDestination: destination, WorkspaceRoot: workspaceRoot}, nameSet, sshSet, rootSet, yes)
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
				name, destination, workspaceRoot = draft.Name, draft.SSHDestination, draft.WorkspaceRoot
			} else if !yes {
				return usageError{cause: fmt.Errorf("--yes is required when prompts are unavailable")}
			}
			service, closeService, err := openBoxService(cmd.Context(), streams, global.build)
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
			result, err := service.Add(cmd.Context(), box.AddRequest{Name: name, SSHDestination: destination, WorkspaceRoot: workspaceRoot, AcceptNewHostKey: acceptNew, BatchMode: !interactive, Progress: progress})
			if err != nil {
				return executionError{cause: err}
			}
			if err = writeAddResult(streams.Out, global.output, result, terminalTheme(global, streams)); err != nil {
				return executionError{cause: err}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&destination, "ssh", "", "OpenSSH destination (alias or user@host)")
	cmd.Flags().StringVar(&providerID, "provider", "", "infrastructure provider (digitalocean)")
	cmd.Flags().StringVar(&profileName, "profile", "", "credential profile (name or digitalocean/name)")
	cmd.Flags().StringVar(&region, "region", "", "DigitalOcean region slug")
	cmd.Flags().StringVar(&size, "size", "", "DigitalOcean size slug")
	cmd.Flags().StringVar(&image, "image", "", "DigitalOcean Ubuntu image slug")
	cmd.Flags().StringVar(&vpc, "vpc", "", "DigitalOcean VPC UUID (empty uses the regional default)")
	cmd.Flags().StringSliceVar(&accountKeys, "account-ssh-key", nil, "DigitalOcean account SSH key ID (repeatable)")
	cmd.Flags().BoolVar(&noAccountKeys, "no-account-ssh-keys", false, "do not add existing DigitalOcean account SSH keys")
	cmd.Flags().BoolVar(&backups, "backups", false, "enable DigitalOcean automatic backups")
	cmd.Flags().BoolVar(&ipv6, "ipv6", true, "enable DigitalOcean IPv6")
	cmd.Flags().StringVar(&workspaceRoot, "workspace-root", "", "remote workspace root")
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm the remote setup")
	cmd.Flags().BoolVar(&acceptNew, "accept-new-host-key", false, "allow OpenSSH to trust a new host key (never a changed key)")
	return cmd
}

type digitalOceanAddOptions struct {
	Name, WorkspaceRoot, Profile, Region, Size, Image, VPC                                  string
	AccountKeys                                                                             []string
	LocalPublicKeys                                                                         []providerdomain.PublicKey
	NoAccountKeys, Backups, IPv6, Yes, AcceptNew, Interactive, IntroShown, AcquisitionShown bool
	Resume                                                                                  bool
}

func interruptedDigitalOceanAdd(ctx context.Context, streams Streams, build BuildInfo, name string) (acquisition.ProvisionOperation, error) {
	services, closeServices, err := openApplication(ctx, streams, build)
	if err != nil {
		return acquisition.ProvisionOperation{}, err
	}
	defer closeServices()
	return services.acquisition.InterruptedProvision(ctx, name)
}

func digitalOceanOptionsFromInterrupted(op acquisition.ProvisionOperation, base digitalOceanAddOptions) digitalOceanAddOptions {
	base.Name = op.Name
	base.WorkspaceRoot = op.WorkspaceRoot
	base.Profile = string(op.Profile)
	base.Region = op.Region
	base.Size = op.Size
	base.Image = op.Image
	base.VPC = op.NetworkID
	base.AccountKeys = append([]string(nil), op.AccessKeyIDs...)
	base.LocalPublicKeys = append([]providerdomain.PublicKey(nil), op.LocalPublicKeys...)
	base.Backups = op.AutomaticBackups
	base.IPv6 = op.IPv6
	base.Resume = true
	return base
}

func runDigitalOceanAdd(cmd *cobra.Command, streams Streams, global *globalOptions, options digitalOceanAddOptions) error {
	if options.NoAccountKeys && len(options.AccountKeys) > 0 {
		return usageError{cause: fmt.Errorf("--no-account-ssh-keys and --account-ssh-key are mutually exclusive")}
	}
	nameSet, rootSet := options.Name != "", cmd.Flags().Changed("workspace-root")
	if options.WorkspaceRoot == "" {
		options.WorkspaceRoot = box.DefaultWorkspaceRoot
	}
	if options.Name != "" {
		if err := box.ValidateName(options.Name); err != nil {
			return usageError{cause: err}
		}
	}
	if err := box.ValidateWorkspaceRoot(options.WorkspaceRoot); err != nil {
		return usageError{cause: err}
	}
	services, closeServices, err := openApplication(cmd.Context(), streams, global.build)
	if err != nil {
		return executionError{cause: err}
	}
	defer closeServices()
	if !options.Resume && options.Name != "" && (options.Region == "" || options.Size == "" || options.Image == "") {
		op, resumeErr := services.acquisition.InterruptedProvision(cmd.Context(), options.Name)
		if resumeErr == nil {
			options = digitalOceanOptionsFromInterrupted(op, options)
		} else if !box.IsNotFound(resumeErr) {
			return executionError{cause: resumeErr}
		}
	}
	if options.Resume {
		return resumeDigitalOceanAdd(cmd, streams, global, options, services)
	}
	if !options.Interactive {
		if options.Name == "" || options.Region == "" || options.Size == "" || options.Image == "" {
			return usageError{cause: fmt.Errorf("name, --region, --size, and --image are required for non-interactive DigitalOcean provisioning")}
		}
		if !options.Yes {
			return usageError{cause: fmt.Errorf("--yes is required when prompts are unavailable")}
		}
		if !options.AcceptNew {
			return usageError{cause: fmt.Errorf("--accept-new-host-key is required for non-interactive DigitalOcean provisioning")}
		}
	}
	if options.Interactive {
		if !options.IntroShown {
			if err := showInteractiveIntro(cmd.Context(), streams, global); err != nil {
				return err
			}
		}
		if !options.AcquisitionShown {
			prompts.ShowAcquisition(promptOptions(streams, global), "digitalocean")
		}
		draft, err := prompts.ProvisionBasics(cmd.Context(), promptOptions(streams, global), prompts.ProvisionDraft{Name: options.Name, WorkspaceRoot: options.WorkspaceRoot}, nameSet, rootSet)
		if errors.Is(err, prompts.ErrAborted) {
			return abortError{cause: err}
		}
		if err != nil {
			return executionError{cause: err}
		}
		options.Name, options.WorkspaceRoot = draft.Name, draft.WorkspaceRoot
	}
	ref, err := profileRef(options.Profile)
	if err != nil {
		return usageError{cause: err}
	}
	if ref == "" && options.Interactive && os.Getenv("DIGITALOCEAN_TOKEN") == "" {
		profiles, listErr := services.credentials.List(cmd.Context())
		if listErr != nil {
			return executionError{cause: listErr}
		}
		var digitalProfiles []credentials.Profile
		for _, profile := range profiles {
			if profile.Provider == providerdomain.DigitalOcean {
				digitalProfiles = append(digitalProfiles, profile)
			}
		}
		if len(digitalProfiles) > 0 {
			ref, err = prompts.PickCredentialProfile(cmd.Context(), promptOptions(streams, global), digitalProfiles)
			if errors.Is(err, prompts.ErrAborted) {
				return abortError{cause: err}
			}
			if err != nil {
				return executionError{cause: err}
			}
			for _, selected := range digitalProfiles {
				if selected.Ref != ref || selected.Status == credentials.StatusConnected {
					continue
				}
				name, token, storeSecret, promptErr := prompts.ConnectDigitalOcean(cmd.Context(), promptOptions(streams, global), selected.Name, "")
				if errors.Is(promptErr, prompts.ErrAborted) {
					return abortError{cause: promptErr}
				}
				if promptErr != nil {
					return executionError{cause: promptErr}
				}
				var profile credentials.Profile
				connectErr := prompts.Wait(cmd.Context(), promptOptions(streams, global), "Connecting DigitalOcean account", func(ctx context.Context) error {
					var err error
					profile, err = services.credentials.Connect(ctx, name, token, storeSecret, selected.Default)
					return err
				})
				if errors.Is(connectErr, prompts.ErrAborted) {
					return abortError{cause: connectErr}
				}
				if connectErr != nil {
					return executionError{cause: connectErr}
				}
				if profile.Warning != "" {
					_, _ = fmt.Fprintln(streams.Err, "Warning:", profile.Warning)
				}
			}
		} else {
			name, token, storeSecret, promptErr := prompts.ConnectDigitalOcean(cmd.Context(), promptOptions(streams, global), "", "")
			if errors.Is(promptErr, prompts.ErrAborted) {
				return abortError{cause: promptErr}
			}
			if promptErr != nil {
				return executionError{cause: promptErr}
			}
			var profile credentials.Profile
			connectErr := prompts.Wait(cmd.Context(), promptOptions(streams, global), "Connecting DigitalOcean account", func(ctx context.Context) error {
				var err error
				profile, err = services.credentials.Connect(ctx, name, token, storeSecret, true)
				return err
			})
			if errors.Is(connectErr, prompts.ErrAborted) {
				return abortError{cause: connectErr}
			}
			if connectErr != nil {
				return executionError{cause: connectErr}
			}
			if profile.Warning != "" {
				_, _ = fmt.Fprintln(streams.Err, "Warning:", profile.Warning)
			}
			ref = profile.Ref
		}
	}
	var catalog providerdomain.Catalog
	var resolvedRef providerdomain.CredentialProfileRef
	err = prompts.Wait(cmd.Context(), promptOptions(streams, global), "Loading DigitalOcean catalog", func(ctx context.Context) error {
		var waitErr error
		catalog, resolvedRef, waitErr = services.acquisition.Catalog(ctx, ref)
		return waitErr
	})
	if errors.Is(err, prompts.ErrAborted) {
		return abortError{cause: err}
	}
	if err != nil {
		return executionError{cause: err}
	}
	ref = resolvedRef
	var localKeys []providerdomain.PublicKey
	if options.Interactive {
		localKeys, err = sshRuntime.DiscoverLocalPublicKeys()
		if err != nil {
			return executionError{cause: err}
		}
	}
	keysSet := cmd.Flags().Changed("account-ssh-key") || options.NoAccountKeys
	selectedLocalKeys := append([]providerdomain.PublicKey(nil), localKeys...)
	if len(selectedLocalKeys) > 15 {
		selectedLocalKeys = selectedLocalKeys[:15]
	}
	if !keysSet {
		for _, key := range catalog.AccessKeys {
			if len(options.AccountKeys)+len(selectedLocalKeys) == 15 {
				break
			}
			options.AccountKeys = append(options.AccountKeys, key.ID)
		}
	}
	draft := prompts.ProvisionDraft{Name: options.Name, WorkspaceRoot: options.WorkspaceRoot, Region: options.Region, Size: options.Size, Image: options.Image, NetworkID: normalizeVPC(options.VPC), AccessKeyIDs: options.AccountKeys, LocalPublicKeys: selectedLocalKeys, AutomaticBackups: options.Backups, IPv6: options.IPv6}
	if options.Interactive {
		draft, err = prompts.DigitalOceanProvision(cmd.Context(), promptOptions(streams, global), draft, catalog, localKeys, cmd.Flags().Changed("region"), cmd.Flags().Changed("size"), cmd.Flags().Changed("image"), cmd.Flags().Changed("vpc"), keysSet)
		if errors.Is(err, prompts.ErrAborted) {
			return abortError{cause: err}
		}
		if err != nil {
			return executionError{cause: err}
		}
		if !options.Yes {
			confirmed, confirmErr := prompts.ConfirmProvision(cmd.Context(), promptOptions(streams, global), ref, draft, catalog, false)
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
	}
	var progress box.Progress
	if global.output == "human" {
		renderer := newProgressRenderer(cmd.Context(), streams.Err, streams.ErrIsTerminal && !global.accessible, terminalTheme(global, streams))
		defer renderer.Close()
		progress = renderer.Event
	}
	result, err := services.acquisition.Provision(cmd.Context(), acquisition.ProvisionRequest{Name: draft.Name, Profile: ref, Region: draft.Region, Size: draft.Size, Image: draft.Image, NetworkID: draft.NetworkID, AccessKeyIDs: draft.AccessKeyIDs, LocalPublicKeys: draft.LocalPublicKeys, AutomaticBackups: draft.AutomaticBackups, IPv6: draft.IPv6, WorkspaceRoot: draft.WorkspaceRoot, AcceptNewHostKey: options.AcceptNew || options.Interactive, BatchMode: true, Progress: progress})
	if err != nil {
		return executionError{cause: err}
	}
	if result.Warning != "" {
		_, _ = fmt.Fprintln(streams.Err, "Warning:", result.Warning)
	}
	return writeAddResult(streams.Out, global.output, result.AddResult, terminalTheme(global, streams))
}

func resumeDigitalOceanAdd(cmd *cobra.Command, streams Streams, global *globalOptions, options digitalOceanAddOptions, services *application) error {
	if options.Name == "" || options.Region == "" || options.Size == "" || options.Image == "" {
		return usageError{cause: fmt.Errorf("interrupted DigitalOcean add is missing required selections")}
	}
	if !options.Interactive {
		if !options.Yes {
			return usageError{cause: fmt.Errorf("--yes is required when prompts are unavailable")}
		}
		if !options.AcceptNew {
			return usageError{cause: fmt.Errorf("--accept-new-host-key is required for non-interactive DigitalOcean provisioning")}
		}
	}
	ref, err := profileRef(options.Profile)
	if err != nil {
		return usageError{cause: err}
	}
	draft := prompts.ProvisionDraft{
		Name:             options.Name,
		WorkspaceRoot:    options.WorkspaceRoot,
		Region:           options.Region,
		Size:             options.Size,
		Image:            options.Image,
		NetworkID:        normalizeVPC(options.VPC),
		AccessKeyIDs:     append([]string(nil), options.AccountKeys...),
		LocalPublicKeys:  append([]providerdomain.PublicKey(nil), options.LocalPublicKeys...),
		AutomaticBackups: options.Backups,
		IPv6:             options.IPv6,
	}
	if options.Interactive {
		if err := showInteractiveIntro(cmd.Context(), streams, global); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(streams.Err, "\nResuming interrupted DigitalOcean add %q.\n", options.Name)
	}
	if options.Interactive && !options.Yes {
		var catalog providerdomain.Catalog
		err = prompts.Wait(cmd.Context(), promptOptions(streams, global), "Loading DigitalOcean catalog", func(ctx context.Context) error {
			var waitErr error
			catalog, ref, waitErr = services.acquisition.Catalog(ctx, ref)
			return waitErr
		})
		if errors.Is(err, prompts.ErrAborted) {
			return abortError{cause: err}
		}
		if err != nil {
			return executionError{cause: err}
		}
		confirmed, confirmErr := prompts.ConfirmProvision(cmd.Context(), promptOptions(streams, global), ref, draft, catalog, true)
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
	var progress box.Progress
	if global.output == "human" {
		renderer := newProgressRenderer(cmd.Context(), streams.Err, streams.ErrIsTerminal && !global.accessible, terminalTheme(global, streams))
		defer renderer.Close()
		progress = renderer.Event
	}
	result, err := services.acquisition.Provision(cmd.Context(), acquisition.ProvisionRequest{Name: draft.Name, Profile: ref, Region: draft.Region, Size: draft.Size, Image: draft.Image, NetworkID: draft.NetworkID, AccessKeyIDs: draft.AccessKeyIDs, LocalPublicKeys: draft.LocalPublicKeys, AutomaticBackups: draft.AutomaticBackups, IPv6: draft.IPv6, WorkspaceRoot: draft.WorkspaceRoot, AcceptNewHostKey: options.AcceptNew || options.Interactive, BatchMode: true, Progress: progress})
	if err != nil {
		return executionError{cause: err}
	}
	if result.Warning != "" {
		_, _ = fmt.Fprintln(streams.Err, "Warning:", result.Warning)
	}
	return writeAddResult(streams.Out, global.output, result.AddResult, terminalTheme(global, streams))
}

func profileRef(value string) (providerdomain.CredentialProfileRef, error) {
	if value == "" {
		return "", nil
	}
	if !slices.Contains([]byte(value), byte('/')) {
		value = "digitalocean/" + value
	}
	return credentials.ParseRef(value)
}
func normalizeVPC(value string) string {
	if value == "default" {
		return ""
	}
	return value
}

func newBoxListCommand(streams Streams, global *globalOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List boxes from local inventory",
		Long:  "Show locally known boxes without probing them. Reachability and last observed time come from the latest successful observation; use box status for a live check.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			service, closeService, err := openBoxService(cmd.Context(), streams, global.build)
			if err != nil {
				return executionError{cause: err}
			}
			defer closeService()
			entries, err := service.ListEntries(cmd.Context())
			if err != nil {
				return executionError{cause: err}
			}
			return writeListResult(streams.Out, global.output, entries, terminalTheme(global, streams))
		},
	}
}

func newBoxStatusCommand(streams Streams, global *globalOptions) *cobra.Command {
	return &cobra.Command{
		Use: "status [name]", Short: "Show live box status", Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			service, closeService, err := openBoxService(cmd.Context(), streams, global.build)
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
				if err := showInteractiveIntro(cmd.Context(), streams, global); err != nil {
					return err
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

func newBoxSSHCommand(streams Streams, global *globalOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "ssh [name]",
		Short: "Open an interactive SSH connection to a box",
		Long:  "Open the current terminal as a normal login shell on a recorded box. A terminal is required; --no-input disables OpenSSH authentication and host-trust prompts.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if global.output != "human" {
				return usageError{cause: fmt.Errorf("box ssh supports human output only")}
			}
			if !streams.InIsTerminal {
				return usageError{cause: fmt.Errorf("box ssh requires an interactive terminal")}
			}

			services, closeServices, err := openApplication(cmd.Context(), streams, global.build)
			if err != nil {
				return executionError{cause: err}
			}
			defer func() {
				if closeServices != nil {
					closeServices()
				}
			}()

			name := ""
			if len(args) == 1 {
				name = args[0]
			}
			if name != "" {
				if err = box.ValidateName(name); err != nil {
					return usageError{cause: err}
				}
			}
			if name == "" {
				if !interactionAllowed(streams, global) {
					return usageError{cause: fmt.Errorf("box name is required when prompts are unavailable")}
				}
				records, listErr := services.boxes.List(cmd.Context())
				if listErr != nil {
					return executionError{cause: listErr}
				}
				if err = showInteractiveIntro(cmd.Context(), streams, global); err != nil {
					return err
				}
				name, err = prompts.PickBox(cmd.Context(), promptOptions(streams, global), "Choose a box to connect to", records)
				if errors.Is(err, prompts.ErrAborted) {
					return abortError{cause: err}
				}
				if err != nil {
					return executionError{cause: err}
				}
			}

			launch, err := services.boxes.PrepareSSH(cmd.Context(), box.SSHRequest{Name: name, BatchMode: global.noInput})
			if err != nil {
				return executionError{cause: err}
			}

			// Do not keep SQLite open for the lifetime of an interactive shell.
			closeServices()
			closeServices = nil

			result, err := services.ssh.OpenShell(cmd.Context(), launch.Connection, sshRuntime.TerminalIO{In: streams.In, Out: streams.Out, Err: streams.Err})
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
		},
	}
}

func newBoxRemoveCommand(streams Streams, global *globalOptions) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use: "remove [name]", Short: "Forget a box without changing its machine", Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			service, closeService, err := openBoxService(cmd.Context(), streams, global.build)
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
			needsIntro := interactive && (name == "" || !yes)
			if needsIntro {
				if err := showInteractiveIntro(cmd.Context(), streams, global); err != nil {
					return err
				}
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

func newBoxDestroyCommand(streams Streams, global *globalOptions) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{Use: "destroy [name]", Short: "Permanently destroy provider infrastructure and remove its box", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		interactive := interactionAllowed(streams, global)
		services, closeServices, err := openApplication(cmd.Context(), streams, global.build)
		if err != nil {
			return executionError{cause: err}
		}
		defer closeServices()
		name := ""
		if len(args) == 1 {
			name = args[0]
		}
		if name == "" {
			if !interactive {
				return usageError{cause: fmt.Errorf("box name is required when prompts are unavailable")}
			}
			records, listErr := services.boxes.List(cmd.Context())
			if listErr != nil {
				return executionError{cause: listErr}
			}
			var provisioned []box.Record
			for _, record := range records {
				if record.Acquisition == "provisioned" {
					provisioned = append(provisioned, record)
				}
			}
			name, err = prompts.PickBox(cmd.Context(), promptOptions(streams, global), "Choose a box to destroy", provisioned)
			if errors.Is(err, prompts.ErrAborted) {
				return abortError{cause: err}
			}
			if err != nil {
				return executionError{cause: err}
			}
		}
		if err = box.ValidateName(name); err != nil {
			return usageError{cause: err}
		}
		record, err := services.boxes.Get(cmd.Context(), name)
		if err != nil {
			return executionError{cause: err}
		}
		if !yes {
			if !interactive {
				return usageError{cause: fmt.Errorf("--yes is required when prompts are unavailable")}
			}
			confirmed, confirmErr := prompts.ConfirmDestroy(cmd.Context(), promptOptions(streams, global), record)
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
		result, err := services.acquisition.Destroy(cmd.Context(), name)
		if err != nil {
			return executionError{cause: err}
		}
		return writeDestroyResult(streams.Out, global.output, result)
	}}
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm permanent provider destruction")
	return cmd
}

func interactionAllowed(streams Streams, options *globalOptions) bool {
	return streams.InIsTerminal && streams.ErrIsTerminal && !options.noInput && options.output == "human"
}
func clearBoxAddScreen(streams Streams, options *globalOptions) {
	if options.accessible {
		return
	}
	_, _ = fmt.Fprint(streams.Err, ansi.EraseEntireScreen+ansi.CursorHomePosition)
}
func promptOptions(streams Streams, options *globalOptions) prompts.Options {
	color := !colorDisabled(options, streams) && !options.accessible
	return prompts.Options{Input: streams.In, Output: streams.Err, Accessible: options.accessible, Theme: uitheme.New(uitheme.Mode(options.theme), color), Summary: options.choiceSummary}
}
func terminalTheme(options *globalOptions, streams Streams) *uitheme.Theme {
	return uitheme.New(uitheme.Mode(options.theme), !colorDisabled(options, streams))
}
func showInteractiveIntro(ctx context.Context, streams Streams, options *globalOptions) error {
	color := !colorDisabled(options, streams)
	decorated := color && !options.accessible
	err := intro.Show(ctx, intro.Options{
		Output:   streams.Err,
		Theme:    uitheme.New(uitheme.Mode(options.theme), decorated),
		Animated: decorated,
		Color:    decorated,
	})
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return abortError{cause: err}
	}
	return executionError{cause: err}
}
func helpRun(cmd *cobra.Command, _ []string) error { return cmd.Help() }

type boxDocument struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Acquisition    string            `json:"acquisition"`
	SSHDestination string            `json:"ssh_destination"`
	RemoteIdentity string            `json:"remote_identity"`
	RuntimePath    string            `json:"runtime_path"`
	WorkspaceRoot  string            `json:"workspace_root"`
	Provider       *providerDocument `json:"provider,omitempty"`
}
type providerDocument struct {
	ID                string `json:"id"`
	ResourceID        string `json:"resource_id,omitempty"`
	CredentialProfile string `json:"credential_profile,omitempty"`
	Region            string `json:"region,omitempty"`
}
type capabilitiesDocument struct {
	OS struct {
		ID      string `json:"id"`
		Version string `json:"version"`
	} `json:"os"`
	Architecture        string          `json:"architecture"`
	WorkspaceRoot       string          `json:"workspace_root"`
	WorkspaceRootExists bool            `json:"workspace_root_exists"`
	Git                 box.Tool        `json:"git"`
	Tmux                box.Tool        `json:"tmux"`
	HostRuntime         box.HostRuntime `json:"host_runtime"`
}

func documentBox(record box.Record) boxDocument {
	doc := boxDocument{ID: record.ID, Name: record.Name, Acquisition: record.Acquisition, SSHDestination: record.SSHDestination, RemoteIdentity: record.RemoteIdentity, RuntimePath: record.RuntimePath, WorkspaceRoot: record.WorkspaceRoot}
	if record.Provider != "" {
		doc.Provider = &providerDocument{ID: record.Provider, ResourceID: record.ProviderResourceID, CredentialProfile: record.CredentialProfile, Region: record.ProviderRegion}
	}
	return doc
}
func documentCapabilities(value box.Capabilities) capabilitiesDocument {
	result := capabilitiesDocument{Architecture: value.Architecture, WorkspaceRoot: value.WorkspaceRoot, WorkspaceRootExists: value.WorkspaceRootExists, Git: value.Git, Tmux: value.Tmux, HostRuntime: value.Host}
	result.OS.ID, result.OS.Version = value.OSID, value.OSVersion
	return result
}

func writeListResult(w io.Writer, output string, entries []box.ListEntry, theme *uitheme.Theme) error {
	if output == "json" {
		type listItem struct {
			Name           string            `json:"name"`
			Acquisition    string            `json:"acquisition"`
			SSHDestination string            `json:"ssh_destination"`
			Provider       *providerDocument `json:"provider"`
			Region         string            `json:"region,omitempty"`
			Reachable      bool              `json:"reachable"`
			LastObservedAt *string           `json:"last_observed_at"`
		}
		doc := struct {
			SchemaVersion string     `json:"schema_version"`
			Boxes         []listItem `json:"boxes"`
		}{SchemaVersion: "1", Boxes: make([]listItem, 0, len(entries))}
		for _, entry := range entries {
			item := listItem{
				Name:           entry.Box.Name,
				Acquisition:    entry.Box.Acquisition,
				SSHDestination: entry.Box.SSHDestination,
				Region:         entry.Box.ProviderRegion,
				Reachable:      entry.Reachable,
			}
			if entry.Box.Provider != "" {
				item.Provider = &providerDocument{ID: entry.Box.Provider, ResourceID: entry.Box.ProviderResourceID, CredentialProfile: entry.Box.CredentialProfile, Region: entry.Box.ProviderRegion}
			}
			if entry.HasObservation {
				observed := entry.LastObservedAt.UTC().Format(time.RFC3339)
				item.LastObservedAt = &observed
			}
			doc.Boxes = append(doc.Boxes, item)
		}
		return json.NewEncoder(w).Encode(doc)
	}
	if len(entries) == 0 {
		_, err := fmt.Fprintln(w, "No boxes in local inventory.")
		return err
	}
	rows := make([][]string, 0, len(entries))
	for _, entry := range entries {
		reachable := "unknown"
		if entry.Reachable {
			reachable = "yes"
		}
		observed := "—"
		if entry.HasObservation {
			observed = entry.LastObservedAt.UTC().Format(time.RFC3339)
		}
		rows = append(rows, []string{
			entry.Box.Name,
			listProviderLabel(entry.Box),
			firstNonEmpty(entry.Box.ProviderRegion, "—"),
			reachable,
			observed,
			entry.Box.SSHDestination,
		})
	}
	return writeTable(w, theme, []string{"NAME", "PROVIDER", "REGION", "REACHABLE", "LAST OBSERVED", "SSH"}, rows)
}

func listProviderLabel(record box.Record) string {
	switch {
	case record.Acquisition == "adopted" || record.Provider == "":
		return "SSH"
	case record.Provider == string(providerdomain.DigitalOcean):
		return "DigitalOcean"
	default:
		return record.Provider
	}
}

func writeTable(w io.Writer, theme *uitheme.Theme, headers []string, rows [][]string) error {
	widths := make([]int, len(headers))
	for i, header := range headers {
		widths[i] = len(header)
	}
	for _, row := range rows {
		for i, cell := range row {
			if len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}
	writeCell := func(value string, width int, header bool) string {
		cell := fmt.Sprintf("%-*s", width, value)
		if theme != nil && theme.HasColor() {
			if header {
				return theme.Style(uitheme.Muted).Render(cell)
			}
			return theme.Style(uitheme.Text).Render(cell)
		}
		return cell
	}
	line := make([]string, len(headers))
	for i, header := range headers {
		line[i] = writeCell(header, widths[i], true)
	}
	if _, err := fmt.Fprintln(w, strings.Join(line, "  ")); err != nil {
		return err
	}
	for _, row := range rows {
		for i, cell := range row {
			line[i] = writeCell(cell, widths[i], false)
		}
		if _, err := fmt.Fprintln(w, strings.Join(line, "  ")); err != nil {
			return err
		}
	}
	return nil
}

func writeAddResult(w io.Writer, output string, result box.AddResult, theme *uitheme.Theme) error {
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
	rows := make([]summaryRow, 0, 8)
	if result.Box.Provider != "" {
		provider := result.Box.Provider
		if result.Box.ProviderResourceID != "" {
			provider = fmt.Sprintf("%s (%s)", result.Box.Provider, result.Box.ProviderResourceID)
		}
		rows = append(rows, summaryRow{Label: "Provider", Value: provider})
	}
	rows = append(rows,
		summaryRow{Label: "SSH", Value: result.Box.SSHDestination},
		summaryRow{Label: "Workspace root", Value: result.Box.WorkspaceRoot},
		summaryRow{Label: "OS", Value: formatOS(result.Capabilities)},
		summaryRow{Label: "Schooner", Value: formatHostRuntime(result.Capabilities.Host)},
		summaryRow{Label: "Git", Value: result.Capabilities.Git.Version},
		summaryRow{Label: "tmux", Value: result.Capabilities.Tmux.Version},
	)
	if len(result.Installed) > 0 {
		rows = append(rows, summaryRow{Label: "Installed", Value: strings.Join(result.Installed, ", ")})
	}
	return writeReadySummary(w, theme, result.Box.Name+" is ready", rows)
}

type summaryRow struct {
	Label, Value string
}

func writeReadySummary(w io.Writer, theme *uitheme.Theme, title string, rows []summaryRow) error {
	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}
	mark, heading := "✓", title
	if theme != nil && theme.HasColor() {
		mark = theme.Style(uitheme.Success).Bold(true).Render("✓")
		heading = theme.Style(uitheme.Text).Bold(true).Render(title)
	}
	if _, err := fmt.Fprintf(w, "%s %s\n", mark, heading); err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	width := 0
	for _, row := range rows {
		width = max(width, len(row.Label))
	}
	for _, row := range rows {
		label := fmt.Sprintf("%-*s", width, row.Label)
		value := row.Value
		if theme != nil && theme.HasColor() {
			label = theme.Style(uitheme.Muted).Render(label)
			value = theme.Style(uitheme.Text).Render(value)
		}
		if _, err := fmt.Fprintf(w, "  %s  %s\n", label, value); err != nil {
			return err
		}
	}
	return nil
}

func formatOS(capabilities box.Capabilities) string {
	name := capabilities.OSID
	if name == "ubuntu" {
		name = "Ubuntu"
	}
	return fmt.Sprintf("%s %s (%s)", name, capabilities.OSVersion, capabilities.Architecture)
}

func formatHostRuntime(runtime box.HostRuntime) string {
	if runtime.Version == "" {
		return "unknown"
	}
	return fmt.Sprintf("%s (protocol %s)", runtime.Version, runtime.ProtocolVersion)
}

func writeDestroyResult(w interface{ Write([]byte) (int, error) }, output string, result acquisition.DestroyResult) error {
	if output == "json" {
		return json.NewEncoder(w).Encode(struct {
			SchemaVersion string      `json:"schema_version"`
			Box           boxDocument `json:"box"`
			Destroyed     bool        `json:"destroyed"`
			LocalRemoved  bool        `json:"local_removed"`
		}{SchemaVersion: "1", Box: documentBox(result.Box), Destroyed: true, LocalRemoved: result.LocalRemoved})
	}
	_, err := fmt.Fprintf(w, "Destroyed box %s\nProvider: %s\nResource: %s\nLocal inventory removed: %t\n", result.Box.Name, result.Resource.Provider, result.Resource.ResourceID, result.LocalRemoved)
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
	workspaceRoot := c.WorkspaceRoot
	if !c.WorkspaceRootExists {
		workspaceRoot += " (missing)"
	}
	_, err := fmt.Fprintf(w, "%s is reachable\nSSH: %s\nObserved: %s\nUbuntu: %s (%s)\nWorkspace root: %s\nSchooner: %s\nRuntime path: %s\nCapabilities: %s\nGit: %s\ntmux: %s\n", result.Box.Name, result.Box.SSHDestination, result.Observation.ObservedAt.Format(time.RFC3339), c.OSVersion, c.Architecture, workspaceRoot, formatHostRuntime(c.Host), c.Host.Path, strings.Join(c.Host.Capabilities, ", "), c.Git.Version, c.Tmux.Version)
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
