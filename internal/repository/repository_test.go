package repository

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestDiscoverGroupsPrimaryAndLinkedWorktreesFromGit(t *testing.T) {
	root := t.TempDir()
	primary := filepath.Join(root, "owner", "repo")
	mustGit(t, "init", primary)
	mustGitAt(t, primary, "config", "user.email", "test@example.com")
	mustGitAt(t, primary, "config", "user.name", "Test")
	mustWrite(t, filepath.Join(primary, "tracked"), "initial\n")
	mustGitAt(t, primary, "add", "tracked")
	mustGitAt(t, primary, "commit", "-m", "initial")
	mustGitAt(t, primary, "remote", "add", "origin", "https://secret:token@example.com/owner/repo.git?credential=x#fragment")
	linked := filepath.Join(root, "owner", "feature")
	mustGitAt(t, primary, "worktree", "add", "-b", "feature", linked)
	mustWrite(t, filepath.Join(primary, "tracked"), "changed\n")
	mustWrite(t, filepath.Join(primary, "staged"), "staged\n")
	mustGitAt(t, primary, "add", "staged")
	mustWrite(t, filepath.Join(primary, "untracked"), "untracked\n")

	catalog, err := Discover(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Repositories) != 1 || len(catalog.Warnings) != 0 {
		t.Fatalf("catalog = %+v", catalog)
	}
	repository := catalog.Repositories[0]
	if repository.Primary == nil || repository.Primary.RelativePath != "owner/repo" || len(repository.Linked) != 1 || repository.Linked[0].RelativePath != "owner/feature" {
		t.Fatalf("repository = %+v", repository)
	}
	if repository.Origin != "https://example.com/owner/repo" {
		t.Fatalf("origin = %q", repository.Origin)
	}
	if repository.Primary.Status.Staged != 1 || repository.Primary.Status.Unstaged != 1 || repository.Primary.Status.Untracked != 1 {
		t.Fatalf("status = %+v", repository.Primary.Status)
	}
	if repository.Primary.Kind != Primary || repository.Linked[0].Kind != Linked {
		t.Fatalf("kinds = %s, %s", repository.Primary.Kind, repository.Linked[0].Kind)
	}

	mustGitAt(t, primary, "remote", "set-url", "origin", "ssh://alice:secret@example.com/owner/repo.git")
	catalog, err = Discover(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if got := catalog.Repositories[0].Origin; got != "ssh://example.com/owner/repo" {
		t.Fatalf("SSH origin = %q", got)
	}
	if got := catalog.Repositories[0].OriginKey; got != "alice@example.com/owner/repo" {
		t.Fatalf("SSH origin key = %q", got)
	}
}

func TestDiscoverIncludesInRootLinkedWorktreeWithoutOutsideSiblings(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	outside := filepath.Join(base, "outside", "repo")
	mustMkdir(t, root)
	mustGit(t, "init", outside)
	mustGitAt(t, outside, "config", "user.email", "test@example.com")
	mustGitAt(t, outside, "config", "user.name", "Test")
	mustWrite(t, filepath.Join(outside, "tracked"), "initial\n")
	mustGitAt(t, outside, "add", "tracked")
	mustGitAt(t, outside, "commit", "-m", "initial")
	inside := filepath.Join(root, "inside")
	mustGitAt(t, outside, "worktree", "add", "-b", "inside", inside)

	catalog, err := Discover(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Repositories) != 1 || catalog.Repositories[0].Primary != nil || len(catalog.Repositories[0].Linked) != 1 {
		t.Fatalf("catalog = %+v", catalog)
	}
	if !strings.Contains(catalog.Repositories[0].CommonDirectory, "outside") || strings.Contains(catalog.Repositories[0].Linked[0].Path, "outside") {
		t.Fatalf("external relationship leaked siblings: %+v", catalog.Repositories[0])
	}
}

func TestDiscoverStopsAtTopmostCheckoutAndIgnoresSymlinks(t *testing.T) {
	root := t.TempDir()
	outer := filepath.Join(root, "outer")
	mustGit(t, "init", outer)
	mustGit(t, "init", filepath.Join(outer, "nested"))
	outside := t.TempDir()
	mustGit(t, "init", filepath.Join(outside, "escaped"))
	if err := os.Symlink(filepath.Join(outside, "escaped"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	catalog, err := Discover(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Repositories) != 1 || catalog.Repositories[0].Primary == nil || catalog.Repositories[0].Primary.RelativePath != "outer" {
		t.Fatalf("catalog = %+v", catalog)
	}
}

func TestDiscoverDoesNotLetStaleGitEntryHideNestedCheckout(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "stale")
	mustWrite(t, filepath.Join(parent, ".git"), "not a checkout\n")
	nested := filepath.Join(parent, "nested")
	mustGit(t, "init", nested)
	catalog, err := Discover(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Repositories) != 1 || catalog.Repositories[0].Primary == nil || catalog.Repositories[0].Primary.RelativePath != "stale/nested" {
		t.Fatalf("catalog = %+v", catalog)
	}
	if len(catalog.Warnings) != 1 || filepath.Base(catalog.Warnings[0].Path) != filepath.Base(parent) {
		t.Fatalf("warnings = %+v", catalog.Warnings)
	}
}

func TestDiscoverRejectsNonUTF8WorktreePaths(t *testing.T) {
	if goruntime.GOOS != "linux" {
		t.Skip("the local filesystem rejects non-UTF-8 path creation")
	}
	root := t.TempDir()
	invalid := filepath.Join(root, string([]byte{'b', 'a', 'd', 0xff}))
	mustGit(t, "init", invalid)
	catalog, err := Discover(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Repositories) != 0 || len(catalog.Warnings) != 1 || !strings.Contains(catalog.Warnings[0].Message, "not valid UTF-8") || !strings.Contains(catalog.Warnings[0].Path, `\xff`) {
		t.Fatalf("catalog = %+v", catalog)
	}
}

func TestParseWorktreeListRejectsNonUTF8Paths(t *testing.T) {
	data := append([]byte("worktree /root/bad"), 0xff, 0, 0)
	if _, err := parseWorktreeList(data); err == nil {
		t.Fatal("non-UTF-8 worktree membership succeeded")
	}
}

func TestDiscoverIgnoresInheritedGitRepositorySelection(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	mustGit(t, "init", target)
	other := filepath.Join(t.TempDir(), "other")
	mustGit(t, "init", other)
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_WORK_TREE", other)
	catalog, err := Discover(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Repositories) != 1 || catalog.Repositories[0].Primary == nil || catalog.Repositories[0].Primary.RelativePath != "target" {
		t.Fatalf("catalog = %+v", catalog)
	}
}

func TestDiscoverDisablesRepositoryFSMonitorHooks(t *testing.T) {
	root := t.TempDir()
	repositoryPath := filepath.Join(root, "repo")
	mustGit(t, "init", repositoryPath)
	marker := filepath.Join(root, "fsmonitor-invoked")
	hook := filepath.Join(root, "fsmonitor-hook")
	mustWrite(t, hook, "#!/bin/sh\nprintf invoked >\"$SCHOONER_FSMONITOR_MARKER\"\nexit 1\n")
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SCHOONER_FSMONITOR_MARKER", marker)
	mustGitAt(t, repositoryPath, "config", "core.fsmonitor", hook)

	catalog, err := Discover(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Repositories) != 1 || len(catalog.Warnings) != 0 {
		t.Fatalf("catalog = %+v", catalog)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("repository fsmonitor hook was invoked: %v", err)
	}
}

func TestInspectRequiresExactConfinedPath(t *testing.T) {
	root := t.TempDir()
	repositoryPath := filepath.Join(root, "owner", "repo")
	mustGit(t, "init", repositoryPath)
	canonicalRepository, err := filepath.EvalSymlinks(repositoryPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, selector := range []string{"owner/repo", canonicalRepository} {
		inspection, inspectErr := Inspect(t.Context(), root, selector)
		if inspectErr != nil || inspection.Worktree.Path != canonicalRepository {
			t.Fatalf("Inspect(%q) = %+v, %v", selector, inspection, inspectErr)
		}
	}
	if repositoryPath != canonicalRepository {
		if _, err := Inspect(t.Context(), root, repositoryPath); err == nil {
			t.Fatal("non-canonical absolute selector succeeded")
		}
	}
	if _, err := Inspect(t.Context(), root, "repo"); ErrorCode(err) != CodeNotFound {
		t.Fatalf("Inspect missing basename error = %v, code = %q", err, ErrorCode(err))
	}
	for _, selector := range []string{"owner/../owner/repo", "../outside", repositoryPath + string(filepath.Separator) + ".." + string(filepath.Separator) + "repo"} {
		if _, err := Inspect(t.Context(), root, selector); ErrorCode(err) != CodeInvalidInput {
			t.Fatalf("Inspect(%q) error = %v, code = %q", selector, err, ErrorCode(err))
		}
	}
	if err := os.Symlink(repositoryPath, filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(t.Context(), root, "alias"); ErrorCode(err) != CodeInvalidInput {
		t.Fatalf("relative symlink selector error = %v, code = %q", err, ErrorCode(err))
	}
}

func TestInspectReturnsTypedNotFoundForMissingExactPath(t *testing.T) {
	root := t.TempDir()
	_, err := Inspect(t.Context(), root, "missing")
	if ErrorCode(err) != CodeNotFound || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("error = %v, code = %q", err, ErrorCode(err))
	}
}

func TestInspectReturnsTypedNotFoundForExistingNonWorktree(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "stale"))
	_, err := Inspect(t.Context(), root, "stale")
	if ErrorCode(err) != CodeNotFound {
		t.Fatalf("error = %v, code = %q", err, ErrorCode(err))
	}
}

func TestInspectReturnsTypedNotFoundForRegularFile(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "file"), "not a worktree\n")
	_, err := Inspect(t.Context(), root, "file")
	if ErrorCode(err) != CodeNotFound {
		t.Fatalf("error = %v, code = %q", err, ErrorCode(err))
	}
}

