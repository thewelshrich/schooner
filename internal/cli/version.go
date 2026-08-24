package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"
)

const versionSchema = "1"

type versionDocument struct {
	SchemaVersion string  `json:"schema_version"`
	Version       string  `json:"version"`
	Commit        string  `json:"commit"`
	BuiltAt       *string `json:"built_at"`
	GoVersion     string  `json:"go_version"`
	OS            string  `json:"os"`
	Arch          string  `json:"arch"`
}

func newVersionCommand(build BuildInfo) *cobra.Command {
	var output string

	cmd := &cobra.Command{
		Use:          "version",
		Short:        "Show Schooner build information",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			document, err := makeVersionDocument(build)
			if err != nil {
				return executionError{cause: err}
			}

			switch output {
			case "human":
				if err := writeHumanVersion(cmd.OutOrStdout(), document); err != nil {
					return executionError{cause: err}
				}
			case "json":
				if err := writeJSONVersion(cmd.OutOrStdout(), document); err != nil {
					return executionError{cause: err}
				}
			default:
				return usageError{cause: fmt.Errorf("unsupported output format %q (expected human or json)", output)}
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&output, "output", "human", "output format: human or json")
	cmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return usageError{cause: err}
	})

	return cmd
}

func makeVersionDocument(build BuildInfo) (versionDocument, error) {
	document := versionDocument{
		SchemaVersion: versionSchema,
		Version:       defaultString(build.Version, "dev"),
		Commit:        defaultString(build.Commit, "unknown"),
		GoVersion:     defaultString(build.GoVersion, "unknown"),
		OS:            defaultString(build.OS, "unknown"),
		Arch:          defaultString(build.Arch, "unknown"),
	}

	if build.BuiltAt != "" {
		builtAt, err := time.Parse(time.RFC3339, build.BuiltAt)
		if err != nil {
			return versionDocument{}, fmt.Errorf("invalid build time %q: %w", build.BuiltAt, err)
		}

		value := builtAt.Format(time.RFC3339)
		document.BuiltAt = &value
	}

	return document, nil
}

func writeHumanVersion(w io.Writer, document versionDocument) error {
	builtAt := "unknown"
	if document.BuiltAt != nil {
		builtAt = *document.BuiltAt
	}

	_, err := fmt.Fprintf(
		w,
		"Schooner %s\nCommit: %s\nBuilt: %s\nGo: %s\nPlatform: %s/%s\n",
		document.Version,
		document.Commit,
		builtAt,
		document.GoVersion,
		document.OS,
		document.Arch,
	)

	return err
}

func writeJSONVersion(w io.Writer, document versionDocument) error {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)

	return encoder.Encode(document)
}
