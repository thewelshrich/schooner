// Package host implements Schooner's bounded operations on the current machine.
package host

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"

	"github.com/thewelshrich/schooner/internal/config"
	"github.com/thewelshrich/schooner/internal/repository"
	hostruntime "github.com/thewelshrich/schooner/internal/runtime"
	"github.com/thewelshrich/schooner/internal/session"
)

const commandOutputLimit = 64 << 10

type Runtime struct {
	build           hostruntime.BuildInfo
	operatingSystem string
	architecture    string
	home            func() (string, error)
	readFile        func(string) ([]byte, error)
	stat            func(string) (os.FileInfo, error)
	evalSymlinks    func(string) (string, error)
	lookPath        func(string) (string, error)
	run             func(context.Context, string, ...string) (string, error)
}

func New(build hostruntime.BuildInfo) *Runtime {
	runtime := &Runtime{
		build:           build,
		operatingSystem: goruntime.GOOS,
		architecture:    goruntime.GOARCH,
		home:            currentHome,
		readFile:        os.ReadFile,
		stat:            os.Stat,
		evalSymlinks:    filepath.EvalSymlinks,
		lookPath:        exec.LookPath,
	}
	runtime.run = runtime.runCommand
	return runtime
}

func NewAtHome(build hostruntime.BuildInfo, home string) *Runtime {
	runtime := New(build)
	runtime.home = func() (string, error) {
		if home == "" || !filepath.IsAbs(home) {
			return "", fmt.Errorf("current user home directory is invalid")
		}
		return filepath.Clean(home), nil
	}
	return runtime
}

func (r *Runtime) Hello() (hostruntime.Hello, error) {
	home, err := r.home()
	if err != nil {
		return hostruntime.Hello{}, err
	}
	identity, err := r.identity(home)
	if err != nil {
		return hostruntime.Hello{}, err
	}
	if identity == "" {
		return hostruntime.Hello{}, fmt.Errorf("Schooner box identity is not established")
	}
	result := hostruntime.Hello{
		SchemaVersion:   hostruntime.SchemaVersion,
		ProtocolVersion: hostruntime.ProtocolVersion,
		SchoonerVersion: defaultString(r.build.Version, "dev"),
		Commit:          defaultString(r.build.Commit, "unknown"),
		BoxIdentity:     identity,
		OS:              r.operatingSystem,
		Architecture:    r.architecture,
		Capabilities:    hostruntime.Capabilities(),
	}
	if err := hostruntime.ValidateHello(result, identity, hostruntime.CapabilityHelloV1); err != nil {
		return hostruntime.Hello{}, err
	}
	return result, nil
}

func (r *Runtime) Inspect(ctx context.Context, request hostruntime.InspectRequest) (hostruntime.Inspection, error) {
	if err := ctx.Err(); err != nil {
		return hostruntime.Inspection{}, err
	}
	if err := hostruntime.ValidateInspectRequest(request); err != nil {
		return hostruntime.Inspection{}, err
	}
	home, err := r.home()
	if err != nil {
		return hostruntime.Inspection{}, err
	}
	identity, err := r.identity(home)
	if err != nil {
		return hostruntime.Inspection{}, err
	}
	osID, osVersion, err := r.operatingSystemRelease()
	if err != nil {
		return hostruntime.Inspection{}, err
	}
	worktreeRoot, worktreeRootExists, err := r.worktreeRoot(home, request.WorktreeRoot)
	if err != nil {
		return hostruntime.Inspection{}, err
	}
	configurationPath, err := config.Path()
	if err != nil {
		return hostruntime.Inspection{}, err
	}
	configured, configurationErr := config.Read(configurationPath)
	if configurationErr == nil {
		worktreeRoot, worktreeRootExists, err = r.worktreeRoot(home, configured.WorktreeRoot)
		if err != nil {
			return hostruntime.Inspection{}, err
		}
	} else if !errors.Is(configurationErr, os.ErrNotExist) {
		return hostruntime.Inspection{}, configurationErr
	}
	git, err := r.tool(ctx, "git", "--version")
	if err != nil {
		return hostruntime.Inspection{}, err
	}
	tmux, err := r.tool(ctx, "tmux", "-V")
	if err != nil {
		return hostruntime.Inspection{}, err
	}
	passwordlessSudo, err := r.passwordlessSudo(ctx)
	if err != nil {
		return hostruntime.Inspection{}, err
	}
	if err = ctx.Err(); err != nil {
		return hostruntime.Inspection{}, err
	}
	result := hostruntime.Inspection{
		SchemaVersion:      hostruntime.SchemaVersion,
		ProtocolVersion:    hostruntime.ProtocolVersion,
		OSID:               osID,
		OSVersion:          osVersion,
		Architecture:       r.architecture,
		Home:               home,
		BoxIdentity:        identity,
		WorktreeRoot:       worktreeRoot,
		WorktreeRootExists: worktreeRootExists,
		Git:                git,
		Tmux:               tmux,
		PasswordlessSudo:   passwordlessSudo,
	}
	if err := hostruntime.ValidateInspection(result, ""); err != nil {
		return hostruntime.Inspection{}, err
	}
	return result, nil
}

