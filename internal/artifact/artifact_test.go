package artifact

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestName(t *testing.T) {
	for _, platform := range []Platform{
		{OS: "darwin", Arch: "amd64"},
		{OS: "darwin", Arch: "arm64"},
		{OS: "linux", Arch: "amd64"},
		{OS: "linux", Arch: "arm64"},
	} {
		got, err := Name("v1.2.3-rc.1", platform)
		want := "schooner_v1.2.3-rc.1_" + platform.OS + "_" + platform.Arch
		if err != nil || got != want {
			t.Fatalf("Name(%+v) = %q, %v; want %q", platform, got, err, want)
		}
	}
}

func TestNameRejectsInvalidInput(t *testing.T) {
	for _, version := range []string{"", "dev", "1.2.3", "v1.2", "v01.2.3", "v1.2.3-01", "v1.2.3+"} {
		if _, err := Name(version, Platform{OS: "linux", Arch: "amd64"}); ErrorCode(err) != CodeInvalidVersion {
			t.Errorf("version %q error = %v", version, err)
		}
	}
	if _, err := Name("v1.2.3", Platform{OS: "windows", Arch: "amd64"}); ErrorCode(err) != CodeUnsupportedPlatform {
		t.Fatalf("unsupported platform error = %v", err)
	}
}

