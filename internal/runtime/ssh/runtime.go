// Package ssh implements typed remote box operations with the system OpenSSH client.
package ssh

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/thewelshrich/schooner/internal/box"
)

const maxOutput = 1 << 20

var identityPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

//go:embed scripts/*.sh
var scripts embed.FS

type Runtime struct {
	Path         string
	Stderr       io.Writer
	probe        func(context.Context, box.Connection) error
	wait         func(context.Context, time.Duration) error
	readyTimeout time.Duration
}

// TerminalIO is the current terminal attached directly to an interactive
// OpenSSH process.
type TerminalIO struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer
}

// ShellResult reports the native OpenSSH exit status. DiagnosticsReported is
// true when OpenSSH already wrote a connection error to the user's terminal.
type ShellResult struct {
	ExitCode            int
	DiagnosticsReported bool
}

func New(path string, stderr io.Writer) *Runtime { return &Runtime{Path: path, Stderr: stderr} }

// OpenShell hands the current terminal to system OpenSSH. Unlike the bounded
// product operations below, it never accepts or appends a remote command.
func (r *Runtime) OpenShell(ctx context.Context, connection box.Connection, terminal TerminalIO) (ShellResult, error) {
	path, err := r.executable()
	if err != nil {
		return ShellResult{}, err
	}
	args := r.shellOptions(connection)
	args = append(args, connection.Destination)
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdin = terminal.In
	cmd.Stdout = terminal.Out
	var stderr limitedBuffer
	if terminal.Err != nil {
		cmd.Stderr = io.MultiWriter(terminal.Err, &stderr)
	} else {
		cmd.Stderr = &stderr
	}
	if err = cmd.Run(); err == nil {
		return ShellResult{}, nil
	}
	if ctx.Err() != nil {
		return ShellResult{}, ctx.Err()
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return ShellResult{}, err
	}
	code := exitErr.ExitCode()
	if code < 0 {
		return ShellResult{}, box.NewError("connection_failed", "SSH connection terminated without an exit status", err)
	}
	if code != 255 {
		return ShellResult{ExitCode: code}, nil
	}
	return ShellResult{ExitCode: code, DiagnosticsReported: len(stderr.Bytes()) > 0}, classify(err, stderr.String())
}

// WaitReady waits for a newly provisioned machine to accept and authenticate
// an SSH connection. Only transport-level failures are retried; host identity
// and authentication failures remain immediate and fail closed.
func (r *Runtime) WaitReady(ctx context.Context, connection box.Connection) error {
	timeout := r.readyTimeout
	if timeout <= 0 {
		timeout = 3 * time.Minute
	}
	readyContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	delay := time.Second
	var last error
	for {
		probe := r.probeConnection
		if r.probe != nil {
			probe = r.probe
		}
		if err := probe(readyContext, connection); err == nil {
			return nil
		} else if box.ErrorCode(err) != "connection_failed" {
			return err
		} else {
			last = err
		}
		wait := waitContext
		if r.wait != nil {
			wait = r.wait
		}
		if err := wait(readyContext, delay); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return box.NewError("connection_failed", "SSH did not become ready within 3 minutes; retry box add to resume this Droplet", last)
		}
		if delay < 10*time.Second {
			delay *= 2
			if delay > 10*time.Second {
				delay = 10 * time.Second
			}
		}
	}
}

func (r *Runtime) probeConnection(ctx context.Context, connection box.Connection) error {
	path, err := r.executable()
	if err != nil {
		return err
	}
	args := r.options(connection)
	args = append(args, "-o", "ConnectTimeout=5", connection.Destination, "true")
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdout = io.Discard
	var stderr limitedBuffer
	cmd.Stderr = &stderr
	if err = cmd.Run(); err != nil {
		return classify(err, stderr.String())
	}
	return nil
}

func (r *Runtime) Resolve(ctx context.Context, connection box.Connection) error {
	path, err := r.executable()
	if err != nil {
		return err
	}
	args := r.options(connection)
	args = append(args, "-G", connection.Destination)
	cmd := exec.CommandContext(ctx, path, args...)
	var stderr limitedBuffer
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return classify(err, stderr.String())
	}
	return nil
}

func (r *Runtime) Inspect(ctx context.Context, connection box.Connection, workspaceRoot string) (box.Capabilities, error) {
	contents, err := scripts.ReadFile("scripts/inspect.sh")
	if err != nil {
		return box.Capabilities{}, err
	}
	var result box.Capabilities
	if workspaceRoot == "" {
		workspaceRoot = "~"
	}
	if err = r.runJSON(ctx, connection, contents, []string{workspaceRoot}, &result); err != nil {
		return box.Capabilities{}, err
	}
	return result, nil
}

