package selfupdate

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thewelshrich/schooner/internal/artifact"
)

type fixedArtifactResolver struct{ result artifact.Result }

func (r fixedArtifactResolver) Resolve(context.Context, string, artifact.Platform) (artifact.Result, error) {
	return r.result, nil
}

func TestDirectUpdateChecksAndPromotesExactRelease(t *testing.T) {
	u, target, receiptPath := testUpdater(t, "linux", "amd64", "v0.2.0", "v0.3.0")

	checked, err := u.run(t.Context(), ModeCheck)
	if err != nil {
		t.Fatal(err)
	}
	if checked.Action != ActionUpdateAvailable || checked.InstallationMethod != MethodDirect || checked.AvailableVersion != "v0.3.0" {
		t.Fatalf("check result = %#v", checked)
	}

	updated, err := u.run(t.Context(), ModeApply)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Action != ActionUpdated || updated.InstalledVersion != "v0.3.0" {
		t.Fatalf("update result = %#v", updated)
	}
	contents, err := os.ReadFile(target)
	if err != nil || !strings.Contains(string(contents), `"version":"v0.3.0"`) {
		t.Fatalf("promoted executable = %q, err=%v", contents, err)
	}
	receipt, err := readReceipt(receiptPath, target)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Version != "v0.3.0" || receipt.ReleaseAssetKind != "raw" || receipt.ReleaseAssetName != "schooner_v0.3.0_linux_amd64" {
		t.Fatalf("receipt = %#v", receipt)
	}
	if _, err = os.Stat(filepath.Join(filepath.Dir(target), lockDirectoryName)); !os.IsNotExist(err) {
		t.Fatalf("update lock remains after success: %v", err)
	}
}

