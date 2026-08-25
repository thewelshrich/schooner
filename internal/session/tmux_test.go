package session

import (
	"context"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/thewelshrich/schooner/internal/process"
	"github.com/thewelshrich/schooner/internal/repository"
)

const testSessionID = "11111111-1111-4111-8111-111111111111"

func TestClassifyRowRequiresCompleteVersionedMetadata(t *testing.T) {
	base := tmuxRow{tmuxID: "$1", name: "work", created: "1720000000", activity: "1720000300", attached: "0"}
	if got := classifyRow(base); got.Ownership != Unmanaged {
		t.Fatalf("unmanaged row = %+v", got)
	}
	managed := base
	managed.schema, managed.id, managed.kind = SchemaVersion, testSessionID, KindShell
	managed.managedCreated, managed.worktree = "2024-07-03T09:46:40Z", "/work/repo"
	got := classifyRow(managed)
	if got.Ownership != Managed || got.ID != testSessionID || got.WorktreePath != "/work/repo" || !got.CreatedAt.Equal(time.Date(2024, 7, 3, 9, 46, 40, 0, time.UTC)) {
		t.Fatalf("managed row = %+v", got)
	}
	managed.id = "bad"
	if got = classifyRow(managed); got.Ownership != Invalid || !got.SchoonerMetadata {
		t.Fatalf("partial row = %+v", got)
	}
}

func TestInvalidUnmanagedMetadataDoesNotClaimSchoonerOwnership(t *testing.T) {
	got := classifyRow(tmuxRow{tmuxID: "bad", name: "external", created: "bad", activity: "bad", attached: "bad"})
	if got.Ownership != Invalid || got.SchoonerMetadata {
		t.Fatalf("invalid unmanaged row = %+v", got)
	}
}

func TestAssociateUnmanagedRequiresEveryPaneToMapToOneWorktree(t *testing.T) {
	worktrees := []liveWorktree{{path: "/work/repo", relative: "repo", common: "/work/repo/.git"}, {path: "/work/other", relative: "other", common: "/work/other/.git"}}
	value := Session{Ownership: Unmanaged}
	associateUnmanaged(&value, []string{"/work/repo", "/work/repo/subdir"}, worktrees)
	if value.Association != AssociationLive || value.WorktreePath != "/work/repo" {
		t.Fatalf("associated = %+v", value)
	}
	value = Session{Ownership: Unmanaged}
	associateUnmanaged(&value, []string{"/work/repo", "/work/other"}, worktrees)
	if value.Association != AssociationAmbiguous {
		t.Fatalf("ambiguous = %+v", value)
	}
}

func TestSessionIdentifiersAreUUIDv4(t *testing.T) {
	value, err := randomID()
	if err != nil || !validID(value) || len(compactID(value)) != 32 {
		t.Fatalf("id = %q, err = %v", value, err)
	}
}

func TestManagedSessionConditionEscapesWorktreeFormatCharacters(t *testing.T) {
	value := Session{ID: testSessionID, Kind: KindShell, CreatedAt: time.Date(2024, 7, 3, 9, 46, 40, 0, time.UTC), WorktreePath: "/work/#{repo,one}"}
	condition := managedSessionCondition(value)
	if !strings.Contains(condition, "#{==:#{"+WorktreePathOption+"},/work/##{repo#,one#}}") {
		t.Fatalf("condition did not escape the Worktree path: %s", condition)
	}
}

type fakeTmux struct {
	path                string
	row                 string
	calls               [][]string
	killed              bool
	captured            bool
	content             string
	replaceBeforeAtomic bool
}

func (f *fakeTmux) Run(_ context.Context, _ int, name string, args ...string) (process.Result, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	switch args[0] {
	case "list-sessions":
		return process.Result{Stdout: []byte(f.row)}, nil
	case "list-panes":
		if f.row == "" {
			return process.Result{}, nil
		}
		return process.Result{Stdout: []byte("$4\t" + f.path + "\n")}, nil
	case "new-session":
		f.row = "$4\tschooner-111111111111\t1720000000\t1720000000\t0\t1\t" + testSessionID + "\tshell\t2024-07-03T09:46:40Z\t" + f.path + "\n"
		return process.Result{Stdout: []byte("$4\n")}, nil
	case "if-shell":
		if f.replaceBeforeAtomic {
			f.row = "$4\texternal\t1720000000\t1720000000\t0\t\t\t\t\t\n"
			f.replaceBeforeAtomic = false
		}
		managed := strings.Contains(f.row, "\t1\t"+testSessionID+"\tshell\t")
		if !managed {
			return process.Result{Stdout: []byte(managedActionRefused + "\n")}, nil
		}
		action := args[5]
		switch {
		case strings.Contains(action, "capture-pane"):
			f.captured = true
			return process.Result{Stdout: []byte(managedActionGranted + "\n" + f.content)}, nil
		case strings.Contains(action, "kill-session"):
			f.killed, f.row = true, ""
			return process.Result{Stdout: []byte(managedActionGranted + "\n")}, nil
		default:
			return process.Result{}, &exec.ExitError{}
		}
	case "capture-pane", "kill-session":
		return process.Result{}, &exec.ExitError{}
	default:
		return process.Result{}, &exec.ExitError{}
	}
}

