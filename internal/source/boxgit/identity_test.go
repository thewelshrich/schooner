package boxgit

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thewelshrich/schooner/internal/process"
	"github.com/thewelshrich/schooner/internal/source"
)

const boxGitTestHostKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAABAgMEBQYHCAkKCwwNDg8QERITFBUWFxgZGhscHR4f"

func TestEnsureCreatesAndRecoversBoxOwnedIdentity(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen is required")
	}
	manager, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	request := testEnsureRequest(t)
	identity, err := manager.Ensure(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !identity.Exists || !identity.TrustConfigured || len(identity.HostFingerprints) != 1 || identity.Fingerprint == "" || !strings.HasPrefix(identity.PublicKey, "ssh-ed25519 ") || strings.Contains(identity.PublicKey, "PRIVATE") {
		t.Fatalf("identity=%+v", identity)
	}
	paths := manager.paths()
	privateInfo, err := os.Stat(paths.private)
	if err != nil || privateInfo.Mode().Perm() != 0o600 {
		t.Fatalf("private mode=%v err=%v", privateInfo.Mode().Perm(), err)
	}
	directoryInfo, err := os.Stat(paths.directory)
	if err != nil || directoryInfo.Mode().Perm() != 0o700 {
		t.Fatalf("directory mode=%v err=%v", directoryInfo.Mode().Perm(), err)
	}
	knownHosts, err := os.ReadFile(paths.knownHosts)
	if err != nil || !strings.HasPrefix(string(knownHosts), "github.com ssh-ed25519 ") {
		t.Fatalf("known_hosts=%q err=%v", knownHosts, err)
	}

	if err = os.Remove(paths.public); err != nil {
		t.Fatal(err)
	}
	recovered, err := manager.Ensure(t.Context(), request)
	if err != nil || recovered.Fingerprint != identity.Fingerprint {
		t.Fatalf("recovered=%+v err=%v", recovered, err)
	}

	removed, err := manager.Remove(t.Context(), source.RemoveIdentityRequest{Provider: source.GitHub, ExpectedFingerprint: identity.Fingerprint})
	if err != nil || !removed.Removed {
		t.Fatalf("removed=%+v err=%v", removed, err)
	}
	inspected, err := manager.Inspect(t.Context(), source.GitHub)
	if err != nil || inspected.Exists {
		t.Fatalf("inspected=%+v err=%v", inspected, err)
	}
}

func TestInspectTreatsMalformedManagedTrustAsUnconfigured(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen is required")
	}
	manager, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Ensure(t.Context(), testEnsureRequest(t)); err != nil {
		t.Fatal(err)
	}
	validLine := "github.com " + boxGitTestHostKey + "\n"
	for name, contents := range map[string]string{
		"empty":           "",
		"invalid key":     "github.com ssh-ed25519 invalid\n",
		"wrong host":      "example.com " + boxGitTestHostKey + "\n",
		"missing newline": strings.TrimSuffix(validLine, "\n"),
		"duplicate key":   validLine + validLine,
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(manager.paths().knownHosts, []byte(contents), 0o644); err != nil {
				t.Fatal(err)
			}
			identity, inspectErr := manager.Inspect(t.Context(), source.GitHub)
			if inspectErr != nil || !identity.Exists || identity.TrustConfigured {
				t.Fatalf("identity=%+v err=%v", identity, inspectErr)
			}
		})
	}
}

func TestEnsureRejectsKnownHostsSymlink(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen is required")
	}
	manager, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	request := testEnsureRequest(t)
	if _, err = manager.Ensure(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	paths := manager.paths()
	if err = os.Remove(paths.knownHosts); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "unrelated")
	if err = os.WriteFile(target, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(target, paths.knownHosts); err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Ensure(t.Context(), request); source.ErrorCode(err) != "conflict" {
		t.Fatalf("err=%v", err)
	}
	contents, _ := os.ReadFile(target)
	if string(contents) != "preserve" {
		t.Fatalf("symlink target changed: %q", contents)
	}
}

func TestEnsureRejectsSymlinkedManagedDirectoryAncestor(t *testing.T) {
	home := t.TempDir()
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(home, ".local")); err != nil {
		t.Fatal(err)
	}
	manager, err := New(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Ensure(t.Context(), testEnsureRequest(t)); source.ErrorCode(err) != "conflict" {
		t.Fatalf("err=%v", err)
	}
	if _, err = os.Stat(filepath.Join(target, "state", "schooner", "source", "github.com", "id_ed25519")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("managed key escaped through symlink: %v", err)
	}
}

func TestVerifyClassifiesSAMLWithoutExposingManagedPaths(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen is required")
	}
	manager, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Ensure(t.Context(), testEnsureRequest(t)); err != nil {
		t.Fatal(err)
	}
	manager.run = interceptRunner{delegate: osRunner{}, result: process.Result{Stderr: []byte("The 'Acme-Tools' organization requires SAML single sign-on")}, err: errors.New("exit status 128")}
	_, err = manager.Verify(t.Context(), source.VerifyRequest{Provider: source.GitHub, Repository: "git@github.com:owner/private.git"})
	if source.ErrorCode(err) != "authentication_required" {
		t.Fatalf("err=%v", err)
	}
	var domain *source.Error
	if !errors.As(err, &domain) || domain.Context["reason"] != "github_saml_sso" || domain.Context["organization"] != "acme-tools" || strings.Contains(err.Error(), manager.paths().private) {
		t.Fatalf("err=%+v", err)
	}
}

