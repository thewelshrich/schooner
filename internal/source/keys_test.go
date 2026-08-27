package source

import (
	"encoding/base64"
	"encoding/binary"
	"testing"
)

func TestPublicKeyFingerprintRejectsDeclaredTypeMismatch(t *testing.T) {
	body := make([]byte, 4+len("ssh-rsa")+4)
	binary.BigEndian.PutUint32(body[:4], uint32(len("ssh-rsa")))
	copy(body[4:], "ssh-rsa")
	value := "ssh-ed25519 " + base64.StdEncoding.EncodeToString(body)
	if _, err := PublicKeyFingerprint(value); err == nil {
		t.Fatal("declared type mismatch was accepted")
	}
}

func TestPublicKeyFingerprintAcceptsOpenSSHSecurityKeyTypes(t *testing.T) {
	for _, keyType := range []string{"sk-ssh-ed25519@openssh.com", "sk-ecdsa-sha2-nistp256@openssh.com"} {
		t.Run(keyType, func(t *testing.T) {
			body := make([]byte, 4+len(keyType))
			binary.BigEndian.PutUint32(body[:4], uint32(len(keyType)))
			copy(body[4:], keyType)
			if _, err := PublicKeyFingerprint(keyType + " " + base64.StdEncoding.EncodeToString(body)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestValidateFingerprintRequiresOneSHA256Digest(t *testing.T) {
	fingerprint, err := PublicKeyFingerprint(testPublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if err = ValidateFingerprint(fingerprint); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"", "MD5:00", "SHA256:not-base64", "SHA256:AA"} {
		if err = ValidateFingerprint(invalid); err == nil {
			t.Fatalf("fingerprint %q was accepted", invalid)
		}
	}
}

func TestValidateHostKeysRejectsDuplicatesAndBounds(t *testing.T) {
	fingerprint, err := PublicKeyFingerprint(testHostKey)
	if err != nil {
		t.Fatal(err)
	}
	key := HostKey{Key: testHostKey, Fingerprint: fingerprint}
	if err = ValidateHostKeys([]HostKey{key, key}); err == nil {
		t.Fatal("duplicate host key was accepted")
	}
	if err = ValidateHostKeys(nil); err == nil {
		t.Fatal("empty host key set was accepted")
	}
}
