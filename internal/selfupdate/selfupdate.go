// Package selfupdate owns stable-release discovery, installation ownership,
// candidate verification, locking, atomic promotion, receipts, and the bounded
// automatic-check cache for the local Schooner executable. Updater.Run is the
// module interface; callers select check, apply, or automatic behavior and
// present the returned result without reproducing these invariants.
package selfupdate

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/thewelshrich/schooner/internal/artifact"
	"github.com/thewelshrich/schooner/internal/semver"
)

const (
	SchemaVersion = "1"

	ActionUpToDate          = "up_to_date"
	ActionUpdateAvailable   = "update_available"
	ActionUpdated           = "updated"
	ActionUsePackageManager = "use_package_manager"
	ActionReinstallSource   = "reinstall_source"
	ActionRefused           = "refused"
	ActionUpdatedUnowned    = "updated_unowned"

	MethodDirect   = "direct"
	MethodHomebrew = "homebrew"
	MethodSource   = "source"
	MethodUnknown  = "unknown"

	defaultAPIURL       = "https://api.github.com/repos/thewelshrich/schooner/releases/latest"
	cacheFileName       = "update-check.json"
	receiptFileName     = ".schooner-install-receipt.json"
	lockDirectoryName   = ".schooner-install.lock"
	maxReleaseResponse  = 1 << 20
	maxCandidateBytes   = 256 << 20
	automaticCheckAge   = 24 * time.Hour
	candidateCheckLimit = 64 << 10
)

type Mode string

const (
	ModeCheck     Mode = "check"
	ModeApply     Mode = "apply"
	ModeAutomatic Mode = "automatic"
)

type Current struct {
	Version        string
	OS             string
	Arch           string
	ExecutablePath string
	InvocationPath string
}

type Result struct {
	InstalledVersion   string `json:"installed_version"`
	AvailableVersion   string `json:"available_version"`
	InstallationMethod string `json:"installation_method"`
	Action             string `json:"action"`
	Guidance           string `json:"guidance"`
}

type Code string

const (
	CodeInvalidCurrent     Code = "invalid_current_version"
	CodeReleaseUnavailable Code = "release_unavailable"
	CodeInvalidRelease     Code = "invalid_release"
	CodeUnsupported        Code = "unsupported_platform"
	CodeOwnershipRefused   Code = "ownership_refused"
	CodeLocked             Code = "update_locked"
	CodeVerification       Code = "verification_failed"
	CodePromotion          Code = "promotion_failed"
	CodeReceipt            Code = "receipt_failed"
)

type Error struct {
	Code    Code
	Message string
	Context map[string]string
	Cause   error
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.Cause }

func ErrorCode(err error) Code {
	var target *Error
	if errors.As(err, &target) {
		return target.Code
	}
	return ""
}

func ErrorContext(err error) map[string]string {
	var target *Error
	if errors.As(err, &target) {
		return target.Context
	}
	return nil
}

type artifactResolver interface {
	Resolve(context.Context, string, artifact.Platform) (artifact.Result, error)
}

type updater struct {
	current         Current
	executablePath  string
	invokedSymlink  bool
	cachePath       string
	apiURL          string
	client          *http.Client
	artifacts       artifactResolver
	now             func() time.Time
	hostname        func() (string, error)
	processAlive    func(int) (bool, error)
	verifySignature func(context.Context, string) error
	publishReceipt  func(string, installationReceipt) error
	maxCandidate    int64
}

type Updater struct{ implementation *updater }

