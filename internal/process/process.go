// Package process provides bounded execution for fixed local tool operations.
package process

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"golang.org/x/term"
)

const commandPipeWaitDelay = time.Second

func Run(ctx context.Context, maximum int, name string, arguments ...string) ([]byte, error) {
	return run(ctx, maximum, nil, name, arguments...)
}

func RunWithoutEnvironment(ctx context.Context, maximum int, excluded []string, name string, arguments ...string) ([]byte, error) {
	blocked := make(map[string]struct{}, len(excluded))
	for _, key := range excluded {
		blocked[key] = struct{}{}
	}
	environment := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		key, _, found := strings.Cut(entry, "=")
		if _, remove := blocked[key]; found && remove {
			continue
		}
		environment = append(environment, entry)
	}
	return run(ctx, maximum, environment, name, arguments...)
}

// Result contains bounded command output. Output beyond the configured limit
// is discarded without interrupting the command, which is important for
// mutations whose external effect must not be coupled to progress volume.
type Result struct {
	Stdout    []byte
	Stderr    []byte
	Truncated bool
}

func RunCapturedWithoutEnvironment(ctx context.Context, maximum int, excluded []string, extra []string, name string, arguments ...string) (Result, error) {
	blocked := make(map[string]struct{}, len(excluded)+len(extra))
	for _, key := range excluded {
		blocked[key] = struct{}{}
	}
	for _, entry := range extra {
		if key, _, found := strings.Cut(entry, "="); found {
			blocked[key] = struct{}{}
		}
	}
	environment := make([]string, 0, len(os.Environ())+len(extra))
	for _, entry := range os.Environ() {
		key, _, found := strings.Cut(entry, "=")
		if _, remove := blocked[key]; found && remove {
			continue
		}
		environment = append(environment, entry)
	}
	environment = append(environment, extra...)
	command := exec.CommandContext(ctx, name, arguments...)
	configureCommandCancellation(command)
	command.WaitDelay = commandPipeWaitDelay
	command.Env = environment
	stdout := &boundedWriter{maximum: maximum}
	stderr := &boundedWriter{maximum: maximum}
	command.Stdout = stdout
	command.Stderr = stderr
	err := command.Run()
	if errors.Is(err, exec.ErrWaitDelay) {
		err = nil
	}
	if ctx.Err() != nil {
		return Result{Stdout: stdout.data, Stderr: stderr.data, Truncated: stdout.truncated || stderr.truncated}, ctx.Err()
	}
	return Result{Stdout: stdout.data, Stderr: stderr.data, Truncated: stdout.truncated || stderr.truncated}, err
}

func run(ctx context.Context, maximum int, environment []string, name string, arguments ...string) ([]byte, error) {
	commandContext, cancel := context.WithCancel(ctx)
	defer cancel()
	command := exec.CommandContext(commandContext, name, arguments...)
	configureCommandCancellation(command)
	command.WaitDelay = commandPipeWaitDelay
	if environment != nil {
		command.Env = environment
	}
	output := &limitedBuffer{maximum: maximum, cancel: cancel}
	command.Stdout = output
	command.Stderr = io.Discard
	err := command.Run()
	if errors.Is(err, exec.ErrWaitDelay) {
		err = nil
	}
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

// RunInteractive attaches the supplied streams directly. A child reading from
// a real terminal must remain in Schooner's foreground process group; non-TTY
// operations retain descendant-aware process-group cancellation.
func RunInteractive(ctx context.Context, directory, name string, arguments []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	return runInteractive(ctx, directory, name, arguments, nil, stdin, stdout, stderr)
}

// RunInteractiveWithoutEnvironment runs an interactive command after removing
// environment variables that would otherwise change the selected external
// resource (for example, tmux's current server socket).
func RunInteractiveWithoutEnvironment(ctx context.Context, directory, name string, arguments, excluded []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	return runInteractive(ctx, directory, name, arguments, excluded, stdin, stdout, stderr)
}

func runInteractive(ctx context.Context, directory, name string, arguments, excluded []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	command := exec.CommandContext(ctx, name, arguments...)
	if !interactiveTerminal(stdin) {
		configureCommandCancellation(command)
	}
	command.Dir = directory
	if len(excluded) > 0 {
		blocked := make(map[string]struct{}, len(excluded))
		for _, key := range excluded {
			blocked[key] = struct{}{}
		}
		for _, entry := range os.Environ() {
			key, _, found := strings.Cut(entry, "=")
			if _, remove := blocked[key]; found && remove {
				continue
			}
			command.Env = append(command.Env, entry)
		}
	}
	command.Stdin, command.Stdout, command.Stderr = stdin, stdout, stderr
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() >= 0 {
			return exitErr.ExitCode(), nil
		}
		return 0, err
	}
	return 0, nil
}

func interactiveTerminal(reader io.Reader) bool {
	file, ok := reader.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}

type limitedBuffer struct {
	maximum  int
	data     []byte
	overflow bool
	cancel   context.CancelFunc
}

type boundedWriter struct {
	maximum   int
	data      []byte
	truncated bool
}

func (buffer *boundedWriter) Write(value []byte) (int, error) {
	written := len(value)
	remaining := buffer.maximum - len(buffer.data)
	if remaining > 0 {
		buffer.data = append(buffer.data, value[:min(len(value), remaining)]...)
	}
	if len(value) > remaining {
		buffer.truncated = true
	}
	return written, nil
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
		buffer.cancel()
	}
	return written, nil
}