func TestExpectedOwnerGuidanceSucceedsAndUnknownOwnershipFails(t *testing.T) {
	t.Run("Homebrew", func(t *testing.T) {
		u, target, _ := testUpdater(t, "linux", "amd64", "v0.2.0", "v0.3.0")
		cellarTarget := filepath.Join(filepath.Dir(target), "Cellar", "schooner", "0.2.0", "bin", "schooner")
		if err := os.MkdirAll(filepath.Dir(cellarTarget), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(target, cellarTarget); err != nil {
			t.Fatal(err)
		}
		u.executablePath = cellarTarget
		result, err := u.run(t.Context(), ModeApply)
		if err != nil || result.Action != ActionUsePackageManager || result.InstallationMethod != MethodHomebrew {
			t.Fatalf("result=%#v err=%v", result, err)
		}
		u.current.Version = "dev"
		result, err = u.run(t.Context(), ModeApply)
		if err != nil || result.InstallationMethod != MethodHomebrew {
			t.Fatalf("development-metadata Homebrew result=%#v err=%v", result, err)
		}
	})

	t.Run("source", func(t *testing.T) {
		u, _, _ := testUpdater(t, "linux", "amd64", "v0.2.0", "v0.3.0")
		u.current.Version = "dev"
		result, err := u.run(t.Context(), ModeApply)
		if err != nil || result.Action != ActionReinstallSource || result.InstallationMethod != MethodSource {
			t.Fatalf("result=%#v err=%v", result, err)
		}
	})

	t.Run("unknown", func(t *testing.T) {
		u, _, receiptPath := testUpdater(t, "linux", "amd64", "v0.2.0", "v0.3.0")
		if err := os.Remove(receiptPath); err != nil {
			t.Fatal(err)
		}
		_, err := u.run(t.Context(), ModeApply)
		if ErrorCode(err) != CodeOwnershipRefused || ErrorContext(err)["action"] != ActionRefused {
			t.Fatalf("error=%v code=%q context=%v", err, ErrorCode(err), ErrorContext(err))
		}
	})
}

func TestReceiptValidationFailsClosed(t *testing.T) {
	u, target, receiptPath := testUpdater(t, "linux", "amd64", "v0.2.0", "v0.3.0")
	original, err := os.ReadFile(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{name: "unknown field", mutate: func(value []byte) []byte {
			return []byte(strings.Replace(string(value), "\n}", ",\n  \"extra\": true\n}", 1))
		}},
		{name: "noncanonical", mutate: func(value []byte) []byte {
			return []byte(strings.Replace(string(value), "  \"schema_version\"", " \"schema_version\"", 1))
		}},
		{name: "wrong version", mutate: func(value []byte) []byte {
			return []byte(strings.Replace(string(value), `"version": "v0.2.0"`, `"version": "v0.1.0"`, 1))
		}},
		{name: "wrong digest", mutate: func(value []byte) []byte {
			return []byte(strings.Replace(string(value), `"executable_sha256": "`, `"executable_sha256": "0`, 1))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(receiptPath, test.mutate(original), 0o600); err != nil {
				t.Fatal(err)
			}
			if got := classifyInstallation(u.current.Version, target, false); got.method != MethodUnknown {
				t.Fatalf("ownership = %#v", got)
			}
		})
	}
	if err := os.WriteFile(receiptPath, original, 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(receiptPath, 0o666); err != nil {
		t.Fatal(err)
	}
	if got := classifyInstallation(u.current.Version, target, false); got.method != MethodUnknown {
		t.Fatalf("unsafe receipt ownership = %#v", got)
	}
}

func TestReadsInstallerReceiptContract(t *testing.T) {
	directory := t.TempDir()
	target := executableScript(t, filepath.Join(directory, "schooner"), "v0.2.0", "linux", "amd64", "")
	digest, err := hashFile(target)
	if err != nil {
		t.Fatal(err)
	}
	contents := fmt.Sprintf("{\n  \"schema_version\": \"1\",\n  \"installation_method\": \"direct\",\n  \"executable_path\": %q,\n  \"version\": \"v0.2.0\",\n  \"executable_sha256\": \"%s\",\n  \"release_asset_kind\": \"archive\",\n  \"release_asset_name\": \"schooner_v0.2.0_linux_amd64.tar.gz\",\n  \"release_asset_sha256\": \"%s\",\n  \"installed_at\": \"2026-08-29T12:00:00Z\"\n}\n", target, digest, strings.Repeat("b", 64))
	if err = os.WriteFile(filepath.Join(directory, receiptFileName), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := classifyInstallation("v0.2.0", target, false); got.method != MethodDirect {
		t.Fatalf("ownership = %#v", got)
	}
}

func TestAutomaticFailuresAreThrottledAndNonFatal(t *testing.T) {
	u, _, _ := testUpdater(t, "linux", "amd64", "v0.2.0", "v0.3.0")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	u.apiURL, u.client = server.URL, server.Client()
	for range 2 {
		result, err := u.run(t.Context(), ModeAutomatic)
		if err != nil || result.Action != "" {
			t.Fatalf("result=%#v err=%v", result, err)
		}
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want 1", requests.Load())
	}
}

func TestExplicitChecksUseConditionalValidators(t *testing.T) {
	u, _, _ := testUpdater(t, "linux", "amd64", "v0.2.0", "v0.3.0")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.Header().Set("ETag", `"release-1"`)
			fmt.Fprint(w, `{"tag_name":"v0.3.0","draft":false,"prerelease":false}`)
			return
		}
		if r.Header.Get("If-None-Match") != `"release-1"` {
			t.Errorf("If-None-Match = %q", r.Header.Get("If-None-Match"))
		}
		w.WriteHeader(http.StatusNotModified)
	}))
	defer server.Close()
	u.apiURL, u.client = server.URL, server.Client()
	for range 2 {
		result, err := u.run(t.Context(), ModeCheck)
		if err != nil || result.Action != ActionUpdateAvailable {
			t.Fatalf("result=%#v err=%v", result, err)
		}
	}
}

