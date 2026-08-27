package source

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"slices"
	"strings"
)

// PublicKeyFingerprint computes the OpenSSH SHA256 fingerprint without
// depending on private key material or invoking an external process.
func PublicKeyFingerprint(value string) (string, error) {
	if value == "" || len(value) > 16<<10 || strings.ContainsAny(value, "\x00\r\n") {
		return "", fmt.Errorf("SSH public key is invalid")
	}
	fields := strings.Fields(value)
	if len(fields) < 2 || !supportedKeyType(fields[0]) {
		return "", fmt.Errorf("SSH public key type is unsupported")
	}
	blob, err := base64.StdEncoding.DecodeString(fields[1])
	if err != nil || len(blob) == 0 || len(blob) > 8<<10 {
		return "", fmt.Errorf("SSH public key body is invalid")
	}
	if len(blob) < 4 {
		return "", fmt.Errorf("SSH public key body is invalid")
	}
	algorithmLength := int(binary.BigEndian.Uint32(blob[:4]))
	if algorithmLength <= 0 || algorithmLength > 256 || 4+algorithmLength > len(blob) || string(blob[4:4+algorithmLength]) != fields[0] {
		return "", fmt.Errorf("SSH public key body does not match its declared type")
	}
	digest := sha256.Sum256(blob)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(digest[:]), nil
}

func ValidateHostKeys(values []HostKey) error {
	if len(values) == 0 || len(values) > 16 {
		return fmt.Errorf("GitHub host key set is empty or too large")
	}
	seen := map[string]bool{}
	for _, value := range values {
		actual, err := PublicKeyFingerprint(value.Key)
		if err != nil || actual != value.Fingerprint || seen[actual] {
			return fmt.Errorf("GitHub host key fingerprint is invalid")
		}
		seen[actual] = true
	}
	return nil
}

func ValidateFingerprint(value string) error {
	if !strings.HasPrefix(value, "SHA256:") {
		return fmt.Errorf("SSH public key fingerprint is invalid")
	}
	digest, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(value, "SHA256:"))
	if err != nil || len(digest) != sha256.Size {
		return fmt.Errorf("SSH public key fingerprint is invalid")
	}
	return nil
}

// HostKeyFingerprints returns the canonical fingerprint set represented by
// validated GitHub host-key metadata.
func HostKeyFingerprints(values []HostKey) ([]string, error) {
	if err := ValidateHostKeys(values); err != nil {
		return nil, err
	}
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = value.Fingerprint
	}
	slices.Sort(result)
	return result, nil
}

// ValidateHostFingerprints checks the bounded, canonical trust evidence sent
// over the runtime protocol.
func ValidateHostFingerprints(values []string) error {
	if len(values) == 0 || len(values) > 16 || !slices.IsSorted(values) {
		return fmt.Errorf("GitHub host fingerprint set is invalid")
	}
	for index, value := range values {
		if ValidateFingerprint(value) != nil || (index > 0 && values[index-1] == value) {
			return fmt.Errorf("GitHub host fingerprint set is invalid")
		}
	}
	return nil
}

func HostFingerprintsMatch(actual []string, expected []HostKey) bool {
	want, err := HostKeyFingerprints(expected)
	return err == nil && ValidateHostFingerprints(actual) == nil && slices.Equal(actual, want)
}

func supportedKeyType(value string) bool {
	switch value {
	case "ssh-ed25519", "ssh-rsa", "ecdsa-sha2-nistp256", "ecdsa-sha2-nistp384", "ecdsa-sha2-nistp521",
		"sk-ssh-ed25519@openssh.com", "sk-ecdsa-sha2-nistp256@openssh.com":
		return true
	default:
		return false
	}
}