func NewDefault(current Current) (*Updater, error) {
	if current.OS == "" {
		current.OS = runtime.GOOS
	}
	if current.Arch == "" {
		current.Arch = runtime.GOARCH
	}
	executable := current.ExecutablePath
	if executable == "" {
		var err error
		executable, err = os.Executable()
		if err != nil {
			return nil, fmt.Errorf("resolve the running executable: %w", err)
		}
	}
	executable, _, err := canonicalExecutable(executable)
	if err != nil {
		return nil, err
	}
	invokedSymlink := false
	if current.InvocationPath != "" {
		invokedExecutable, invocationWasSymlink, invocationErr := canonicalExecutable(current.InvocationPath)
		invokedSymlink = invocationErr != nil || invokedExecutable != executable || invocationWasSymlink
	}
	cachePath := ""
	if cacheRoot, cacheErr := os.UserCacheDir(); cacheErr == nil {
		cachePath = filepath.Join(cacheRoot, "schooner", cacheFileName)
	}
	return &Updater{implementation: &updater{
		current: current, executablePath: executable, invokedSymlink: invokedSymlink,
		cachePath: cachePath,
		apiURL:    defaultAPIURL, client: &http.Client{Timeout: 30 * time.Second},
		artifacts: artifact.NewDeferredDefault(), now: time.Now, hostname: os.Hostname,
		processAlive: processIsAlive, verifySignature: verifyPlatformSignature,
		publishReceipt: writeReceipt,
		maxCandidate:   maxCandidateBytes,
	}}, nil
}

func (u *Updater) Run(ctx context.Context, mode Mode) (Result, error) {
	if u == nil || u.implementation == nil {
		return Result{}, &Error{Code: CodeInvalidCurrent, Message: "local updater is unavailable"}
	}
	return u.implementation.run(ctx, mode)
}

func (u *updater) run(ctx context.Context, mode Mode) (Result, error) {
	if mode != ModeCheck && mode != ModeApply && mode != ModeAutomatic {
		return Result{}, &Error{Code: CodeInvalidCurrent, Message: fmt.Sprintf("unsupported update mode %q", mode)}
	}
	if err := validatePlatform(u.current.OS, u.current.Arch); err != nil {
		return Result{}, err
	}
	if u.current.Version == "" || u.current.Version == "dev" {
		if ownership := classifyInstallation(u.current.Version, u.executablePath, u.invokedSymlink); ownership.method == MethodHomebrew {
			return Result{InstalledVersion: u.current.Version, InstallationMethod: MethodHomebrew, Action: ActionUsePackageManager, Guidance: homebrewGuidance()}, nil
		}
		return Result{InstalledVersion: u.current.Version, InstallationMethod: MethodSource, Action: ActionReinstallSource, Guidance: "Rebuild or reinstall Schooner from its source checkout."}, nil
	}
	if !semver.Valid(u.current.Version) {
		return Result{}, &Error{Code: CodeInvalidCurrent, Message: fmt.Sprintf("installed version %q is not a released semantic version", u.current.Version)}
	}
	ownership := ownership{}
	if mode != ModeAutomatic {
		ownership = classifyInstallation(u.current.Version, u.executablePath, u.invokedSymlink)
	}

	available, found, err := u.latest(ctx, mode)
	if err != nil {
		return Result{}, err
	}
	if !found {
		return Result{}, nil
	}
	comparison, ok := semver.Compare(available, u.current.Version)
	if !ok {
		return Result{}, &Error{Code: CodeInvalidRelease, Message: "compare installed and available Schooner versions"}
	}
	if comparison <= 0 {
		if mode == ModeAutomatic {
			return Result{}, nil
		}
		result := Result{InstalledVersion: u.current.Version, AvailableVersion: available, InstallationMethod: ownership.method}
		result.Action = ActionUpToDate
		result.Guidance = "No local update is needed."
		return result, nil
	}
	if mode == ModeAutomatic {
		ownership = classifyInstallation(u.current.Version, u.executablePath, u.invokedSymlink)
	}
	result := Result{InstalledVersion: u.current.Version, AvailableVersion: available, InstallationMethod: ownership.method}
	if mode != ModeApply {
		result.Action = ActionUpdateAvailable
		result.Guidance = updateGuidance(ownership.method)
		return result, nil
	}

	switch ownership.method {
	case MethodHomebrew:
		result.Action = ActionUsePackageManager
		result.Guidance = homebrewGuidance()
		return result, nil
	case MethodUnknown:
		return Result{}, ownershipError(result, ownership.reason)
	case MethodDirect:
		return u.apply(ctx, result)
	default:
		return Result{}, ownershipError(result, "installation ownership is unsupported")
	}
}

