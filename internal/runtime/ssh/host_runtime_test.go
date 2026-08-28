package ssh

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thewelshrich/schooner/internal/artifact"
	"github.com/thewelshrich/schooner/internal/box"
	hostruntime "github.com/thewelshrich/schooner/internal/runtime"
	"github.com/thewelshrich/schooner/internal/source"
)

const hostTestIdentity = "11111111-1111-4111-8111-111111111111"

func TestInteractiveHostHandshakePreservesTransportError(t *testing.T) {
	ssh := writeExecutable(t, "#!/bin/sh\nprintf 'Permission denied (publickey).\\n' >&2\nexit 255\n")
	runtime := NewHost(ssh, nil, "v1.2.3", nil)
	_, err := runtime.OpenWorktreeShell(t.Context(), box.Connection{Destination: "trusted-host"}, box.HostRuntime{Path: "/home/alice/.local/bin/schooner"}, hostTestIdentity, "/worktrees", "repo", TerminalIO{})
	if box.ErrorCode(err) != "authentication_required" {
		t.Fatalf("error = %v, code = %s", err, box.ErrorCode(err))
	}
}

func TestInteractiveHostRequestUsesStandardPaddedBase64(t *testing.T) {
	testRemoteShell(t)
	target := filepath.Join(t.TempDir(), "host-runtime")
	hello := fmt.Sprintf(`{"schema_version":"1","protocol_version":"1","schooner_version":"v1.2.3","commit":"abc123","box_identity":%q,"os":"linux","architecture":"amd64","capabilities":["worktree.shell.v1"]}`, hostTestIdentity)
	contents := fmt.Sprintf("#!/bin/sh\ncase \"$1 $2 $3\" in\n  'host hello '*) printf '%%s\\n' '%s' ;;\n  'host worktree shell') printf '%%s' \"$4\" | base64 -d ;;\n  *) exit 64 ;;\nesac\n", hello)
	if err := os.WriteFile(target, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
	runtime := NewHost(testSSHExecutable(t), nil, "v1.2.3", nil)
	var stdout strings.Builder
	result, err := runtime.OpenWorktreeShell(t.Context(), box.Connection{Destination: "trusted-host"}, box.HostRuntime{Path: target}, hostTestIdentity, "/home/alice/worktrees", "repo", TerminalIO{Out: &stdout})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("interactive shell = %+v, error = %v", result, err)
	}
	var request hostruntime.WorktreeShellRequest
	if err = json.Unmarshal([]byte(strings.TrimSpace(stdout.String())), &request); err != nil {
		t.Fatalf("decoded request %q: %v", stdout.String(), err)
	}
	if request.WorktreeRoot != "/home/alice/worktrees" || request.Worktree != "repo" {
		t.Fatalf("request = %+v", request)
	}
}

