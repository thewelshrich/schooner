package ssh

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureIdentityCreatesAndReusesProtectedEd25519Key(t *testing.T) {
	first, err := EnsureIdentity(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	second, err := EnsureIdentity(t.Context(), filepath.Dir(filepath.Dir(first.PrivateKey)))
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	info, err := os.Stat(first.PrivateKey)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%v err=%v", info.Mode().Perm(), err)
	}
}

func TestEnsureIdentityRejectsSymlinkDirectory(t *testing.T) {
	state := t.TempDir()
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(state, "ssh")); err != nil {
		t.Skip(err)
	}
	if _, err := EnsureIdentity(t.Context(), state); err == nil {
		t.Fatal("symlink identity directory accepted")
	}
}