func (u *updater) apply(ctx context.Context, result Result) (Result, error) {
	resolved, err := u.artifacts.Resolve(ctx, result.AvailableVersion, artifact.Platform{OS: u.current.OS, Arch: u.current.Arch})
	if err != nil {
		return Result{}, &Error{Code: CodeVerification, Message: "download and verify the selected Schooner release", Cause: err}
	}
	defer resolved.Release()

	lock, err := u.acquireLock()
	if err != nil {
		return Result{}, err
	}
	defer lock.release()

	ownership := classifyInstallation(u.current.Version, u.executablePath, u.invokedSymlink)
	if ownership.method != MethodDirect {
		result.InstallationMethod = ownership.method
		return Result{}, ownershipError(result, "installation ownership changed before the update")
	}
	receiptPath := filepath.Join(filepath.Dir(u.executablePath), receiptFileName)
	baselineTarget, err := fileFingerprint(u.executablePath)
	if err != nil {
		return Result{}, &Error{Code: CodeVerification, Message: "fingerprint the installed executable", Cause: err}
	}
	baselineReceipt, err := fileFingerprint(receiptPath)
	if err != nil {
		return Result{}, &Error{Code: CodeVerification, Message: "fingerprint the installation receipt", Cause: err}
	}

	candidate, err := u.stageCandidate(resolved)
	if err != nil {
		return Result{}, err
	}
	defer os.Remove(candidate)
	if u.current.OS == "darwin" {
		if err = u.verifySignature(ctx, candidate); err != nil {
			return Result{}, &Error{Code: CodeVerification, Message: "candidate has an invalid Schooner Developer ID signature", Cause: err}
		}
	}
	if err = verifyCandidateIdentity(ctx, candidate, result.AvailableVersion, u.current.OS, u.current.Arch); err != nil {
		return Result{}, &Error{Code: CodeVerification, Message: "candidate build identity is invalid", Cause: err}
	}
	if err = revalidateFingerprint(u.executablePath, baselineTarget); err != nil {
		return Result{}, err
	}
	if err = revalidateFingerprint(receiptPath, baselineReceipt); err != nil {
		return Result{}, err
	}
	if err = os.Rename(candidate, u.executablePath); err != nil {
		return Result{}, &Error{Code: CodePromotion, Message: "atomically replace the installed Schooner executable", Cause: err}
	}

	result.Action = ActionUpdated
	result.InstalledVersion = result.AvailableVersion
	result.Guidance = "Schooner was updated successfully."
	receipt := installationReceipt{
		SchemaVersion: SchemaVersion, InstallationMethod: MethodDirect,
		ExecutablePath: u.executablePath, Version: result.AvailableVersion,
		ExecutableSHA256: resolved.SHA256, ReleaseAssetKind: "raw",
		ReleaseAssetName: filepath.Base(resolved.Path), ReleaseAssetSHA256: resolved.SHA256,
		InstalledAt: u.now().UTC().Truncate(time.Second).Format(time.RFC3339),
	}
	if err = u.publishReceipt(receiptPath, receipt); err != nil {
		contextValues := resultContext(result)
		contextValues["action"] = ActionUpdatedUnowned
		contextValues["installation_method"] = MethodUnknown
		contextValues["guidance"] = repairGuidance(result.AvailableVersion, filepath.Dir(u.executablePath))
		return Result{}, &Error{Code: CodeReceipt, Message: "Schooner was updated, but its ownership receipt could not be written", Context: contextValues, Cause: err}
	}
	return result, nil
}