func TestCloneRepositoryUsesTypedHostLifecycleOperation(t *testing.T) {
	testRemoteShell(t)
	target := filepath.Join(t.TempDir(), "host-runtime")
	hello := fmt.Sprintf(`{"schema_version":"1","protocol_version":"1","schooner_version":"v1.2.3","commit":"abc123","box_identity":%q,"os":"linux","architecture":"amd64","capabilities":["repository.clone.v1"]}`, hostTestIdentity)
	result := fmt.Sprintf(`{"schema_version":"1","protocol_version":"1","box_identity":%q,"action":"clone","recovered":false,"worktree_root":"/home/alice/schooner","inspection":{"worktree_root":"/home/alice/schooner","repository":{"common_directory":"/home/alice/schooner/repo/.git","linked":[]},"worktree":{"path":"/home/alice/schooner/repo","relative_path":"repo","git_directory":"/home/alice/schooner/repo/.git","kind":"primary","detached":false,"status":{"staged":0,"unstaged":0,"untracked":0,"conflicted":0}},"warnings":[]},"path":"/home/alice/schooner/repo"}`, hostTestIdentity)
	contents := fmt.Sprintf("#!/bin/sh\ncase \"$1 $2 $3 $4\" in\n  'host hello  '*) printf '%%s\\n' '%s' ;;\n  'host repository clone ') cat >/dev/null; printf '%%s\\n' '%s' ;;\n  '--no-input host repository clone') cat >/dev/null; printf '%%s\\n' '%s' ;;\n  *) exit 64 ;;\nesac\n", hello, result, result)
	if err := os.WriteFile(target, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
	runtime := NewHost(testSSHExecutable(t), nil, "v1.2.3", nil)
	got, err := runtime.CloneRepository(t.Context(), box.Connection{Destination: "trusted-host"}, box.HostRuntime{Path: target}, hostTestIdentity, "/worktrees", "git@example.com:owner/repo.git", "main")
	if err != nil || got.Action != "clone" || got.Path != "/home/alice/schooner/repo" || got.Inspection == nil {
		t.Fatalf("clone = %+v, %v", got, err)
	}
	if _, err = runtime.CloneRepository(t.Context(), box.Connection{Destination: "trusted-host", BatchMode: true}, box.HostRuntime{Path: target}, hostTestIdentity, "/worktrees", "git@example.com:owner/repo.git", "main"); err != nil {
		t.Fatalf("noninteractive clone = %v", err)
	}
}

func TestLegacyCloneAuthenticationRequiresRuntimeUpdateBeforeManagedRecovery(t *testing.T) {
	testRemoteShell(t)
	target := filepath.Join(t.TempDir(), "host-runtime")
	hello := fmt.Sprintf(`{"schema_version":"1","protocol_version":"1","schooner_version":"v1.2.3","commit":"abc123","box_identity":%q,"os":"linux","architecture":"amd64","capabilities":["repository.clone.v1"]}`, hostTestIdentity)
	failure := hostruntime.NewOperationError(hostTestIdentity, hostruntime.CodeAuthentication, "Git source authentication failed")
	encoded, err := json.Marshal(failure)
	if err != nil {
		t.Fatal(err)
	}
	contents := fmt.Sprintf("#!/bin/sh\ncase \"$1 $2 $3 $4\" in\n  'host hello  '*) printf '%%s\\n' '%s' ;;\n  'host repository clone ') cat >/dev/null; printf '%%s\\n' '%s' ;;\n  *) exit 64 ;;\nesac\n", hello, encoded)
	if err = os.WriteFile(target, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
	runtime := NewHost(testSSHExecutable(t), nil, "v1.2.3", nil)
	_, err = runtime.CloneRepository(t.Context(), box.Connection{Destination: "trusted-host"}, box.HostRuntime{Path: target}, hostTestIdentity, "/worktrees", "https://github.com/owner/repo.git", "")
	var domain *box.Error
	if !errors.As(err, &domain) || domain.Code != "authentication_required" || domain.Context["reason"] != "host_runtime_update_required" {
		t.Fatalf("error = %#v", err)
	}
}

func TestCloneRepositoryPrefersV2WhenAdvertised(t *testing.T) {
	testRemoteShell(t)
	target := filepath.Join(t.TempDir(), "host-runtime")
	hello := fmt.Sprintf(`{"schema_version":"1","protocol_version":"1","schooner_version":"v1.2.3","commit":"abc123","box_identity":%q,"os":"linux","architecture":"amd64","capabilities":["repository.clone.v1","repository.clone.v2"]}`, hostTestIdentity)
	result := fmt.Sprintf(`{"schema_version":"1","protocol_version":"1","box_identity":%q,"action":"clone","recovered":false,"worktree_root":"/worktrees","path":"/worktrees/repo"}`, hostTestIdentity)
	contents := fmt.Sprintf("#!/bin/sh\ncase \"$1 $2 $3 $4\" in\n  'host hello  '*) printf '%%s\\n' '%s' ;;\n  'host repository clone-v2 ') cat >/dev/null; printf '%%s\\n' '%s' ;;\n  *) exit 64 ;;\nesac\n", hello, result)
	if err := os.WriteFile(target, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
	runtime := NewHost(testSSHExecutable(t), nil, "v1.2.3", nil)
	installed := box.HostRuntime{Path: target}
	got, err := runtime.CloneRepository(t.Context(), box.Connection{Destination: "trusted-host"}, installed, hostTestIdentity, "/worktrees", "https://github.com/owner/repo.git", "")
	if err != nil || got.Action != "clone" || got.Path != "/worktrees/repo" {
		t.Fatalf("clone = %+v, %v", got, err)
	}
}

func TestSourceOperationRejectsLegacyRuntimeBeforeSendingRequest(t *testing.T) {
	testRemoteShell(t)
	target := filepath.Join(t.TempDir(), "host-runtime")
	requestMarker := filepath.Join(t.TempDir(), "source-requested")
	hello := fmt.Sprintf(`{"schema_version":"1","protocol_version":"1","schooner_version":"v1.2.3","commit":"abc123","box_identity":%q,"os":"linux","architecture":"amd64","capabilities":["worktree.list.v1"]}`, hostTestIdentity)
	contents := fmt.Sprintf("#!/bin/sh\ncase \"$1 $2 $3 $4\" in\n  'host hello  '*) printf '%%s\\n' '%s' ;;\n  *'host source'*) : > '%s'; exit 64 ;;\n  *) exit 64 ;;\nesac\n", hello, requestMarker)
	if err := os.WriteFile(target, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
	runtime := NewHost(testSSHExecutable(t), nil, "v1.2.3", nil)
	_, err := runtime.InspectSourceIdentity(t.Context(), box.Connection{Destination: "trusted-host"}, box.HostRuntime{Path: target}, hostTestIdentity, source.GitHub)
	if box.ErrorCode(err) != "host_runtime_incompatible" || !strings.Contains(err.Error(), "box update") {
		t.Fatalf("err=%v", err)
	}
	if _, statErr := os.Stat(requestMarker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("legacy runtime received source request: %v", statErr)
	}
}

func TestSourceOperationPreservesBoundedReasonContext(t *testing.T) {
	testRemoteShell(t)
	target := filepath.Join(t.TempDir(), "host-runtime")
	hello := fmt.Sprintf(`{"schema_version":"1","protocol_version":"1","schooner_version":"v1.2.3","commit":"abc123","box_identity":%q,"os":"linux","architecture":"amd64","capabilities":["source.repository.verify.v1"]}`, hostTestIdentity)
	failure := hostruntime.NewOperationErrorWithContext(hostTestIdentity, hostruntime.CodeAuthentication, "SAML authorization is required", map[string]string{"reason": "github_saml_sso", "organization": "acme-tools"})
	encoded, err := json.Marshal(failure)
	if err != nil {
		t.Fatal(err)
	}
	contents := fmt.Sprintf("#!/bin/sh\ncase \"$1 $2 $3 $4\" in\n  'host hello  '*) printf '%%s\\n' '%s' ;;\n  'host source repository verify') cat >/dev/null; printf '%%s\\n' '%s' ;;\n  *) exit 64 ;;\nesac\n", hello, encoded)
	if err = os.WriteFile(target, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
	runtime := NewHost(testSSHExecutable(t), nil, "v1.2.3", nil)
	_, err = runtime.VerifySourceRepository(t.Context(), box.Connection{Destination: "trusted-host"}, box.HostRuntime{Path: target}, hostTestIdentity, source.VerifyRequest{Provider: source.GitHub})
	var domain *box.Error
	if box.ErrorCode(err) != "authentication_required" || !errors.As(err, &domain) || domain.Context["reason"] != "github_saml_sso" || domain.Context["organization"] != "acme-tools" {
		t.Fatalf("err=%+v", err)
	}
}

func TestInspectWorktreePreservesRemoteNotFound(t *testing.T) {
	testRemoteShell(t)
	target := filepath.Join(t.TempDir(), "host-runtime")
	hello := fmt.Sprintf(`{"schema_version":"1","protocol_version":"1","schooner_version":"v1.2.3","commit":"abc123","box_identity":%q,"os":"linux","architecture":"amd64","capabilities":["worktree.inspect.v1"]}`, hostTestIdentity)
	notFound := hostruntime.NewOperationError(hostTestIdentity, hostruntime.CodeNotFound, `worktree "missing" was not found`)
	encoded, err := json.Marshal(notFound)
	if err != nil {
		t.Fatal(err)
	}
	contents := fmt.Sprintf("#!/bin/sh\ncase \"$1 $2 $3\" in\n  'host hello '*) printf '%%s\\n' '%s' ;;\n  'host worktree inspect') cat >/dev/null; printf '%%s\\n' '%s' ;;\n  *) exit 64 ;;\nesac\n", hello, encoded)
	if err = os.WriteFile(target, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
	runtime := NewHost(testSSHExecutable(t), nil, "v1.2.3", nil)
	_, err = runtime.InspectWorktree(t.Context(), box.Connection{Destination: "trusted-host"}, box.HostRuntime{Path: target}, hostTestIdentity, "missing")
	if box.ErrorCode(err) != "not_found" || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("error = %v, code = %s", err, box.ErrorCode(err))
	}
}

func TestEnsureHostInstallsReusesAndInspectsTypedRuntime(t *testing.T) {
	testRemoteShell(t)
	root := t.TempDir()
	target := filepath.Join(root, "home'alice", ".local", "bin", "schooner")
	resolved := writeHostArtifact(t, root, "v1.2.3", "1", "amd64")
	resolver := &staticArtifactResolver{result: resolved}
	runtime := NewHost(testSSHExecutable(t), nil, "v1.2.3", resolver)
	runtime.randomSuffix = func() (string, error) { return "111111111111111111111111", nil }
	request := box.HostInstallRequest{Path: target, OS: "linux", Architecture: "amd64", ExpectedIdentity: hostTestIdentity}

	installed, err := runtime.EnsureHost(t.Context(), box.Connection{Destination: "trusted-host", BatchMode: true}, request)
	if err != nil {
		t.Fatalf("%v (cause: %v)", err, errors.Unwrap(err))
	}
	if installed.Runtime.Path != target || installed.Runtime.Version != "v1.2.3" || installed.Runtime.ProtocolVersion != "1" || installed.Action != box.HostInstalled || resolver.calls != 1 {
		t.Fatalf("installed=%+v resolver calls=%d", installed, resolver.calls)
	}
	contents, err := os.ReadFile(target)
	if err != nil || string(contents) != resolved.contents {
		t.Fatalf("promoted contents match=%t err=%v", string(contents) == resolved.contents, err)
	}

	reused, err := runtime.EnsureHost(t.Context(), box.Connection{Destination: "trusted-host", BatchMode: true}, request)
	if err != nil || reused.Runtime.Version != installed.Runtime.Version || reused.Action != box.HostReused || resolver.calls != 1 {
		t.Fatalf("reused=%+v err=%v resolver calls=%d", reused, err, resolver.calls)
	}
	capabilities, err := runtime.InspectHost(t.Context(), box.Connection{Destination: "trusted-host", BatchMode: true}, reused.Runtime, "~/schooner", hostTestIdentity)
	if err != nil || capabilities.RemoteIdentity != hostTestIdentity || capabilities.Host.Version != "v1.2.3" || !capabilities.Git.Available {
		t.Fatalf("InspectHost() = %+v, %v", capabilities, err)
	}
}

func TestInspectHostRejectsPreWorktreeInspectionCapabilityBeforeRequest(t *testing.T) {
	testRemoteShell(t)
	target := filepath.Join(t.TempDir(), "host-runtime")
	artifact := writeHostArtifactAt(t, target, "v1.2.3", "1", "amd64")
	legacy := strings.ReplaceAll(artifact.contents, "host.inspect.v2", "host.inspect.v1")
	if err := os.WriteFile(target, []byte(legacy), 0o755); err != nil {
		t.Fatal(err)
	}
	runtime := NewHost(testSSHExecutable(t), nil, "v1.2.3", nil)
	_, err := runtime.InspectHost(t.Context(), box.Connection{Destination: "trusted-host", BatchMode: true}, box.HostRuntime{Path: target}, "/home/alice/schooner", hostTestIdentity)
	if box.ErrorCode(err) != "host_runtime_incompatible" || !strings.Contains(err.Error(), "box update") {
		t.Fatalf("error = %v", err)
	}
}

func TestEnsureHostInstallsArm64Artifact(t *testing.T) {
	testRemoteShell(t)
	root := t.TempDir()
	target := filepath.Join(root, "home", ".local", "bin", "schooner")
	resolver := &staticArtifactResolver{result: writeHostArtifact(t, root, "v1.2.3", "1", "arm64")}
	runtime := NewHost(testSSHExecutable(t), nil, "v1.2.3", resolver)
	runtime.randomSuffix = func() (string, error) { return "555555555555555555555555", nil }
	request := box.HostInstallRequest{Path: target, OS: "linux", Architecture: "arm64", ExpectedIdentity: hostTestIdentity}

	installed, err := runtime.EnsureHost(t.Context(), box.Connection{Destination: "trusted-host"}, request)
	if err != nil || installed.Runtime.Version != "v1.2.3" || resolver.calls != 1 {
		t.Fatalf("installed=%+v err=%v calls=%d", installed, err, resolver.calls)
	}
}

func TestEnsureHostUpdateReplacesCompatibleOlderRuntimeAndThenNoOps(t *testing.T) {
	testRemoteShell(t)
	root := t.TempDir()
	target := filepath.Join(root, "home", ".local", "bin", "schooner")
	writeHostArtifactAt(t, target, "v1.0.0", "1", "amd64")
	want := writeHostArtifact(t, root, "v2.0.0", "1", "amd64")
	resolver := &staticArtifactResolver{result: want}
	runtime := NewHost(testSSHExecutable(t), nil, "v2.0.0", resolver)
	runtime.randomSuffix = func() (string, error) { return "999999999999999999999999", nil }
	request := box.HostInstallRequest{Path: target, OS: "linux", Architecture: "amd64", ExpectedIdentity: hostTestIdentity, Mode: box.HostUpdate}

	updated, err := runtime.EnsureHost(t.Context(), box.Connection{Destination: "trusted-host"}, request)
	if err != nil || updated.Action != box.HostReplaced || updated.PreviousVersion != "v1.0.0" || updated.Runtime.Version != "v2.0.0" || resolver.calls != 1 {
		t.Fatalf("updated=%+v err=%v calls=%d", updated, err, resolver.calls)
	}
	current, err := runtime.EnsureHost(t.Context(), box.Connection{Destination: "trusted-host"}, request)
	if err != nil || current.Action != box.HostReused || current.Runtime.Version != "v2.0.0" || resolver.calls != 1 {
		t.Fatalf("current=%+v err=%v calls=%d", current, err, resolver.calls)
	}
}

func TestEnsureHostRepairReplacesUnidentifiableRuntime(t *testing.T) {
	testRemoteShell(t)
	root := t.TempDir()
	target := filepath.Join(root, "home", ".local", "bin", "schooner")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("#!/bin/sh\nexit 64\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	want := writeHostArtifact(t, root, "v2.0.0", "1", "amd64")
	resolver := &staticArtifactResolver{result: want}
	runtime := NewHost(testSSHExecutable(t), nil, "v2.0.0", resolver)
	runtime.randomSuffix = func() (string, error) { return "666666666666666666666666", nil }
	request := box.HostInstallRequest{Path: target, OS: "linux", Architecture: "amd64", ExpectedIdentity: hostTestIdentity, Mode: box.HostRepair}

	result, err := runtime.EnsureHost(t.Context(), box.Connection{Destination: "trusted-host"}, request)
	if err != nil || result.Action != box.HostReplaced || result.PreviousVersion != "" || resolver.calls != 1 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, resolver.calls)
	}
	contents, err := os.ReadFile(target)
	if err != nil || string(contents) != want.contents {
		t.Fatalf("installed repaired runtime: err=%v", err)
	}
}

func TestEnsureHostRepairReplacesLegacyRuntimeMissingSetupCapabilities(t *testing.T) {
	testRemoteShell(t)
	root := t.TempDir()
	target := filepath.Join(root, "home", ".local", "bin", "schooner")
	legacy := writeHostArtifactAt(t, target, "v1.2.3", "1", "amd64")
	legacyContents := strings.Replace(legacy.contents, `"host.configure.v1",`, "", 1)
	if err := os.WriteFile(target, []byte(legacyContents), 0o755); err != nil {
		t.Fatal(err)
	}
	want := writeHostArtifact(t, root, "v1.2.3", "1", "amd64")
	resolver := &staticArtifactResolver{result: want}
	runtime := NewHost(testSSHExecutable(t), nil, "v1.2.3", resolver)
	runtime.randomSuffix = func() (string, error) { return "777777777777777777777777", nil }
	request := box.HostInstallRequest{Path: target, OS: "linux", Architecture: "amd64", ExpectedIdentity: hostTestIdentity, Mode: box.HostRepair}

	result, err := runtime.EnsureHost(t.Context(), box.Connection{Destination: "trusted-host"}, request)
	if err != nil || result.Action != box.HostReplaced || resolver.calls != 1 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, resolver.calls)
	}
}

func TestEnsureHostUpdateReplacesDifferentBuildMetadata(t *testing.T) {
	testRemoteShell(t)
	root := t.TempDir()
	target := filepath.Join(root, "home", ".local", "bin", "schooner")
	writeHostArtifactAt(t, target, "v1.2.3+build.1", "1", "amd64")
	want := writeHostArtifact(t, root, "v1.2.3+build.2", "1", "amd64")
	resolver := &staticArtifactResolver{result: want}
	runtime := NewHost(testSSHExecutable(t), nil, "v1.2.3+build.2", resolver)
	runtime.randomSuffix = func() (string, error) { return "888888888888888888888888", nil }
	request := box.HostInstallRequest{Path: target, OS: "linux", Architecture: "amd64", ExpectedIdentity: hostTestIdentity, Mode: box.HostUpdate}

	result, err := runtime.EnsureHost(t.Context(), box.Connection{Destination: "trusted-host"}, request)
	if err != nil || result.Action != box.HostReplaced || result.PreviousVersion != "v1.2.3+build.1" || result.Runtime.Version != "v1.2.3+build.2" || resolver.calls != 1 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, resolver.calls)
	}
}

