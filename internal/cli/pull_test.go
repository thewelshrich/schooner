package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/thewelshrich/schooner/internal/boxtarget"
	"github.com/thewelshrich/schooner/internal/repository"
	uitheme "github.com/thewelshrich/schooner/internal/ui/theme"
	"github.com/thewelshrich/schooner/internal/workspacetransfer"
)

func TestPullUsageErrorReturnsUsageExit(t *testing.T) {
	t.Chdir(t.TempDir())
	var stdout, stderr bytes.Buffer
	exitCode := Run(t.Context(), []string{"pull"}, Streams{In: bytes.NewReader(nil), Out: &stdout, Err: &stderr}, BuildInfo{})
	if exitCode != exitUsage || !strings.Contains(stderr.String(), "must run inside a Git Worktree") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
}

func TestWritePullResultJSONUsesDedicatedVersionedDocument(t *testing.T) {
	result := workspacetransfer.PullResult{
		Action:      workspacetransfer.ActionPulled,
		Source:      repository.CheckoutState{Worktree: "/remote/repo", RepositoryIdentity: "github.com/owner/repo"},
		Destination: repository.CheckoutState{Worktree: "/local/repo"}, RemoteWorktree: "/remote/repo",
		FilesChanged: 4, BytesTransferred: 2048,
	}
	var output bytes.Buffer
	if err := writePullResult(&output, "json", result, boxtarget.Target{}, true, false, nil); err != nil {
		t.Fatal(err)
	}
	var document pullDocument
	if err := json.Unmarshal(output.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if document.SchemaVersion != "1" || document.Operation != "pull" || document.Action != "pulled" || document.LocalWorktree != "/local/repo" || document.RemoteWorktree != "/remote/repo" || document.RepositoryIdentity != "github.com/owner/repo" || document.FilesChanged != 4 || document.BytesTransferred != 2048 || !document.LinkCreated || document.Warnings == nil {
		t.Fatalf("document = %+v", document)
	}
}

func TestWritePullResultHumanDryRun(t *testing.T) {
	result := workspacetransfer.PullResult{Action: workspacetransfer.ActionWouldPull, RemoteWorktree: "/remote/repo", FilesChanged: 2}
	var output bytes.Buffer
	if err := writePullResult(&output, "human", result, boxtarget.Target{}, false, true, uitheme.New(uitheme.Auto, false)); err != nil {
		t.Fatal(err)
	}
	value := output.String()
	if !strings.Contains(value, "Would pull from this box") || !strings.Contains(value, "/remote/repo") || !strings.Contains(value, "2 changed") || strings.Contains(value, "Transferred") {
		t.Fatalf("output = %q", value)
	}
}

func TestPullConflictHumanOutputConfirmsLocalWasNotChanged(t *testing.T) {
	err := normalizePullError(&workspacetransfer.Error{
		Code: workspacetransfer.CodeConflict, Operation: "pull", Message: "Pull stopped: the local Worktree contains changes that would be overwritten",
		Context: map[string]string{"unstaged": "2"},
	})
	var output bytes.Buffer
	printError(&output, err, "human", uitheme.New(uitheme.Auto, false))
	value := output.String()
	for _, expected := range []string{"local Worktree contains changes", "2 unstaged", "No local files were changed."} {
		if !strings.Contains(value, expected) {
			t.Fatalf("output = %q, want %q", value, expected)
		}
	}
}
