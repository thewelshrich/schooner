package boxtarget

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/thewelshrich/schooner/internal/box"
	"github.com/thewelshrich/schooner/internal/config"
	hostruntime "github.com/thewelshrich/schooner/internal/runtime"
	"github.com/thewelshrich/schooner/internal/runtime/host"
	"github.com/thewelshrich/schooner/internal/runtime/ssh"
)

func TestDirectAndSSHAdaptersConformForWorktreeObservation(t *testing.T) {
	direct := directConformanceTarget(t)
	remote := sshConformanceTarget(t)
	for _, harness := range []struct {
		name string
		root string
		make func() Target
	}{
		{name: "direct", root: direct.state.worktreeRoot, make: func() Target { return direct }},
		{name: "ssh", root: remote.state.worktreeRoot, make: func() Target { return remote }},
	} {
		t.Run(harness.name, func(t *testing.T) {
			target := harness.make()
			catalog, err := target.ListWorktrees(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if catalog.WorktreeRoot != harness.root || len(catalog.Repositories) != 1 || catalog.Repositories[0].Primary == nil || catalog.Repositories[0].Primary.RelativePath != "repo" {
				t.Fatalf("catalog = %+v", catalog)
			}
			inspection, err := target.InspectWorktree(t.Context(), "repo")
			if err != nil || inspection.Worktree.RelativePath != "repo" || inspection.WorktreeRoot != harness.root {
				t.Fatalf("inspection = %+v, error = %v", inspection, err)
			}
			if _, err = target.InspectWorktree(t.Context(), "../outside"); box.ErrorCode(err) != "invalid_input" {
				t.Fatalf("invalid selector error = %v, code = %s", err, box.ErrorCode(err))
			}
			sessions, err := target.ListSessions(t.Context())
			if err != nil || sessions.WorktreeRoot != harness.root || len(sessions.Sessions) != 0 {
				t.Fatalf("Sessions = %+v, error = %v", sessions, err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if _, err = target.ListWorktrees(ctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation error = %v", err)
			}
			changed := *target.state
			changed.worktreeRoot += "-changed"
			if _, err = (Target{state: &changed}).ListWorktrees(t.Context()); box.ErrorCode(err) != "conflict" {
				t.Fatalf("root drift error = %v, code = %s", err, box.ErrorCode(err))
			}
		})
	}
}

func directConformanceTarget(t *testing.T) Target {
	t.Helper()
	home := t.TempDir()
	root := filepath.Join(home, "worktrees")
	repositoryPath := filepath.Join(root, "repo")
	if output, err := exec.Command("git", "init", repositoryPath).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, output)
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	writeIdentity(t, home, testIdentity)
	configuration := filepath.Join(home, "config.toml")
	if err = config.Write(configuration, config.Host{WorktreeRoot: root}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SCHOONER_CONFIG", configuration)
	bin := filepath.Join(home, "bin")
	if err = os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(bin, "tmux"), []byte("#!/bin/sh\nprintf 'no server running\\n' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	state := &targetState{boxIdentity: testIdentity, worktreeRoot: root, direct: true}
	state.run = directAdapter{runtime: host.NewAtHome(hostruntime.BuildInfo{}, home), state: state, nonInteractive: true}
	return Target{state: state}
}

func sshConformanceTarget(t *testing.T) Target {
	t.Helper()
	root := "/remote/worktrees"
	repositoryValue := fmt.Sprintf(`{"common_directory":%q,"primary":{"path":%q,"relative_path":"repo","git_directory":%q,"kind":"primary","branch":"main","detached":false,"status":{"staged":0,"unstaged":0,"untracked":0,"conflicted":0}},"linked":[]}`, root+"/repo/.git", root+"/repo", root+"/repo/.git")
	worktreeValue := fmt.Sprintf(`{"path":%q,"relative_path":"repo","git_directory":%q,"kind":"primary","branch":"main","detached":false,"status":{"staged":0,"unstaged":0,"untracked":0,"conflicted":0}}`, root+"/repo", root+"/repo/.git")
	hello := fmt.Sprintf(`{"schema_version":"1","protocol_version":"1","schooner_version":"dev","commit":"test","box_identity":%q,"os":"linux","architecture":"amd64","capabilities":["session.list.v1","worktree.inspect.v1","worktree.list.v1"]}`, testIdentity)
	catalog := fmt.Sprintf(`{"schema_version":"1","protocol_version":"1","box_identity":%q,"worktree_root":%q,"repositories":[%s],"warnings":[]}`, testIdentity, root, repositoryValue)
	inspection := fmt.Sprintf(`{"schema_version":"1","protocol_version":"1","box_identity":%q,"worktree_root":%q,"repository":%s,"worktree":%s,"warnings":[]}`, testIdentity, root, repositoryValue, worktreeValue)
	invalid := fmt.Sprintf(`{"schema_version":"1","protocol_version":"1","box_identity":%q,"error":{"code":"invalid_input","message":"worktree selector is invalid"}}`, testIdentity)
	sessions := fmt.Sprintf(`{"schema_version":"1","protocol_version":"1","box_identity":%q,"worktree_root":%q,"sessions":[]}`, testIdentity, root)
	script := fmt.Sprintf(`#!/bin/sh
case " $* " in
  *"host hello"*) printf '%%s\n' '%s' ;;
  *"host worktree list"*) cat >/dev/null; printf '%%s\n' '%s' ;;
  *"host worktree inspect"*) payload=$(cat); case "$payload" in *"../outside"*) printf '%%s\n' '%s' ;; *) printf '%%s\n' '%s' ;; esac ;;
  *"host session list"*) cat >/dev/null; printf '%%s\n' '%s' ;;
  *) exit 64 ;;
esac
`, hello, catalog, invalid, inspection, sessions)
	path := filepath.Join(t.TempDir(), "ssh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	state := &targetState{boxName: "work", boxIdentity: testIdentity, worktreeRoot: root}
	state.run = sshAdapter{
		runtime:    ssh.NewHost(path, nil, "dev", nil),
		state:      state,
		connection: box.Connection{Destination: "work-host", BatchMode: true},
		installed:  box.HostRuntime{Path: "/home/alice/.local/bin/schooner"},
	}
	return Target{state: state}
}
