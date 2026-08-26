package cli_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thewelshrich/schooner/internal/acquisition"
	"github.com/thewelshrich/schooner/internal/box"
	"github.com/thewelshrich/schooner/internal/cli"
	"github.com/thewelshrich/schooner/internal/config"
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

func TestWorkCommandHelpExplainsLifecycleSemantics(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		command string
		want    []string
	}{
		{command: "start", want: []string{"Open persistent work for a Repository", "Starting is idempotent", "Schooner reuses it"}},
		{command: "resume", want: []string{"Return to an existing live Session", "resumes only a Session matching", "outside a local Repository", "never creates a Session"}},
		{command: "clone", want: []string{"Clone a Repository onto a Box", "normal primary Git Worktree"}},
	} {
		t.Run(tt.command, func(t *testing.T) {
			t.Parallel()
			code, stdout, stderr := run(t.Context(), []string{tt.command, "--help"}, testBuild(), nil)
			if code != 0 || stderr != "" {
				t.Fatalf("code=%d stderr=%q", code, stderr)
			}
			for _, want := range tt.want {
				if !strings.Contains(stdout, want) {
					t.Fatalf("help missing %q:\n%s", want, stdout)
				}
			}
		})
	}
}

func TestBoxHelpListsSelectionAndMaintenanceCommands(t *testing.T) {
	code, stdout, stderr := run(t.Context(), []string{"box", "--help"}, testBuild(), nil)
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	for _, command := range []string{"setup", "update", "use"} {
		if !strings.Contains(stdout, command) {
			t.Fatalf("box help missing %q: %s", command, stdout)
		}
	}
}

func TestWorktreeHelpListsDiscoveryAndLifecycleCommands(t *testing.T) {
	code, stdout, stderr := run(t.Context(), []string{"worktree", "--help"}, testBuild(), nil)
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	for _, command := range []string{"list", "inspect", "add", "remove", "prune"} {
		if !strings.Contains(stdout, command) {
			t.Fatalf("worktree help missing %q: %s", command, stdout)
		}
	}
}