func TestEnsureHostUpdateRetainsCompatibleNewerRuntime(t *testing.T) {
	testRemoteShell(t)
	root := t.TempDir()
	target := filepath.Join(root, "home", ".local", "bin", "schooner")
	writeHostArtifactAt(t, target, "v9.0.0", "1", "amd64")
	resolver := &staticArtifactResolver{result: writeHostArtifact(t, root, "v2.0.0", "1", "amd64")}
	runtime := NewHost(testSSHExecutable(t), nil, "v2.0.0", resolver)
	request := box.HostInstallRequest{Path: target, OS: "linux", Architecture: "amd64", ExpectedIdentity: hostTestIdentity, Mode: box.HostUpdate}

	result, err := runtime.EnsureHost(t.Context(), box.Connection{Destination: "trusted-host"}, request)
	if err != nil || result.Action != box.HostNewerRetained || result.Runtime.Version != "v9.0.0" || resolver.calls != 0 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, resolver.calls)
	}
}

func TestEnsureHostUpdateRefreshesDevelopmentRuntime(t *testing.T) {
	testRemoteShell(t)
	root := t.TempDir()
	target := filepath.Join(root, "home", ".local", "bin", "schooner")
	writeHostArtifactAt(t, target, "dev", "1", "amd64")
	want := writeHostArtifact(t, root, "dev", "1", "amd64")
	want.contents = strings.ReplaceAll(want.contents, "abc123", "def456")
	if err := os.WriteFile(want.result.Path, []byte(want.contents), 0o755); err != nil {
		t.Fatal(err)
	}
	want.result.SHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(want.contents)))
	resolver := &staticArtifactResolver{result: want}
	runtime := NewHost(testSSHExecutable(t), nil, "dev", resolver)
	runtime.randomSuffix = func() (string, error) { return "777777777777777777777777", nil }
	request := box.HostInstallRequest{Path: target, OS: "linux", Architecture: "amd64", ExpectedIdentity: hostTestIdentity, Mode: box.HostUpdate}

	result, err := runtime.EnsureHost(t.Context(), box.Connection{Destination: "trusted-host"}, request)
	if err != nil || result.Action != box.HostReplaced || result.PreviousVersion != "dev" || resolver.calls != 1 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, resolver.calls)
	}
	contents, err := os.ReadFile(target)
	if err != nil || string(contents) != want.contents {
		t.Fatalf("installed rebuilt development artifact: err=%v", err)
	}
}

