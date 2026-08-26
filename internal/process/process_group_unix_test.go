//go:build unix

package process

import (
	"bufio"
	"io"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

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

func TestTerminateDescendantsFindsBackgroundProcessGroup(t *testing.T) {
	command := exec.Command("/bin/sh", "-c", "set -m; sleep 10 & printf '%d\\n' $!; wait")
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
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