func (r *Runtime) Doctor(ctx context.Context, request hostruntime.InspectRequest) (hostruntime.DoctorReport, error) {
	inspection, err := r.Inspect(ctx, request)
	if err != nil {
		return hostruntime.DoctorReport{}, err
	}
	checks := []hostruntime.Check{
		check("platform", r.operatingSystem == "linux" && (r.architecture == "amd64" || r.architecture == "arm64"), fmt.Sprintf("Platform is %s/%s.", r.operatingSystem, r.architecture)),
		check("operating_system", inspection.OSID == "ubuntu" && (inspection.OSVersion == "24.04" || inspection.OSVersion == "26.04"), operatingSystemMessage(inspection.OSID, inspection.OSVersion)),
		check("box_identity", inspection.BoxIdentity != "", identityMessage(inspection.BoxIdentity)),
		check("git", inspection.Git.Available, toolMessage("Git", inspection.Git)),
		check("tmux", inspection.Tmux.Available, toolMessage("tmux", inspection.Tmux)),
		check("worktree_root", inspection.WorktreeRootExists, worktreeMessage(inspection.WorktreeRoot, inspection.WorktreeRootExists)),
	}
	healthy := true
	for _, item := range checks {
		healthy = healthy && item.OK
	}
	return hostruntime.DoctorReport{
		SchemaVersion:   hostruntime.SchemaVersion,
		ProtocolVersion: hostruntime.ProtocolVersion,
		Healthy:         healthy,
		Inspection:      inspection,
		Checks:          checks,
	}, nil
}

func (r *Runtime) Configure(request hostruntime.ConfigureRequest) (hostruntime.ConfigureResult, error) {
	if err := hostruntime.ValidateConfigureRequest(request); err != nil {
		return hostruntime.ConfigureResult{}, err
	}
	identity, err := r.operationIdentity(request.BoxIdentity)
	if err != nil {
		return hostruntime.ConfigureResult{}, err
	}
	path, err := config.Path()
	if err != nil {
		return hostruntime.ConfigureResult{}, err
	}
	if err = config.Write(path, config.Host{SchemaVersion: config.SchemaVersion, WorktreeRoot: request.WorktreeRoot}); err != nil {
		return hostruntime.ConfigureResult{}, err
	}
	return hostruntime.ConfigureResult{SchemaVersion: hostruntime.SchemaVersion, ProtocolVersion: hostruntime.ProtocolVersion, BoxIdentity: identity, WorktreeRoot: request.WorktreeRoot}, nil
}