func TestManagedActionsRefuseReusedUnmanagedTarget(t *testing.T) {
	root, worktree := initializeSessionWorktree(t)
	for _, operation := range []string{"logs", "stop"} {
		t.Run(operation, func(t *testing.T) {
			fake := &fakeTmux{
				path:                worktree,
				row:                 "$4\tmanaged\t1720000000\t1720000000\t0\t1\t" + testSessionID + "\tshell\t2024-07-03T09:46:40Z\t" + worktree + "\n",
				content:             "private unmanaged output\n",
				replaceBeforeAtomic: true,
			}
			manager, err := New(root, filepath.Join(t.TempDir(), "operations", "git"))
			if err != nil {
				t.Fatal(err)
			}
			manager.commands = fake
			if operation == "logs" {
				_, err = manager.Logs(t.Context(), testSessionID, 2)
			} else {
				_, err = manager.Stop(t.Context(), testSessionID)
			}
			if code := repository.ErrorCode(err); code != repository.CodeConflict {
				t.Fatalf("error code = %q, error = %v", code, err)
			}
			if fake.captured || fake.killed {
				t.Fatalf("unmanaged target was touched: captured=%t killed=%t", fake.captured, fake.killed)
			}
		})
	}
}

func TestServiceStartsReusesCapturesAndStopsManagedSession(t *testing.T) {
	canonicalRoot, canonicalWorktree := initializeSessionWorktree(t)
	fake := &fakeTmux{path: canonicalWorktree, content: "one\ntwo\n"}
	manager, err := New(canonicalRoot, filepath.Join(t.TempDir(), "operations", "git"))
	if err != nil {
		t.Fatal(err)
	}
	manager.commands = fake
	manager.newID = func() (string, error) { return testSessionID, nil }
	manager.now = func() time.Time { return time.Date(2024, 7, 3, 9, 46, 40, 0, time.UTC) }

	started, err := manager.Start(t.Context(), "repo")
	if err != nil || !started.Created || started.Session.ID != testSessionID || started.Session.Association != AssociationLive {
		t.Fatalf("start = %+v, %v", started, err)
	}
	reused, err := manager.Start(t.Context(), "repo")
	if err != nil || reused.Created || reused.Session.TmuxID != "$4" {
		t.Fatalf("reuse = %+v, %v", reused, err)
	}
	attachment, err := manager.Attachment(t.Context(), testSessionID, false)
	if err != nil || attachment.Path != "tmux" || !reflect.DeepEqual(attachment.Args, []string{"attach-session", "-t", "$4"}) {
		t.Fatalf("attachment = %+v, %v", attachment, err)
	}
	logs, err := manager.Logs(t.Context(), testSessionID, 2)
	if err != nil || logs.Content != "one\ntwo\n" || logs.Lines != 2 {
		t.Fatalf("logs = %+v, %v", logs, err)
	}
	stopped, err := manager.Stop(t.Context(), testSessionID)
	if err != nil || !stopped.Stopped || !fake.killed {
		t.Fatalf("stop = %+v, %v", stopped, err)
	}
	if _, err = manager.Resolve(t.Context(), testSessionID); err == nil {
		t.Fatal("stopped Session still resolved")
	}
}

func initializeSessionWorktree(t *testing.T) (string, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "worktrees")
	worktree := filepath.Join(root, "repo")
	if output, err := exec.Command("git", "init", worktree).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, output)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	canonicalWorktree, err := filepath.EvalSymlinks(worktree)
	if err != nil {
		t.Fatal(err)
	}
	return canonicalRoot, canonicalWorktree
}

func TestTmuxUseFailsClosedOnMalformedSchoonerMetadata(t *testing.T) {
	fake := &fakeTmux{row: "$4\tbroken\t1720000000\t1720000000\t0\t1\tbad\tshell\t2024-07-03T09:46:40Z\t/work/repo\n"}
	_, err := (TmuxUse{commands: fake}).ManagedSessions(t.Context(), "/work/repo")
	if err == nil || !strings.Contains(err.Error(), "invalid Schooner Session metadata") {
		t.Fatalf("error = %v", err)
	}
}
