package artifact

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBuildDevelopmentBuildsAndVerifiesBothLinuxArchitectures(t *testing.T) {
	tools := t.TempDir()
	goBinary := filepath.Join(tools, "fake-go")
	if err := os.WriteFile(goBinary, []byte(`#!/bin/sh
output=
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-o" ]; then output="$2"; shift 2; else shift; fi
done
printf '%s/%s\n' "$GOOS" "$GOARCH" > "$output"
chmod 0755 "$output"
`), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CGO_ENABLED", "1")
	t.Setenv("GOOS", "darwin")
	t.Setenv("GOARCH", "386")
	output := t.TempDir()
	result, err := BuildDevelopment(t.Context(), DevelopmentBuildOptions{SourceDir: t.TempDir(), OutputDir: output, GoBinary: goBinary})
	if err != nil {
		t.Fatal(err)
	}
	if result.Directory != output || len(result.Artifacts) != 2 {
		t.Fatalf("result = %+v", result)
	}
	generation := filepath.Dir(result.Artifacts[0].Path)
	pointer, err := os.ReadFile(filepath.Join(output, developmentCurrentName))
	if err != nil || strings.TrimSpace(string(pointer)) != filepath.Base(generation) {
		t.Fatalf("current generation = %q, err = %v", pointer, err)
	}
	for index, arch := range []string{"amd64", "arm64"} {
		artifact := result.Artifacts[index]
		name := "schooner_dev_linux_" + arch
		if artifact.Platform != (Platform{OS: "linux", Arch: arch}) || filepath.Dir(artifact.Path) != generation || filepath.Base(artifact.Path) != name {
			t.Errorf("artifact[%d] = %+v", index, artifact)
		}
		contents, readErr := os.ReadFile(artifact.Path)
		if readErr != nil || string(contents) != "linux/"+arch+"\n" {
			t.Errorf("contents[%s] = %q, %v", arch, contents, readErr)
		}
		if digest, manifestErr := readManifest(filepath.Join(generation, manifestName), name); manifestErr != nil || digest != artifact.SHA256 {
			t.Errorf("manifest[%s] = %q, %v", arch, digest, manifestErr)
		}
		if verifyErr := verifyFile(artifact.Path, artifact.SHA256); verifyErr != nil {
			t.Errorf("verify[%s]: %v", arch, verifyErr)
		}
	}
	resolver, err := New(Config{CacheDir: t.TempDir(), DevelopmentDir: output})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := resolver.Resolve(t.Context(), "dev", Platform{OS: "linux", Arch: "amd64"})
	if err != nil || resolved.Path != result.Artifacts[0].Path {
		t.Fatalf("resolved = %+v, err = %v", resolved, err)
	}
}

func TestBuildDevelopmentReportsBuildFailureAndCleansTemporaryFiles(t *testing.T) {
	tools := t.TempDir()
	goBinary := filepath.Join(tools, "fake-go")
	if err := os.WriteFile(goBinary, []byte("#!/bin/sh\necho compiler exploded >&2\nexit 9\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	output := t.TempDir()
	_, err := BuildDevelopment(t.Context(), DevelopmentBuildOptions{SourceDir: t.TempDir(), OutputDir: output, GoBinary: goBinary})
	if err == nil || !strings.Contains(err.Error(), "build linux/amd64 host runtime") || !strings.Contains(err.Error(), "compiler exploded") {
		t.Fatalf("error = %v", err)
	}
	entries, readErr := os.ReadDir(output)
	if readErr != nil || len(entries) != 1 || entries[0].Name() != developmentLockName {
		t.Fatalf("output entries = %v, err = %v", entries, readErr)
	}
}

func TestBuildDevelopmentUsesActiveArtifactOverride(t *testing.T) {
	tools := t.TempDir()
	goBinary := filepath.Join(tools, "fake-go")
	if err := os.WriteFile(goBinary, []byte(`#!/bin/sh
output=
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-o" ]; then output="$2"; shift 2; else shift; fi
done
printf '%s/%s\n' "$GOOS" "$GOARCH" > "$output"
chmod 0755 "$output"
`), 0o755); err != nil {
		t.Fatal(err)
	}
	override := t.TempDir()
	t.Setenv("SCHOONER_ARTIFACT_DIR", override)
	result, err := BuildDevelopment(t.Context(), DevelopmentBuildOptions{SourceDir: t.TempDir(), GoBinary: goBinary})
	if err != nil {
		t.Fatal(err)
	}
	if result.Directory != override {
		t.Fatalf("directory = %q, want %q", result.Directory, override)
	}
	for _, artifact := range result.Artifacts {
		if !strings.HasPrefix(artifact.Path, override+string(os.PathSeparator)) {
			t.Errorf("artifact path = %q, want it beneath override", artifact.Path)
		}
	}
}

func TestBuildDevelopmentFailedRebuildPreservesPublishedGeneration(t *testing.T) {
	tools := t.TempDir()
	goBinary := filepath.Join(tools, "fake-go")
	if err := os.WriteFile(goBinary, []byte(`#!/bin/sh
output=
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-o" ]; then output="$2"; shift 2; else shift; fi
done
printf 'first/%s\n' "$GOARCH" > "$output"
chmod 0755 "$output"
`), 0o755); err != nil {
		t.Fatal(err)
	}
	output := t.TempDir()
	first, err := BuildDevelopment(t.Context(), DevelopmentBuildOptions{SourceDir: t.TempDir(), OutputDir: output, GoBinary: goBinary})
	if err != nil {
		t.Fatal(err)
	}
	pointerBefore, err := os.ReadFile(filepath.Join(output, developmentCurrentName))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(goBinary, []byte("#!/bin/sh\necho compiler exploded >&2\nexit 9\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err = BuildDevelopment(t.Context(), DevelopmentBuildOptions{SourceDir: t.TempDir(), OutputDir: output, GoBinary: goBinary}); err == nil {
		t.Fatal("failed rebuild succeeded")
	}
	pointerAfter, err := os.ReadFile(filepath.Join(output, developmentCurrentName))
	if err != nil || string(pointerAfter) != string(pointerBefore) {
		t.Fatalf("current generation changed from %q to %q, err = %v", pointerBefore, pointerAfter, err)
	}
	resolver, err := New(Config{CacheDir: t.TempDir(), DevelopmentDir: output})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := resolver.Resolve(t.Context(), "dev", Platform{OS: "linux", Arch: "amd64"})
	if err != nil || resolved.Path != first.Artifacts[0].Path {
		t.Fatalf("resolved = %+v, err = %v", resolved, err)
	}
}

func TestBuildDevelopmentReclaimsSupersededGenerationsAfterReadersRelease(t *testing.T) {
	tools := t.TempDir()
	goBinary := filepath.Join(tools, "fake-go")
	if err := os.WriteFile(goBinary, []byte(`#!/bin/sh
output=
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-o" ]; then output="$2"; shift 2; else shift; fi
done
printf '%s/%s\n' "$GOOS" "$GOARCH" > "$output"
chmod 0755 "$output"
`), 0o755); err != nil {
		t.Fatal(err)
	}
	output := t.TempDir()
	first, err := BuildDevelopment(t.Context(), DevelopmentBuildOptions{SourceDir: t.TempDir(), OutputDir: output, GoBinary: goBinary})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := New(Config{CacheDir: t.TempDir(), DevelopmentDir: output})
	if err != nil {
		t.Fatal(err)
	}
	leased, err := resolver.Resolve(t.Context(), "dev", Platform{OS: "linux", Arch: "amd64"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildDevelopment(t.Context(), DevelopmentBuildOptions{SourceDir: t.TempDir(), OutputDir: output, GoBinary: goBinary})
	if err != nil {
		t.Fatal(err)
	}
	firstGeneration := filepath.Dir(first.Artifacts[0].Path)
	if _, err = os.Stat(firstGeneration); err != nil {
		t.Fatalf("leased generation was removed: %v", err)
	}
	if err = leased.Release(); err != nil {
		t.Fatal(err)
	}
	third, err := BuildDevelopment(t.Context(), DevelopmentBuildOptions{SourceDir: t.TempDir(), OutputDir: output, GoBinary: goBinary})
	if err != nil {
		t.Fatal(err)
	}
	for _, superseded := range []string{firstGeneration, filepath.Dir(second.Artifacts[0].Path)} {
		if _, statErr := os.Stat(superseded); !errors.Is(statErr, os.ErrNotExist) {
			t.Errorf("superseded generation %s still exists: %v", superseded, statErr)
		}
	}
	if _, err = os.Stat(filepath.Dir(third.Artifacts[0].Path)); err != nil {
		t.Fatalf("active generation is unavailable: %v", err)
	}
}

func TestBuildDevelopmentStopsWaitingForBuildLockWhenContextEnds(t *testing.T) {
	output := t.TempDir()
	lock, err := acquireDevelopmentBuildLock(t.Context(), output)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	_, err = BuildDevelopment(ctx, DevelopmentBuildOptions{SourceDir: t.TempDir(), OutputDir: output})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context deadline exceeded", err)
	}
}
