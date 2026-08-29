//go:build darwin

package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/thewelshrich/schooner/internal/process"
)

const (
	appleTeamID     = "LDCWNW7T7K"
	appleIdentifier = "app.schooner.cli"
)

func verifyPlatformSignature(ctx context.Context, path string) error {
	if info, err := os.Stat("/usr/bin/codesign"); err != nil || !info.Mode().IsRegular() {
		return errors.New("/usr/bin/codesign is required on macOS")
	}
	requirement := fmt.Sprintf(`anchor apple generic and identifier %q and certificate leaf[subject.OU] = %q and certificate 1[field.1.2.840.113635.100.6.2.6] exists and certificate leaf[field.1.2.840.113635.100.6.1.13] exists`, appleIdentifier, appleTeamID)
	_, err := process.Run(ctx, candidateCheckLimit, "/usr/bin/codesign", "--verify", "--strict", "--verbose=2", "-R="+requirement, path)
	return err
}