func (r *Runtime) ListWorktrees(ctx context.Context, request hostruntime.WorktreeRequest) (hostruntime.WorktreeCatalog, error) {
	if err := hostruntime.ValidateWorktreeRequest(request, false); err != nil {
		return hostruntime.WorktreeCatalog{}, err
	}
	identity, err := r.operationIdentity(request.BoxIdentity)
	if err != nil {
		return hostruntime.WorktreeCatalog{}, err
	}
	configured, err := config.ReadDefault()
	if err != nil {
		return hostruntime.WorktreeCatalog{}, err
	}
	catalog, err := repository.Discover(ctx, configured.WorktreeRoot)
	if err != nil {
		return hostruntime.WorktreeCatalog{}, err
	}
	return hostruntime.WorktreeCatalog{SchemaVersion: hostruntime.SchemaVersion, ProtocolVersion: hostruntime.ProtocolVersion, BoxIdentity: identity, Catalog: catalog}, nil
}

func (r *Runtime) InspectWorktree(ctx context.Context, request hostruntime.WorktreeRequest) (hostruntime.WorktreeInspection, error) {
	if err := hostruntime.ValidateWorktreeRequest(request, true); err != nil {
		return hostruntime.WorktreeInspection{}, err
	}
	identity, err := r.operationIdentity(request.BoxIdentity)
	if err != nil {
		return hostruntime.WorktreeInspection{}, err
	}
	configured, err := config.ReadDefault()
	if err != nil {
		return hostruntime.WorktreeInspection{}, err
	}
	inspection, err := repository.Inspect(ctx, configured.WorktreeRoot, request.Selector)
	if err != nil {
		return hostruntime.WorktreeInspection{}, err
	}
	return hostruntime.WorktreeInspection{SchemaVersion: hostruntime.SchemaVersion, ProtocolVersion: hostruntime.ProtocolVersion, BoxIdentity: identity, Inspection: inspection}, nil
}

func (r *Runtime) CloneRepository(ctx context.Context, request hostruntime.CloneRequest) (hostruntime.LifecycleResult, error) {
	if err := hostruntime.ValidateCloneRequest(request); err != nil {
		return hostruntime.LifecycleResult{}, err
	}
	lifecycle, identity, err := r.lifecycle(request.BoxIdentity)
	if err != nil {
		return hostruntime.LifecycleResult{}, err
	}
	result, err := lifecycle.Clone(ctx, repository.CloneRequest{Source: request.Source, Branch: request.Branch})
	if err != nil {
		return hostruntime.LifecycleResult{}, err
	}
	return hostruntime.LifecycleResult{SchemaVersion: hostruntime.SchemaVersion, ProtocolVersion: hostruntime.ProtocolVersion, BoxIdentity: identity, MutationResult: result}, nil
}

func (r *Runtime) AddWorktree(ctx context.Context, request hostruntime.WorktreeMutationRequest) (hostruntime.LifecycleResult, error) {
	if err := hostruntime.ValidateWorktreeMutationRequest(request, "add"); err != nil {
		return hostruntime.LifecycleResult{}, err
	}
	lifecycle, identity, err := r.lifecycle(request.BoxIdentity)
	if err != nil {
		return hostruntime.LifecycleResult{}, err
	}
	result, err := lifecycle.Add(ctx, repository.AddRequest{RepositoryPath: request.RepositoryPath, Path: request.Path, Branch: request.Branch})
	if err != nil {
		return hostruntime.LifecycleResult{}, err
	}
	return hostruntime.LifecycleResult{SchemaVersion: hostruntime.SchemaVersion, ProtocolVersion: hostruntime.ProtocolVersion, BoxIdentity: identity, MutationResult: result}, nil
}

func (r *Runtime) RemoveWorktree(ctx context.Context, request hostruntime.WorktreeMutationRequest) (hostruntime.LifecycleResult, error) {
	if err := hostruntime.ValidateWorktreeMutationRequest(request, "remove"); err != nil {
		return hostruntime.LifecycleResult{}, err
	}
	lifecycle, identity, err := r.lifecycle(request.BoxIdentity)
	if err != nil {
		return hostruntime.LifecycleResult{}, err
	}
	result, err := lifecycle.Remove(ctx, request.Path)
	if err != nil {
		return hostruntime.LifecycleResult{}, err
	}
	return hostruntime.LifecycleResult{SchemaVersion: hostruntime.SchemaVersion, ProtocolVersion: hostruntime.ProtocolVersion, BoxIdentity: identity, MutationResult: result}, nil
}

