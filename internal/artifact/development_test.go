package artifact

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	for index, arch := range []string{"amd64", "arm64"} {
		artifact := result.Artifacts[index]
		name := "schooner_dev_linux_" + arch
		if artifact.Platform != (Platform{OS: "linux", Arch: arch}) || filepath.Base(artifact.Path) != name {
			t.Errorf("artifact[%d] = %+v", index, artifact)
		}
		contents, readErr := os.ReadFile(artifact.Path)
		if readErr != nil || string(contents) != "linux/"+arch+"\n" {
			t.Errorf("contents[%s] = %q, %v", arch, contents, readErr)
		}
		if digest, manifestErr := readManifest(filepath.Join(output, manifestName), name); manifestErr != nil || digest != artifact.SHA256 {
			t.Errorf("manifest[%s] = %q, %v", arch, digest, manifestErr)
		}
		if verifyErr := verifyFile(artifact.Path, artifact.SHA256); verifyErr != nil {
			t.Errorf("verify[%s]: %v", arch, verifyErr)
		}
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
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("output entries = %v, err = %v", entries, readErr)
	}
}