func TestInspectAcceptsDotForWorktreeAtConfiguredRoot(t *testing.T) {
	root := t.TempDir()
	mustGit(t, "init", root)
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := Inspect(t.Context(), root, ".")
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Worktree.Path != canonicalRoot || inspection.Worktree.RelativePath != "." {
		t.Fatalf("inspection = %+v", inspection)
	}
}

func TestInspectCarriesDiscoveryTruncationWarnings(t *testing.T) {
	root := t.TempDir()
	selected := filepath.Join(root, "selected")
	mustGit(t, "init", selected)
	deep := filepath.Join(root, "deep")
	for depth := 0; depth < maxDepth; depth++ {
		deep = filepath.Join(deep, fmtInt(depth))
	}
	mustGit(t, "init", deep)
	inspection, err := Inspect(t.Context(), root, "selected")
	if err != nil {
		t.Fatal(err)
	}
	if len(inspection.Warnings) != 1 || !strings.Contains(inspection.Warnings[0].Message, "depth limit") {
		t.Fatalf("warnings = %+v", inspection.Warnings)
	}
}

func TestDiscoverHandlesDetachedAndUnbornHeads(t *testing.T) {
	root := t.TempDir()
	unborn := filepath.Join(root, "unborn")
	mustGit(t, "init", unborn)
	detached := filepath.Join(root, "detached")
	mustGit(t, "init", detached)
	mustGitAt(t, detached, "config", "user.email", "test@example.com")
	mustGitAt(t, detached, "config", "user.name", "Test")
	mustWrite(t, filepath.Join(detached, "a"), "a")
	mustGitAt(t, detached, "add", "a")
	mustGitAt(t, detached, "commit", "-m", "initial")
	mustGitAt(t, detached, "checkout", "--detach")
	catalog, err := Discover(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	worktrees := map[string]Worktree{}
	for _, repository := range catalog.Repositories {
		if repository.Primary != nil {
			worktrees[repository.Primary.RelativePath] = *repository.Primary
		}
	}
	if worktrees["unborn"].HEAD != "" || worktrees["unborn"].Detached || worktrees["unborn"].Branch == "" {
		t.Fatalf("unborn = %+v", worktrees["unborn"])
	}
	if !worktrees["detached"].Detached || worktrees["detached"].HEAD == "" || worktrees["detached"].Branch != "" {
		t.Fatalf("detached = %+v", worktrees["detached"])
	}
}

func TestParseStatusCountsConflictAndRename(t *testing.T) {
	data := []byte("u UU N... 100644 100644 100644 100644 a b c path\x00" +
		"2 R. N... 100644 100644 100644 a b R100 new\x00old\x00? untracked\x00! ignored\x00")
	status, err := parseStatus(data)
	if err != nil {
		t.Fatal(err)
	}
	if status.Conflicted != 1 || status.Staged != 1 || status.Untracked != 1 || status.Unstaged != 0 || status.Ignored != 1 {
		t.Fatalf("status = %+v", status)
	}
}

func TestStatusIgnoredCountIsNotAddedToVersionOneJSON(t *testing.T) {
	data, err := json.Marshal(Status{Ignored: 1})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "ignored") {
		t.Fatalf("version-one status JSON changed: %s", data)
	}
}

