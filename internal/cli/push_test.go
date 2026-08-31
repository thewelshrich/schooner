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

func TestPushUsageErrorReturnsUsageExit(t *testing.T) {
	t.Chdir(t.TempDir())
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := Run(t.Context(), []string{"push"}, Streams{In: bytes.NewReader(nil), Out: &stdout, Err: &stderr}, BuildInfo{})
	if exitCode != exitUsage {
		t.Fatalf("exit code = %d, want %d; stderr = %q", exitCode, exitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "must run inside a Git Worktree") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestWritePushResultJSONUsesDedicatedVersionedDocument(t *testing.T) {
	result := workspacetransfer.PushResult{
		Action:         workspacetransfer.ActionPushed,
		Source:         repository.CheckoutState{Worktree: "/local/repo", RepositoryIdentity: "github.com/owner/repo", CloneSource: "https://github.com/owner/repo.git"},
		RemoteWorktree: "/remote/repo", FilesChanged: 4, BytesTransferred: 2048,
		Created: true,
	}
	var output bytes.Buffer
	if err := writePushResult(&output, "json", result, boxtarget.Target{}, true, false, nil); err != nil {
		t.Fatal(err)
	}
	var document pushDocument
	if err := json.Unmarshal(output.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if document.SchemaVersion != "1" || document.Operation != "push" || document.Action != "pushed" || document.LocalWorktree != "/local/repo" || document.RemoteWorktree != "/remote/repo" || document.FilesChanged != 4 || document.BytesTransferred != 2048 || !document.LinkCreated || !document.RemoteCreated || document.CreationMethod != "clone" || document.Warnings == nil {
		t.Fatalf("document = %+v", document)
	}
	if value := output.String(); !strings.Contains(value, `"remote_created":true`) || !strings.Contains(value, `"creation_method":"clone"`) {
		t.Fatalf("JSON output = %q", value)
	}
}

func TestWritePushResultHumanDryRun(t *testing.T) {
	result := workspacetransfer.PushResult{Action: workspacetransfer.ActionWouldPush, RemoteWorktree: "/remote/repo", FilesChanged: 2}
	var output bytes.Buffer
	if err := writePushResult(&output, "human", result, boxtarget.Target{}, false, true, uitheme.New(uitheme.Auto, false)); err != nil {
		t.Fatal(err)
	}
	if value := output.String(); !strings.Contains(value, "Would push to this box") || !strings.Contains(value, "/remote/repo") || !strings.Contains(value, "2 changed") || strings.Contains(value, "Transferred") {
		t.Fatalf("output = %q", value)
	}
}

func TestWritePushResultHumanShowsFirstPushCreationPlan(t *testing.T) {
	for _, test := range []struct {
		name        string
		cloneSource string
		want        string
	}{
		{name: "clone", cloneSource: "https://github.com/owner/repo.git", want: "clone Repository, then overlay the local workspace"},
		{name: "direct", want: "normal Git Repository from the local checkout"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := workspacetransfer.PushResult{
				Action:         workspacetransfer.ActionWouldPush,
				Source:         repository.CheckoutState{Worktree: "/local/repo", CloneSource: test.cloneSource},
				RemoteWorktree: "/remote/chosen-destination",
				Created:        true,
			}
			var output bytes.Buffer
			if err := writePushResult(&output, "human", result, boxtarget.Target{}, false, true, uitheme.New(uitheme.Auto, false)); err != nil {
				t.Fatal(err)
			}
			value := output.String()
			if !strings.Contains(value, "Would push to this box") || !strings.Contains(value, "Plan") || !strings.Contains(value, test.want) || strings.Contains(value, "Transferred") {
				t.Fatalf("output = %q", value)
			}
		})
	}
}

func TestPushConflictHumanOutputConfirmsRemoteWasNotChanged(t *testing.T) {
	err := normalizePushError(&workspacetransfer.Error{
		Code: workspacetransfer.CodeConflict, Message: "Push stopped: the remote Worktree contains changes",
		Context: map[string]string{"unstaged": "2", "untracked": "1"},
	})
	var output bytes.Buffer
	printError(&output, err, "human", uitheme.New(uitheme.Auto, false))
	value := output.String()
	for _, expected := range []string{"remote Worktree contains changes", "2 unstaged", "1 untracked", "No remote files were changed."} {
		if !strings.Contains(value, expected) {
			t.Fatalf("output = %q, want %q", value, expected)
		}
	}
}

func TestPushConflictHumanOutputReportsOperationCreatedClone(t *testing.T) {
	err := normalizePushError(&workspacetransfer.Error{
		Code: workspacetransfer.CodeConflict, Message: "Push stopped: the newly cloned remote Worktree changed",
		Context: map[string]string{"remote_created": "true"},
	})
	var output bytes.Buffer
	printError(&output, err, "human", uitheme.New(uitheme.Auto, false))
	value := output.String()
	if !strings.Contains(value, "remote clone was created") || !strings.Contains(value, "no local workspace files were applied") || strings.Contains(value, "No remote files were changed") {
		t.Fatalf("output = %q", value)
	}
}
