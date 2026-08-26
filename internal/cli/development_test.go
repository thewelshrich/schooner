package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/thewelshrich/schooner/internal/artifact"
	hostruntime "github.com/thewelshrich/schooner/internal/runtime"
	"github.com/thewelshrich/schooner/internal/runtime/host"
)

func TestDevelopmentArtifactsCommandBuildsFromSelectedSource(t *testing.T) {
	var requested artifact.DevelopmentBuildOptions
	builder := func(_ context.Context, options artifact.DevelopmentBuildOptions) (artifact.DevelopmentBuild, error) {
		requested = options
		return artifact.DevelopmentBuild{
			Directory: "/cache/dev",
			Artifacts: []artifact.Result{
				{Path: "/cache/dev/schooner_dev_linux_amd64", Platform: artifact.Platform{OS: "linux", Arch: "amd64"}, SHA256: strings.Repeat("a", 64)},
				{Path: "/cache/dev/schooner_dev_linux_arm64", Platform: artifact.Platform{OS: "linux", Arch: "arm64"}, SHA256: strings.Repeat("b", 64)},
			},
		}, nil
	}
	options := &globalOptions{output: "human"}
	command := newDevelopmentArtifactsCommand(options, builder)
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetArgs([]string{"--source", "/source"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if requested.SourceDir != "/source" {
		t.Errorf("source = %q", requested.SourceDir)
	}
	for _, expected := range []string{"Development host runtimes ready", "linux/amd64", "linux/arm64", "automatically"} {
		if !strings.Contains(output.String(), expected) {
			t.Errorf("output %q does not contain %q", output.String(), expected)
		}
	}
}

func TestDevelopmentArtifactsCommandWritesJSONAndMapsFailures(t *testing.T) {
	t.Run("json", func(t *testing.T) {
		command := newDevelopmentArtifactsCommand(&globalOptions{output: "json"}, func(context.Context, artifact.DevelopmentBuildOptions) (artifact.DevelopmentBuild, error) {
			return artifact.DevelopmentBuild{Directory: "/cache/dev"}, nil
		})
		var output bytes.Buffer
		command.SetOut(&output)
		if err := command.ExecuteContext(t.Context()); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(output.String(), `"schema_version":"1"`) || !strings.Contains(output.String(), `"directory":"/cache/dev"`) {
			t.Fatalf("output = %q", output.String())
		}
	})

	t.Run("failure", func(t *testing.T) {
		want := errors.New("build failed")
		command := newDevelopmentArtifactsCommand(&globalOptions{output: "human"}, func(context.Context, artifact.DevelopmentBuildOptions) (artifact.DevelopmentBuild, error) {
			return artifact.DevelopmentBuild{}, want
		})
		err := command.ExecuteContext(t.Context())
		var execution executionError
		if !errors.As(err, &execution) || !errors.Is(err, want) {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestDevelopmentCommandIsRegistered(t *testing.T) {
	home := t.TempDir()
	root := newRootCommand(BuildInfo{}, Streams{}, &globalOptions{hostRuntime: func() *host.Runtime {
		return host.NewAtHome(hostruntime.BuildInfo{}, home)
	}})
	command, _, err := root.Find([]string{"dev", "artifacts"})
	if err != nil || command == nil || command.CommandPath() != "schooner dev artifacts" {
		t.Fatalf("command = %v, err = %v", command, err)
	}
}

func TestDevelopmentCommandIsNotExposedByReleaseBuilds(t *testing.T) {
	home := t.TempDir()
	root := newRootCommand(BuildInfo{Version: "v1.2.3"}, Streams{}, &globalOptions{hostRuntime: func() *host.Runtime {
		return host.NewAtHome(hostruntime.BuildInfo{}, home)
	}})
	for _, command := range root.Commands() {
		if command.Name() == "dev" {
			t.Fatal("release build exposed development command")
		}
	}
}