func TestDiscoverBoundsWarningsAndHonorsCancellation(t *testing.T) {
	root := t.TempDir()
	for index := 0; index < maxWarnings+20; index++ {
		path := filepath.Join(root, "candidate-"+strings.Repeat("x", index%5), string(rune('a'+index%26)), fmtInt(index))
		mustMkdir(t, filepath.Join(path, ".git"))
	}
	catalog, err := Discover(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Warnings) != maxWarnings {
		t.Fatalf("warnings = %d", len(catalog.Warnings))
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Discover(ctx, root); !errorsIsCanceled(err) {
		t.Fatalf("cancel error = %v", err)
	}
}

func TestWalkCandidatesBoundsVisitedEntries(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a", "b", "c", "d"} {
		mustWrite(t, filepath.Join(root, name), name)
	}
	candidates, warnings, err := walkCandidatesBounded(t.Context(), root, 3, commandRunner{})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 0 || len(warnings) != 1 || !strings.Contains(warnings[0].Message, "filesystem entry limit") {
		t.Fatalf("candidates = %v, warnings = %+v", candidates, warnings)
	}
}

func TestDiscoverReportsDepthTruncation(t *testing.T) {
	root := t.TempDir()
	deep := root
	for depth := 0; depth <= maxDepth; depth++ {
		deep = filepath.Join(deep, fmtInt(depth))
	}
	mustGit(t, "init", deep)
	catalog, err := Discover(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Repositories) != 0 || len(catalog.Warnings) != 1 || !strings.Contains(catalog.Warnings[0].Message, "depth limit") {
		t.Fatalf("catalog = %+v", catalog)
	}
}

