// Package artifact resolves verified Schooner executables for supported platforms.
package artifact

import (
	"bufio"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/thewelshrich/schooner/internal/semver"
)

const (
	defaultBaseURL              = "https://github.com/thewelshrich/schooner/releases/download"
	manifestName                = "SHA256SUMS"
	developmentCurrentName      = ".current"
	developmentGenerationPrefix = ".generation-"
	maxManifest                 = 1 << 20
)

type Code string

const (
	CodeInvalidVersion      Code = "invalid_version"
	CodeUnsupportedPlatform Code = "unsupported_platform"
	CodeUnavailable         Code = "artifact_unavailable"
	CodeInvalidManifest     Code = "invalid_manifest"
	CodeChecksumMismatch    Code = "checksum_mismatch"
	CodeCacheFailure        Code = "cache_failure"
)

type Error struct {
	Code    Code
	Message string
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

type Platform struct {
	OS   string
	Arch string
}

type Result struct {
	Path     string
	Version  string
	Platform Platform
	SHA256   string
	lease    *developmentLease
}

// Release relinquishes any lease protecting a resolved artifact generation.
// Callers should release a result after they finish reading its Path.
func (r *Result) Release() error {
	if r == nil || r.lease == nil {
		return nil
	}
	lease := r.lease
	r.lease = nil
	return lease.Release()
}

type Config struct {
	CacheDir       string
	OverrideDir    string
	DevelopmentDir string
	BaseURL        string
	HTTPClient     *http.Client
}

type Resolver struct {
	cacheDir       string
	overrideDir    string
	developmentDir string
	baseURL        string
	client         *http.Client
}

// DeferredResolver postpones cache discovery until a host artifact is
// actually requested. Most CLI commands never need the artifact subsystem.
type DeferredResolver struct {
	config        Config
	defaultConfig bool
	once          sync.Once
	resolver      *Resolver
	err           error
}

func New(config Config) (*Resolver, error) {
	if config.CacheDir == "" && config.OverrideDir == "" {
		root, err := os.UserCacheDir()
		if err != nil {
			return nil, cacheError("resolve the local cache directory", err)
		}
		config.CacheDir = filepath.Join(root, "schooner", "artifacts")
	}
	if config.BaseURL == "" {
		config.BaseURL = defaultBaseURL
	}
	parsed, err := url.Parse(config.BaseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, &Error{Code: CodeUnavailable, Message: "artifact base URL is invalid", Cause: err}
	}
	if config.HTTPClient == nil {
		config.HTTPClient = &http.Client{Timeout: 2 * time.Minute}
	}
	return &Resolver{
		cacheDir:       config.CacheDir,
		overrideDir:    config.OverrideDir,
		developmentDir: config.DevelopmentDir,
		baseURL:        strings.TrimRight(config.BaseURL, "/"),
		client:         config.HTTPClient,
	}, nil
}

func NewDefault() (*Resolver, error) {
	config, err := defaultConfig()
	if err != nil {
		return nil, err
	}
	return New(config)
}

func NewDeferred(config Config) *DeferredResolver {
	return &DeferredResolver{config: config}
}

func NewDeferredDefault() *DeferredResolver {
	return &DeferredResolver{defaultConfig: true}
}

func (r *DeferredResolver) Resolve(ctx context.Context, version string, platform Platform) (Result, error) {
	r.once.Do(func() {
		config := r.config
		if r.defaultConfig {
			config, r.err = defaultConfig()
			if r.err != nil {
				return
			}
		}
		r.resolver, r.err = New(config)
	})
	if r.err != nil {
		return Result{}, r.err
	}
	return r.resolver.Resolve(ctx, version, platform)
}

func Name(version string, platform Platform) (string, error) {
	if !validVersion(version) {
		return "", &Error{Code: CodeInvalidVersion, Message: fmt.Sprintf("artifact version %q must be a v-prefixed semantic version", version)}
	}
	if err := validatePlatform(platform); err != nil {
		return "", err
	}
	return fileName(version, platform), nil
}

func (r *Resolver) Resolve(ctx context.Context, version string, platform Platform) (Result, error) {
	if err := validatePlatform(platform); err != nil {
		return Result{}, err
	}
	if version == "dev" {
		directory := r.overrideDir
		if directory == "" {
			directory = r.developmentDir
		}
		if directory == "" {
			return Result{}, &Error{Code: CodeInvalidVersion, Message: "development builds require SCHOONER_ARTIFACT_DIR"}
		}
		for attempt := 0; attempt < 3; attempt++ {
			activeDirectory, published, err := activeDevelopmentDirectory(directory)
			if err != nil {
				return Result{}, err
			}
			if !published {
				if r.overrideDir == "" {
					if _, err = os.Stat(filepath.Join(activeDirectory, manifestName)); errors.Is(err, os.ErrNotExist) {
						return Result{}, &Error{Code: CodeUnavailable, Message: "development host runtimes are unavailable; run `schooner dev artifacts` from the Schooner source directory"}
					} else if err != nil {
						return Result{}, &Error{Code: CodeInvalidManifest, Message: fmt.Sprintf("inspect checksum manifest %s", filepath.Join(activeDirectory, manifestName)), Cause: err}
					}
				}
				name := fileName(version, platform)
				return resolveDirectory(activeDirectory, version, platform, name)
			}

			lease, lockErr := acquireDevelopmentGenerationLease(ctx, activeDirectory)
			if errors.Is(lockErr, os.ErrNotExist) {
				continue
			}
			if lockErr != nil {
				if ctx.Err() != nil {
					return Result{}, ctx.Err()
				}
				return Result{}, &Error{Code: CodeCacheFailure, Message: "protect the active development artifact generation", Cause: lockErr}
			}
			confirmedDirectory, stillPublished, confirmErr := activeDevelopmentDirectory(directory)
			if confirmErr != nil {
				_ = lease.Release()
				return Result{}, confirmErr
			}
			if !stillPublished || confirmedDirectory != activeDirectory {
				_ = lease.Release()
				continue
			}
			name := fileName(version, platform)
			result, resolveErr := resolveDirectory(activeDirectory, version, platform, name)
			if resolveErr != nil {
				_ = lease.Release()
				return Result{}, resolveErr
			}
			result.lease = lease
			return result, nil
		}
		return Result{}, &Error{Code: CodeUnavailable, Message: "development artifact generation changed repeatedly; retry the operation"}
	} else if !validVersion(version) {
		return Result{}, &Error{Code: CodeInvalidVersion, Message: fmt.Sprintf("artifact version %q must be a v-prefixed semantic version", version)}
	}

	name := fileName(version, platform)
	if r.overrideDir != "" {
		return resolveDirectory(r.overrideDir, version, platform, name)
	}
	if result, ok := r.cached(version, platform, name); ok {
		return result, nil
	}

	digest, err := r.fetchManifest(ctx, version, name)
	if err != nil {
		return Result{}, err
	}
	return r.download(ctx, version, platform, name, digest)
}

func defaultConfig() (Config, error) {
	override := os.Getenv("SCHOONER_ARTIFACT_DIR")
	if override != "" {
		return Config{OverrideDir: override}, nil
	}
	directory, err := DefaultDevelopmentDirectory()
	if err != nil {
		return Config{}, err
	}
	return Config{DevelopmentDir: directory}, nil
}

func DefaultDevelopmentDirectory() (string, error) {
	root, err := os.UserCacheDir()
	if err != nil {
		return "", cacheError("resolve the local cache directory", err)
	}
	return filepath.Join(root, "schooner", "artifacts", "dev"), nil
}

func effectiveDevelopmentDirectory() (string, error) {
	if override := os.Getenv("SCHOONER_ARTIFACT_DIR"); override != "" {
		return override, nil
	}
	return DefaultDevelopmentDirectory()
}

func activeDevelopmentDirectory(directory string) (string, bool, error) {
	pointer := filepath.Join(directory, developmentCurrentName)
	contents, err := os.ReadFile(pointer)
	if errors.Is(err, os.ErrNotExist) {
		return directory, false, nil
	}
	if err != nil {
		return "", false, &Error{Code: CodeInvalidManifest, Message: fmt.Sprintf("read development artifact pointer %s", pointer), Cause: err}
	}
	generation := strings.TrimSpace(string(contents))
	if generation == "" || filepath.Base(generation) != generation || !strings.HasPrefix(generation, developmentGenerationPrefix) {
		return "", false, &Error{Code: CodeInvalidManifest, Message: "development artifact pointer is invalid"}
	}
	return filepath.Join(directory, generation), true, nil
}

func validatePlatform(platform Platform) error {
	supported := (platform.OS == "darwin" || platform.OS == "linux") && (platform.Arch == "amd64" || platform.Arch == "arm64")
	if !supported {
		return &Error{Code: CodeUnsupportedPlatform, Message: fmt.Sprintf("platform %s/%s is not supported", platform.OS, platform.Arch)}
	}
	return nil
}

func fileName(version string, platform Platform) string {
	return fmt.Sprintf("schooner_%s_%s_%s", version, platform.OS, platform.Arch)
}

func validVersion(version string) bool {
	return semver.Valid(version)
}

func resolveDirectory(directory, version string, platform Platform, name string) (Result, error) {
	digest, err := readManifest(filepath.Join(directory, manifestName), name)
	if err != nil {
		return Result{}, err
	}
	path := filepath.Join(directory, name)
	if err = verifyFile(path, digest); err != nil {
		return Result{}, err
	}
	return Result{Path: path, Version: version, Platform: platform, SHA256: digest}, nil
}

func (r *Resolver) cached(version string, platform Platform, name string) (Result, bool) {
	directory := filepath.Join(r.cacheDir, version)
	digestBytes, err := os.ReadFile(filepath.Join(directory, name+".sha256"))
	if err != nil {
		return Result{}, false
	}
	digest := strings.TrimSpace(string(digestBytes))
	if !validDigest(digest) || verifyFile(filepath.Join(directory, name), digest) != nil {
		return Result{}, false
	}
	return Result{Path: filepath.Join(directory, name), Version: version, Platform: platform, SHA256: digest}, true
}

func (r *Resolver) fetchManifest(ctx context.Context, version, name string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, r.assetURL(version, manifestName), nil)
	if err != nil {
		return "", unavailable("create the checksum-manifest request", err)
	}
	response, err := r.client.Do(request)
	if err != nil {
		return "", unavailable("download the checksum manifest", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", &Error{Code: CodeUnavailable, Message: fmt.Sprintf("checksum manifest is unavailable (HTTP %d)", response.StatusCode)}
	}
	contents, err := io.ReadAll(io.LimitReader(response.Body, maxManifest+1))
	if err != nil {
		return "", unavailable("read the checksum manifest", err)
	}
	if len(contents) > maxManifest {
		return "", &Error{Code: CodeInvalidManifest, Message: "checksum manifest exceeds 1 MiB"}
	}
	return parseManifest(strings.NewReader(string(contents)), name)
}

func (r *Resolver) download(ctx context.Context, version string, platform Platform, name, expected string) (Result, error) {
	directory := filepath.Join(r.cacheDir, version)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return Result{}, cacheError("create the artifact cache directory", err)
	}
	temporary, err := os.CreateTemp(directory, name+".tmp-")
	if err != nil {
		return Result{}, cacheError("create a temporary artifact file", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, r.assetURL(version, name), nil)
	if err != nil {
		temporary.Close()
		return Result{}, unavailable("create the artifact request", err)
	}
	response, err := r.client.Do(request)
	if err != nil {
		temporary.Close()
		return Result{}, unavailable("download the artifact", err)
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		temporary.Close()
		return Result{}, &Error{Code: CodeUnavailable, Message: fmt.Sprintf("artifact is unavailable (HTTP %d)", response.StatusCode)}
	}
	hash := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(temporary, hash), response.Body)
	closeBodyErr := response.Body.Close()
	closeFileErr := temporary.Close()
	if copyErr != nil {
		return Result{}, unavailable("download the artifact", copyErr)
	}
	if closeBodyErr != nil {
		return Result{}, unavailable("finish the artifact download", closeBodyErr)
	}
	if closeFileErr != nil {
		return Result{}, cacheError("finish the temporary artifact file", closeFileErr)
	}
	actual := fmt.Sprintf("%x", hash.Sum(nil))
	if actual != expected {
		return Result{}, &Error{Code: CodeChecksumMismatch, Message: fmt.Sprintf("artifact checksum mismatch for %s", name)}
	}
	if err = os.Chmod(temporaryName, 0o755); err != nil {
		return Result{}, cacheError("make the verified artifact executable", err)
	}
	path := filepath.Join(directory, name)
	if err = os.Rename(temporaryName, path); err != nil {
		return Result{}, cacheError("store the verified artifact", err)
	}
	if err = writeDigest(filepath.Join(directory, name+".sha256"), expected); err != nil {
		return Result{}, err
	}
	return Result{Path: path, Version: version, Platform: platform, SHA256: expected}, nil
}