func TestExplicitCheckDoesNotDependOnWritableAdvisoryCache(t *testing.T) {
	u, _, _ := testUpdater(t, "linux", "amd64", "v0.2.0", "v0.3.0")
	u.cachePath = t.TempDir()
	result, err := u.run(t.Context(), ModeCheck)
	if err != nil || result.Action != ActionUpdateAvailable {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestReleaseSelectionRejectsInvalidLatestAndNeverDowngrades(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "draft", body: `{"tag_name":"v0.3.0","draft":true,"prerelease":false}`},
		{name: "prerelease", body: `{"tag_name":"v0.3.0-rc.1","draft":false,"prerelease":true}`},
		{name: "prerelease tag with stable metadata", body: `{"tag_name":"v0.3.0-rc.1","draft":false,"prerelease":false}`},
		{name: "invalid version", body: `{"tag_name":"latest","draft":false,"prerelease":false}`},
		{name: "malformed", body: `{`},
		{name: "trailing", body: `{"tag_name":"v0.3.0","draft":false,"prerelease":false}{}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			u, _, _ := testUpdater(t, "linux", "amd64", "v0.2.0", "v0.3.0")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, test.body) }))
			defer server.Close()
			u.apiURL, u.client = server.URL, server.Client()
			if _, err := u.run(t.Context(), ModeCheck); ErrorCode(err) != CodeInvalidRelease {
				t.Fatalf("error=%v code=%q", err, ErrorCode(err))
			}
		})
	}

	u, _, _ := testUpdater(t, "linux", "amd64", "v0.3.0", "v0.2.0")
	result, err := u.run(t.Context(), ModeApply)
	if err != nil || result.Action != ActionUpToDate || result.AvailableVersion != "v0.2.0" {
		t.Fatalf("downgrade result=%#v err=%v", result, err)
	}
}

func TestAutomaticNoticeDoesNotClaimOrHashDirectOwnership(t *testing.T) {
	u, _, _ := testUpdater(t, "linux", "amd64", "v0.2.0", "v0.3.0")
	result, err := u.run(t.Context(), ModeAutomatic)
	if err != nil || result.Action != ActionUpdateAvailable || result.InstallationMethod != MethodUnknown {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestConcurrentAutomaticChecksMakeOneRequest(t *testing.T) {
	u, _, _ := testUpdater(t, "linux", "amd64", "v0.2.0", "v0.3.0")
	started := make(chan struct{})
	release := make(chan struct{})
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		close(started)
		<-release
		fmt.Fprint(w, `{"tag_name":"v0.3.0","draft":false,"prerelease":false}`)
	}))
	defer server.Close()
	u.apiURL, u.client = server.URL, server.Client()
	firstDone := make(chan error, 1)
	go func() {
		_, err := u.run(t.Context(), ModeAutomatic)
		firstDone <- err
	}()
	<-started
	second, err := u.run(t.Context(), ModeAutomatic)
	if err != nil || second.Action != "" {
		t.Fatalf("second result=%#v err=%v", second, err)
	}
	close(release)
	if err = <-firstDone; err != nil {
		t.Fatal(err)
	}
	third, err := u.run(t.Context(), ModeAutomatic)
	if err != nil || third.Action != "" {
		t.Fatalf("recent third result=%#v err=%v", third, err)
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want 1", requests.Load())
	}
}

func TestOversizedOrWrongIdentityCandidateLeavesInstallationUntouched(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*updater, string)
	}{
		{name: "oversized", change: func(u *updater, _ string) { u.maxCandidate = 8 }},
		{name: "wrong architecture", change: func(u *updater, _ string) {
			candidate := executableScript(t, filepath.Join(t.TempDir(), "candidate"), "v0.3.0", "linux", "arm64", "")
			digest, _ := hashFile(candidate)
			u.artifacts = fixedArtifactResolver{result: artifact.Result{Path: candidate, Version: "v0.3.0", Platform: artifact.Platform{OS: "linux", Arch: "amd64"}, SHA256: digest}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			u, target, _ := testUpdater(t, "linux", "amd64", "v0.2.0", "v0.3.0")
			before, _ := os.ReadFile(target)
			test.change(u, target)
			if _, err := u.run(t.Context(), ModeApply); ErrorCode(err) != CodeVerification {
				t.Fatalf("error=%v code=%q", err, ErrorCode(err))
			}
			after, _ := os.ReadFile(target)
			if string(after) != string(before) {
				t.Fatal("failed verification changed the installed executable")
			}
		})
	}
}

func TestNonHomebrewInvocationSymlinkCannotGrantDirectOwnership(t *testing.T) {
	u, target, _ := testUpdater(t, "linux", "amd64", "v0.2.0", "v0.3.0")
	link := filepath.Join(t.TempDir(), "schooner")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	canonical, invokedSymlink, err := canonicalExecutable(link)
	expected, resolveErr := filepath.EvalSymlinks(target)
	if err != nil || resolveErr != nil || canonical != expected || !invokedSymlink {
		t.Fatalf("canonical=%q symlink=%t err=%v", canonical, invokedSymlink, err)
	}
	if got := classifyInstallation(u.current.Version, canonical, invokedSymlink); got.method != MethodUnknown {
		t.Fatalf("ownership = %#v", got)
	}
}

func TestCandidateIsNeverRunBeforeMacOSSignatureVerification(t *testing.T) {
	u, _, _ := testUpdater(t, "darwin", "amd64", "v0.2.0", "v0.3.0")
	marker := filepath.Join(t.TempDir(), "executed")
	candidate := executableScript(t, filepath.Join(t.TempDir(), "candidate"), "v0.3.0", "darwin", "amd64", "touch "+shellQuote(marker))
	digest, _ := hashFile(candidate)
	u.artifacts = fixedArtifactResolver{result: artifact.Result{Path: candidate, Version: "v0.3.0", Platform: artifact.Platform{OS: "darwin", Arch: "amd64"}, SHA256: digest}}
	u.verifySignature = func(context.Context, string) error {
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatal("candidate executed before signature verification")
		}
		return fmt.Errorf("invalid signature")
	}
	_, err := u.run(t.Context(), ModeApply)
	if ErrorCode(err) != CodeVerification {
		t.Fatalf("error = %v", err)
	}
	if _, err = os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("candidate executed after failed signature verification")
	}
}

func TestPromotionRevalidatesTargetAndReceipt(t *testing.T) {
	u, target, _ := testUpdater(t, "darwin", "amd64", "v0.2.0", "v0.3.0")
	candidate := executableScript(t, filepath.Join(t.TempDir(), "candidate"), "v0.3.0", "darwin", "amd64", "")
	digest, _ := hashFile(candidate)
	u.artifacts = fixedArtifactResolver{result: artifact.Result{Path: candidate, Version: "v0.3.0", Platform: artifact.Platform{OS: "darwin", Arch: "amd64"}, SHA256: digest}}
	u.verifySignature = func(context.Context, string) error {
		return os.WriteFile(target, []byte("changed during verification"), 0o755)
	}
	_, err := u.run(t.Context(), ModeApply)
	if ErrorCode(err) != CodeVerification || !strings.Contains(err.Error(), "changed during update verification") {
		t.Fatalf("error = %v", err)
	}
	contents, _ := os.ReadFile(target)
	if string(contents) != "changed during verification" {
		t.Fatalf("target was promoted despite race: %q", contents)
	}
}

func TestReceiptFailureReportsValidButUnownedUpdate(t *testing.T) {
	u, target, _ := testUpdater(t, "linux", "amd64", "v0.2.0", "v0.3.0")
	u.publishReceipt = func(string, installationReceipt) error { return fmt.Errorf("disk full") }
	_, err := u.run(t.Context(), ModeApply)
	if ErrorCode(err) != CodeReceipt || ErrorContext(err)["action"] != ActionUpdatedUnowned || ErrorContext(err)["installation_method"] != MethodUnknown {
		t.Fatalf("error=%v context=%v", err, ErrorContext(err))
	}
	contents, readErr := os.ReadFile(target)
	if readErr != nil || !strings.Contains(string(contents), `"version":"v0.3.0"`) {
		t.Fatalf("new executable was not retained: %q err=%v", contents, readErr)
	}
}

func TestInstallationLockFailsClosedAndReclaimsDeadLocalOwner(t *testing.T) {
	u, target, _ := testUpdater(t, "linux", "amd64", "v0.2.0", "v0.3.0")
	lockPath := filepath.Join(filepath.Dir(target), lockDirectoryName)
	if err := os.Mkdir(lockPath, 0o700); err != nil {
		t.Fatal(err)
	}
	owner := lockOwner{host: "test-host", pid: 42, target: target, token: "old-token"}
	if err := writeLockOwner(filepath.Join(lockPath, "owner"), owner); err != nil {
		t.Fatal(err)
	}
	u.processAlive = func(int) (bool, error) { return true, nil }
	if _, err := u.acquireLock(); ErrorCode(err) != CodeLocked {
		t.Fatalf("live lock error = %v", err)
	}
	u.processAlive = func(int) (bool, error) { return false, nil }
	lock, err := u.acquireLock()
	if err != nil {
		t.Fatal(err)
	}
	lock.release()
	if _, err = os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("lock remains after release: %v", err)
	}
}

func testUpdater(t *testing.T, osName, arch, currentVersion, availableVersion string) (*updater, string, string) {
	t.Helper()
	directory := t.TempDir()
	target := executableScript(t, filepath.Join(directory, "schooner"), currentVersion, osName, arch, "")
	digest, err := hashFile(target)
	if err != nil {
		t.Fatal(err)
	}
	receiptPath := filepath.Join(directory, receiptFileName)
	if err = writeReceipt(receiptPath, installationReceipt{
		SchemaVersion: SchemaVersion, InstallationMethod: MethodDirect, ExecutablePath: target,
		Version: currentVersion, ExecutableSHA256: digest, ReleaseAssetKind: "archive",
		ReleaseAssetName:   "schooner_" + currentVersion + "_" + osName + "_" + arch + ".tar.gz",
		ReleaseAssetSHA256: strings.Repeat("a", 64), InstalledAt: "2026-08-29T12:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	candidate := executableScript(t, filepath.Join(t.TempDir(), "schooner_"+availableVersion+"_"+osName+"_"+arch), availableVersion, osName, arch, "")
	candidateDigest, err := hashFile(candidate)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"tag_name":%q,"draft":false,"prerelease":false}`, availableVersion)
	}))
	t.Cleanup(server.Close)
	u := &updater{
		current: Current{Version: currentVersion, OS: osName, Arch: arch}, executablePath: target,
		cachePath: filepath.Join(t.TempDir(), cacheFileName), apiURL: server.URL, client: server.Client(),
		artifacts: fixedArtifactResolver{result: artifact.Result{Path: candidate, Version: availableVersion, Platform: artifact.Platform{OS: osName, Arch: arch}, SHA256: candidateDigest}},
		now:       func() time.Time { return time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC) },
		hostname:  func() (string, error) { return "test-host", nil }, processAlive: func(int) (bool, error) { return false, nil },
		verifySignature: func(context.Context, string) error { return nil }, publishReceipt: writeReceipt, maxCandidate: 1 << 20,
	}
	return u, target, receiptPath
}

func executableScript(t *testing.T, path, version, osName, arch, before string) string {
	t.Helper()
	contents := "#!/bin/sh\nset -eu\n" + before + "\nprintf '%s\\n' '" + fmt.Sprintf(`{"schema_version":"1","version":%q,"os":%q,"arch":%q}`, version, osName, arch) + "'\n"
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func TestHashFixtureUsesExpectedDigest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "value")
	if err := os.WriteFile(path, []byte("value"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := hashFile(path)
	want := fmt.Sprintf("%x", sha256.Sum256([]byte("value")))
	if err != nil || got != want {
		t.Fatalf("digest=%q want=%q err=%v", got, want, err)
	}
}