func TestWorktreeListRunsDirectlyOnIdentifiedBox(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "worktrees")
	repositoryPath := filepath.Join(root, "owner", "repo")
	if output, err := exec.Command("git", "init", repositoryPath).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, output)
	}
	identityPath := filepath.Join(home, ".local", "state", "schooner", "identity")
	if err := os.MkdirAll(filepath.Dir(identityPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(identityPath, []byte("11111111-1111-4111-8111-111111111111\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("SCHOONER_CONFIG", "")
	configPath, err := config.Path()
	if err != nil {
		t.Fatal(err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = config.Write(configPath, config.Host{WorktreeRoot: canonicalRoot}); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := run(t.Context(), []string{"worktree", "list", "--output", "json", "--no-input"}, testBuild(), nil)
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q stdout=%q", code, stderr, stdout)
	}
	if !strings.Contains(stdout, `"schema_version":"1"`) || !strings.Contains(stdout, `"relative_path":"owner/repo"`) || !strings.Contains(stdout, `"kind":"primary"`) {
		t.Fatalf("stdout = %s", stdout)
	}
	inventoryPath, err := sqlite.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(inventoryPath); !os.IsNotExist(err) {
		t.Fatalf("direct observation created local inventory at %s: %v", inventoryPath, err)
	}
	otherRoot := filepath.Join(home, "other-worktrees")
	if output, initErr := exec.Command("git", "init", filepath.Join(otherRoot, "other")).CombinedOutput(); initErr != nil {
		t.Fatalf("git init other: %v\n%s", initErr, output)
	}
	if err = os.Rename(canonicalRoot, canonicalRoot+"-original"); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(otherRoot, canonicalRoot); err != nil {
		t.Fatal(err)
	}
	code, _, stderr = run(t.Context(), []string{"worktree", "list", "--no-input"}, testBuild(), nil)
	if code != 1 || !strings.Contains(stderr, "worktree root differs from host configuration") {
		t.Fatalf("drift code=%d stderr=%q", code, stderr)
	}
}

func TestGitLifecycleCommandsRunDirectlyOnIdentifiedBox(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "worktrees")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	identityPath := filepath.Join(home, ".local", "state", "schooner", "identity")
	if err := os.MkdirAll(filepath.Dir(identityPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(identityPath, []byte("11111111-1111-4111-8111-111111111111\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("SCHOONER_CONFIG", "")
	configPath, err := config.Path()
	if err != nil {
		t.Fatal(err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	root = canonicalRoot
	if err = config.Write(configPath, config.Host{WorktreeRoot: root}); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(home, "source.git")
	seed := filepath.Join(home, "seed")
	runGitTestCommand(t, home, "init", "--bare", source)
	runGitTestCommand(t, home, "clone", source, seed)
	runGitTestCommand(t, seed, "config", "user.name", "Schooner Test")
	runGitTestCommand(t, seed, "config", "user.email", "test@example.com")
	if err = os.WriteFile(filepath.Join(seed, "README.md"), []byte("test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTestCommand(t, seed, "add", "README.md")
	runGitTestCommand(t, seed, "commit", "-m", "initial")
	runGitTestCommand(t, seed, "branch", "-M", "main")
	runGitTestCommand(t, seed, "push", "origin", "main")
	runGitTestCommand(t, source, "symbolic-ref", "HEAD", "refs/heads/main")
	bin := filepath.Join(home, "bin")
	if err = os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	tmuxMetadata := filepath.Join(home, "tmux-metadata")
	tmuxPanes := filepath.Join(home, "tmux-panes")
	tmuxScript := "#!/bin/sh\ncase \"$3\" in\n  list-sessions) metadata=" + fmt.Sprintf("%q", tmuxMetadata) + " ;;\n  list-panes) metadata=" + fmt.Sprintf("%q", tmuxPanes) + " ;;\n  *) exit 2 ;;\nesac\nif [ -f \"$metadata\" ]; then cat \"$metadata\"; exit 0; fi\nprintf '%s\\n' 'no server running' >&2\nexit 1\n"
	if err = os.WriteFile(filepath.Join(bin, "tmux"), []byte(tmuxScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	code, stdout, stderr := run(t.Context(), []string{"clone", source, "--output", "json", "--no-input"}, testBuild(), nil)
	if code != 0 || stderr != "" || !strings.Contains(stdout, `"action":"clone"`) || !strings.Contains(stdout, `"relative_path":"source"`) {
		t.Fatalf("clone code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	cloned := filepath.Join(root, "source")
	runGitTestCommand(t, cloned, "branch", "feature")
	code, stdout, stderr = run(t.Context(), []string{"worktree", "add", "source", "feature", "--branch", "feature", "--output", "json", "--no-input"}, testBuild(), nil)
	if code != 0 || stderr != "" || !strings.Contains(stdout, `"action":"worktree_add"`) || !strings.Contains(stdout, `"relative_path":"feature"`) {
		t.Fatalf("add code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	featurePath := filepath.Join(root, "feature")
	if err = os.WriteFile(tmuxMetadata, frameTmuxSessionFields([]string{"$1", "schooner-test", "1720000000", "1720000000", "0", "2", "11111111-1111-4111-8111-111111111111", "shell", "2024-07-03T09:46:40Z", featurePath}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(tmuxPanes, []byte(fmt.Sprintf("2:$1;%d:%s\n", len(featurePath), featurePath)), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr = run(t.Context(), []string{"worktree", "remove", "feature", "--output", "json", "--no-input"}, testBuild(), nil)
	if code != 1 || !strings.Contains(stderr, `"code":"conflict"`) {
		t.Fatalf("protected remove code=%d stderr=%q", code, stderr)
	}
	if err = os.Remove(tmuxMetadata); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(tmuxPanes); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = run(t.Context(), []string{"worktree", "remove", "feature", "--output", "json", "--no-input"}, testBuild(), nil)
	if code != 0 || stderr != "" || !strings.Contains(stdout, `"action":"worktree_remove"`) {
		t.Fatalf("remove code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	code, stdout, stderr = run(t.Context(), []string{"worktree", "prune", "--output", "json", "--no-input"}, testBuild(), nil)
	if code != 0 || stderr != "" || !strings.Contains(stdout, `"action":"worktree_prune"`) || !strings.Contains(stdout, `"repositories_checked":1`) {
		t.Fatalf("prune code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func frameTmuxSessionFields(fields []string) []byte {
	var output strings.Builder
	for index, field := range fields {
		if index != 0 {
			output.WriteByte(';')
		}
		_, _ = fmt.Fprintf(&output, "%d:%s", len(field), field)
	}
	output.WriteByte('\n')
	return []byte(output.String())
}

func runGitTestCommand(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
}

func TestWorktreeDirectModeRequiresHostConfiguration(t *testing.T) {
	home := t.TempDir()
	identityPath := filepath.Join(home, ".local", "state", "schooner", "identity")
	if err := os.MkdirAll(filepath.Dir(identityPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(identityPath, []byte("11111111-1111-4111-8111-111111111111\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("SCHOONER_CONFIG", "")
	code, _, stderr := run(t.Context(), []string{"worktree", "list", "--no-input"}, testBuild(), nil)
	if code != 1 || !strings.Contains(stderr, "run box setup from a workstation") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
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

	want := "\n✓ Schooner v0.1.0-test\n" +
		"  Commit    abc1234\n" +
		"  Built     2026-08-24T12:34:56Z\n" +
		"  Go        go1.27.0\n" +
		"  Platform  linux/arm64\n"
	if stdout != want {
		t.Errorf("stdout mismatch\n--- want ---\n%s\n--- got ---\n%s", want, stdout)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
}

func TestVersionAutoColorUsesStdoutTerminalState(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	for _, test := range []struct {
		name        string
		outTerminal bool
		errTerminal bool
		wantColor   bool
	}{
		{name: "redirected stdout", outTerminal: false, errTerminal: true, wantColor: false},
		{name: "terminal stdout", outTerminal: true, errTerminal: false, wantColor: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := cli.RunAtHostHome(t.Context(), []string{"version"}, cli.Streams{
				In: strings.NewReader(""), Out: &stdout, Err: &stderr,
				OutIsTerminal: test.outTerminal, ErrIsTerminal: test.errTerminal,
			}, testBuild(), t.TempDir())
			if code != 0 || stderr.Len() != 0 {
				t.Fatalf("code=%d stderr=%q", code, stderr.String())
			}
			if hasColor := strings.Contains(stdout.String(), "\x1b["); hasColor != test.wantColor {
				t.Fatalf("stdout color = %t, want %t: %q", hasColor, test.wantColor, stdout.String())
			}
		})
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
		{
			name:    "default box requires name",
			args:    []string{"box", "use"},
			message: "accepts 1 arg(s), received 0",
		},
		{
			name:    "setup accepts one box",
			args:    []string{"box", "setup", "one", "two"},
			message: "accepts at most 1 arg",
		},
		{
			name:    "update accepts one box",
			args:    []string{"box", "update", "one", "two"},
			message: "accepts at most 1 arg",
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
	artifactDir := filepath.Join(home, "artifacts")
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		t.Fatal(err)
	}
	artifactName := "schooner_v0.1.0-test_linux_amd64"
	artifactContents := []byte("verified test artifact")
	if err := os.WriteFile(filepath.Join(artifactDir, artifactName), artifactContents, 0o755); err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(artifactContents))
	if err := os.WriteFile(filepath.Join(artifactDir, "SHA256SUMS"), []byte(digest+"  "+artifactName+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("SCHOONER_ARTIFACT_DIR", artifactDir)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	code, stdout, stderr := run(t.Context(), []string{"box", "add", "work", "--ssh", "work-host", "--yes", "--accept-new-host-key", "--output", "json"}, testBuild(), nil)
	if code != 0 || stderr != "" {
		t.Fatalf("add: code=%d stderr=%q", code, stderr)
	}
	add := normalizeBoxJSON(t, stdout, false)
	if want := golden(t, "box-add.json"); add != want {
		t.Fatalf("add JSON mismatch\nwant: %s\ngot:  %s", want, add)
	}

	code, stdout, stderr = run(t.Context(), []string{"box", "setup", "work", "--output", "json"}, testBuild(), nil)
	if code != 0 || stderr != "" || !strings.Contains(stdout, `"action":"reused"`) || !strings.Contains(stdout, `"installed":[]`) {
		t.Fatalf("setup: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}

	code, stdout, stderr = run(t.Context(), []string{"box", "update", "work", "--output", "json"}, testBuild(), nil)
	if code != 0 || stderr != "" || !strings.Contains(stdout, `"action":"reused"`) || !strings.Contains(stdout, `"target_version":"v0.1.0-test"`) {
		t.Fatalf("update: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}

	code, stdout, stderr = run(t.Context(), []string{"box", "status", "work", "--output", "json"}, testBuild(), nil)
	if code != 0 || stderr != "" {
		t.Fatalf("status: code=%d stderr=%q", code, stderr)
	}
	status := normalizeBoxJSON(t, stdout, true)
	if want := golden(t, "box-status.json"); status != want {
		t.Fatalf("status JSON mismatch\nwant: %s\ngot:  %s", want, status)
	}
	// Establish a valid local Box identity and deliberately unusable direct
	// configuration. Explicit --box must still select the SSH runtime.
	identityPath := filepath.Join(home, ".local", "state", "schooner", "identity")
	if err := os.MkdirAll(filepath.Dir(identityPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(identityPath, []byte("11111111-1111-4111-8111-111111111111\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	configPath, err := config.Path()
	if err != nil {
		t.Fatal(err)
	}
	if err = config.Write(configPath, config.Host{WorktreeRoot: filepath.Join(home, "missing-direct-root")}); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr = run(t.Context(), []string{"worktree", "list", "--box", "work", "--output", "json", "--no-input"}, testBuild(), nil)
	if code != 0 || stderr != "" || !strings.Contains(stdout, `"relative_path":"owner/repo"`) {
		t.Fatalf("worktree list: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	code, stdout, stderr = run(t.Context(), []string{"worktree", "inspect", "owner/repo", "--box", "work", "--output", "json", "--no-input"}, testBuild(), nil)
	if code != 0 || stderr != "" || !strings.Contains(stdout, `"common_directory":"/home/alice/schooner/owner/repo/.git"`) {
		t.Fatalf("worktree inspect: code=%d stdout=%q stderr=%q", code, stdout, stderr)
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
	if code != 0 || stdout != "args=-o BatchMode=yes work-alias\n" || stderr != "" {
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
	saveTestBox(t, box.Record{Name: "other", Acquisition: "adopted", SSHDestination: "other-alias"})

	code, stdout, stderr := runTerminal(t.Context(), []string{"box", "ssh", "--accessible"}, "1\n")
	if code != 0 || stdout != "" || !strings.Contains(stderr, "Choose a box to connect to") || !strings.Contains(stderr, "work") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestBoxUseMarksListAndResolvesDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	installTestSSH(t, home, "#!/bin/sh\nprintf 'args=%s\\n' \"$*\"\n")
	saveTestBox(t, box.Record{Name: "alpha", Acquisition: "adopted", SSHDestination: "alpha-host"})
	saveTestBox(t, box.Record{Name: "beta", Acquisition: "adopted", SSHDestination: "beta-host"})
	code, stdout, stderr := run(t.Context(), []string{"box", "status", "--output", "json"}, testBuild(), nil)
	if code != 1 || stdout != "" || !strings.Contains(stderr, `"code":"box_selection_ambiguous"`) || !strings.Contains(stderr, `"candidates":"alpha,beta"`) {
		t.Fatalf("ambiguity: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}

	code, stdout, stderr = run(t.Context(), []string{"box", "use", "alpha", "--output", "json"}, testBuild(), nil)
	if code != 0 || stdout != "{\"schema_version\":\"1\",\"default_box\":\"alpha\"}\n" || stderr != "" {
		t.Fatalf("use json: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	code, stdout, stderr = run(t.Context(), []string{"box", "use", "beta"}, testBuild(), nil)
	if code != 0 || stdout != "\n✓ Default box: beta\n" || stderr != "" {
		t.Fatalf("use: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	code, stdout, stderr = run(t.Context(), []string{"box", "list", "--output", "json"}, testBuild(), nil)
	if code != 0 || stderr != "" || !strings.Contains(stdout, `"name":"beta"`) || !strings.Contains(stdout, `"default":true`) {
		t.Fatalf("list: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	code, stdout, stderr = runTerminal(t.Context(), []string{"box", "ssh", "--no-input"}, "")
	if code != 0 || stdout != "args=-o BatchMode=yes beta-host\n" || stderr != "" {
		t.Fatalf("ssh: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	code, stdout, stderr = run(t.Context(), []string{"box", "remove", "--yes", "--no-input"}, testBuild(), nil)
	if code != 2 || stdout != "" || !strings.Contains(stderr, "box name is required") {
		t.Fatalf("remove: code=%d stdout=%q stderr=%q", code, stdout, stderr)
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
		WorktreeRoot:  box.DefaultWorktreeRoot,
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
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	database, err := sqlite.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Dir(database)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
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

	code := cli.RunAtHostHome(ctx, args, cli.Streams{
		In:  strings.NewReader(""),
		Out: out,
		Err: &stderr,
	}, build, os.Getenv("HOME"))

	return code, stdout.String(), stderr.String()
}

func runTerminal(ctx context.Context, args []string, input string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := cli.RunAtHostHome(ctx, args, cli.Streams{
		In:            strings.NewReader(input),
		Out:           &stdout,
		Err:           &stderr,
		InIsTerminal:  true,
		OutIsTerminal: true,
		ErrIsTerminal: true,
	}, testBuild(), os.Getenv("HOME"))
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
	record.WorktreeRoot = "/home/test/schooner"
	record.CreatedAt = now
	record.UpdatedAt = now
	op := box.AddOperation{Name: record.Name, SSHDestination: record.SSHDestination, WorktreeRoot: record.WorktreeRoot, UpdatedAt: now}
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
state="$HOME/fake-host-installed"
hello='{"schema_version":"1","protocol_version":"1","schooner_version":"v0.1.0-test","commit":"abc1234","box_identity":"11111111-1111-4111-8111-111111111111","os":"linux","architecture":"amd64","capabilities":["host.configure.v1","host.doctor.v1","host.hello.v1","host.inspect.v2","worktree.inspect.v1","worktree.list.v1"]}'
inspection='{"schema_version":"1","protocol_version":"1","os_id":"ubuntu","os_version":"24.04","architecture":"amd64","home":"/home/alice","box_identity":"11111111-1111-4111-8111-111111111111","worktree_root":"/home/alice/schooner","worktree_root_exists":true,"git":{"available":true,"version":"git version 2.43.0"},"tmux":{"available":true,"version":"tmux 3.4"},"passwordless_sudo":true}'
case " $* " in
  *"host hello"*)
    command=; for argument do command=$argument; done
    runtime_path=$(printf %s "${command##* }" | base64 -d)
    case "$runtime_path" in *.stage-*) printf '%s\n' "$hello"; exit 0 ;; esac
    if [ -f "$state" ]; then printf '%s\n' "$hello"; exit 0; fi
    exit 127 ;;
  *"host inspect"*) dd bs=4096 >/dev/null 2>&1; printf '%s\n' "$inspection"; exit 0 ;;
  *"host configure"*) dd bs=4096 >/dev/null 2>&1; printf '%s\n' '{"schema_version":"1","protocol_version":"1","box_identity":"11111111-1111-4111-8111-111111111111","worktree_root":"/home/alice/schooner"}'; exit 0 ;;
  *"host worktree list"*) dd bs=4096 >/dev/null 2>&1; printf '%s\n' '{"schema_version":"1","protocol_version":"1","box_identity":"11111111-1111-4111-8111-111111111111","worktree_root":"/home/alice/schooner","repositories":[{"common_directory":"/home/alice/schooner/owner/repo/.git","origin":"https://example.com/owner/repo","primary":{"path":"/home/alice/schooner/owner/repo","relative_path":"owner/repo","git_directory":"/home/alice/schooner/owner/repo/.git","kind":"primary","branch":"main","detached":false,"head":"abc","status":{"staged":0,"unstaged":0,"untracked":0,"conflicted":0}},"linked":[]}],"warnings":[]}'; exit 0 ;;
  *"host worktree inspect"*) dd bs=4096 >/dev/null 2>&1; printf '%s\n' '{"schema_version":"1","protocol_version":"1","box_identity":"11111111-1111-4111-8111-111111111111","worktree_root":"/home/alice/schooner","repository":{"common_directory":"/home/alice/schooner/owner/repo/.git","origin":"https://example.com/owner/repo","primary":{"path":"/home/alice/schooner/owner/repo","relative_path":"owner/repo","git_directory":"/home/alice/schooner/owner/repo/.git","kind":"primary","branch":"main","detached":false,"head":"abc","status":{"staged":0,"unstaged":0,"untracked":0,"conflicted":0}},"linked":[]},"worktree":{"path":"/home/alice/schooner/owner/repo","relative_path":"owner/repo","git_directory":"/home/alice/schooner/owner/repo/.git","kind":"primary","branch":"main","detached":false,"head":"abc","status":{"staged":0,"unstaged":0,"untracked":0,"conflicted":0}},"warnings":[]}'; exit 0 ;;
  *"flock -x 9"*) : > "$state"; exit 0 ;;
  *"cat >&3"*) dd bs=4096 >/dev/null 2>&1; exit 0 ;;
  *"printf \"%s\\n\" missing"*) printf '%s\n' missing; exit 0 ;;
  *"rm -f --"*) exit 0 ;;
esac
program=$(dd bs=4096 2>/dev/null)
case "$program" in
  *"candidate=\$1"*)
    printf '%s\n' '{"schema_version":"1","remote_identity":"11111111-1111-4111-8111-111111111111"}' ;;
  *"requested=\$1"*)
    printf '%s\n' '{"schema_version":"1","worktree_root":"/home/alice/schooner"}' ;;
  *)
    printf '%s\n' '{"schema_version":"1","os_id":"ubuntu","os_version":"24.04","architecture":"amd64","home":"/home/alice","remote_identity":"11111111-1111-4111-8111-111111111111","worktree_root":"/home/alice/schooner","worktree_root_exists":true,"git":{"available":true,"version":"git version 2.43.0"},"tmux":{"available":true,"version":"tmux 3.4"},"passwordless_sudo":true}' ;;
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