func (r *Resolver) assetURL(version, name string) string {
	return r.baseURL + "/" + url.PathEscape(version) + "/" + url.PathEscape(name)
}

func readManifest(path, name string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", &Error{Code: CodeInvalidManifest, Message: fmt.Sprintf("read checksum manifest %s", path), Cause: err}
	}
	defer file.Close()
	return parseManifest(file, name)
}

func parseManifest(reader io.Reader, name string) (string, error) {
	entries := make(map[string]string)
	scanner := bufio.NewScanner(io.LimitReader(reader, maxManifest+1))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return "", &Error{Code: CodeInvalidManifest, Message: "checksum manifest contains a malformed entry"}
		}
		digest, entry := strings.ToLower(fields[0]), strings.TrimPrefix(fields[1], "*")
		if !validDigest(digest) || filepath.Base(entry) != entry {
			return "", &Error{Code: CodeInvalidManifest, Message: "checksum manifest contains an unsafe entry"}
		}
		if _, exists := entries[entry]; exists {
			return "", &Error{Code: CodeInvalidManifest, Message: fmt.Sprintf("checksum manifest contains duplicate entry %q", entry)}
		}
		entries[entry] = digest
	}
	if err := scanner.Err(); err != nil {
		return "", &Error{Code: CodeInvalidManifest, Message: "read checksum manifest", Cause: err}
	}
	digest, ok := entries[name]
	if !ok {
		return "", &Error{Code: CodeInvalidManifest, Message: fmt.Sprintf("checksum manifest does not contain %s", name)}
	}
	return digest, nil
}