func TestResolveDownloadsVerifiesAndReusesCache(t *testing.T) {
	contents := []byte("verified binary")
	name := "schooner_v1.2.3_linux_arm64"
	digest := checksum(contents)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch filepath.Base(r.URL.Path) {
		case manifestName:
			fmt.Fprintf(w, "%s  %s\n", digest, name)
		case name:
			_, _ = w.Write(contents)
		default:
			http.NotFound(w, r)
		}
	}))
	cache := t.TempDir()
	resolver, err := New(Config{CacheDir: cache, BaseURL: server.URL, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}

	result, err := resolver.Resolve(t.Context(), "v1.2.3", Platform{OS: "linux", Arch: "arm64"})
	if err != nil || result.SHA256 != digest {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if mode, statErr := os.Stat(result.Path); statErr != nil || mode.Mode().Perm() != 0o755 {
		t.Fatalf("mode=%v err=%v", mode, statErr)
	}
	server.Close()
	second, err := resolver.Resolve(t.Context(), "v1.2.3", Platform{OS: "linux", Arch: "arm64"})
	if err != nil || second != result || requests.Load() != 2 {
		t.Fatalf("cached result=%+v err=%v requests=%d", second, err, requests.Load())
	}
}

func TestResolveRepairsCorruptCache(t *testing.T) {
	contents := []byte("fresh binary")
	name := "schooner_v1.0.0_linux_amd64"
	digest := checksum(contents)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if filepath.Base(r.URL.Path) == manifestName {
			fmt.Fprintf(w, "%s  %s\n", digest, name)
			return
		}
		_, _ = w.Write(contents)
	}))
	defer server.Close()
	resolver, err := New(Config{CacheDir: t.TempDir(), BaseURL: server.URL, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	first, err := resolver.Resolve(t.Context(), "v1.0.0", Platform{OS: "linux", Arch: "amd64"})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(first.Path, []byte("corrupt"), 0o755); err != nil {
		t.Fatal(err)
	}
	second, err := resolver.Resolve(t.Context(), "v1.0.0", Platform{OS: "linux", Arch: "amd64"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(second.Path)
	if err != nil || string(got) != string(contents) {
		t.Fatalf("contents=%q err=%v", got, err)
	}
}

func TestResolveRejectsChecksumMismatchWithoutCaching(t *testing.T) {
	name := "schooner_v1.0.0_linux_amd64"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if filepath.Base(r.URL.Path) == manifestName {
			fmt.Fprintf(w, "%s  %s\n", checksum([]byte("expected")), name)
			return
		}
		_, _ = w.Write([]byte("different"))
	}))
	defer server.Close()
	cache := t.TempDir()
	resolver, err := New(Config{CacheDir: cache, BaseURL: server.URL, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = resolver.Resolve(t.Context(), "v1.0.0", Platform{OS: "linux", Arch: "amd64"})
	if ErrorCode(err) != CodeChecksumMismatch {
		t.Fatalf("error = %v", err)
	}
	if matches, _ := filepath.Glob(filepath.Join(cache, "v1.0.0", name+"*")); len(matches) != 0 {
		t.Fatalf("checksum failure left cache files: %v", matches)
	}
}

func TestResolveUsesVerifiedDevelopmentOverride(t *testing.T) {
	directory := t.TempDir()
	name := "schooner_dev_linux_amd64"
	contents := []byte("development binary")
	if err := os.WriteFile(filepath.Join(directory, name), contents, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, manifestName), []byte(fmt.Sprintf("%s  %s\n", checksum(contents), name)), 0o600); err != nil {
		t.Fatal(err)
	}
	resolver, err := New(Config{CacheDir: t.TempDir(), OverrideDir: directory})
	if err != nil {
		t.Fatal(err)
	}
	result, err := resolver.Resolve(t.Context(), "dev", Platform{OS: "linux", Arch: "amd64"})
	if err != nil || result.Path != filepath.Join(directory, name) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if err = os.WriteFile(filepath.Join(directory, name), []byte("changed"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err = resolver.Resolve(t.Context(), "dev", Platform{OS: "linux", Arch: "amd64"}); ErrorCode(err) != CodeChecksumMismatch {
		t.Fatalf("mismatch error = %v", err)
	}
}

func TestResolveUsesDevelopmentDirectoryWithoutOverride(t *testing.T) {
	directory := t.TempDir()
	name := "schooner_dev_linux_amd64"
	contents := []byte("cached development binary")
	if err := os.WriteFile(filepath.Join(directory, name), contents, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, manifestName), []byte(fmt.Sprintf("%s  %s\n", checksum(contents), name)), 0o600); err != nil {
		t.Fatal(err)
	}
	resolver, err := New(Config{CacheDir: t.TempDir(), DevelopmentDir: directory})
	if err != nil {
		t.Fatal(err)
	}
	result, err := resolver.Resolve(t.Context(), "dev", Platform{OS: "linux", Arch: "amd64"})
	if err != nil || result.Path != filepath.Join(directory, name) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestResolveExplainsHowToPrepareDefaultDevelopmentDirectory(t *testing.T) {
	resolver, err := New(Config{CacheDir: t.TempDir(), DevelopmentDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = resolver.Resolve(t.Context(), "dev", Platform{OS: "linux", Arch: "amd64"})
	if ErrorCode(err) != CodeUnavailable || !strings.Contains(err.Error(), "schooner dev artifacts") {
		t.Fatalf("error = %v", err)
	}
}

func TestDeferredDefaultResolverUsesPreparedDevelopmentCache(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("SCHOONER_ARTIFACT_DIR", "")
	directory, err := DefaultDevelopmentDirectory()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	name := "schooner_dev_linux_arm64"
	contents := []byte("default cached development binary")
	if err = os.WriteFile(filepath.Join(directory, name), contents, 0o755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(directory, manifestName), []byte(fmt.Sprintf("%s  %s\n", checksum(contents), name)), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := NewDeferredDefault().Resolve(t.Context(), "dev", Platform{OS: "linux", Arch: "arm64"})
	if err != nil || result.Path != filepath.Join(directory, name) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestOverrideAndDeferredResolverDoNotRequireCacheHome(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	directory := t.TempDir()
	name := "schooner_dev_linux_amd64"
	contents := []byte("development binary")
	if err := os.WriteFile(filepath.Join(directory, name), contents, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, manifestName), []byte(fmt.Sprintf("%s  %s\n", checksum(contents), name)), 0o600); err != nil {
		t.Fatal(err)
	}

	resolver, err := New(Config{OverrideDir: directory})
	if err != nil {
		t.Fatalf("override resolver required a cache home: %v", err)
	}
	if _, err = resolver.Resolve(t.Context(), "dev", Platform{OS: "linux", Arch: "amd64"}); err != nil {
		t.Fatal(err)
	}

	deferred := NewDeferred(Config{})
	if _, err = deferred.Resolve(t.Context(), "v1.2.3", Platform{OS: "linux", Arch: "amd64"}); ErrorCode(err) != CodeCacheFailure {
		t.Fatalf("deferred cache error = %v", err)
	}
}

func TestResolveRequiresOverrideForDevelopmentBuild(t *testing.T) {
	resolver, err := New(Config{CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = resolver.Resolve(t.Context(), "dev", Platform{OS: "linux", Arch: "amd64"}); ErrorCode(err) != CodeInvalidVersion {
		t.Fatalf("error = %v", err)
	}
}

func TestResolvePreservesCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	resolver, err := New(Config{CacheDir: t.TempDir(), BaseURL: server.URL, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = resolver.Resolve(ctx, "v1.0.0", Platform{OS: "linux", Arch: "amd64"})
	if !errors.Is(err, context.Canceled) || ErrorCode(err) != CodeUnavailable {
		t.Fatalf("error = %v", err)
	}
}

func TestParseManifestRejectsMalformedAndDuplicateEntries(t *testing.T) {
	name := "schooner_v1.0.0_linux_amd64"
	digest := strings.Repeat("a", 64)
	for _, manifest := range []string{
		"not-a-manifest\n",
		fmt.Sprintf("%s  ../%s\n", digest, name),
		fmt.Sprintf("%s  %s\n%s  %s\n", digest, name, digest, name),
		fmt.Sprintf("%s  another-file\n", digest),
	} {
		if _, err := parseManifest(strings.NewReader(manifest), name); ErrorCode(err) != CodeInvalidManifest {
			t.Errorf("manifest %q error = %v", manifest, err)
		}
	}
}

func checksum(contents []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(contents))
}