func TestEnsureHostRepairRefreshesDevelopmentRuntime(t *testing.T) {
	testRemoteShell(t)
	root := t.TempDir()
	target := filepath.Join(root, "home", ".local", "bin", "schooner")
	writeHostArtifactAt(t, target, "dev", "1", "amd64")
	want := writeHostArtifact(t, root, "dev", "1", "amd64")
	want.contents = strings.ReplaceAll(want.contents, "abc123", "def456")
	if err := os.WriteFile(want.result.Path, []byte(want.contents), 0o755); err != nil {
		t.Fatal(err)
	}
	want.result.SHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(want.contents)))
	resolver := &staticArtifactResolver{result: want}
	runtime := NewHost(testSSHExecutable(t), nil, "dev", resolver)
	runtime.randomSuffix = func() (string, error) { return "777777777777777777777777", nil }
	request := box.HostInstallRequest{Path: target, OS: "linux", Architecture: "amd64", ExpectedIdentity: hostTestIdentity, Mode: box.HostRepair}

	result, err := runtime.EnsureHost(t.Context(), box.Connection{Destination: "trusted-host"}, request)
	if err != nil || result.Action != box.HostReplaced || result.PreviousVersion != "dev" || resolver.calls != 1 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, resolver.calls)
	}
	contents, err := os.ReadFile(target)
	if err != nil || string(contents) != want.contents {
		t.Fatalf("installed rebuilt development artifact: err=%v", err)
	}
}

