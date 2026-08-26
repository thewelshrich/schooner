//go:build unix

package process

import (
	"io"
	"os/exec"
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
