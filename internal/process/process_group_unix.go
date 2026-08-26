//go:build unix

package process

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
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
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		var cancellationErrors []error
		if stopErr := stopProcessGroup(command.Process.Pid); stopErr != nil && !errors.Is(stopErr, os.ErrProcessDone) {
			cancellationErrors = append(cancellationErrors, stopErr)
		}
		if treeErr := terminateDescendants(command.Process.Pid); treeErr != nil {
			cancellationErrors = append(cancellationErrors, treeErr)
		}
		commandKillErr := killProcessGroup(command.Process.Pid)
		if commandKillErr == nil {
			return nil
		}
		cancellationErrors = append(cancellationErrors, commandKillErr)
		return errors.Join(cancellationErrors...)
	}
	if err := command.Start(); err != nil {
		return err
	}
	childChanges := make(chan os.Signal, 1)
	signal.Notify(childChanges, syscall.SIGCHLD)
	defer signal.Stop(childChanges)
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
	waitDone := make(chan error, 1)
	go func() { waitDone <- command.Wait() }()
	for {
		select {
		case err := <-waitDone:
			return err
		case <-childChanges:
			// Avoid feeding the SIGCHLD from the short-lived ps probe back into
			// this channel and creating a probe loop.
			signal.Stop(childChanges)
			stopped, stateErr := processIsStopped(command.Process.Pid)
			signal.Notify(childChanges, syscall.SIGCHLD)
			if stateErr != nil || !stopped {
				continue
			}
			if err := setForegroundProcessGroup(terminal, parentGroup); err != nil {
				_ = command.Cancel()
				<-waitDone
				return fmt.Errorf("restore terminal for stopped interactive command: %w", err)
			}
			// Stop Schooner itself so the invoking shell can report and resume
			// the job. SIGSTOP cannot be inherited as ignored.
			if err := syscall.Kill(os.Getpid(), syscall.SIGSTOP); err != nil {
				_ = command.Cancel()
				<-waitDone
				return fmt.Errorf("suspend for stopped interactive command: %w", err)
			}
			for {
				foregroundGroup, foregroundErr := unix.IoctlGetInt(int(terminal.Fd()), unix.TIOCGPGRP)
				if foregroundErr != nil {
					_ = command.Cancel()
					<-waitDone
					return fmt.Errorf("inspect terminal after resuming interactive command: %w", foregroundErr)
				}
				if foregroundGroup == parentGroup {
					break
				}
				// `bg` resumes Schooner without returning terminal ownership. Keep
				// both process groups stopped and recheck after every resume until the
				// invoking shell uses `fg`.
				if err := syscall.Kill(os.Getpid(), syscall.SIGSTOP); err != nil {
					_ = command.Cancel()
					<-waitDone
					return fmt.Errorf("preserve stopped background interactive command: %w", err)
				}
			}
			if err := setForegroundProcessGroup(terminal, command.Process.Pid); err != nil {
				_ = command.Cancel()
				<-waitDone
				return fmt.Errorf("return terminal to stopped interactive command: %w", err)
			}
			if err := continueProcessGroup(command.Process.Pid); err != nil && !errors.Is(err, os.ErrProcessDone) {
				_ = command.Cancel()
				<-waitDone
				return fmt.Errorf("resume stopped interactive command: %w", err)
			}
		}
	}
}

func killProcessGroup(group int) error {
	err := syscall.Kill(-group, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}

func stopProcessGroup(group int) error {
	err := syscall.Kill(-group, syscall.SIGSTOP)
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

func terminateDescendants(root int) error {
	stopped := make(map[int]struct{})
	var result error
	for attempts := 0; attempts < 4; attempts++ {
		descendants, err := descendantProcessIDs(root)
		if err != nil {
			return errors.Join(result, err)
		}
		added := false
		for _, pid := range descendants {
			if _, exists := stopped[pid]; exists {
				continue
			}
			added = true
			stopped[pid] = struct{}{}
			if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil && !errors.Is(err, syscall.ESRCH) {
				result = errors.Join(result, err)
			}
		}
		if !added {
			break
		}
	}
	for pid := range stopped {
		if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			result = errors.Join(result, err)
		}
	}
	return result
}

func descendantProcessIDs(root int) ([]int, error) {
	output, err := exec.Command("/bin/ps", "-axo", "pid=,ppid=").Output()
	if err != nil {
		return nil, fmt.Errorf("inspect interactive descendants: %w", err)
	}
	children := make(map[int][]int)
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		pid, pidErr := strconv.Atoi(fields[0])
		parent, parentErr := strconv.Atoi(fields[1])
		if pidErr == nil && parentErr == nil && pid > 0 && parent >= 0 {
			children[parent] = append(children[parent], pid)
		}
	}
	result := make([]int, 0)
	queue := append([]int(nil), children[root]...)
	for len(queue) != 0 {
		pid := queue[0]
		queue = queue[1:]
		result = append(result, pid)
		queue = append(queue, children[pid]...)
	}
	return result, nil
}

func processIsStopped(pid int) (bool, error) {
	output, err := exec.Command("/bin/ps", "-o", "state=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return false, nil
		}
		return false, fmt.Errorf("inspect interactive command state: %w", err)
	}
	return strings.Contains(strings.TrimSpace(string(output)), "T"), nil
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
