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

func configureCommandCancellationWithoutProcessGroup(command *exec.Cmd) {
	configureCommandCancellation(command)
}

func cleanupInteractiveProcessTree(_ *exec.Cmd) error { return nil }

func cleanupInteractiveProcessTreeWithoutGroup(_ *exec.Cmd) error { return nil }

func runInteractiveTerminal(command *exec.Cmd, _ *os.File) error {
	configureCommandCancellation(command)
	return command.Run()
}
