package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thewelshrich/schooner/internal/cli"
)

func TestHelp(t *testing.T) {
	t.Parallel()

	want := golden(t, "help.txt")

	for _, tt := range []struct {
		name string
		args []string
	}{
		{name: "no arguments"},
		{name: "help flag", args: []string{"--help"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			code, stdout, stderr := run(t.Context(), tt.args, testBuild(), nil)
			if code != 0 {
				t.Fatalf("exit status = %d, want 0", code)
			}
			if stdout != want {
				t.Errorf("stdout mismatch\n--- want ---\n%s\n--- got ---\n%s", want, stdout)
			}
			if stderr != "" {
				t.Errorf("stderr = %q, want empty", stderr)
			}
		})
	}
}

func TestShortVersion(t *testing.T) {
	t.Parallel()

	code, stdout, stderr := run(t.Context(), []string{"--version"}, testBuild(), nil)

	if code != 0 {
		t.Fatalf("exit status = %d, want 0", code)
	}
	if stdout != "schooner version v0.1.0-test\n" {
		t.Errorf("stdout = %q", stdout)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
}

func TestVersionHuman(t *testing.T) {
	t.Parallel()

	code, stdout, stderr := run(t.Context(), []string{"version"}, testBuild(), nil)

	if code != 0 {
		t.Fatalf("exit status = %d, want 0", code)
	}

	want := "Schooner v0.1.0-test\n" +
		"Commit: abc1234\n" +
		"Built: 2026-08-24T12:34:56Z\n" +
		"Go: go1.27.0\n" +
		"Platform: linux/arm64\n"
	if stdout != want {
		t.Errorf("stdout mismatch\n--- want ---\n%s\n--- got ---\n%s", want, stdout)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
}

func TestVersionJSON(t *testing.T) {
	t.Parallel()

	code, stdout, stderr := run(t.Context(), []string{"version", "--output", "json"}, testBuild(), nil)

	if code != 0 {
		t.Fatalf("exit status = %d, want 0", code)
	}
	if want := golden(t, "version.json"); stdout != want {
		t.Errorf("stdout mismatch\n--- want ---\n%s\n--- got ---\n%s", want, stdout)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}

	var document struct {
		SchemaVersion string  `json:"schema_version"`
		Version       string  `json:"version"`
		Commit        string  `json:"commit"`
		BuiltAt       *string `json:"built_at"`
		GoVersion     string  `json:"go_version"`
		OS            string  `json:"os"`
		Arch          string  `json:"arch"`
	}
	if err := json.Unmarshal([]byte(stdout), &document); err != nil {
		t.Fatalf("decode JSON output: %v", err)
	}
	if document.SchemaVersion != "1" {
		t.Errorf("schema version = %q, want 1", document.SchemaVersion)
	}
	if document.BuiltAt == nil || *document.BuiltAt != "2026-08-24T12:34:56Z" {
		t.Errorf("built_at = %v", document.BuiltAt)
	}
}

func TestVersionJSONWithoutBuildTime(t *testing.T) {
	t.Parallel()

	build := testBuild()
	build.BuiltAt = ""
	code, stdout, stderr := run(t.Context(), []string{"version", "--output=json"}, build, nil)

	if code != 0 {
		t.Fatalf("exit status = %d, want 0; stderr = %q", code, stderr)
	}

	var document struct {
		BuiltAt *string `json:"built_at"`
	}
	if err := json.Unmarshal([]byte(stdout), &document); err != nil {
		t.Fatalf("decode JSON output: %v", err)
	}
	if document.BuiltAt != nil {
		t.Errorf("built_at = %q, want null", *document.BuiltAt)
	}
}

func TestUsageErrors(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name    string
		args    []string
		message string
	}{
		{
			name:    "unknown command",
			args:    []string{"bogus"},
			message: "unknown command",
		},
		{
			name:    "unknown flag",
			args:    []string{"--bogus"},
			message: "unknown flag",
		},
		{
			name:    "invalid output format",
			args:    []string{"version", "--output", "yaml"},
			message: "unsupported output format",
		},
		{
			name:    "unexpected argument",
			args:    []string{"version", "extra"},
			message: "unknown command",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			code, stdout, stderr := run(t.Context(), tt.args, testBuild(), nil)
			if code != 2 {
				t.Errorf("exit status = %d, want 2", code)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want empty", stdout)
			}
			if !strings.Contains(stderr, tt.message) {
				t.Errorf("stderr = %q, want it to contain %q", stderr, tt.message)
			}
			if strings.Contains(stderr, "Usage:") {
				t.Errorf("stderr contains an unsolicited usage block: %q", stderr)
			}
			if strings.Count(stderr, "Error:") != 1 {
				t.Errorf("stderr contains duplicated errors: %q", stderr)
			}
		})
	}
}

func TestExecutionErrors(t *testing.T) {
	t.Parallel()

	t.Run("invalid linker build time", func(t *testing.T) {
		t.Parallel()

		build := testBuild()
		build.BuiltAt = "not-a-time"
		code, stdout, stderr := run(t.Context(), []string{"version"}, build, nil)

		if code != 1 {
			t.Errorf("exit status = %d, want 1", code)
		}
		if stdout != "" {
			t.Errorf("stdout = %q, want empty", stdout)
		}
		if !strings.Contains(stderr, "invalid build time") {
			t.Errorf("stderr = %q, want invalid build time error", stderr)
		}
	})

	t.Run("output write failure", func(t *testing.T) {
		t.Parallel()

		wantErr := errors.New("write failed")
		var stderr bytes.Buffer
		code := cli.Run(t.Context(), []string{"version", "--output", "json"}, cli.Streams{
			In:  strings.NewReader(""),
			Out: failingWriter{err: wantErr},
			Err: &stderr,
		}, testBuild())

		if code != 1 {
			t.Errorf("exit status = %d, want 1", code)
		}
		if !strings.Contains(stderr.String(), wantErr.Error()) {
			t.Errorf("stderr = %q, want write error", stderr.String())
		}
	})
}

func TestCanceledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	code, stdout, stderr := run(ctx, []string{"version"}, testBuild(), nil)
	if code != 1 {
		t.Errorf("exit status = %d, want 1", code)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if stderr != "Error: context canceled\n" {
		t.Errorf("stderr = %q", stderr)
	}
}

func run(ctx context.Context, args []string, build cli.BuildInfo, out io.Writer) (int, string, string) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if out == nil {
		out = &stdout
	}

	code := cli.Run(ctx, args, cli.Streams{
		In:  strings.NewReader(""),
		Out: out,
		Err: &stderr,
	}, build)

	return code, stdout.String(), stderr.String()
}

func testBuild() cli.BuildInfo {
	return cli.BuildInfo{
		Version:   "v0.1.0-test",
		Commit:    "abc1234",
		BuiltAt:   "2026-08-24T12:34:56Z",
		GoVersion: "go1.27.0",
		OS:        "linux",
		Arch:      "arm64",
	}
}

func golden(t *testing.T, name string) string {
	t.Helper()

	contents, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read golden file %s: %v", name, err)
	}

	return string(contents)
}

type failingWriter struct {
	err error
}

func (w failingWriter) Write([]byte) (int, error) {
	return 0, w.err
}
