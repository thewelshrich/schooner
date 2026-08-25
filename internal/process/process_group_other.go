//go:build !unix

package process

import (
	"os"
	"os/exec"
)

func configureCommandCancellation(command *exec.Cmd) {
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		return command.Process.Kill()
	}
}
