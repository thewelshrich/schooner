package artifact

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type DevelopmentBuildOptions struct {
	SourceDir string
	OutputDir string
	GoBinary  string
}

type DevelopmentBuild struct {
	Directory string
	Artifacts []Result
}

func BuildDevelopment(ctx context.Context, options DevelopmentBuildOptions) (DevelopmentBuild, error) {
	sourceDir := defaultString(options.SourceDir, ".")
	goBinary := defaultString(options.GoBinary, "go")
	outputDir := options.OutputDir
	if outputDir == "" {
		var err error
		outputDir, err = effectiveDevelopmentDirectory()
		if err != nil {
			return DevelopmentBuild{}, err
		}
	}

	var err error
	if sourceDir, err = filepath.Abs(sourceDir); err != nil {
		return DevelopmentBuild{}, fmt.Errorf("resolve development source directory: %w", err)
	}
	if outputDir, err = filepath.Abs(outputDir); err != nil {
		return DevelopmentBuild{}, fmt.Errorf("resolve development artifact directory: %w", err)
	}
	if err = os.MkdirAll(outputDir, 0o700); err != nil {
		return DevelopmentBuild{}, fmt.Errorf("create development artifact directory: %w", err)
	}

	generationDir, err := os.MkdirTemp(outputDir, developmentGenerationPrefix)
	if err != nil {
		return DevelopmentBuild{}, fmt.Errorf("create development build directory: %w", err)
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(generationDir)
		}
	}()

	build := DevelopmentBuild{Directory: outputDir}
	var manifest strings.Builder
	for _, arch := range []string{"amd64", "arm64"} {
		platform := Platform{OS: "linux", Arch: arch}
		name := fileName("dev", platform)
		path := filepath.Join(generationDir, name)
		command := exec.CommandContext(ctx, goBinary, "build", "-trimpath", "-o", path, "./cmd/schooner")
		command.Dir = sourceDir
		command.Env = developmentEnvironment(os.Environ(), arch)
		output, buildErr := command.CombinedOutput()
		if buildErr != nil {
			if ctx.Err() != nil {
				return DevelopmentBuild{}, ctx.Err()
			}
			return DevelopmentBuild{}, fmt.Errorf("build linux/%s host runtime: %w: %s", arch, buildErr, strings.TrimSpace(string(output)))
		}
		contents, readErr := os.ReadFile(path)
		if readErr != nil {
			return DevelopmentBuild{}, fmt.Errorf("read linux/%s host runtime: %w", arch, readErr)
		}
		if len(contents) == 0 {
			return DevelopmentBuild{}, fmt.Errorf("build linux/%s host runtime: output is empty", arch)
		}
		digest := fmt.Sprintf("%x", sha256.Sum256(contents))
		fmt.Fprintf(&manifest, "%s  %s\n", digest, name)
		build.Artifacts = append(build.Artifacts, Result{Path: path, Version: "dev", Platform: platform, SHA256: digest})
	}
	if err = os.WriteFile(filepath.Join(generationDir, manifestName), []byte(manifest.String()), 0o600); err != nil {
		return DevelopmentBuild{}, fmt.Errorf("write development checksum manifest: %w", err)
	}
	for _, result := range build.Artifacts {
		if _, err = readManifest(filepath.Join(generationDir, manifestName), filepath.Base(result.Path)); err != nil {
			return DevelopmentBuild{}, err
		}
		if err = verifyFile(result.Path, result.SHA256); err != nil {
			return DevelopmentBuild{}, err
		}
	}
	if err = publishDevelopmentGeneration(outputDir, generationDir); err != nil {
		return DevelopmentBuild{}, err
	}
	published = true
	return build, nil
}

func publishDevelopmentGeneration(outputDir, generationDir string) error {
	pointer, err := os.CreateTemp(outputDir, ".current-")
	if err != nil {
		return fmt.Errorf("create development artifact pointer: %w", err)
	}
	pointerPath := pointer.Name()
	defer os.Remove(pointerPath)

	if _, err = fmt.Fprintln(pointer, filepath.Base(generationDir)); err != nil {
		_ = pointer.Close()
		return fmt.Errorf("write development artifact pointer: %w", err)
	}
	if err = pointer.Sync(); err != nil {
		_ = pointer.Close()
		return fmt.Errorf("sync development artifact pointer: %w", err)
	}
	if err = pointer.Close(); err != nil {
		return fmt.Errorf("close development artifact pointer: %w", err)
	}
	if err = os.Rename(pointerPath, filepath.Join(outputDir, developmentCurrentName)); err != nil {
		return fmt.Errorf("publish development artifacts: %w", err)
	}
	return nil
}

func developmentEnvironment(environment []string, arch string) []string {
	values := map[string]string{"CGO_ENABLED": "0", "GOOS": "linux", "GOARCH": arch}
	result := make([]string, 0, len(environment)+len(values))
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		if _, replaced := values[key]; !replaced {
			result = append(result, entry)
		}
	}
	for _, key := range []string{"CGO_ENABLED", "GOOS", "GOARCH"} {
		result = append(result, key+"="+values[key])
	}
	return result
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