func TestRequiredWarningReplacesLastBoundedWarning(t *testing.T) {
	warnings := make([]Warning, maxWarnings)
	for index := range warnings {
		warnings[index] = Warning{Message: fmtInt(index)}
	}
	appendRequiredWarning(&warnings, "/root", "filesystem entry limit reached")
	if len(warnings) != maxWarnings || warnings[len(warnings)-1].Message != "filesystem entry limit reached" {
		t.Fatalf("warnings = %+v", warnings)
	}
}

func TestSanitizeOriginRemovesUserInfoFromEveryURI(t *testing.T) {
	if got := sanitizeOrigin("ssh://alice:secret@example.com/owner/repo.git?token=x#fragment"); got != "ssh://example.com/owner/repo" {
		t.Fatalf("origin = %q", got)
	}
	if got := sanitizeOrigin("alice@example.com:owner/repo.git?token=x#fragment"); got != "example.com:owner/repo" {
		t.Fatalf("SCP origin = %q", got)
	}
	if got := sanitizeOrigin("ssh://example.com/" + strings.Repeat("é", maxOriginBytes)); got != "" {
		t.Fatalf("oversized origin length = %d", len(got))
	}
	if got := sanitizeOrigin("https://alice:secret@example.com/%zz.git"); got != "" {
		t.Fatalf("unparsable URI origin = %q", got)
	}
	if got := sanitizeOrigin("hg::https://alice:secret@example.com/owner/repo.git"); got != "" {
		t.Fatalf("remote-helper origin = %q", got)
	}
}

func TestGitDisablesOptionalLocksAndFSMonitor(t *testing.T) {
	commands := &recordingRunner{}
	if _, err := git(t.Context(), commands, "/root/repo", "status", "--porcelain=v2"); err != nil {
		t.Fatal(err)
	}
	want := []string{"--no-optional-locks", "-c", "core.fsmonitor=false", "-C", "/root/repo", "status", "--porcelain=v2"}
	if strings.Join(commands.arguments, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("arguments = %q", commands.arguments)
	}
}

