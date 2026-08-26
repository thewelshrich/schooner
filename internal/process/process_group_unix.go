//go:build unix

package process

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"golang.org/x/sys/unix"
)

func configureCommandCancellation(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		return killProcessGroup(command.Process.Pid)
	}
}

func runInteractiveTerminal(command *exec.Cmd, terminal *os.File) (err error) {
	parentGroup := syscall.Getpgrp()
	configureCommandCancellation(command)
	cancelCommandGroup := command.Cancel
	command.Cancel = func() error {
		foregroundGroup, foregroundErr := unix.IoctlGetInt(int(terminal.Fd()), unix.TIOCGPGRP)
		var cancellationErrors []error
		cancelled := false
		if foregroundErr == nil && foregroundGroup != parentGroup && (command.Process == nil || foregroundGroup != command.Process.Pid) {
			if killErr := killProcessGroup(foregroundGroup); killErr == nil {
				cancelled = true
			} else {
				cancellationErrors = append(cancellationErrors, killErr)
			}
		} else if foregroundErr != nil {
			cancellationErrors = append(cancellationErrors, foregroundErr)
		}
		if commandKillErr := cancelCommandGroup(); commandKillErr == nil {
			cancelled = true
		} else {
			cancellationErrors = append(cancellationErrors, commandKillErr)
		}
		if cancelled {
			return nil
		}
		return errors.Join(cancellationErrors...)
	}
	if err := command.Start(); err != nil {
		return err
	}
	if err := setForegroundProcessGroup(terminal, command.Process.Pid); err != nil {
		_ = command.Cancel()
		_ = command.Wait()
		return fmt.Errorf("give terminal to interactive command: %w", err)
	}
	defer func() {
		restoreErr := setForegroundProcessGroup(terminal, parentGroup)
		if err == nil && restoreErr != nil {
			err = fmt.Errorf("restore terminal foreground process group: %w", restoreErr)
		}
	}()
	// The child may have attempted a terminal read after Start and received
	// SIGTTIN before the foreground handoff completed. Resume its group after
	// ownership changes; SIGCONT is harmless when it never stopped.
	if err := continueProcessGroup(command.Process.Pid); err != nil {
		if errors.Is(err, os.ErrProcessDone) {
			return command.Wait()
		}
		_ = command.Cancel()
		_ = command.Wait()
		return fmt.Errorf("resume interactive command after terminal handoff: %w", err)
	}
	return command.Wait()
}

func killProcessGroup(group int) error {
	err := syscall.Kill(-group, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}

func continueProcessGroup(group int) error {
	err := syscall.Kill(-group, syscall.SIGCONT)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}

func setForegroundProcessGroup(terminal *os.File, group int) error {
	// A background process group is normally stopped when it changes terminal
	// ownership. Preserve an inherited ignored disposition; otherwise ignore
	// SIGTTOU only for the short ioctl handoff.
	ignored := signal.Ignored(syscall.SIGTTOU)
	if !ignored {
		signal.Ignore(syscall.SIGTTOU)
		defer signal.Reset(syscall.SIGTTOU)
	}
	return unix.IoctlSetPointerInt(int(terminal.Fd()), unix.TIOCSPGRP, group)
}
