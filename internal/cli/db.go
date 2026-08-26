package cli

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/spf13/cobra"
	invsqlite "github.com/thewelshrich/schooner/internal/inventory/sqlite"
	"github.com/thewelshrich/schooner/internal/ui/prompts"
)

func newDatabaseCommand(streams Streams, global *globalOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "db", Short: "Manage local Schooner database state", Args: cobra.NoArgs, RunE: helpRun}
	cmd.AddCommand(newDatabaseDestroyCommand(streams, global))
	return cmd
}

func newDatabaseDestroyCommand(streams Streams, global *globalOptions) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "destroy",
		Short: "Destroy the local Schooner database",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			interactive := interactionAllowed(streams, global)
			if !yes {
				if !interactive {
					return usageError{cause: fmt.Errorf("--yes is required when prompts are unavailable")}
				}
				confirmed, err := prompts.ConfirmDatabaseDestroy(cmd.Context(), promptOptions(streams, global))
				if errors.Is(err, prompts.ErrAborted) {
					return abortError{cause: err}
				}
				if err != nil {
					return executionError{cause: err}
				}
				if !confirmed {
					writeCancelled(streams.Out)
					return nil
				}
			}

			path, err := invsqlite.DefaultPath()
			if err != nil {
				return executionError{cause: err}
			}
			destroyed, err := invsqlite.Destroy(path)
			if err != nil {
				return executionError{cause: err}
			}
			if global.output == "json" {
				return json.NewEncoder(streams.Out).Encode(struct {
					SchemaVersion string `json:"schema_version"`
					Destroyed     bool   `json:"destroyed"`
				}{SchemaVersion: "1", Destroyed: destroyed})
			}
			theme := outputTheme(global, streams)
			title := "No local Schooner database existed"
			if destroyed {
				title = "Destroyed the local Schooner database"
			}
			return writeReadySummary(streams.Out, theme, title, []summaryRow{
				{Label: "DigitalOcean resources", Value: "unchanged"},
				{Label: "Credential-store entries", Value: "unchanged"},
				{Label: "Migration backups", Value: "unchanged"},
				{Label: "SSH identities", Value: "unchanged"},
			})
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm permanent local database destruction")
	return cmd
}