func TestEnsureHostUpdateDirectsMissingRuntimeToSetup(t *testing.T) {
	testRemoteShell(t)
	root := t.TempDir()
	target := filepath.Join(root, "home", ".local", "bin", "schooner")
	resolver := &staticArtifactResolver{result: writeHostArtifact(t, root, "v2.0.0", "1", "amd64")}
	runtime := NewHost(testSSHExecutable(t), nil, "v2.0.0", resolver)
	request := box.HostInstallRequest{Path: target, OS: "linux", Architecture: "amd64", ExpectedIdentity: hostTestIdentity, Mode: box.HostUpdate}

	_, err := runtime.EnsureHost(t.Context(), box.Connection{Destination: "trusted-host"}, request)
	if box.ErrorCode(err) != "host_runtime_missing" || !strings.Contains(err.Error(), "box setup") || resolver.calls != 0 {
		t.Fatalf("error=%v calls=%d", err, resolver.calls)
	}
}

func TestEnsureHostNeverDowngradesAndToleratesCompatibleSkew(t *testing.T) {
	testRemoteShell(t)
	root := t.TempDir()
	target := filepath.Join(root, "home", ".local", "bin", "schooner")
	newer := writeHostArtifactAt(t, target, "v9.0.0", "1", "amd64")
	resolver := &staticArtifactResolver{result: writeHostArtifact(t, root, "v1.2.3", "1", "amd64")}
	runtime := NewHost(testSSHExecutable(t), nil, "v1.2.3", resolver)
	request := box.HostInstallRequest{Path: target, OS: "linux", Architecture: "amd64", ExpectedIdentity: hostTestIdentity}

	installed, err := runtime.EnsureHost(t.Context(), box.Connection{Destination: "trusted-host"}, request)
	if err != nil || installed.Runtime.Version != "v9.0.0" || resolver.calls != 0 {
		t.Fatalf("compatible newer host = %+v, err=%v, resolver calls=%d", installed, err, resolver.calls)
	}
	contents, _ := os.ReadFile(target)
	if string(contents) != newer.contents {
		t.Fatal("compatible newer host was replaced")
	}

	writeHostArtifactAt(t, target, "v9.0.0", "2", "amd64")
	_, err = runtime.EnsureHost(t.Context(), box.Connection{Destination: "trusted-host"}, request)
	if box.ErrorCode(err) != "host_runtime_incompatible" || !strings.Contains(err.Error(), "newer") || resolver.calls != 0 {
		t.Fatalf("incompatible newer error=%v calls=%d", err, resolver.calls)
	}
}