func TestRemoveIsIdempotentWhenIdentityDirectoryIsAbsent(t *testing.T) {
	manager, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		result, removeErr := manager.Remove(t.Context(), source.RemoveIdentityRequest{Provider: source.GitHub, ExpectedFingerprint: "SHA256:AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"})
		if removeErr != nil || !result.Removed {
			t.Fatalf("attempt=%d result=%+v err=%v", attempt, result, removeErr)
		}
	}
}

func TestRemoveNeverDeletesAMismatchedManagedKey(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen is required")
	}
	manager, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	identity, err := manager.Ensure(t.Context(), testEnsureRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	other := "SHA256:AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"
	if other == identity.Fingerprint {
		t.Fatal("test fingerprint unexpectedly matches generated identity")
	}
	_, err = manager.Remove(t.Context(), source.RemoveIdentityRequest{Provider: source.GitHub, ExpectedFingerprint: other})
	if source.ErrorCode(err) != "conflict" {
		t.Fatalf("err=%v", err)
	}
	after, inspectErr := manager.Inspect(t.Context(), source.GitHub)
	if inspectErr != nil || !after.Exists || after.Fingerprint != identity.Fingerprint {
		t.Fatalf("identity=%+v err=%v", after, inspectErr)
	}
}

func TestRemoveReconcilesAPrivateOnlyInterruptedIdentity(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen is required")
	}
	manager, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	identity, err := manager.Ensure(t.Context(), testEnsureRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(manager.paths().public); err != nil {
		t.Fatal(err)
	}
	removed, err := manager.Remove(t.Context(), source.RemoveIdentityRequest{Provider: source.GitHub, ExpectedFingerprint: identity.Fingerprint})
	if err != nil || !removed.Removed {
		t.Fatalf("removed=%+v err=%v", removed, err)
	}
	if _, statErr := os.Stat(manager.paths().private); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("private key survived cleanup: %v", statErr)
	}
}

func TestInspectRejectsUnsafePrivateKeyPermissions(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen is required")
	}
	manager, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Ensure(t.Context(), testEnsureRequest(t)); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(manager.paths().private, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Inspect(t.Context(), source.GitHub); source.ErrorCode(err) != "conflict" || strings.Contains(err.Error(), manager.paths().private) {
		t.Fatalf("err=%v", err)
	}
}

func TestVerifyMissingManagedTrustReportsStableReason(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen is required")
	}
	manager, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Ensure(t.Context(), testEnsureRequest(t)); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(manager.paths().knownHosts); err != nil {
		t.Fatal(err)
	}
	_, err = manager.Verify(t.Context(), source.VerifyRequest{Provider: source.GitHub})
	var domain *source.Error
	if source.ErrorCode(err) != "conflict" || !errors.As(err, &domain) || domain.Context["reason"] != "host_key_changed" {
		t.Fatalf("err=%+v", err)
	}
}

func TestRepositoryVerificationSuppressesPromptsAndPinsManagedSSH(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen is required")
	}
	manager, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Ensure(t.Context(), testEnsureRequest(t)); err != nil {
		t.Fatal(err)
	}
	recorder := &recordingRunner{delegate: osRunner{}}
	manager.run = recorder
	result, err := manager.Verify(t.Context(), source.VerifyRequest{Provider: source.GitHub, Repository: "git@github.com:owner/private.git"})
	if err != nil || !result.Authenticated || recorder.name != "git" {
		t.Fatalf("result=%+v command=%q err=%v", result, recorder.name, err)
	}
	environment := strings.Join(recorder.environment, "\n")
	for _, required := range []string{"GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never", "SSH_ASKPASS_REQUIRE=never", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_SSH_COMMAND=", "BatchMode=yes", "IdentitiesOnly=yes", "StrictHostKeyChecking=yes", "-F"} {
		if !strings.Contains(environment, required) {
			t.Fatalf("managed environment is missing %q", required)
		}
	}
}

func testEnsureRequest(t *testing.T) source.EnsureIdentityRequest {
	t.Helper()
	key := boxGitTestHostKey
	fingerprint, err := source.PublicKeyFingerprint(key)
	if err != nil {
		t.Fatal(err)
	}
	return source.EnsureIdentityRequest{Provider: source.GitHub, HostKeys: []source.HostKey{{Key: key, Fingerprint: fingerprint}}}
}

type interceptRunner struct {
	delegate commandRunner
	result   process.Result
	err      error
}

func (r interceptRunner) Run(ctx context.Context, environment []string, name string, arguments ...string) (process.Result, error) {
	if name == "ssh-keygen" {
		return r.delegate.Run(ctx, environment, name, arguments...)
	}
	return r.result, r.err
}

type recordingRunner struct {
	delegate    commandRunner
	environment []string
	name        string
	arguments   []string
}

func (r *recordingRunner) Run(ctx context.Context, environment []string, name string, arguments ...string) (process.Result, error) {
	if name == "ssh-keygen" {
		return r.delegate.Run(ctx, environment, name, arguments...)
	}
	r.environment = append([]string(nil), environment...)
	r.name = name
	r.arguments = append([]string(nil), arguments...)
	return process.Result{}, nil
}