func (r *Runtime) PruneWorktrees(ctx context.Context, request hostruntime.WorktreeMutationRequest) (hostruntime.LifecycleResult, error) {
	if err := hostruntime.ValidateWorktreeMutationRequest(request, "prune"); err != nil {
		return hostruntime.LifecycleResult{}, err
	}
	lifecycle, identity, err := r.lifecycle(request.BoxIdentity)
	if err != nil {
		return hostruntime.LifecycleResult{}, err
	}
	result, err := lifecycle.Prune(ctx)
	if err != nil {
		return hostruntime.LifecycleResult{}, err
	}
	return hostruntime.LifecycleResult{SchemaVersion: hostruntime.SchemaVersion, ProtocolVersion: hostruntime.ProtocolVersion, BoxIdentity: identity, MutationResult: result}, nil
}

func (r *Runtime) lifecycle(expectedIdentity string) (*repository.Lifecycle, string, error) {
	identity, err := r.operationIdentity(expectedIdentity)
	if err != nil {
		return nil, "", err
	}
	configured, err := config.ReadDefault()
	if err != nil {
		return nil, "", err
	}
	home, err := r.home()
	if err != nil {
		return nil, "", err
	}
	stateDirectory, err := repository.OperationStateDirectory(home)
	if err != nil {
		return nil, "", err
	}
	lifecycle, err := repository.NewLifecycle(configured.WorktreeRoot, stateDirectory, session.NewTmuxUse())
	return lifecycle, identity, err
}

func (r *Runtime) operationIdentity(expected string) (string, error) {
	home, err := r.home()
	if err != nil {
		return "", err
	}
	identity, err := r.identity(home)
	if err != nil {
		return "", err
	}
	if identity != expected {
		return "", &hostruntime.Error{Code: hostruntime.CodeInvalidIdentity, Message: "connected machine does not match the requested Box identity"}
	}
	return identity, nil
}

func (r *Runtime) identity(home string) (string, error) {
	contents, err := r.readFile(filepath.Join(home, ".local", "state", "schooner", "identity"))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read Schooner box identity: %w", err)
	}
	identity := strings.TrimSpace(string(contents))
	if identity == "" || strings.Contains(identity, "\n") {
		return "", fmt.Errorf("Schooner box identity is malformed")
	}
	return identity, nil
}

func (r *Runtime) operatingSystemRelease() (string, string, error) {
	if r.operatingSystem != "linux" {
		return r.operatingSystem, "", nil
	}
	contents, err := r.readFile("/etc/os-release")
	if err != nil {
		return "", "", fmt.Errorf("read /etc/os-release: %w", err)
	}
	values := map[string]string{}
	scanner := bufio.NewScanner(bytes.NewReader(contents))
	for scanner.Scan() {
		key, value, ok := strings.Cut(scanner.Text(), "=")
		if !ok || (key != "ID" && key != "VERSION_ID") {
			continue
		}
		value = strings.TrimSpace(value)
		if strings.HasPrefix(value, `"`) {
			decoded, decodeErr := strconv.Unquote(value)
			if decodeErr != nil {
				return "", "", fmt.Errorf("parse /etc/os-release: %w", decodeErr)
			}
			value = decoded
		}
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return "", "", fmt.Errorf("read /etc/os-release: %w", err)
	}
	if values["ID"] == "" || values["VERSION_ID"] == "" {
		return "", "", fmt.Errorf("/etc/os-release does not identify the operating system")
	}
	return values["ID"], values["VERSION_ID"], nil
}

