// Package boxgit owns the filesystem and fixed OpenSSH/Git operations for a
// Box Source Identity. Private key material never crosses this module's
// interface.
package boxgit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"

	"github.com/thewelshrich/schooner/internal/process"
	"github.com/thewelshrich/schooner/internal/source"
)

const outputLimit = 64 << 10

var excludedEnvironment = []string{
	"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_ASKPASS", "GIT_COMMON_DIR", "GIT_CONFIG", "GIT_CONFIG_COUNT",
	"GIT_CONFIG_PARAMETERS", "GIT_DIR", "GIT_SSH", "GIT_SSH_COMMAND", "GIT_SSH_VARIANT", "GIT_TERMINAL_PROMPT",
	"GIT_WORK_TREE", "GCM_INTERACTIVE", "SSH_ASKPASS", "SSH_ASKPASS_REQUIRE",
}

type Manager struct {
	home string
	run  commandRunner
}

type commandRunner interface {
	Run(context.Context, []string, string, ...string) (process.Result, error)
}

type osRunner struct{}

func (osRunner) Run(ctx context.Context, environment []string, name string, arguments ...string) (process.Result, error) {
	return process.RunCapturedWithoutEnvironment(ctx, outputLimit, excludedEnvironment, environment, name, arguments...)
}

func New(home string) (*Manager, error) {
	if home == "" || !filepath.IsAbs(home) || filepath.Clean(home) != home {
		return nil, source.NewError("invalid_input", "Box home directory is invalid", nil)
	}
	return &Manager{home: home, run: osRunner{}}, nil
}

func (m *Manager) Inspect(ctx context.Context, provider string) (result source.HostIdentity, err error) {
	defer func() { err = operationError(err) }()
	if err := validateProvider(provider); err != nil {
		return source.HostIdentity{}, err
	}
	if err := ctx.Err(); err != nil {
		return source.HostIdentity{}, err
	}
	paths := m.paths()
	if err = validateDirectoryChain(m.home, paths.root, false); err != nil {
		return source.HostIdentity{}, err
	}
	rootPresent, err := privateDirectory(paths.root)
	if err != nil || !rootPresent {
		return source.HostIdentity{Provider: provider}, err
	}
	directoryPresent, err := privateDirectory(paths.directory)
	if err != nil || !directoryPresent {
		return source.HostIdentity{Provider: provider}, err
	}
	privatePresent, err := regularFile(paths.private, true)
	if err != nil {
		return source.HostIdentity{}, err
	}
	publicPresent, err := regularFile(paths.public, true)
	if err != nil {
		return source.HostIdentity{}, err
	}
	trustPresent, err := regularFile(paths.knownHosts, true)
	if err != nil {
		return source.HostIdentity{}, err
	}
	trustConfigured, hostFingerprints, err := inspectKnownHosts(paths.knownHosts, trustPresent)
	if err != nil {
		return source.HostIdentity{}, err
	}
	if !privatePresent && !publicPresent {
		return source.HostIdentity{Provider: provider, TrustConfigured: trustConfigured, HostFingerprints: hostFingerprints}, nil
	}
	if !privatePresent || !publicPresent {
		return source.HostIdentity{}, source.NewError("outcome_unknown", "the Box GitHub source identity is incomplete", nil)
	}
	if err = requirePrivateFile(paths.private); err != nil {
		return source.HostIdentity{}, err
	}
	publicKey, fingerprint, err := m.validatePair(ctx, paths)
	if err != nil {
		return source.HostIdentity{}, err
	}
	return source.HostIdentity{Provider: provider, Exists: true, PublicKey: publicKey, Fingerprint: fingerprint, TrustConfigured: trustConfigured, HostFingerprints: hostFingerprints}, nil
}

func (m *Manager) Ensure(ctx context.Context, request source.EnsureIdentityRequest) (result source.HostIdentity, err error) {
	defer func() { err = operationError(err) }()
	if err := validateProvider(request.Provider); err != nil {
		return source.HostIdentity{}, err
	}
	if err := source.ValidateHostKeys(request.HostKeys); err != nil {
		return source.HostIdentity{}, &source.Error{Code: "conflict", Message: "GitHub host-key metadata is invalid", Context: map[string]string{"reason": "host_key_changed"}, Cause: err}
	}
	paths := m.paths()
	if err := ensurePrivateDirectory(m.home, paths.root); err != nil {
		return source.HostIdentity{}, err
	}
	lock, err := acquireLock(paths.lock)
	if err != nil {
		return source.HostIdentity{}, err
	}
	defer lock.close()
	if err = ensurePrivateDirectory(m.home, paths.directory); err != nil {
		return source.HostIdentity{}, err
	}
	if err = m.ensurePair(ctx, paths); err != nil {
		return source.HostIdentity{}, err
	}
	if _, err = regularFile(paths.knownHosts, true); err != nil {
		return source.HostIdentity{}, err
	}
	if err = writeKnownHosts(paths.knownHosts, request.HostKeys); err != nil {
		return source.HostIdentity{}, err
	}
	return m.Inspect(ctx, request.Provider)
}

