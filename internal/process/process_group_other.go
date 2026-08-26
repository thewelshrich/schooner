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

func runInteractiveTerminal(command *exec.Cmd, _ *os.File) error {
	configureCommandCancellation(command)
	return command.Run()
}