func TestEnsureHostUpdateDoesNotReplaceUnidentifiableExistingFile(t *testing.T) {
	testRemoteShell(t)
	root := t.TempDir()
	target := filepath.Join(root, "home", ".local", "bin", "schooner")
	existing := writeHostArtifactAt(t, target, "v9.0.0", "2", "amd64")
	if err := os.Chmod(target, 0o644); err != nil {
		t.Fatal(err)
	}
	resolver := &staticArtifactResolver{result: writeHostArtifact(t, root, "v2.0.0", "1", "amd64")}
	runtime := NewHost(testSSHExecutable(t), nil, "v2.0.0", resolver)
	request := box.HostInstallRequest{Path: target, OS: "linux", Architecture: "amd64", ExpectedIdentity: hostTestIdentity, Mode: box.HostUpdate}

	_, err := runtime.EnsureHost(t.Context(), box.Connection{Destination: "trusted-host"}, request)
	if box.ErrorCode(err) != "host_runtime_incompatible" || resolver.calls != 0 {
		t.Fatalf("error=%v resolver calls=%d", err, resolver.calls)
	}
	contents, readErr := os.ReadFile(target)
	if readErr != nil || string(contents) != existing.contents {
		t.Fatalf("unidentifiable existing runtime was replaced: err=%v", readErr)
	}
}

func TestEnsureHostFallsBackToVersionWhenRejectedHelloOmitsVersion(t *testing.T) {
	testRemoteShell(t)
	root := t.TempDir()
	target := filepath.Join(root, "home", ".local", "bin", "schooner")
	existing := writeHostArtifactAt(t, target, "v9.0.0", "0", "amd64")
	contents := strings.Replace(existing.contents, `"schooner_version":"v9.0.0"`, `"schooner_version":""`, 1)
	if contents == existing.contents {
		t.Fatal("test runtime hello version was not replaced")
	}
	if err := os.WriteFile(target, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
	resolver := &staticArtifactResolver{result: writeHostArtifact(t, root, "v2.0.0", "1", "amd64")}
	runtime := NewHost(testSSHExecutable(t), nil, "v2.0.0", resolver)
	request := box.HostInstallRequest{Path: target, OS: "linux", Architecture: "amd64", ExpectedIdentity: hostTestIdentity}

	_, err := runtime.EnsureHost(t.Context(), box.Connection{Destination: "trusted-host"}, request)
	if box.ErrorCode(err) != "host_runtime_incompatible" || !strings.Contains(err.Error(), "newer") || resolver.calls != 0 {
		t.Fatalf("error=%v resolver calls=%d", err, resolver.calls)
	}
	installed, readErr := os.ReadFile(target)
	if readErr != nil || string(installed) != contents {
		t.Fatalf("version-identifiable newer runtime was replaced: err=%v", readErr)
	}
}

func TestEnsureHostReinstallsAnOlderIncompatibleRuntime(t *testing.T) {
	testRemoteShell(t)
	root := t.TempDir()
	target := filepath.Join(root, "home", ".local", "bin", "schooner")
	writeHostArtifactAt(t, target, "v1.0.0", "0", "amd64")
	want := writeHostArtifact(t, root, "v2.0.0", "1", "amd64")
	resolver := &staticArtifactResolver{result: want}
	runtime := NewHost(testSSHExecutable(t), nil, "v2.0.0", resolver)
	runtime.randomSuffix = func() (string, error) { return "444444444444444444444444", nil }
	request := box.HostInstallRequest{Path: target, OS: "linux", Architecture: "amd64", ExpectedIdentity: hostTestIdentity}

	installed, err := runtime.EnsureHost(t.Context(), box.Connection{Destination: "trusted-host"}, request)
	if err != nil || installed.Runtime.Version != "v2.0.0" || installed.Action != box.HostReplaced || resolver.calls != 1 {
		t.Fatalf("installed=%+v err=%v calls=%d", installed, err, resolver.calls)
	}
	contents, _ := os.ReadFile(target)
	if string(contents) != want.contents {
		t.Fatal("older incompatible runtime was not atomically replaced")
	}
}

func TestEnsureHostRechecksTargetBeforePromotion(t *testing.T) {
	testRemoteShell(t)
	root := t.TempDir()
	target := filepath.Join(root, "home", ".local", "bin", "schooner")
	writeHostArtifactAt(t, target, "v1.0.0", "0", "amd64")
	candidate := writeHostArtifact(t, root, "v2.0.0", "1", "amd64")
	newer := writeHostArtifact(t, root, "v9.0.0", "2", "amd64")
	resolver := &staticArtifactResolver{result: candidate}
	runtime := NewHost(testSSHExecutable(t), nil, "v2.0.0", resolver)
	runtime.randomSuffix = func() (string, error) { return "777777777777777777777777", nil }
	request := box.HostInstallRequest{Path: target, OS: "linux", Architecture: "amd64", ExpectedIdentity: hostTestIdentity}
	t.Setenv("SCHOONER_TEST_REPLACE_TARGET", target)
	t.Setenv("SCHOONER_TEST_REPLACEMENT", newer.result.Path)
	t.Setenv("SCHOONER_TEST_REPLACE_MARKER", filepath.Join(root, "replaced"))

	_, err := runtime.EnsureHost(t.Context(), box.Connection{Destination: "trusted-host"}, request)
	if box.ErrorCode(err) != "host_runtime_incompatible" || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("error=%v code=%s", err, box.ErrorCode(err))
	}
	contents, readErr := os.ReadFile(target)
	if readErr != nil || string(contents) != newer.contents {
		t.Fatalf("newer concurrent runtime was overwritten: err=%v", readErr)
	}
}

func TestEnsureHostUsesFixedPOSIXShell(t *testing.T) {
	testRemoteShell(t)
	t.Setenv("SCHOONER_TEST_REQUIRE_POSIX_ENTRY", "1")
	root := t.TempDir()
	target := filepath.Join(root, "home", ".local", "bin", "schooner")
	resolver := &staticArtifactResolver{result: writeHostArtifact(t, root, "v1.2.3", "1", "amd64")}
	runtime := NewHost(testSSHExecutable(t), nil, "v1.2.3", resolver)
	runtime.randomSuffix = func() (string, error) { return "888888888888888888888888", nil }
	request := box.HostInstallRequest{Path: target, OS: "linux", Architecture: "amd64", ExpectedIdentity: hostTestIdentity}

	installed, err := runtime.EnsureHost(t.Context(), box.Connection{Destination: "non-posix-login-shell"}, request)
	if err != nil || installed.Runtime.Version != "v1.2.3" {
		t.Fatalf("installed=%+v err=%v", installed, err)
	}
}

