package ssh

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/thewelshrich/schooner/internal/box"
)

type Identity struct {
	PublicKey  string
	PrivateKey string
}

// EnsureIdentity prepares the one local identity used for provider-created
// boxes. It refuses symlinks and never overwrites partial key material.
func EnsureIdentity(ctx context.Context, stateDirectory string) (Identity, error) {
	directory := filepath.Join(stateDirectory, "ssh")
	if err := ensurePrivateDirectory(directory); err != nil {
		return Identity{}, err
	}
	privatePath := filepath.Join(directory, "id_ed25519")
	publicPath := privatePath + ".pub"
	privateExists, err := regularFile(privatePath)
	if err != nil {
		return Identity{}, err
	}
	publicExists, err := regularFile(publicPath)
	if err != nil {
		return Identity{}, err
	}
	if privateExists != publicExists {
		return Identity{}, box.NewError("conflict", "Schooner SSH identity is incomplete; repair or remove both identity files", nil)
	}
	if !privateExists {
		path, lookErr := exec.LookPath("ssh-keygen")
		if lookErr != nil {
			return Identity{}, box.NewError("unsupported", "system ssh-keygen was not found in PATH", lookErr)
		}
		command := exec.CommandContext(ctx, path, "-q", "-t", "ed25519", "-N", "", "-C", "Schooner CLI", "-f", privatePath)
		if output, runErr := command.CombinedOutput(); runErr != nil {
			return Identity{}, box.NewError("internal", "could not generate Schooner SSH identity", fmt.Errorf("%w: %s", runErr, strings.TrimSpace(string(output))))
		}
	}
	if err = os.Chmod(privatePath, 0o600); err != nil {
		return Identity{}, err
	}
	if err = os.Chmod(publicPath, 0o644); err != nil {
		return Identity{}, err
	}
	contents, err := os.ReadFile(publicPath)
	if err != nil {
		return Identity{}, err
	}
	fields := strings.Fields(string(contents))
	if len(fields) < 2 || fields[0] != "ssh-ed25519" {
		return Identity{}, box.NewError("conflict", "Schooner SSH public identity is invalid", nil)
	}
	return Identity{PublicKey: fields[0] + " " + fields[1] + " Schooner CLI", PrivateKey: privatePath}, nil
}

func ensurePrivateDirectory(path string) error {
	if info, err := os.Lstat(path); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return box.NewError("conflict", "Schooner SSH identity directory must not be a symlink", nil)
		}
	} else if !os.IsNotExist(err) {
		return err
	} else if err = os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return os.Chmod(path, 0o700)
}

func regularFile(path string) (bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, box.NewError("conflict", "Schooner SSH identity files must be regular files", nil)
	}
	return true, nil
}