func (r *Runtime) EnsureIdentity(ctx context.Context, connection box.Connection, candidate string) (string, error) {
	if err := validateArgument(candidate); err != nil {
		return "", err
	}
	if !identityPattern.MatchString(candidate) {
		return "", box.NewError("conflict", "remote box identity is malformed", nil)
	}
	probe, err := r.Inspect(ctx, connection, "~")
	if err != nil {
		return "", err
	}
	contents, err := scripts.ReadFile("scripts/identity.sh")
	if err != nil {
		return "", err
	}
	var result struct {
		RemoteIdentity string `json:"remote_identity"`
	}
	if err = r.runJSON(ctx, connection, contents, []string{candidate, probe.Home}, &result); err != nil {
		return "", err
	}
	if result.RemoteIdentity == "" {
		return "", box.NewError("internal", "remote identity operation returned an empty identity", nil)
	}
	return result.RemoteIdentity, nil
}

func (r *Runtime) InstallTools(ctx context.Context, connection box.Connection, tools []string) error {
	contents, err := scripts.ReadFile("scripts/install.sh")
	if err != nil {
		return err
	}
	var result struct {
		Installed bool `json:"installed"`
	}
	return r.runJSON(ctx, connection, contents, tools, &result)
}

func (r *Runtime) EnsureWorkspaceRoot(ctx context.Context, connection box.Connection, requested string) (string, error) {
	probe, err := r.Inspect(ctx, connection, "~")
	if err != nil {
		return "", err
	}
	contents, err := scripts.ReadFile("scripts/workspace_root.sh")
	if err != nil {
		return "", err
	}
	var result struct {
		WorkspaceRoot string `json:"workspace_root"`
	}
	if err = r.runJSON(ctx, connection, contents, []string{requested, probe.Home}, &result); err != nil {
		return "", err
	}
	return result.WorkspaceRoot, nil
}

func (r *Runtime) runJSON(ctx context.Context, connection box.Connection, program []byte, arguments []string, target any) error {
	path, err := r.executable()
	if err != nil {
		return err
	}
	for _, argument := range arguments {
		if err := validateArgument(argument); err != nil {
			return err
		}
	}
	command := "sh -s --"
	for _, argument := range arguments {
		command += " " + shellQuote(argument)
	}
	args := r.options(connection)
	args = append(args, connection.Destination, command)
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdin = bytes.NewReader(program)
	var stdout, stderr limitedBuffer
	cmd.Stdout = &stdout
	if !connection.BatchMode && r.Stderr != nil {
		cmd.Stderr = io.MultiWriter(&stderr, r.Stderr)
	} else {
		cmd.Stderr = &stderr
	}
	if err := cmd.Run(); err != nil {
		return classify(err, stderr.String())
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		return box.NewError("internal", "remote operation returned an invalid result", err)
	}
	var schema string
	if err := json.Unmarshal(envelope["schema_version"], &schema); err != nil || schema != "1" {
		return box.NewError("internal", "remote operation returned an unsupported result schema", err)
	}
	delete(envelope, "schema_version")
	payload, err := json.Marshal(envelope)
	if err != nil {
		return box.NewError("internal", "remote operation returned an invalid result", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return box.NewError("internal", "remote operation returned an invalid result", err)
	}
	return nil
}

func (r *Runtime) options(connection box.Connection) []string {
	args := []string{"-o", "LogLevel=ERROR"}
	if connection.IdentityFile != "" {
		args = append(args, "-i", connection.IdentityFile, "-o", "IdentitiesOnly=yes")
	}
	if connection.BatchMode {
		args = append(args, "-o", "BatchMode=yes")
	}
	if connection.AcceptNewHostKey {
		args = append(args, "-o", "StrictHostKeyChecking=accept-new")
	}
	return args
}

func (r *Runtime) shellOptions(connection box.Connection) []string {
	var args []string
	if connection.IdentityFile != "" {
		args = append(args, "-i", connection.IdentityFile, "-o", "IdentitiesOnly=yes")
	}
	if connection.BatchMode {
		args = append(args, "-o", "BatchMode=yes")
	}
	return args
}

func (r *Runtime) executable() (string, error) {
	if r.Path != "" {
		return r.Path, nil
	}
	path, err := exec.LookPath("ssh")
	if err != nil {
		return "", box.NewError("unsupported", "system OpenSSH client was not found in PATH", err)
	}
	return path, nil
}

func classify(err error, diagnostics string) error {
	message := strings.ToLower(diagnostics)
	switch {
	case strings.Contains(message, "remote host identification has changed"):
		return box.NewError("host_identity_changed", "SSH host identity changed; inspect and repair known_hosts before retrying", err)
	case strings.Contains(message, "host key verification failed"):
		return box.NewError("permission_denied", "SSH host key verification failed", err)
	case strings.Contains(message, "permission denied"):
		return box.NewError("authentication_required", "SSH authentication failed", err)
	case errors.Is(err, context.Canceled):
		return err
	default:
		return box.NewError("connection_failed", "SSH operation failed", err)
	}
}

func validateArgument(value string) error {
	if strings.ContainsRune(value, 0) || strings.ContainsAny(value, "\r\n") {
		return box.NewError("invalid_input", "remote operation argument contains unsupported control characters", nil)
	}
	return nil
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }

func waitContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type limitedBuffer struct{ data []byte }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	written := len(p)
	remaining := maxOutput - len(b.data)
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		b.data = append(b.data, p...)
	}
	return written, nil
}
func (b *limitedBuffer) Bytes() []byte  { return b.data }
func (b *limitedBuffer) String() string { return string(b.data) }