func TestEnsureHostRejectsIdentityMismatchWithoutReplacement(t *testing.T) {
	testRemoteShell(t)
	root := t.TempDir()
	target := filepath.Join(root, "home", ".local", "bin", "schooner")
	existing := writeHostArtifactAtIdentity(t, target, "v1.2.3", "1", "amd64", "22222222-2222-4222-8222-222222222222")
	resolver := &staticArtifactResolver{result: writeHostArtifact(t, root, "v1.2.3", "1", "amd64")}
	runtime := NewHost(testSSHExecutable(t), nil, "v1.2.3", resolver)
	request := box.HostInstallRequest{Path: target, OS: "linux", Architecture: "amd64", ExpectedIdentity: hostTestIdentity}

	_, err := runtime.EnsureHost(t.Context(), box.Connection{Destination: "trusted-host"}, request)
	if box.ErrorCode(err) != "conflict" || resolver.calls != 0 {
		t.Fatalf("error=%v resolver calls=%d", err, resolver.calls)
	}
	contents, _ := os.ReadFile(target)
	if string(contents) != existing.contents {
		t.Fatal("identity-mismatched host runtime was replaced")
	}
}

func TestEnsureHostKeepsOldRuntimeWhenPromotionIsInterrupted(t *testing.T) {
	testRemoteShell(t)
	t.Setenv("SCHOONER_TEST_FAIL_PROMOTE", "1")
	root := t.TempDir()
	target := filepath.Join(root, "home", ".local", "bin", "schooner")
	old := writeHostArtifactAt(t, target, "v1.0.0", "1", "amd64")
	resolver := &staticArtifactResolver{result: writeHostArtifact(t, root, "v2.0.0", "1", "amd64")}
	runtime := NewHost(testSSHExecutable(t), nil, "v2.0.0", resolver)
	runtime.randomSuffix = func() (string, error) { return "222222222222222222222222", nil }
	request := box.HostInstallRequest{Path: target, OS: "linux", Architecture: "amd64", ExpectedIdentity: hostTestIdentity, Mode: box.HostUpdate}

	_, err := runtime.EnsureHost(t.Context(), box.Connection{Destination: "trusted-host"}, request)
	if box.ErrorCode(err) != "host_runtime_install_failed" {
		t.Fatalf("error=%v", err)
	}
	contents, _ := os.ReadFile(target)
	if string(contents) != old.contents {
		t.Fatal("interrupted promotion changed the existing runtime")
	}
	if stages, _ := filepath.Glob(target + ".stage-*"); len(stages) != 0 {
		t.Fatalf("staging files remain: %v", stages)
	}
}

func TestEnsureHostPreservesConnectionFailureDuringStagedHandshake(t *testing.T) {
	testRemoteShell(t)
	t.Setenv("SCHOONER_TEST_FAIL_CANDIDATE_CONNECTION", "1")
	root := t.TempDir()
	target := filepath.Join(root, "home", ".local", "bin", "schooner")
	resolver := &staticArtifactResolver{result: writeHostArtifact(t, root, "v1.2.3", "1", "amd64")}
	runtime := NewHost(testSSHExecutable(t), nil, "v1.2.3", resolver)
	runtime.randomSuffix = func() (string, error) { return "666666666666666666666666", nil }
	request := box.HostInstallRequest{Path: target, OS: "linux", Architecture: "amd64", ExpectedIdentity: hostTestIdentity}

	_, err := runtime.EnsureHost(t.Context(), box.Connection{Destination: "trusted-host"}, request)
	if box.ErrorCode(err) != "connection_failed" {
		t.Fatalf("error=%v code=%s", err, box.ErrorCode(err))
	}
}

func TestEnsureHostDetectsTransferCorruptionAndWrongArchitecture(t *testing.T) {
	for _, test := range []struct {
		name            string
		artifactArch    string
		corruptTransfer bool
		wantCode        string
	}{
		{name: "checksum", artifactArch: "amd64", corruptTransfer: true, wantCode: "checksum_mismatch"},
		{name: "architecture", artifactArch: "arm64", wantCode: "unsupported"},
	} {
		t.Run(test.name, func(t *testing.T) {
			testRemoteShell(t)
			if test.corruptTransfer {
				t.Setenv("SCHOONER_TEST_CORRUPT_TRANSFER", "1")
			}
			root := t.TempDir()
			target := filepath.Join(root, "home", ".local", "bin", "schooner")
			resolved := writeHostArtifact(t, root, "v1.2.3", "1", test.artifactArch)
			// Metadata deliberately selects the requested artifact platform; the
			// staged handshake remains authoritative about executable architecture.
			resolved.result.Platform.Arch = "amd64"
			resolver := &staticArtifactResolver{result: resolved}
			runtime := NewHost(testSSHExecutable(t), nil, "v1.2.3", resolver)
			runtime.randomSuffix = func() (string, error) { return "333333333333333333333333", nil }
			request := box.HostInstallRequest{Path: target, OS: "linux", Architecture: "amd64", ExpectedIdentity: hostTestIdentity}

			_, err := runtime.EnsureHost(t.Context(), box.Connection{Destination: "trusted-host"}, request)
			if box.ErrorCode(err) != test.wantCode {
				t.Fatalf("error=%v code=%s", err, box.ErrorCode(err))
			}
			if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
				t.Fatalf("failed candidate was promoted: %v", statErr)
			}
		})
	}
}

func TestReplacementAllowedRejectsUnorderedDevelopmentAndInvalidVersions(t *testing.T) {
	for _, pair := range [][2]string{{"dev", "v1.0.0"}, {"v1.0.0", "dev"}, {"v1.0.0", "not-a-version"}} {
		if err := replacementAllowed(pair[0], pair[1]); box.ErrorCode(err) != "host_runtime_incompatible" {
			t.Fatalf("replacementAllowed(%q, %q) = %v", pair[0], pair[1], err)
		}
	}
	if err := replacementAllowed("v1.2.3", "v1.2.2"); err != nil {
		t.Fatal(err)
	}
}

