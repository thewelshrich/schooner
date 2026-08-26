package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"
	"github.com/thewelshrich/schooner/internal/artifact"
)

const developmentArtifactsSchema = "1"

type developmentArtifactBuilder func(context.Context, artifact.DevelopmentBuildOptions) (artifact.DevelopmentBuild, error)

type developmentArtifactsDocument struct {
	SchemaVersion string                        `json:"schema_version"`
	Directory     string                        `json:"directory"`
	Artifacts     []developmentArtifactDocument `json:"artifacts"`
}

type developmentArtifactDocument struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

func newDevelopmentCommand(global *globalOptions, build developmentArtifactBuilder) *cobra.Command {
	command := &cobra.Command{Use: "dev", Short: "Prepare local Schooner development dependencies", Args: cobra.NoArgs, RunE: helpRun}
	command.AddCommand(newDevelopmentArtifactsCommand(global, build))
	return command
}

func newDevelopmentArtifactsCommand(global *globalOptions, build developmentArtifactBuilder) *cobra.Command {
	var source string
	command := &cobra.Command{
		Use:          "artifacts",
		Short:        "Build verified Linux host runtimes for development",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := build(cmd.Context(), artifact.DevelopmentBuildOptions{SourceDir: source})
			if err != nil {
				return executionError{cause: err}
			}
			document := makeDevelopmentArtifactsDocument(result)
			switch global.output {
			case "human":
				err = writeHumanDevelopmentArtifacts(cmd.OutOrStdout(), document)
			case "json":
				err = json.NewEncoder(cmd.OutOrStdout()).Encode(document)
			default:
				return usageError{cause: fmt.Errorf("unsupported output format %q (expected human or json)", global.output)}
			}
			if err != nil {
				return executionError{cause: err}
			}
			return nil
		},
	}
	command.Flags().StringVar(&source, "source", ".", "Schooner source directory")
	return command
}

func makeDevelopmentArtifactsDocument(result artifact.DevelopmentBuild) developmentArtifactsDocument {
	document := developmentArtifactsDocument{SchemaVersion: developmentArtifactsSchema, Directory: result.Directory}
	for _, item := range result.Artifacts {
		document.Artifacts = append(document.Artifacts, developmentArtifactDocument{
			OS: item.Platform.OS, Arch: item.Platform.Arch, Path: item.Path, SHA256: item.SHA256,
		})
	}
	return document
}

func writeHumanDevelopmentArtifacts(w io.Writer, document developmentArtifactsDocument) error {
	if _, err := fmt.Fprintf(w, "Development host runtimes ready\nDirectory: %s\n", document.Directory); err != nil {
		return err
	}
	for _, artifact := range document.Artifacts {
		if _, err := fmt.Fprintf(w, "- %s/%s: %s\n", artifact.OS, artifact.Arch, artifact.Path); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(w, "Development builds use this directory automatically.")
	return err
}
