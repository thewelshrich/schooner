package ssh

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverLocalPublicKeysReadsOnlyValidRegularPublicKeys(t *testing.T) {
	directory := t.TempDir()
	wire := base64.StdEncoding.EncodeToString([]byte("valid-key-wire"))
	if err := os.WriteFile(filepath.Join(directory, "id_ed25519.pub"), []byte("ssh-ed25519 "+wire+" laptop\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "id_ed25519"), []byte("PRIVATE"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "broken.pub"), []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(directory, "id_ed25519.pub"), filepath.Join(directory, "linked.pub")); err != nil {
		t.Fatal(err)
	}

	keys, err := discoverLocalPublicKeys(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0].Name != "id_ed25519" || keys[0].PublicKey != "ssh-ed25519 "+wire || keys[0].Fingerprint == "" {
		t.Fatalf("keys = %+v", keys)
	}
}
