package session

import (
	"context"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thewelshrich/schooner/internal/process"
	"github.com/thewelshrich/schooner/internal/repository"
)

const testSessionID = "11111111-1111-4111-8111-111111111111"
const testLegacySessionID = "legacy-session"

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

func TestClassifyRowPreservesLegacySchemaOneMetadata(t *testing.T) {
	row := tmuxRow{tmuxID: "$1", name: "work", created: "1720000000", activity: "1720000300", attached: "0", schema: LegacySchemaVersion, id: testLegacySessionID, worktree: "/work/repo"}
	got := classifyRow(row)
	if got.Ownership != Managed || !got.LegacyMetadata || got.ID != testLegacySessionID || got.Kind != KindShell || got.WorktreePath != "/work/repo" {
		t.Fatalf("legacy managed row = %+v", got)
	}
	condition := managedSessionCondition(got)
	if !strings.Contains(condition, "#{==:#{"+SchemaOption+"},"+LegacySchemaVersion+"}") || !strings.Contains(condition, "#{==:#{"+KindOption+"},}") {
		t.Fatalf("legacy ownership condition = %s", condition)
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

func TestParsePanesUsesByteLengthsForControlCharacters(t *testing.T) {
	first := "/work/repo/tab\tdirectory"
	second := "/work/repo/new\nline"
	output := []byte("$1\t" + strconv.Itoa(len(first)) + "\t" + first + "\n$2\t" + strconv.Itoa(len(second)) + "\t" + second + "\n")
	got, err := parsePanes(output)
	if err != nil {
		t.Fatal(err)
	}
	if len(got["$1"]) != 1 || got["$1"][0] != first || len(got["$2"]) != 1 || got["$2"][0] != second {
		t.Fatalf("panes = %#v", got)
	}
}

func TestInsideTmuxRecognizesOnlyPinnedDefaultSocket(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-501/work,123,0")
	if InsideTmux() {
		t.Fatal("named tmux server was treated as Schooner's pinned server")
	}
	t.Setenv("TMUX", "/tmp/tmux-501/default,123,0")
	if !InsideTmux() {
		t.Fatal("default tmux server was not recognized")
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
	return f.run(name, args...)
}

func (f *fakeTmux) RunTail(_ context.Context, maximum int, name string, args ...string) (process.Result, error) {
	result, err := f.run(name, args...)
	if len(result.Stdout) > maximum {
		result.Stdout = result.Stdout[len(result.Stdout)-maximum:]
		result.Truncated = true
	}
	return result, err
}

func (f *fakeTmux) run(name string, args ...string) (process.Result, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	switch args[0] {
	case "list-sessions":
		return process.Result{Stdout: frameFakeRows(f.row)}, nil
	case "list-panes":
		if f.row == "" {
			return process.Result{}, nil
		}
		return process.Result{Stdout: []byte("$4\t" + strconv.Itoa(len(f.path)) + "\t" + f.path + "\n")}, nil
	case "new-session":
		f.row = "$4\tschooner-111111111111\t1720000000\t1720000000\t0\t" + SchemaVersion + "\t" + testSessionID + "\tshell\t2024-07-03T09:46:40Z\t" + f.path + "\n"
		return process.Result{Stdout: []byte("$4\n")}, nil
	case "if-shell":
		if f.replaceBeforeAtomic {
			f.row = "$4\texternal\t1720000000\t1720000000\t0\t\t\t\t\t\n"
			f.replaceBeforeAtomic = false
		}
		managed := strings.Contains(f.row, "\t"+SchemaVersion+"\t"+testSessionID+"\tshell\t") || strings.Contains(f.row, "\t"+LegacySchemaVersion+"\t"+testLegacySessionID+"\t\t\t")
		if !managed {
			return process.Result{Stdout: []byte(managedActionRefused + "\n")}, nil
		}
		action := args[5]
		switch {
		case strings.Contains(action, "capture-pane"):
			f.captured = true
			return process.Result{Stdout: []byte(f.content + managedActionGranted + "\n")}, nil
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

func frameFakeRows(rows string) []byte {
	var output strings.Builder
	for _, row := range strings.Split(strings.TrimSuffix(rows, "\n"), "\n") {
		if row == "" {
			continue
		}
		fields := strings.Split(row, "\t")
		for index, field := range fields {
			if index != 0 {
				output.WriteByte('\t')
			}
			output.WriteString(strconv.Itoa(len(field)))
			output.WriteByte('\t')
			output.WriteString(field)
		}
		output.WriteByte('\n')
	}
	return []byte(output.String())
}

func TestParseRowsLengthFramesUnmanagedNames(t *testing.T) {
	name := "external\tname\ncontinued"
	fields := []string{"$8", name, "1720000000", "1720000300", "0", "", "", "", "", ""}
	var framed strings.Builder
	for index, field := range fields {
		if index != 0 {
			framed.WriteByte('\t')
		}
		framed.WriteString(strconv.Itoa(len(field)))
		framed.WriteByte('\t')
		framed.WriteString(field)
	}
	framed.WriteByte('\n')
	rows, err := parseRows([]byte(framed.String()))
	if err != nil || len(rows) != 1 || rows[0].name != name {
		t.Fatalf("rows = %+v, error = %v", rows, err)
	}
}

func TestLegacyManagedSessionCanBeCapturedAndStopped(t *testing.T) {
	root, worktree := initializeSessionWorktree(t)
	fake := &fakeTmux{
		path:    worktree,
		row:     "$4\tlegacy\t1720000000\t1720000000\t0\t" + LegacySchemaVersion + "\t" + testLegacySessionID + "\t\t\t" + worktree + "\n",
		content: "legacy output\n",
	}
	manager, err := New(root, filepath.Join(t.TempDir(), "operations", "git"))
	if err != nil {
		t.Fatal(err)
	}
	manager.commands = fake
	logs, err := manager.Logs(t.Context(), testLegacySessionID, 2)
	if err != nil || logs.Content != "legacy output\n" || !fake.captured {
		t.Fatalf("logs = %+v, captured=%t, error=%v", logs, fake.captured, err)
	}
	stopped, err := manager.Stop(t.Context(), testLegacySessionID)
	if err != nil || !stopped.Stopped || !fake.killed {
		t.Fatalf("stop = %+v, killed=%t, error=%v", stopped, fake.killed, err)
	}
}

func TestLegacyManagedIDOutranksUnmanagedSelectorNamespace(t *testing.T) {
	root, worktree := initializeSessionWorktree(t)
	fake := &fakeTmux{
		path: worktree,
		row:  "$4\tlegacy\t1720000000\t1720000000\t0\t" + LegacySchemaVersion + "\ttmux:$3\t\t\t" + worktree + "\n",
	}
	manager, err := New(root, filepath.Join(t.TempDir(), "operations", "git"))
	if err != nil {
		t.Fatal(err)
	}
	manager.commands = fake
	value, err := manager.Resolve(t.Context(), "tmux:$3")
	if err != nil || value.Ownership != Managed || value.ID != "tmux:$3" || value.TmuxID != "$4" {
		t.Fatalf("resolved = %+v, error = %v", value, err)
	}
}

func TestManagedActionsRefuseReusedUnmanagedTarget(t *testing.T) {
	root, worktree := initializeSessionWorktree(t)
	for _, operation := range []string{"logs", "stop"} {
		t.Run(operation, func(t *testing.T) {
			fake := &fakeTmux{
				path:                worktree,
				row:                 "$4\tmanaged\t1720000000\t1720000000\t0\t" + SchemaVersion + "\t" + testSessionID + "\tshell\t2024-07-03T09:46:40Z\t" + worktree + "\n",
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
	fake := &fakeTmux{path: canonicalWorktree, content: "zero\none\ntwo\n"}
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
	if err != nil || attachment.Path != "tmux" || len(attachment.Args) != 9 || attachment.Args[0] != "-L" || attachment.Args[1] != tmuxSocketName || attachment.Args[2] != "if-shell" || !strings.Contains(attachment.Args[6], testSessionID) || attachment.Args[7] != "attach-session -t $4" || len(attachment.ExcludedEnvironment) != 2 {
		t.Fatalf("attachment = %+v, %v", attachment, err)
	}
	logs, err := manager.Logs(t.Context(), testSessionID, 2)
	if err != nil || logs.Content != "one\ntwo\n" || logs.Lines != 2 || !logs.Truncated {
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

func TestLogsRetainNewestOutputWhenByteBoundIsExceeded(t *testing.T) {
	root, worktree := initializeSessionWorktree(t)
	fake := &fakeTmux{
		path:    worktree,
		row:     "$4\tmanaged\t1720000000\t1720000000\t0\t" + SchemaVersion + "\t" + testSessionID + "\tshell\t2024-07-03T09:46:40Z\t" + worktree + "\n",
		content: "oldest-output\n" + strings.Repeat("middle-output\n", MaxLogBytes) + "newest-output\n",
	}
	manager, err := New(root, filepath.Join(t.TempDir(), "operations", "git"))
	if err != nil {
		t.Fatal(err)
	}
	manager.commands = fake
	logs, err := manager.Logs(t.Context(), testSessionID, MaxLogLines)
	if err != nil {
		t.Fatal(err)
	}
	if !logs.Truncated || !strings.HasSuffix(logs.Content, "newest-output\n") || strings.Contains(logs.Content, "oldest-output\n") {
		t.Fatalf("bounded logs did not retain newest output: truncated=%t prefix=%q suffix=%q", logs.Truncated, logs.Content[:min(16, len(logs.Content))], logs.Content[max(0, len(logs.Content)-24):])
	}
}

func TestLogsAndStopResolveOnlyExplicitSessionIDs(t *testing.T) {
	root, worktree := initializeSessionWorktree(t)
	fake := &fakeTmux{
		path: worktree,
		row:  "$4\tmanaged\t1720000000\t1720000000\t0\t" + SchemaVersion + "\t" + testSessionID + "\tshell\t2024-07-03T09:46:40Z\t" + worktree + "\n",
	}
	manager, err := New(root, filepath.Join(t.TempDir(), "operations", "git"))
	if err != nil {
		t.Fatal(err)
	}
	manager.commands = fake
	for _, operation := range []string{"logs", "stop"} {
		if operation == "logs" {
			_, err = manager.Logs(t.Context(), "repo", 2)
		} else {
			_, err = manager.Stop(t.Context(), "repo")
		}
		if repository.ErrorCode(err) != repository.CodeNotFound {
			t.Fatalf("%s error = %v", operation, err)
		}
	}
	if fake.captured || fake.killed {
		t.Fatalf("Worktree selector touched Session: captured=%t killed=%t", fake.captured, fake.killed)
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
	fake := &fakeTmux{path: "/work/repo", row: "$4\tbroken\t1720000000\t1720000000\t0\t" + SchemaVersion + "\tbad\tshell\t2024-07-03T09:46:40Z\t/work/repo\n"}
	_, err := (TmuxUse{commands: fake}).ManagedSessions(t.Context(), "/work/repo")
	if err == nil || !strings.Contains(err.Error(), "invalid Schooner Session metadata") {
		t.Fatalf("error = %v", err)
	}
}

func TestMovedManagedSessionStillBlocksCurrentWorktreePath(t *testing.T) {
	fake := &fakeTmux{
		path: "/work/repo-moved",
		row:  "$4\tmanaged\t1720000000\t1720000000\t0\t" + SchemaVersion + "\t" + testSessionID + "\tshell\t2024-07-03T09:46:40Z\t/work/repo-old\n",
	}
	sessions, err := (TmuxUse{commands: fake}).ManagedSessions(t.Context(), "/work/repo-moved")
	if err != nil || len(sessions) != 1 || sessions[0] != testSessionID {
		t.Fatalf("sessions = %v, error = %v", sessions, err)
	}
	value := classifyRow(tmuxRow{tmuxID: "$4", name: "managed", created: "1720000000", activity: "1720000000", attached: "0", schema: SchemaVersion, id: testSessionID, kind: KindShell, managedCreated: "2024-07-03T09:46:40Z", worktree: "/work/repo-old"})
	associateManaged(&value, []string{"/work/repo-moved"}, []liveWorktree{{path: "/work/repo-moved", relative: "repo-moved", common: "/work/repo/.git"}})
	if value.Association != AssociationLive || value.WorktreePath != "/work/repo-moved" || value.MetadataWorktreePath != "/work/repo-old" || !strings.Contains(managedSessionCondition(value), "/work/repo-old") {
		t.Fatalf("moved Session = %+v", value)
	}
	if stale, err := (TmuxUse{commands: fake}).ManagedSessions(t.Context(), "/work/repo-old"); err != nil || len(stale) != 1 {
		t.Fatalf("conservative metadata path sessions = %v, error = %v", stale, err)
	}
}

func TestManagedAssociationFailsClosedWhenMetadataPathIsReused(t *testing.T) {
	value := classifyRow(tmuxRow{tmuxID: "$4", name: "managed", created: "1720000000", activity: "1720000000", attached: "0", schema: SchemaVersion, id: testSessionID, kind: KindShell, managedCreated: "2024-07-03T09:46:40Z", worktree: "/work/reused"})
	worktrees := []liveWorktree{
		{path: "/work/reused", relative: "reused", common: "/work/reused/.git"},
		{path: "/work/moved", relative: "moved", common: "/work/repo/.git"},
	}
	associateManaged(&value, []string{"/work/moved"}, worktrees)
	if value.Association != AssociationAmbiguous || value.WorktreePath != "/work/reused" || value.ObservedWorktreePath != "/work/moved" || value.MetadataWorktreePath != "/work/reused" {
		t.Fatalf("reused metadata path association = %+v", value)
	}
	if !managedAssociationConflict([]Session{value}, "/work/reused") || !managedAssociationConflict([]Session{value}, "/work/moved") || len(managedForPath([]Session{value}, "/work/reused")) != 0 {
		t.Fatal("ambiguous managed Session could be reused or duplicated")
	}
}

func TestManagedAssociationDoesNotMoveAfterOrdinaryDirectoryChange(t *testing.T) {
	value := classifyRow(tmuxRow{tmuxID: "$4", name: "managed", created: "1720000000", activity: "1720000000", attached: "0", schema: SchemaVersion, id: testSessionID, kind: KindShell, managedCreated: "2024-07-03T09:46:40Z", worktree: "/work/original"})
	worktrees := []liveWorktree{
		{path: "/work/original", relative: "original", common: "/work/original/.git"},
		{path: "/work/other", relative: "other", common: "/work/other/.git"},
	}
	associateManaged(&value, []string{"/work/other"}, worktrees)
	if value.WorktreePath != "/work/original" || value.ObservedWorktreePath != "/work/other" || value.Association != AssociationAmbiguous {
		t.Fatalf("directory change rewrote managed association: %+v", value)
	}
}
