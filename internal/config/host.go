// Package config owns Schooner's strict per-host configuration contract.
package config

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

const SchemaVersion = 1

type Host struct {
	SchemaVersion int    `toml:"schema_version"`
	WorktreeRoot  string `toml:"worktree_root"`
}

func Path() (string, error) {
	if override := os.Getenv("SCHOONER_CONFIG"); override != "" {
		if !filepath.IsAbs(override) {
			return "", fmt.Errorf("SCHOONER_CONFIG must be an absolute path")
		}
		return filepath.Clean(override), nil
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		current, err := user.Current()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		if current.HomeDir == "" || !filepath.IsAbs(current.HomeDir) {
			return "", fmt.Errorf("current user home directory is invalid")
		}
		base = filepath.Join(current.HomeDir, ".config")
	} else if !filepath.IsAbs(base) {
		return "", fmt.Errorf("XDG_CONFIG_HOME must be an absolute path")
	}
	return filepath.Join(base, "schooner", "config.toml"), nil
}

func Read(path string) (Host, error) {
	var result Host
	metadata, err := toml.DecodeFile(path, &result)
	if err != nil {
		return Host{}, fmt.Errorf("read host configuration: %w", err)
	}
	if undecoded := metadata.Undecoded(); len(undecoded) != 0 {
		return Host{}, fmt.Errorf("host configuration contains unknown field %q", undecoded[0].String())
	}
	if result.SchemaVersion != SchemaVersion {
		return Host{}, fmt.Errorf("host configuration schema version %d is unsupported", result.SchemaVersion)
	}
	if err := validateRoot(result.WorktreeRoot); err != nil {
		return Host{}, err
	}
	return result, nil
}

func Write(path string, value Host) error {
	if value.SchemaVersion == 0 {
		value.SchemaVersion = SchemaVersion
	}
	if value.SchemaVersion != SchemaVersion {
		return fmt.Errorf("host configuration schema version %d is unsupported", value.SchemaVersion)
	}
	if err := validateRoot(value.WorktreeRoot); err != nil {
		return err
	}
	directory := filepath.Dir(path)
	_, statErr := os.Stat(directory)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create host configuration directory: %w", err)
	}
	if errors.Is(statErr, os.ErrNotExist) {
		statErr = os.Chmod(directory, 0o700)
	}
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("inspect host configuration directory: %w", statErr)
	}
	temporary, err := os.CreateTemp(directory, ".config-*.toml")
	if err != nil {
		return fmt.Errorf("create host configuration: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err = temporary.Chmod(0o600); err == nil {
		err = toml.NewEncoder(temporary).Encode(value)
	}
	if err == nil {
		err = temporary.Sync()
	}
	closeErr := temporary.Close()
	if err != nil {
		return fmt.Errorf("write host configuration: %w", err)
	}
	if closeErr != nil {
		return fmt.Errorf("close host configuration: %w", closeErr)
	}
	if err = os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace host configuration: %w", err)
	}
	return nil
}

func ReadDefault() (Host, error) {
	path, err := Path()
	if err != nil {
		return Host{}, err
	}
	result, err := Read(path)
	if errors.Is(err, os.ErrNotExist) {
		return Host{}, fmt.Errorf("host configuration is missing at %s; run box setup", path)
	}
	return result, err
}

func validateRoot(root string) error {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root || strings.ContainsAny(root, "\x00\r\n") {
		return fmt.Errorf("worktree_root must be a canonical absolute path")
	}
	return nil
}