func (r *Runtime) worktreeRoot(home, requested string) (string, bool, error) {
	var target string
	switch {
	case requested == "~":
		target = home
	case strings.HasPrefix(requested, "~/"):
		target = filepath.Join(home, strings.TrimPrefix(requested, "~/"))
	case filepath.IsAbs(requested):
		target = filepath.Clean(requested)
	default:
		return "", false, fmt.Errorf("worktree root must be absolute or begin with ~/")
	}
	info, err := r.stat(target)
	if errors.Is(err, os.ErrNotExist) {
		return target, false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("inspect worktree root: %w", err)
	}
	if !info.IsDir() {
		return target, false, nil
	}
	resolved, err := r.evalSymlinks(target)
	if err != nil {
		return "", false, fmt.Errorf("resolve worktree root: %w", err)
	}
	return resolved, true, nil
}

func (r *Runtime) tool(ctx context.Context, name string, arguments ...string) (hostruntime.Tool, error) {
	if err := ctx.Err(); err != nil {
		return hostruntime.Tool{}, err
	}
	path, err := r.lookPath(name)
	if err != nil {
		if ctx.Err() != nil {
			return hostruntime.Tool{}, ctx.Err()
		}
		return hostruntime.Tool{}, nil
	}
	version, err := r.run(ctx, path, arguments...)
	if err != nil {
		if ctx.Err() != nil {
			return hostruntime.Tool{}, ctx.Err()
		}
		return hostruntime.Tool{}, nil
	}
	return hostruntime.Tool{Available: true, Version: firstLine(version)}, nil
}

func (r *Runtime) passwordlessSudo(ctx context.Context) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	path, err := r.lookPath("sudo")
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, nil
	}
	_, err = r.run(ctx, path, "-n", "true")
	if err != nil && ctx.Err() != nil {
		return false, ctx.Err()
	}
	return err == nil, nil
}

func (r *Runtime) runCommand(ctx context.Context, path string, arguments ...string) (string, error) {
	cmd := exec.CommandContext(ctx, path, arguments...)
	var stdout limitedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", err
	}
	if stdout.overflow {
		return "", fmt.Errorf("command output exceeded %d bytes", commandOutputLimit)
	}
	return stdout.String(), nil
}

func currentHome() (string, error) {
	current, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("resolve current user: %w", err)
	}
	if current.HomeDir == "" || !filepath.IsAbs(current.HomeDir) {
		return "", fmt.Errorf("current user home directory is invalid")
	}
	return filepath.Clean(current.HomeDir), nil
}

func check(id string, ok bool, message string) hostruntime.Check {
	return hostruntime.Check{ID: id, OK: ok, Message: message}
}

func identityMessage(identity string) string {
	if identity == "" {
		return "Schooner box identity is not established."
	}
	return "Schooner box identity is established."
}

func operatingSystemMessage(id, version string) string {
	if version == "" {
		return "Operating system is " + id + "."
	}
	return fmt.Sprintf("Operating system is %s %s.", id, version)
}

func worktreeMessage(worktreeRoot string, exists bool) string {
	if !exists {
		return "Worktree root is missing: " + worktreeRoot + "."
	}
	return "Worktree root is available: " + worktreeRoot + "."
}

func toolMessage(name string, tool hostruntime.Tool) string {
	if !tool.Available {
		return name + " is unavailable."
	}
	return name + " is available: " + tool.Version + "."
}

func firstLine(value string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(value), "\n")
	return line
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

type limitedBuffer struct {
	data     []byte
	overflow bool
}

func (b *limitedBuffer) Write(value []byte) (int, error) {
	written := len(value)
	remaining := commandOutputLimit - len(b.data)
	if remaining <= 0 {
		b.overflow = true
		return written, nil
	}
	if len(value) > remaining {
		b.data = append(b.data, value[:remaining]...)
		b.overflow = true
		return written, nil
	}
	b.data = append(b.data, value...)
	return written, nil
}

func (b *limitedBuffer) String() string { return string(b.data) }
