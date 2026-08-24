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
	"time"

	"github.com/thewelshrich/schooner/internal/acquisition"
	"github.com/thewelshrich/schooner/internal/box"
	"github.com/thewelshrich/schooner/internal/cli"
	"github.com/thewelshrich/schooner/internal/inventory/sqlite"
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
			name:    "invalid terminal theme",
			args:    []string{"version", "--theme", "sepia"},
			message: "unsupported theme mode",
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
	if code != 130 {
		t.Errorf("exit status = %d, want 130", code)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if stderr != "" {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestBoxLifecycleJSONThroughRun(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	sshPath := filepath.Join(bin, "ssh")
	if err := os.WriteFile(sshPath, []byte(fakeSSH), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	code, stdout, stderr := run(t.Context(), []string{"box", "add", "work", "--ssh", "work-host", "--yes", "--accept-new-host-key", "--output", "json"}, testBuild(), nil)
	if code != 0 || stderr != "" {
		t.Fatalf("add: code=%d stderr=%q", code, stderr)
	}
	add := normalizeBoxJSON(t, stdout, false)
	if want := golden(t, "box-add.json"); add != want {
		t.Fatalf("add JSON mismatch\nwant: %s\ngot:  %s", want, add)
	}

	code, stdout, stderr = run(t.Context(), []string{"box", "status", "work", "--output", "json"}, testBuild(), nil)
	if code != 0 || stderr != "" {
		t.Fatalf("status: code=%d stderr=%q", code, stderr)
	}
	status := normalizeBoxJSON(t, stdout, true)
	if want := golden(t, "box-status.json"); status != want {
		t.Fatalf("status JSON mismatch\nwant: %s\ngot:  %s", want, status)
	}

	code, stdout, stderr = run(t.Context(), []string{"box", "remove", "work", "--yes", "--output", "json"}, testBuild(), nil)
	if code != 0 || stderr != "" {
		t.Fatalf("remove: code=%d stderr=%q", code, stderr)
	}
	removed := normalizeBoxJSON(t, stdout, false)
	if want := golden(t, "box-remove.json"); removed != want {
		t.Fatalf("remove JSON mismatch\nwant: %s\ngot:  %s", want, removed)
	}
}

func TestBoxSSHOpensRecordedBoxesInCurrentTerminal(t *testing.T) {
	for _, tt := range []struct {
		name   string
		record box.Record
		want   string
	}{
		{
			name:   "adopted",
			record: box.Record{Name: "work", Acquisition: "adopted", SSHDestination: "work-alias"},
			want:   "args=work-alias\n",
		},
		{
			name: "provisioned",
			record: box.Record{
				Name:                  "cloud",
				Acquisition:           "provisioned",
				SSHDestination:        "root@203.0.113.8",
				IdentityFile:          "/state/ssh/id_ed25519",
				Provider:              "digitalocean",
				ProviderResourceID:    "42",
				ProviderCorrelationID: "operation-1",
				CredentialProfile:     "digitalocean/personal",
			},
			want: "args=-i /state/ssh/id_ed25519 -o IdentitiesOnly=yes root@203.0.113.8\n",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
			installTestSSH(t, home, "#!/bin/sh\nprintf 'args=%s\\n' \"$*\"\n")
			saveTestBox(t, tt.record)

			var stdout, stderr bytes.Buffer
			code := cli.Run(t.Context(), []string{"box", "ssh", tt.record.Name}, cli.Streams{
				In:            strings.NewReader(""),
				Out:           &stdout,
				Err:           &stderr,
				InIsTerminal:  true,
				OutIsTerminal: true,
				ErrIsTerminal: true,
			}, testBuild())
			if code != 0 || stdout.String() != tt.want || stderr.String() != "" {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			if strings.Contains(stdout.String(), "schooner\n") || strings.Contains(stdout.String(), "cd --") {
				t.Fatalf("named SSH handoff emitted UI or remote command: %q", stdout.String())
			}
		})
	}
}

func TestBoxSSHGlobalInteractionPolicy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	installTestSSH(t, home, "#!/bin/sh\nprintf 'args=%s\\n' \"$*\"\n")
	saveTestBox(t, box.Record{Name: "work", Acquisition: "adopted", SSHDestination: "work-alias"})

	code, stdout, stderr := run(t.Context(), []string{"box", "ssh", "work"}, testBuild(), nil)
	if code != 2 || stdout != "" || !strings.Contains(stderr, "requires an interactive terminal") {
		t.Fatalf("non-TTY: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}

	code, stdout, stderr = runTerminal(t.Context(), []string{"box", "ssh", "work", "--output", "json"}, "")
	if code != 2 || stdout != "" || !strings.Contains(stderr, "human output only") {
		t.Fatalf("JSON: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}

	code, stdout, stderr = runTerminal(t.Context(), []string{"box", "ssh", "--no-input"}, "")
	if code != 2 || stdout != "" || !strings.Contains(stderr, "box name is required") {
		t.Fatalf("unnamed batch: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}

	code, stdout, stderr = runTerminal(t.Context(), []string{"box", "ssh", "work", "--no-input"}, "")
	if code != 0 || stdout != "args=-o BatchMode=yes work-alias\n" || stderr != "" {
		t.Fatalf("named batch: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}

	code, stdout, stderr = runTerminal(t.Context(), []string{"box", "ssh", "missing"}, "")
	if code != 1 || stdout != "" || !strings.Contains(stderr, `box "missing" was not found`) {
		t.Fatalf("missing box: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}

	code, stdout, stderr = runTerminal(t.Context(), []string{"box", "ssh", "Not Valid"}, "")
	if code != 2 || stdout != "" || !strings.Contains(stderr, "lowercase slug") {
		t.Fatalf("invalid name: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestBoxSSHEmptyInventory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	code, stdout, stderr := runTerminal(t.Context(), []string{"box", "ssh", "--accessible"}, "")
	if code != 1 || stdout != "" || !strings.Contains(stderr, "no boxes are registered") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestBoxSSHExitAndDiagnosticMapping(t *testing.T) {
	for _, tt := range []struct {
		name       string
		script     string
		wantCode   int
		wantStderr string
	}{
		{name: "remote shell exit", script: "#!/bin/sh\nexit 42\n", wantCode: 42},
		{name: "OpenSSH failure", script: "#!/bin/sh\nprintf 'Permission denied (publickey).\\n' >&2\nexit 255\n", wantCode: 1, wantStderr: "Permission denied (publickey).\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
			installTestSSH(t, home, tt.script)
			saveTestBox(t, box.Record{Name: "work", Acquisition: "adopted", SSHDestination: "work-alias"})
			code, stdout, stderr := runTerminal(t.Context(), []string{"box", "ssh", "work"}, "")
			if code != tt.wantCode || stdout != "" || stderr != tt.wantStderr {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			if strings.Contains(stderr, "Error:") {
				t.Fatalf("native diagnostic was duplicated: %q", stderr)
			}
		})
	}
}

func TestBoxSSHPicksABoxWhenNameIsOmitted(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	installTestSSH(t, home, "#!/bin/sh\nexit 0\n")
	saveTestBox(t, box.Record{Name: "work", Acquisition: "adopted", SSHDestination: "work-alias"})

	code, stdout, stderr := runTerminal(t.Context(), []string{"box", "ssh", "--accessible"}, "1\n")
	if code != 0 || stdout != "" || !strings.Contains(stderr, "Choose a box to connect to") || !strings.Contains(stderr, "work") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestBoxListEmptyInventory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	code, stdout, stderr := run(t.Context(), []string{"box", "list"}, testBuild(), nil)
	if code != 0 || stderr != "" || stdout != "No boxes in local inventory.\n" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	code, stdout, stderr = run(t.Context(), []string{"box", "list", "--output", "json"}, testBuild(), nil)
	if code != 0 || stderr != "" || stdout != "{\"schema_version\":\"1\",\"boxes\":[]}\n" {
		t.Fatalf("json code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestBoxMutationRequiresYesWithoutPrompts(t *testing.T) {
	code, stdout, stderr := run(t.Context(), []string{"box", "add", "work", "--ssh", "work-host"}, testBuild(), nil)
	if code != 2 || stdout != "" || !strings.Contains(stderr, "--yes is required") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestInvalidBoxNameIsUsageError(t *testing.T) {
	code, _, stderr := run(t.Context(), []string{"box", "add", "Not Valid", "--ssh", "work-host", "--yes"}, testBuild(), nil)
	if code != 2 || !strings.Contains(stderr, "lowercase slug") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func TestDigitalOceanAddValidatesAutomationInputsBeforeNetwork(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("DIGITALOCEAN_TOKEN", "")
	for _, tt := range []struct {
		args    []string
		message string
	}{
		{args: []string{"box", "add", "work", "--provider", "digitalocean", "--ssh", "work-host", "--yes"}, message: "mutually exclusive"},
		{args: []string{"box", "add", "work", "--provider", "digitalocean", "--yes"}, message: "--region"},
		{args: []string{"box", "add", "work", "--provider", "digitalocean", "--region", "fra1", "--size", "small", "--image", "ubuntu-24-04-x64", "--yes"}, message: "--accept-new-host-key"},
		{args: []string{"provider", "connect", "digitalocean", "personal"}, message: "DIGITALOCEAN_TOKEN"},
	} {
		code, stdout, stderr := run(t.Context(), tt.args, testBuild(), nil)
		if code != 2 || stdout != "" || !strings.Contains(stderr, tt.message) {
			t.Fatalf("args=%v code=%d stdout=%q stderr=%q", tt.args, code, stdout, stderr)
		}
	}
}

func TestDigitalOceanAddResumesInterruptedByName(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("DIGITALOCEAN_TOKEN", "")

	path, err := sqlite.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	op := acquisition.ProvisionOperation{
		Name:          "work",
		CorrelationID: "correlation-1",
		Profile:       "digitalocean/default",
		Region:        "fra1",
		Size:          "small",
		Image:         "ubuntu-24-04-x64",
		WorkspaceRoot: box.DefaultWorkspaceRoot,
		Checkpoint:    "provider_request_pending",
		UpdatedAt:     time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC),
	}
	if _, err = store.BeginProvision(t.Context(), op); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := run(t.Context(), []string{"box", "add", "work", "--yes"}, testBuild(), nil)
	if code != 2 || stdout != "" || !strings.Contains(stderr, "--accept-new-host-key") {
		t.Fatalf("resume without accept-new: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}

	code, stdout, stderr = run(t.Context(), []string{"box", "add", "work", "--yes", "--accept-new-host-key"}, testBuild(), nil)
	if code == 2 && strings.Contains(stderr, "--region") {
		t.Fatalf("interrupted name should resume without requiring region/size/image: stderr=%q", stderr)
	}
	if code == 0 || stdout != "" {
		t.Fatalf("expected credential failure after resume, code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if strings.Contains(stderr, "--region") || strings.Contains(stderr, "--ssh") {
		t.Fatalf("interrupted name should not fall back to fresh add validation: stderr=%q", stderr)
	}
	if !strings.Contains(stderr, "digitalocean/default") && !strings.Contains(stderr, "DIGITALOCEAN_TOKEN") && !strings.Contains(stderr, "credential") {
		t.Fatalf("expected resume to load stored profile, stderr=%q", stderr)
	}
}

func TestDatabaseDestroyRemovesOnlyActiveDatabaseFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	directory := filepath.Join(home, "Library", "Application Support", "Schooner")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(directory, "state.db")
	for _, path := range []string{database, database + "-wal", database + "-shm"} {
		if err := os.WriteFile(path, []byte("local state"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	backup := database + ".pre-v2-backup"
	identity := filepath.Join(directory, "ssh", "id_ed25519")
	if err := os.MkdirAll(filepath.Dir(identity), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{backup, identity} {
		if err := os.WriteFile(path, []byte("preserve"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	code, stdout, stderr := run(t.Context(), []string{"db", "destroy", "--yes", "--output", "json"}, testBuild(), nil)
	if code != 0 || stderr != "" || stdout != "{\"schema_version\":\"1\",\"destroyed\":true}\n" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	for _, path := range []string{database, database + "-wal", database + "-shm"} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("active database file still exists: %s", path)
		}
	}
	for _, path := range []string{backup, identity} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("preserved file %s: %v", path, err)
		}
	}
}

func TestDatabaseDestroyRequiresConfirmationWithoutPrompts(t *testing.T) {
	code, stdout, stderr := run(t.Context(), []string{"db", "destroy"}, testBuild(), nil)
	if code != 2 || stdout != "" || !strings.Contains(stderr, "--yes is required") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestInteractiveAccessibleDeclineThroughRun(t *testing.T) {
	var stdout, stderr bytes.Buffer
	input := &slowReader{reader: strings.NewReader("1\nn\n")}
	code := cli.Run(t.Context(), []string{"box", "add", "work", "--ssh", "work-host", "--accessible"}, cli.Streams{In: input, Out: &stdout, Err: &stderr, InIsTerminal: true, ErrIsTerminal: true}, testBuild())
	if code != 0 || stdout.String() != "Cancelled. No changes made.\n" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "Existing SSH") || !strings.Contains(stderr.String(), "Review") {
		t.Fatalf("stderr = %q", stderr.String())
	}
	acquisitionAt := strings.Index(stderr.String(), "Acquisition")
	detailsAt := strings.Index(stderr.String(), "Box details")
	reviewAt := strings.Index(stderr.String(), "Review")
	if acquisitionAt < 0 || detailsAt <= acquisitionAt || reviewAt <= detailsAt {
		t.Fatalf("steps are not retained in sequential order: %q", stderr.String())
	}
	if strings.Contains(stderr.String(), "Choices so far") || strings.Contains(stderr.String(), "│ Acquisition") {
		t.Fatalf("cumulative choice table should not appear: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "schooner\n▁▂▄▆▆▄▂▁") {
		t.Fatalf("interactive heading missing from stderr: %q", stderr.String())
	}
	if strings.Contains(stderr.String(), "\x1b[") {
		t.Fatalf("accessible interaction contains ANSI control sequences: %q", stderr.String())
	}
}

func TestInteractiveAbortExits130(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	var stdout, stderr bytes.Buffer
	code := cli.Run(ctx, []string{"box", "add"}, cli.Streams{In: reader, Out: &stdout, Err: &stderr, InIsTerminal: true, ErrIsTerminal: true}, testBuild())
	if code != 130 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.HasPrefix(stderr.String(), "\x1b[2J\x1b[H") {
		t.Fatalf("box add did not clear the terminal before rendering: %q", stderr.String())
	}
	if strings.Count(stderr.String(), "\x1b[?25l") != strings.Count(stderr.String(), "\x1b[?25h") {
		t.Fatalf("cursor was not restored after abort: %q", stderr.String())
	}
}

func normalizeBoxJSON(t *testing.T, value string, status bool) string {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal([]byte(value), &document); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	boxValue := document["box"].(map[string]any)
	boxValue["id"] = "box-id"
	if status {
		document["status"].(map[string]any)["observed_at"] = "2026-08-24T12:00:00Z"
	}
	contents, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return string(contents) + "\n"
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

func runTerminal(ctx context.Context, args []string, input string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := cli.Run(ctx, args, cli.Streams{
		In:            strings.NewReader(input),
		Out:           &stdout,
		Err:           &stderr,
		InIsTerminal:  true,
		OutIsTerminal: true,
		ErrIsTerminal: true,
	}, testBuild())
	return code, stdout.String(), stderr.String()
}

func installTestSSH(t *testing.T, home, contents string) {
	t.Helper()
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func saveTestBox(t *testing.T, record box.Record) {
	t.Helper()
	path, err := sqlite.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	record.ID = "box-" + record.Name
	record.RemoteIdentity = "remote-" + record.Name
	record.WorkspaceRoot = "/home/test/schooner"
	record.CreatedAt = now
	record.UpdatedAt = now
	op := box.AddOperation{Name: record.Name, SSHDestination: record.SSHDestination, WorkspaceRoot: record.WorkspaceRoot, UpdatedAt: now}
	if err := store.BeginAdd(t.Context(), op); err != nil {
		t.Fatal(err)
	}
	observation := box.Observation{BoxID: record.ID, ObservedAt: now, Capabilities: box.Capabilities{OSID: "ubuntu", OSVersion: "24.04", Architecture: "amd64"}}
	if err := store.CompleteAdd(t.Context(), op, record, observation); err != nil {
		t.Fatal(err)
	}
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

const fakeSSH = `#!/bin/sh
case " $* " in
  *" -G "*) exit 0 ;;
esac
program=$(dd bs=4096 2>/dev/null)
case "$program" in
  *"candidate=\$1"*)
    printf '%s\n' '{"schema_version":"1","remote_identity":"11111111-1111-4111-8111-111111111111"}' ;;
  *"requested=\$1"*)
    printf '%s\n' '{"schema_version":"1","workspace_root":"/home/alice/schooner"}' ;;
  *)
    printf '%s\n' '{"schema_version":"1","os_id":"ubuntu","os_version":"24.04","architecture":"amd64","home":"/home/alice","remote_identity":"11111111-1111-4111-8111-111111111111","workspace_root":"/home/alice/schooner","workspace_root_exists":true,"git":{"available":true,"version":"git version 2.43.0"},"tmux":{"available":true,"version":"tmux 3.4"},"passwordless_sudo":true}' ;;
esac
`

type slowReader struct{ reader *strings.Reader }

func (r *slowReader) Read(buffer []byte) (int, error) {
	if len(buffer) > 1 {
		buffer = buffer[:1]
	}
	return r.reader.Read(buffer)
}

func (w failingWriter) Write([]byte) (int, error) {
	return 0, w.err
}