func (m *Manager) Remove(ctx context.Context, request source.RemoveIdentityRequest) (result source.RemoveIdentityResult, err error) {
	defer func() { err = operationError(err) }()
	if err := validateProvider(request.Provider); err != nil {
		return source.RemoveIdentityResult{}, err
	}
	if err := source.ValidateFingerprint(request.ExpectedFingerprint); err != nil {
		return source.RemoveIdentityResult{}, source.NewError("invalid_input", "the expected Box source fingerprint is invalid", err)
	}
	if err := ctx.Err(); err != nil {
		return source.RemoveIdentityResult{}, err
	}
	paths := m.paths()
	if err := ensurePrivateDirectory(m.home, paths.root); err != nil {
		return source.RemoveIdentityResult{}, err
	}
	lock, err := acquireLock(paths.lock)
	if err != nil {
		return source.RemoveIdentityResult{}, err
	}
	defer lock.close()
	directoryPresent, err := privateDirectory(paths.directory)
	if err != nil {
		return source.RemoveIdentityResult{}, err
	}
	if !directoryPresent {
		return source.RemoveIdentityResult{Provider: request.Provider, Removed: true}, nil
	}
	fingerprint, err := m.removalFingerprint(ctx, paths)
	if err != nil {
		return source.RemoveIdentityResult{}, err
	}
	if fingerprint != "" && fingerprint != request.ExpectedFingerprint {
		return source.RemoveIdentityResult{}, source.NewError("conflict", "the Box GitHub key differs from the expected source identity", nil)
	}
	for _, path := range []string{paths.private, paths.public, paths.knownHosts} {
		present, inspectErr := regularFile(path, true)
		if inspectErr != nil {
			return source.RemoveIdentityResult{}, inspectErr
		}
		if present {
			if removeErr := os.Remove(path); removeErr != nil {
				return source.RemoveIdentityResult{}, fmt.Errorf("remove Box source identity: %w", removeErr)
			}
		}
	}
	if err := syncDirectory(paths.directory); err != nil {
		return source.RemoveIdentityResult{}, fmt.Errorf("sync Box source identity removal: %w", err)
	}
	if err := os.Remove(paths.directory); err != nil && !errors.Is(err, os.ErrNotExist) {
		return source.RemoveIdentityResult{}, fmt.Errorf("remove Box source identity directory: %w", err)
	}
	if err := syncDirectory(paths.root); err != nil {
		return source.RemoveIdentityResult{}, fmt.Errorf("sync Box source identity directory removal: %w", err)
	}
	return source.RemoveIdentityResult{Provider: request.Provider, Removed: true}, nil
}

func (m *Manager) removalFingerprint(ctx context.Context, paths identityPaths) (string, error) {
	privatePresent, err := regularFile(paths.private, true)
	if err != nil {
		return "", err
	}
	publicPresent, err := regularFile(paths.public, true)
	if err != nil {
		return "", err
	}
	if !privatePresent && !publicPresent {
		return "", nil
	}
	if privatePresent {
		if err = requirePrivateFile(paths.private); err != nil {
			return "", err
		}
	}
	if privatePresent && publicPresent {
		_, fingerprint, pairErr := m.validatePair(ctx, paths)
		return fingerprint, pairErr
	}
	if privatePresent {
		derived, deriveErr := m.derivePublic(ctx, paths.private)
		if deriveErr != nil {
			return "", deriveErr
		}
		return source.PublicKeyFingerprint(derived)
	}
	contents, err := os.ReadFile(paths.public)
	if err != nil || len(contents) == 0 || len(contents) > 16<<10 {
		return "", source.NewError("outcome_unknown", "the Box GitHub public key is invalid", err)
	}
	return source.PublicKeyFingerprint(strings.TrimSpace(string(contents)))
}