func (u *updater) stageCandidate(resolved artifact.Result) (string, error) {
	source, err := os.Open(resolved.Path)
	if err != nil {
		return "", &Error{Code: CodeVerification, Message: "open the verified release executable", Cause: err}
	}
	defer source.Close()
	temporary, err := os.CreateTemp(filepath.Dir(u.executablePath), ".schooner.tmp-*")
	if err != nil {
		return "", &Error{Code: CodePromotion, Message: "create the update candidate beside the installed executable", Cause: err}
	}
	path := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(path)
		}
	}()
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(temporary, hash), io.LimitReader(source, u.maxCandidate+1))
	if err != nil {
		return "", &Error{Code: CodeVerification, Message: "copy the verified release executable", Cause: err}
	}
	if written == 0 || written > u.maxCandidate {
		return "", &Error{Code: CodeVerification, Message: "release executable is empty or exceeds 256 MiB"}
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if actual != resolved.SHA256 {
		return "", &Error{Code: CodeVerification, Message: "staged executable checksum does not match the release manifest"}
	}
	if err = temporary.Chmod(0o755); err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", &Error{Code: CodePromotion, Message: "finish the update candidate", Cause: err}
	}
	keep = true
	return path, nil
}

func canonicalExecutable(path string) (string, bool, error) {
	if strings.ContainsAny(path, "\x00\r\n") {
		return "", false, errors.New("running executable path contains unsafe characters")
	}
	if !strings.ContainsRune(path, filepath.Separator) {
		resolved, err := exec.LookPath(path)
		if err != nil {
			return "", false, fmt.Errorf("locate the invoked executable: %w", err)
		}
		path = resolved
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", false, fmt.Errorf("resolve the running executable path: %w", err)
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return "", false, fmt.Errorf("inspect the invoked executable path: %w", err)
	}
	invokedSymlink := info.Mode()&os.ModeSymlink != 0
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", false, fmt.Errorf("resolve the running executable path: %w", err)
	}
	info, err = os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return "", false, fmt.Errorf("running executable is not a regular file: %s", resolved)
	}
	return filepath.Clean(resolved), invokedSymlink, nil
}

func validatePlatform(osName, arch string) error {
	if (osName != "darwin" && osName != "linux") || (arch != "amd64" && arch != "arm64") {
		return &Error{Code: CodeUnsupported, Message: fmt.Sprintf("platform %s/%s is not supported for local updates", osName, arch)}
	}
	return nil
}

func updateGuidance(method string) string {
	switch method {
	case MethodHomebrew:
		return homebrewGuidance()
	case MethodDirect:
		return "Run `schooner update` to install it."
	case MethodSource:
		return "Rebuild or reinstall Schooner from its source checkout."
	default:
		return "Run `schooner update` for installation ownership guidance."
	}
}

func homebrewGuidance() string {
	return "Run `brew update && brew upgrade thewelshrich/tap/schooner`."
}

func repairGuidance(version, directory string) string {
	return fmt.Sprintf("Rerun the verified installer for %s with --install-dir %q to repair ownership.", version, directory)
}

func ownershipError(result Result, reason string) error {
	result.Action = ActionRefused
	result.Guidance = "Reinstall Schooner with Homebrew or the verified direct installer before requesting automatic replacement."
	message := "refusing to replace an installation that Schooner does not own"
	if strings.TrimSpace(reason) != "" {
		message += ": " + reason
	}
	return &Error{Code: CodeOwnershipRefused, Message: message, Context: resultContext(result)}
}

func resultContext(result Result) map[string]string {
	return map[string]string{
		"installed_version": result.InstalledVersion, "available_version": result.AvailableVersion,
		"installation_method": result.InstallationMethod, "action": result.Action, "guidance": result.Guidance,
	}
}

func randomToken() (string, error) {
	value := make([]byte, 18)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}
