package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/thewelshrich/schooner/internal/credentials"
	"github.com/thewelshrich/schooner/internal/provider"
	"github.com/thewelshrich/schooner/internal/ui/prompts"
)

func newProviderCommand(streams Streams, global *globalOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "provider", Short: "Manage cloud provider credential profiles", Args: cobra.NoArgs, RunE: helpRun}
	cmd.AddCommand(newProviderConnectCommand(streams, global), newProviderListCommand(streams, global), newProviderDisconnectCommand(streams, global))
	return cmd
}

func newProviderConnectCommand(streams Streams, global *globalOptions) *cobra.Command {
	var makeDefault bool
	cmd := &cobra.Command{
		Use: "connect digitalocean [profile]", Short: "Verify and connect a DigitalOcean credential profile", Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if args[0] != string(provider.DigitalOcean) {
				return usageError{cause: fmt.Errorf("unsupported provider %q", args[0])}
			}
			interactive := interactionAllowed(streams, global)
			name := ""
			if len(args) == 2 {
				name = args[1]
			}
			token := os.Getenv("DIGITALOCEAN_TOKEN")
			storeSecret := false
			if name == "" || token == "" {
				if !interactive {
					return usageError{cause: fmt.Errorf("profile name and DIGITALOCEAN_TOKEN are required when prompts are unavailable")}
				}
				if err := showInteractiveIntro(cmd.Context(), streams, global); err != nil {
					return err
				}
				var err error
				name, token, storeSecret, err = prompts.ConnectDigitalOcean(cmd.Context(), promptOptions(streams, global), name, token)
				if errors.Is(err, prompts.ErrAborted) {
					return abortError{cause: err}
				}
				if err != nil {
					return executionError{cause: err}
				}
			}
			services, closeServices, err := openApplication(cmd.Context(), streams, global.build)
			if err != nil {
				return executionError{cause: err}
			}
			defer closeServices()
			profile, err := services.credentials.Connect(cmd.Context(), name, token, storeSecret, makeDefault)
			if err != nil {
				return executionError{cause: err}
			}
			if profile.Warning != "" {
				_, _ = fmt.Fprintln(streams.Err, "Warning:", profile.Warning)
			}
			return writeCredentialProfile(streams.Out, global.output, profile)
		},
	}
	cmd.Flags().BoolVar(&makeDefault, "default", false, "make this the default DigitalOcean profile")
	return cmd
}

func newProviderListCommand(streams Streams, global *globalOptions) *cobra.Command {
	return &cobra.Command{Use: "list", Short: "List provider credential profiles", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		services, closeServices, err := openApplication(cmd.Context(), streams, global.build)
		if err != nil {
			return executionError{cause: err}
		}
		defer closeServices()
		profiles, err := services.credentials.List(cmd.Context())
		if err != nil {
			return executionError{cause: err}
		}
		if global.output == "json" {
			doc := struct {
				SchemaVersion string            `json:"schema_version"`
				Profiles      []profileDocument `json:"profiles"`
			}{SchemaVersion: "1", Profiles: make([]profileDocument, 0, len(profiles))}
			for _, profile := range profiles {
				doc.Profiles = append(doc.Profiles, documentProfile(profile))
			}
			if err = json.NewEncoder(streams.Out).Encode(doc); err != nil {
				return executionError{cause: err}
			}
			return nil
		}
		if len(profiles) == 0 {
			_, err = fmt.Fprintln(streams.Out, "No provider credential profiles connected.")
			return err
		}
		for _, profile := range profiles {
			marker := ""
			if profile.Default {
				marker = " (default)"
			}
			_, _ = fmt.Fprintf(streams.Out, "%s%s  %s  %s\n", profile.Ref, marker, profile.Status, firstNonEmpty(profile.AccountName, profile.AccountEmail, profile.ExternalID))
		}
		return nil
	}}
}

func newProviderDisconnectCommand(streams Streams, global *globalOptions) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{Use: "disconnect digitalocean/<profile>", Short: "Remove a stored provider credential", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		ref, err := credentials.ParseRef(args[0])
		if err != nil {
			return usageError{cause: err}
		}
		interactive := interactionAllowed(streams, global)
		if !yes {
			if !interactive {
				return usageError{cause: fmt.Errorf("--yes is required when prompts are unavailable")}
			}
			confirmed, confirmErr := prompts.ConfirmProviderDisconnect(cmd.Context(), promptOptions(streams, global), string(ref))
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
		services, closeServices, err := openApplication(cmd.Context(), streams, global.build)
		if err != nil {
			return executionError{cause: err}
		}
		defer closeServices()
		profile, err := services.credentials.Disconnect(cmd.Context(), ref)
		if err != nil {
			return executionError{cause: err}
		}
		return writeCredentialProfile(streams.Out, global.output, profile)
	}}
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm local credential removal")
	return cmd
}

type profileDocument struct {
	Ref          string `json:"ref"`
	Provider     string `json:"provider"`
	AccountName  string `json:"account_name,omitempty"`
	AccountEmail string `json:"account_email,omitempty"`
	Status       string `json:"status"`
	Default      bool   `json:"default"`
}

func documentProfile(profile credentials.Profile) profileDocument {
	return profileDocument{Ref: string(profile.Ref), Provider: string(profile.Provider), AccountName: profile.AccountName, AccountEmail: profile.AccountEmail, Status: string(profile.Status), Default: profile.Default}
}
func writeCredentialProfile(w interface{ Write([]byte) (int, error) }, output string, profile credentials.Profile) error {
	if output == "json" {
		return json.NewEncoder(w).Encode(struct {
			SchemaVersion string          `json:"schema_version"`
			Profile       profileDocument `json:"profile"`
		}{SchemaVersion: "1", Profile: documentProfile(profile)})
	}
	_, err := fmt.Fprintf(w, "Credential profile %s\nAccount: %s\nStatus: %s\n", profile.Ref, firstNonEmpty(profile.AccountName, profile.AccountEmail, profile.ExternalID), profile.Status)
	return err
}
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return "—"
}