func (m *Manager) Verify(ctx context.Context, request source.VerifyRequest) (verification source.VerifyResult, err error) {
	defer func() { err = operationError(err) }()
	if err := validateProvider(request.Provider); err != nil {
		return source.VerifyResult{}, err
	}
	identity, err := m.Inspect(ctx, request.Provider)
	if err != nil {
		return source.VerifyResult{}, err
	}
	if !identity.Exists {
		return source.VerifyResult{}, &source.Error{Code: "authentication_required", Message: "the Box has no complete GitHub source identity", Context: map[string]string{"reason": "credentials_missing"}}
	}
	if !identity.TrustConfigured {
		return source.VerifyResult{}, &source.Error{Code: "conflict", Message: "managed GitHub SSH host trust is missing", Context: map[string]string{"reason": "host_key_changed"}}
	}
	paths := m.paths()
	sshArgs := []string{
		"-F", "/dev/null", "-i", paths.private, "-o", "IdentitiesOnly=yes", "-o", "BatchMode=yes",
		"-o", "UserKnownHostsFile=" + paths.knownHosts, "-o", "GlobalKnownHostsFile=/dev/null", "-o", "StrictHostKeyChecking=yes",
	}
	if request.Repository == "" {
		result, runErr := m.run.Run(ctx, []string{"LC_ALL=C", "LANG=C"}, "ssh", append(sshArgs, "-T", "git@github.com")...)
		message := strings.ToLower(string(result.Stdout) + "\n" + string(result.Stderr))
		if runErr == nil || (process.ExitCode(runErr) == 1 && strings.Contains(message, "successfully authenticated")) {
			return source.VerifyResult{Provider: request.Provider, Authenticated: true}, nil
		}
		return source.VerifyResult{}, verifyError(result, runErr)
	}
	if !validGitHubSSHRepository(request.Repository) {
		return source.VerifyResult{}, source.NewError("invalid_input", "GitHub repository verification source is invalid", nil)
	}
	command := managedSSHCommand(paths)
	environment := []string{
		"LC_ALL=C", "LANG=C", "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never", "SSH_ASKPASS_REQUIRE=never",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_SSH_COMMAND=" + command, "GIT_SSH_VARIANT=ssh",
	}
	result, runErr := m.run.Run(ctx, environment, "git", "--no-optional-locks", "-c", "core.fsmonitor=false", "ls-remote", "--exit-code", "--", request.Repository, "HEAD")
	if runErr != nil {
		return source.VerifyResult{}, verifyError(result, runErr)
	}
	return source.VerifyResult{Provider: request.Provider, Authenticated: true}, nil
}

type identityPaths struct{ root, directory, private, public, knownHosts, lock string }

func (m *Manager) paths() identityPaths {
	root := filepath.Join(m.home, ".local", "state", "schooner", "source")
	directory := filepath.Join(root, source.GitHubHost)
	private := filepath.Join(directory, "id_ed25519")
	return identityPaths{root: root, directory: directory, private: private, public: private + ".pub", knownHosts: filepath.Join(directory, "known_hosts"), lock: filepath.Join(root, "github.com.lock")}
}

func (m *Manager) ensurePair(ctx context.Context, paths identityPaths) error {
	privatePresent, err := regularFile(paths.private, true)
	if err != nil {
		return err
	}
	publicPresent, err := regularFile(paths.public, true)
	if err != nil {
		return err
	}
	if privatePresent {
		if err = os.Chmod(paths.private, 0o600); err != nil {
			return err
		}
		if !publicPresent {
			derived, deriveErr := m.derivePublic(ctx, paths.private)
			if deriveErr != nil {
				return deriveErr
			}
			if err = writeAtomic(paths.public, []byte(derived+" schooner:github.com\n"), 0o644); err != nil {
				return err
			}
		}
		_, _, err = m.validatePair(ctx, paths)
		return err
	}
	if publicPresent {
		if err = os.Remove(paths.public); err != nil {
			return err
		}
	}
	temporary, err := os.MkdirTemp(paths.directory, ".identity-")
	if err != nil {
		return fmt.Errorf("create Box source identity stage: %w", err)
	}
	defer os.RemoveAll(temporary)
	staged := filepath.Join(temporary, "id_ed25519")
	result, runErr := m.run.Run(ctx, []string{"LC_ALL=C", "LANG=C"}, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "schooner:github.com", "-f", staged)
	if runErr != nil {
		return source.NewError("unsupported", "ssh-keygen could not create the Box GitHub identity", fmt.Errorf("%w: %s", runErr, safeDiagnostic(result.Stderr)))
	}
	if err = os.Chmod(staged, 0o600); err != nil {
		return err
	}
	if err = os.Chmod(staged+".pub", 0o644); err != nil {
		return err
	}
	if err = os.Rename(staged, paths.private); err != nil {
		return fmt.Errorf("promote Box source private key: %w", err)
	}
	if err = syncDirectory(paths.directory); err != nil {
		return fmt.Errorf("sync Box source private key: %w", err)
	}
	if err = os.Rename(staged+".pub", paths.public); err != nil {
		return fmt.Errorf("promote Box source public key: %w", err)
	}
	if err = syncDirectory(paths.directory); err != nil {
		return fmt.Errorf("sync Box source public key: %w", err)
	}
	_, _, err = m.validatePair(ctx, paths)
	return err
}

