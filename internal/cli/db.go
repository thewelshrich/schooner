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
					_, _ = fmt.Fprintln(streams.Out, "Cancelled. No changes made.")
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
			if destroyed {
				_, _ = fmt.Fprintln(streams.Out, "Destroyed the local Schooner database.")
			} else {
				_, _ = fmt.Fprintln(streams.Out, "No local Schooner database existed.")
			}
			_, err = fmt.Fprintln(streams.Out, "DigitalOcean resources, credential-store entries, migration backups, and SSH identities were not changed.")
			return err
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm permanent local database destruction")
	return cmd
}
