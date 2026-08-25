package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPathPrecedence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("SCHOONER_CONFIG", "")
	path, err := Path()
	if err != nil || path != filepath.Join(home, ".config", "schooner", "config.toml") {
		t.Fatalf("Path() = %q, %v", path, err)
	}
	xdg := filepath.Join(home, "xdg")
	t.Setenv("XDG_CONFIG_HOME", xdg)
	path, _ = Path()
	if path != filepath.Join(xdg, "schooner", "config.toml") {
		t.Fatalf("XDG path = %q", path)
	}
	override := filepath.Join(home, "custom.toml")
	t.Setenv("SCHOONER_CONFIG", override)
	path, _ = Path()
	if path != override {
		t.Fatalf("override path = %q", path)
	}
}

func TestWriteReadStrictAndPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.toml")
	root := filepath.Join(t.TempDir(), "worktrees")
	if err := Write(path, Host{WorktreeRoot: root}); err != nil {
		t.Fatal(err)
	}
	result, err := Read(path)
	if err != nil || result.SchemaVersion != 1 || result.WorktreeRoot != root {
		t.Fatalf("Read() = %+v, %v", result, err)
	}
	fileInfo, _ := os.Stat(path)
	directoryInfo, _ := os.Stat(filepath.Dir(path))
	if fileInfo.Mode().Perm() != 0o600 || directoryInfo.Mode().Perm() != 0o700 {
		t.Fatalf("permissions = %o, %o", fileInfo.Mode().Perm(), directoryInfo.Mode().Perm())
	}
	contents, _ := os.ReadFile(path)
	if !strings.Contains(string(contents), "schema_version = 1") || !strings.Contains(string(contents), "worktree_root = ") {
		t.Fatalf("contents = %s", contents)
	}
}

func TestReadRejectsUnknownVersionAndNonCanonicalRoot(t *testing.T) {
	for name, body := range map[string]string{
		"unknown":      "schema_version = 1\nworktree_root = \"/tmp/work\"\nextra = true\n",
		"version":      "schema_version = 2\nworktree_root = \"/tmp/work\"\n",
		"relative":     "schema_version = 1\nworktree_root = \"work\"\n",
		"noncanonical": "schema_version = 1\nworktree_root = \"/tmp/../tmp/work\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Read(path); err == nil {
				t.Fatal("Read succeeded")
			}
		})
	}
}