func (m *Manager) validatePair(ctx context.Context, paths identityPaths) (string, string, error) {
	contents, err := os.ReadFile(paths.public)
	if err != nil {
		return "", "", err
	}
	if len(contents) == 0 || len(contents) > 16<<10 {
		return "", "", source.NewError("outcome_unknown", "the Box GitHub public key is invalid", nil)
	}
	publicKey := strings.TrimSpace(string(contents))
	fingerprint, err := source.PublicKeyFingerprint(publicKey)
	if err != nil {
		return "", "", source.NewError("outcome_unknown", "the Box GitHub public key is invalid", err)
	}
	derived, err := m.derivePublic(ctx, paths.private)
	if err != nil {
		return "", "", err
	}
	if keyBody(publicKey) != keyBody(derived) {
		return "", "", source.NewError("conflict", "the Box GitHub public and private keys do not match", nil)
	}
	return publicKey, fingerprint, nil
}

func (m *Manager) derivePublic(ctx context.Context, privatePath string) (string, error) {
	result, err := m.run.Run(ctx, []string{"LC_ALL=C", "LANG=C"}, "ssh-keygen", "-y", "-f", privatePath)
	if err != nil || result.Truncated {
		return "", source.NewError("outcome_unknown", "the Box GitHub private key could not be verified", err)
	}
	value := strings.TrimSpace(string(result.Stdout))
	if _, err = source.PublicKeyFingerprint(value); err != nil {
		return "", source.NewError("outcome_unknown", "ssh-keygen returned an invalid GitHub public key", err)
	}
	return value, nil
}

func writeKnownHosts(path string, keys []source.HostKey) error {
	var contents strings.Builder
	for _, key := range source.SortedHostKeys(keys) {
		contents.WriteString("github.com ")
		contents.WriteString(strings.Join(strings.Fields(key.Key)[:2], " "))
		contents.WriteByte('\n')
	}
	return writeAtomic(path, []byte(contents.String()), 0o644)
}

func inspectKnownHosts(path string, present bool) (bool, []string, error) {
	if !present {
		return false, nil, nil
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return false, nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return false, nil, err
	}
	if !info.Mode().IsRegular() {
		return false, nil, source.NewError("conflict", "Box source identity contains a non-regular file", nil)
	}
	contents, err := io.ReadAll(io.LimitReader(file, int64(outputLimit)+1))
	if err != nil {
		return false, nil, err
	}
	if len(contents) == 0 || len(contents) > outputLimit || !strings.HasSuffix(string(contents), "\n") {
		return false, nil, nil
	}
	lines := strings.Split(strings.TrimSuffix(string(contents), "\n"), "\n")
	if len(lines) == 0 || len(lines) > 16 {
		return false, nil, nil
	}
	seen := map[string]bool{}
	fingerprints := make([]string, 0, len(lines))
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) != 3 || line != "github.com "+fields[1]+" "+fields[2] {
			return false, nil, nil
		}
		fingerprint, fingerprintErr := source.PublicKeyFingerprint(fields[1] + " " + fields[2])
		if fingerprintErr != nil || seen[fingerprint] {
			return false, nil, nil
		}
		seen[fingerprint] = true
		fingerprints = append(fingerprints, fingerprint)
	}
	slices.Sort(fingerprints)
	return true, fingerprints, nil
}

func writeAtomic(path string, contents []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	file, err := os.CreateTemp(directory, ".write-")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if err = file.Chmod(mode); err == nil {
		_, err = file.Write(contents)
	}
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(temporary, path); err != nil {
		return err
	}
	return syncDirectory(directory)
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	err = directory.Sync()
	if closeErr := directory.Close(); err == nil {
		err = closeErr
	}
	return err
}

func ensurePrivateDirectory(base, path string) error {
	if err := validateDirectoryChain(base, path, true); err != nil {
		return err
	}
	return os.Chmod(path, 0o700)
}

