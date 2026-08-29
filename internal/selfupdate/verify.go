package selfupdate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/thewelshrich/schooner/internal/process"
)

type candidateVersion struct {
	SchemaVersion string `json:"schema_version"`
	Version       string `json:"version"`
	OS            string `json:"os"`
	Arch          string `json:"arch"`
}

func verifyCandidateIdentity(ctx context.Context, path, version, osName, arch string) error {
	checkContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	output, err := process.Run(checkContext, candidateCheckLimit, path, "version", "--output", "json")
	if err != nil {
		return fmt.Errorf("run candidate version check: %w", err)
	}
	var document candidateVersion
	decoder := json.NewDecoder(bytes.NewReader(output))
	if err = decoder.Decode(&document); err != nil {
		return fmt.Errorf("decode candidate version output: %w", err)
	}
	var trailing any
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("candidate version output contains trailing JSON")
		}
		return err
	}
	if document.SchemaVersion != SchemaVersion || document.Version != version || document.OS != osName || document.Arch != arch {
		return fmt.Errorf("candidate reports schema=%q version=%q platform=%s/%s, expected schema=%q version=%q platform=%s/%s", document.SchemaVersion, document.Version, document.OS, document.Arch, SchemaVersion, version, osName, arch)
	}
	return nil
}
