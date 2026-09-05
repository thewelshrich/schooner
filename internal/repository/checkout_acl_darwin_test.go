package repository

import (
	"os/exec"
	"testing"
)

func installCheckoutTestACL(t *testing.T, path string) {
	t.Helper()
	output, err := exec.Command("/bin/chmod", "+a", "everyone allow readattr", path).CombinedOutput()
	if err != nil {
		t.Fatalf("set directory ACL: %v: %s", err, output)
	}
}