type resolvedArtifact struct {
	result   artifact.Result
	contents string
}

type staticArtifactResolver struct {
	result resolvedArtifact
	err    error
	calls  int
}

func (r *staticArtifactResolver) Resolve(_ context.Context, _ string, _ artifact.Platform) (artifact.Result, error) {
	r.calls++
	return r.result.result, r.err
}

func writeHostArtifact(t *testing.T, directory, version, protocol, architecture string) resolvedArtifact {
	t.Helper()
	return writeHostArtifactAtIdentity(t, filepath.Join(directory, "artifact-"+version+"-"+architecture), version, protocol, architecture, hostTestIdentity)
}

func writeHostArtifactAt(t *testing.T, target, version, protocol, architecture string) resolvedArtifact {
	t.Helper()
	return writeHostArtifactAtIdentity(t, target, version, protocol, architecture, hostTestIdentity)
}

func writeHostArtifactAtIdentity(t *testing.T, target, version, protocol, architecture, identity string) resolvedArtifact {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	hello := fmt.Sprintf(`{"schema_version":"1","protocol_version":%q,"schooner_version":%q,"commit":"abc123","box_identity":%q,"os":"linux","architecture":%q,"capabilities":["host.configure.v1","host.doctor.v1","host.hello.v1","host.inspect.v2","worktree.inspect.v1","worktree.list.v1"]}`, protocol, version, identity, architecture)
	inspection := fmt.Sprintf(`{"schema_version":"1","protocol_version":"1","os_id":"ubuntu","os_version":"24.04","architecture":%q,"home":"/home/alice","box_identity":%q,"worktree_root":"/home/alice/schooner","worktree_root_exists":true,"git":{"available":true,"version":"git version 2.43.0"},"tmux":{"available":true,"version":"tmux 3.4"},"passwordless_sudo":true}`, architecture, identity)
	versionDocument := fmt.Sprintf(`{"schema_version":"1","version":%q,"commit":"abc123","built_at":null,"go_version":"go1.27.0","os":"linux","arch":%q}`, version, architecture)
	contents := "#!/bin/sh\ncase \"$1 $2 $3\" in\n  'host hello '*) printf '%s\\n' '" + hello + "' ;;\n  'host inspect '*) cat >/dev/null; printf '%s\\n' '" + inspection + "' ;;\n  '--output json version') printf '%s\\n' '" + versionDocument + "' ;;\n  *) exit 64 ;;\nesac\n"
	if err := os.WriteFile(target, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(contents)))
	return resolvedArtifact{result: artifact.Result{Path: target, Version: version, Platform: artifact.Platform{OS: "linux", Arch: architecture}, SHA256: digest}, contents: contents}
}

func testRemoteShell(t *testing.T) {
	t.Helper()
	directory := t.TempDir()
	helpers := map[string]string{
		"sha256sum": "#!/bin/sh\nif [ -x /usr/bin/sha256sum ]; then exec /usr/bin/sha256sum \"$@\"; fi\n[ \"$1\" = -- ] && shift\nexec /usr/bin/shasum -a 256 \"$@\"\n",
		"flock":     "#!/bin/sh\nexit 0\n",
		"chmod":     "#!/bin/sh\nmode=$1; shift\n[ \"$1\" = -- ] && shift\nexec /bin/chmod \"$mode\" \"$@\"\n",
		"mkdir":     "#!/bin/sh\noption=$1; shift\n[ \"$1\" = -- ] && shift\nexec /bin/mkdir \"$option\" \"$@\"\n",
		"mv":        "#!/bin/sh\noption=$1; shift\n[ \"$1\" = -- ] && shift\nexec /bin/mv \"$option\" \"$@\"\n",
		"rm":        "#!/bin/sh\noption=$1; shift\n[ \"$1\" = -- ] && shift\nexec /bin/rm \"$option\" \"$@\"\n",
	}
	for name, contents := range helpers {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(contents), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func testSSHExecutable(t *testing.T) string {
	t.Helper()
	return writeExecutable(t, `#!/bin/sh
command=
for argument do command=$argument; done
case "$command" in
  /bin/sh\ -c\ *) ;;
  *) if [ "${SCHOONER_TEST_REQUIRE_POSIX_ENTRY:-}" = 1 ]; then exit 64; fi ;;
esac
case "$command" in
  *"host hello"*)
    runtime_path=$(printf %s "${command##* }" | base64 -d)
    case "$runtime_path" in *.stage-*) candidate=1 ;; *) candidate= ;; esac
    if [ -n "$candidate" ] && [ "${SCHOONER_TEST_FAIL_CANDIDATE_CONNECTION:-}" = 1 ]; then
      printf 'connection reset during candidate handshake\n' >&2
      exit 255
    fi ;;
esac
case "$command" in
  *"flock -x 9"*)
    if [ -n "${SCHOONER_TEST_REPLACE_TARGET:-}" ] && [ ! -e "$SCHOONER_TEST_REPLACE_MARKER" ]; then
      /bin/cp "$SCHOONER_TEST_REPLACEMENT" "$SCHOONER_TEST_REPLACE_TARGET" || exit 1
      /usr/bin/touch "$SCHOONER_TEST_REPLACE_MARKER" || exit 1
    fi
    if [ "${SCHOONER_TEST_FAIL_PROMOTE:-}" = 1 ]; then exit 75; fi ;;
esac
case "$command" in
  *"cat >&3"*)
    if [ "${SCHOONER_TEST_CORRUPT_TRANSFER:-}" = 1 ]; then
      /usr/bin/sed 's/host/most/g' | /bin/sh -c "$command"
      exit $?
    fi ;;
esac
exec /bin/sh -c "$command"
`)
}
