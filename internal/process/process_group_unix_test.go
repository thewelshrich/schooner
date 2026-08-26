//go:build unix

package process

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunInteractiveNormalExitTerminatesBackgroundProcessGroup(t *testing.T) {
	stdout, err := os.CreateTemp(t.TempDir(), "background-pid")
	if err != nil {
		t.Fatal(err)
	}
	exitCode, err := RunInteractive(t.Context(), "", "/bin/sh", []string{"-c", `nohup sleep 60 >/dev/null 2>&1 & printf '%d' "$!"`}, nil, stdout, io.Discard)
	if err != nil || exitCode != 0 {
		t.Fatalf("exit code = %d, error = %v", exitCode, err)
	}
	if err = stdout.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := os.ReadFile(stdout.Name())
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(output))
	if err != nil {
		t.Fatalf("background pid %q: %v", output, err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		err = syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("background process %d survived normal exit: %v", pid, err)
}

func TestMain(m *testing.M) {
	if os.Getenv("SCHOONER_PROCESS_TREE_CHILD") == "1" {
		if err := syscall.Setpgid(0, 0); err != nil {
			os.Exit(2)
		}
		fmt.Println(os.Getpid())
		for {
			time.Sleep(time.Hour)
		}
	}
	if os.Getenv("SCHOONER_PROCESS_TREE_ROOT") == "1" {
		child := exec.Command(os.Args[0], "-test.run=^$")
		child.Env = append(os.Environ(), "SCHOONER_PROCESS_TREE_ROOT=", "SCHOONER_PROCESS_TREE_CHILD=1")
		child.Stdout = os.Stdout
		if err := child.Run(); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestContinueProcessGroupResumesStoppedChild(t *testing.T) {
	command := exec.Command("/bin/sh", "-c", "trap 'printf resumed; exit 0' CONT; printf ready; while :; do :; done")
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	ready := make([]byte, len("ready"))
	if _, err := io.ReadFull(stdout, ready); err != nil || string(ready) != "ready" {
		t.Fatalf("ready = %q, error = %v", ready, err)
	}
	if err := syscall.Kill(-command.Process.Pid, syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if stopped, err := processIsStopped(command.Process.Pid); err != nil || !stopped {
		t.Fatalf("stopped = %t, error = %v", stopped, err)
	}
	if err := continueProcessGroup(command.Process.Pid); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(stdout)
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatal(err)
	}
	if string(output) != "resumed" {
		t.Fatalf("output = %q", output)
	}
}

func TestCancellationWithoutJobControlKeepsChildInForegroundGroup(t *testing.T) {
	command := exec.CommandContext(t.Context(), "/bin/sh", "-c", "ps -o pgid= -p $$")
	configureCommandCancellationWithoutProcessGroup(command)
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	group, err := strconv.Atoi(strings.TrimSpace(string(output)))
	if err != nil {
		t.Fatalf("child process group %q: %v", output, err)
	}
	if group != syscall.Getpgrp() {
		t.Fatalf("child process group = %d, parent = %d", group, syscall.Getpgrp())
	}
}

func TestTerminateDescendantsFindsBackgroundProcessGroup(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=^$")
	command.Env = append(os.Environ(), "SCHOONER_PROCESS_TREE_ROOT=1", "SCHOONER_PROCESS_TREE_CHILD=")
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = terminateDescendants(command.Process.Pid)
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		_ = command.Wait()
	}()
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	child, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatal(err)
	}
	childGroup, err := syscall.Getpgid(child)
	if err != nil {
		t.Fatal(err)
	}
	if childGroup == command.Process.Pid {
		t.Fatal("background child remained in the shell process group")
	}
	descendants, err := descendantProcessIDs(command.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(descendants, child) {
		t.Fatalf("background child %d missing from descendants %v", child, descendants)
	}
	if err := stopProcessGroup(command.Process.Pid); err != nil {
		t.Fatal(err)
	}
	if err := terminateDescendants(command.Process.Pid); err != nil {
		t.Fatal(err)
	}
}