func validateDirectoryChain(base, target string, create bool) error {
	baseInfo, baseErr := os.Lstat(base)
	if baseErr != nil || !baseInfo.IsDir() || baseInfo.Mode()&os.ModeSymlink != 0 {
		return source.NewError("conflict", "Box home directory is not a regular directory", baseErr)
	}
	relative, err := filepath.Rel(base, target)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return source.NewError("conflict", "Box source identity directory is outside the Box home", err)
	}
	current := base
	for _, component := range strings.Split(relative, string(os.PathSeparator)) {
		current = filepath.Join(current, component)
		info, inspectErr := os.Lstat(current)
		if errors.Is(inspectErr, os.ErrNotExist) && !create {
			return nil
		}
		if errors.Is(inspectErr, os.ErrNotExist) {
			if inspectErr = os.Mkdir(current, 0o700); inspectErr != nil && !errors.Is(inspectErr, os.ErrExist) {
				return inspectErr
			}
			info, inspectErr = os.Lstat(current)
		}
		if inspectErr != nil {
			return inspectErr
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return source.NewError("conflict", "Box source identity path contains a non-directory component", nil)
		}
	}
	return nil
}

func privateDirectory(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return false, source.NewError("conflict", "Box source identity directory is not private", nil)
	}
	return true, nil
}

func regularFile(path string, allowMissing bool) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) && allowMissing {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return false, source.NewError("conflict", "Box source identity contains a non-regular file", nil)
	}
	return true, nil
}

func requirePrivateFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return source.NewError("conflict", "Box source private key permissions are unsafe", nil)
	}
	return nil
}

type fileLock struct{ file *os.File }

func acquireLock(path string) (*fileLock, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, source.NewError("operation_in_progress", "another source identity operation is already running on this Box", err)
	}
	return &fileLock{file: file}, nil
}

func (l *fileLock) close() {
	_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	_ = l.file.Close()
}

func managedSSHCommand(paths identityPaths) string {
	arguments := []string{
		"ssh", "-F", "/dev/null", "-i", paths.private, "-o", "IdentitiesOnly=yes", "-o", "BatchMode=yes",
		"-o", "UserKnownHostsFile=" + paths.knownHosts, "-o", "GlobalKnownHostsFile=/dev/null", "-o", "StrictHostKeyChecking=yes",
	}
	quoted := make([]string, len(arguments))
	for index, value := range arguments {
		quoted[index] = shellQuote(value)
	}
	return strings.Join(quoted, " ")
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func validGitHubSSHRepository(value string) bool {
	if !strings.HasPrefix(value, "git@github.com:") || strings.ContainsAny(value, "\x00\r\n?#") {
		return false
	}
	path := strings.TrimSuffix(strings.TrimPrefix(value, "git@github.com:"), ".git")
	parts := strings.Split(path, "/")
	return len(parts) == 2 && parts[0] != "" && parts[1] != "" && !strings.ContainsAny(path, " \\:")
}

func validateProvider(provider string) error {
	if provider != source.GitHub {
		return source.NewError("invalid_input", "only GitHub source identities are supported", nil)
	}
	return nil
}

func keyBody(value string) string {
	fields := strings.Fields(value)
	if len(fields) < 2 {
		return ""
	}
	return fields[0] + " " + fields[1]
}

func verifyError(result process.Result, cause error) error {
	message := strings.ToLower(string(result.Stdout) + "\n" + string(result.Stderr))
	if strings.Contains(message, "saml") || strings.Contains(message, "single sign-on") {
		contextValues := map[string]string{"reason": "github_saml_sso"}
		if organization := githubOrganization(message); organization != "" {
			contextValues["organization"] = organization
		}
		return &source.Error{Code: "authentication_required", Message: "GitHub organization SAML SSO must authorize this Box SSH key", Context: contextValues, Cause: cause}
	}
	if strings.Contains(message, "host key verification failed") || strings.Contains(message, "remote host identification has changed") {
		return &source.Error{Code: "conflict", Message: "GitHub SSH host-key verification failed", Context: map[string]string{"reason": "host_key_changed"}, Cause: cause}
	}
	return source.NewError("authentication_required", "GitHub SSH access verification failed", cause)
}

var githubOrganizationPattern = regexp.MustCompile(`(?:the\s+)?['\"]([a-z0-9](?:[a-z0-9._-]{0,37}[a-z0-9])?)['\"]\s+organization`)

func githubOrganization(message string) string {
	match := githubOrganizationPattern.FindStringSubmatch(strings.ToLower(message))
	if len(match) == 2 {
		return match[1]
	}
	return ""
}

func operationError(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var domain *source.Error
	if errors.As(err, &domain) {
		return err
	}
	return source.NewError("outcome_unknown", "the Box source identity operation could not be completed", err)
}

func safeDiagnostic(value []byte) string {
	result := strings.TrimSpace(string(value))
	result = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, result)
	if len(result) > 256 {
		result = result[:256]
	}
	return result
}