func validDigest(digest string) bool {
	if len(digest) != sha256.Size*2 {
		return false
	}
	for _, char := range digest {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func verifyFile(path, expected string) error {
	file, err := os.Open(path)
	if err != nil {
		return &Error{Code: CodeChecksumMismatch, Message: fmt.Sprintf("read artifact %s", path), Cause: err}
	}
	defer file.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, file); err != nil {
		return &Error{Code: CodeChecksumMismatch, Message: fmt.Sprintf("hash artifact %s", path), Cause: err}
	}
	if actual := fmt.Sprintf("%x", hash.Sum(nil)); actual != expected {
		return &Error{Code: CodeChecksumMismatch, Message: fmt.Sprintf("artifact checksum mismatch for %s", filepath.Base(path))}
	}
	return nil
}

func writeDigest(path, digest string) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-")
	if err != nil {
		return cacheError("create a temporary checksum file", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if _, err = fmt.Fprintln(temporary, digest); err == nil {
		err = temporary.Close()
	} else {
		_ = temporary.Close()
	}
	if err != nil {
		return cacheError("write the artifact checksum", err)
	}
	if err = os.Chmod(temporaryName, 0o600); err != nil {
		return cacheError("protect the artifact checksum", err)
	}
	if err = os.Rename(temporaryName, path); err != nil {
		return cacheError("store the artifact checksum", err)
	}
	return nil
}

func cacheError(action string, err error) error {
	return &Error{Code: CodeCacheFailure, Message: action, Cause: err}
}

func unavailable(action string, err error) error {
	return &Error{Code: CodeUnavailable, Message: action, Cause: err}
}