func TestGitBoundsEachCommandDuration(t *testing.T) {
	started := time.Now()
	_, err := gitWithTimeout(t.Context(), blockingRunner{}, "/root/repo", 20*time.Millisecond, "status")
	if err == nil || !strings.Contains(err.Error(), "timed out") || time.Since(started) > time.Second {
		t.Fatalf("error = %v, duration = %s", err, time.Since(started))
	}
}

func TestRepositoryRelationshipFallsBackToRevalidatedIdentity(t *testing.T) {
	catalog := Catalog{Repositories: []Repository{{CommonDirectory: "/old/common", Primary: &Worktree{Path: "/root/repo", GitDirectory: "/old/git", Kind: Primary}}}}
	latest := observation{repository: "/new/common", origin: "ssh://example.com/new/repo", worktree: Worktree{Path: "/root/repo", GitDirectory: "/new/git", Kind: Primary}}
	relationship := repositoryRelationship(catalog, latest)
	if relationship.CommonDirectory != latest.repository || relationship.Primary == nil || relationship.Primary.GitDirectory != latest.worktree.GitDirectory {
		t.Fatalf("relationship = %+v", relationship)
	}
}

func TestBoundCatalogFitsRemoteMessageBudget(t *testing.T) {
	catalog := Catalog{WorktreeRoot: "/root", Repositories: []Repository{{CommonDirectory: "/common", Linked: []Worktree{}}}, Warnings: []Warning{}}
	for index := 0; index < 500; index++ {
		path := "/root/" + strings.Repeat("x", 2_000) + fmtInt(index)
		catalog.Repositories[0].Linked = append(catalog.Repositories[0].Linked, Worktree{Path: path, RelativePath: path, GitDirectory: path + "/.git", Kind: Linked})
	}
	boundCatalog(&catalog)
	encoded, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > maxCatalogBytes || len(catalog.Repositories) == 0 || len(catalog.Repositories[0].Linked) >= 500 {
		t.Fatalf("encoded bytes = %d, catalog repositories = %d", len(encoded), len(catalog.Repositories))
	}
	if len(catalog.Warnings) != 1 || !strings.Contains(catalog.Warnings[0].Message, "catalog output limit") {
		t.Fatalf("warnings = %+v", catalog.Warnings)
	}
}

func TestBoundCatalogDropsOversizedWarningDetails(t *testing.T) {
	catalog := Catalog{WorktreeRoot: "/root", Repositories: []Repository{{CommonDirectory: "/root/repo/.git", Primary: &Worktree{Path: "/root/repo", RelativePath: "repo", GitDirectory: "/root/repo/.git", Kind: Primary}}}, Warnings: []Warning{{Path: "/root/repo", Message: strings.Repeat("x", maxCatalogBytes)}}}
	boundCatalog(&catalog)
	encoded, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > maxCatalogBytes || len(catalog.Repositories) != 1 || catalog.Repositories[0].Primary == nil || len(catalog.Warnings) != 1 || !strings.Contains(catalog.Warnings[0].Message, "catalog output limit") {
		t.Fatalf("encoded bytes = %d, repositories = %+v, warnings = %+v", len(encoded), catalog.Repositories, catalog.Warnings)
	}
}

func mustGit(t *testing.T, arguments ...string) { t.Helper(); mustRun(t, "git", arguments...) }
func mustGitAt(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	mustRun(t, "git", append([]string{"-C", directory}, arguments...)...)
}
func mustRun(t *testing.T, name string, arguments ...string) {
	t.Helper()
	command := exec.Command(name, arguments...)
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, arguments, err, output)
	}
}
func mustWrite(t *testing.T, path, contents string) {
	t.Helper()
	mustMkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
}
func fmtInt(value int) string         { return strconv.Itoa(value) }
func errorsIsCanceled(err error) bool { return errors.Is(err, context.Canceled) }

type recordingRunner struct{ arguments []string }

func (runner *recordingRunner) Run(_ context.Context, _ string, arguments ...string) ([]byte, error) {
	runner.arguments = append([]string(nil), arguments...)
	return nil, nil
}

type blockingRunner struct{}

func (blockingRunner) Run(ctx context.Context, _ string, _ ...string) ([]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
