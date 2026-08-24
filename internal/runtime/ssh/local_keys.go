package ssh

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/thewelshrich/schooner/internal/provider"
)

func DiscoverLocalPublicKeys() ([]provider.PublicKey, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home directory for SSH keys: %w", err)
	}
	return discoverLocalPublicKeys(filepath.Join(home, ".ssh"))
}

func discoverLocalPublicKeys(directory string) ([]provider.PublicKey, error) {
	entries, err := os.ReadDir(directory)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect local SSH keys: %w", err)
	}
	var keys []provider.PublicKey
	seen := map[string]bool{}
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() || !strings.HasSuffix(entry.Name(), ".pub") {
			continue
		}
		contents, readErr := os.ReadFile(filepath.Join(directory, entry.Name()))
		if readErr != nil {
			return nil, fmt.Errorf("read local SSH public key %s: %w", entry.Name(), readErr)
		}
		fields := strings.Fields(string(contents))
		if len(fields) < 2 || !supportedPublicKeyType(fields[0]) {
			continue
		}
		wire, decodeErr := base64.StdEncoding.DecodeString(fields[1])
		if decodeErr != nil || len(wire) == 0 {
			continue
		}
		digest := sha256.Sum256(wire)
		fingerprint := "SHA256:" + base64.RawStdEncoding.EncodeToString(digest[:])
		if seen[fingerprint] {
			continue
		}
		seen[fingerprint] = true
		keys = append(keys, provider.PublicKey{Name: strings.TrimSuffix(entry.Name(), ".pub"), Fingerprint: fingerprint, PublicKey: fields[0] + " " + fields[1]})
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].Name < keys[j].Name })
	return keys, nil
}

func supportedPublicKeyType(value string) bool {
	return value == "ssh-ed25519" || value == "ssh-rsa" || strings.HasPrefix(value, "ecdsa-sha2-") || strings.HasPrefix(value, "sk-ssh-") || strings.HasPrefix(value, "sk-ecdsa-")
}
