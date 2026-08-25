// Package process provides bounded execution for fixed local tool operations.
package process

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
)

func Run(ctx context.Context, maximum int, name string, arguments ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, arguments...)
	output := &limitedBuffer{maximum: maximum}
	command.Stdout = output
	command.Stderr = io.Discard
	err := command.Run()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if output.overflow {
		return nil, fmt.Errorf("command output exceeded %d bytes", maximum)
	}
	return output.data, err
}

func ExitCode(err error) int {
	var target *exec.ExitError
	if errors.As(err, &target) {
		return target.ExitCode()
	}
	return -1
}

type limitedBuffer struct {
	maximum  int
	data     []byte
	overflow bool
}

func (buffer *limitedBuffer) Write(value []byte) (int, error) {
	written := len(value)
	remaining := buffer.maximum - len(buffer.data)
	if remaining > 0 {
		if len(value) > remaining {
			buffer.data = append(buffer.data, value[:remaining]...)
		} else {
			buffer.data = append(buffer.data, value...)
		}
	}
	if len(value) > remaining {
		buffer.overflow = true
	}
	return written, nil
}
