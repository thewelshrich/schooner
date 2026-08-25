package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/thewelshrich/schooner/internal/box"
	"github.com/thewelshrich/schooner/internal/config"
	"github.com/thewelshrich/schooner/internal/repository"
	hostruntime "github.com/thewelshrich/schooner/internal/runtime"
)

func TestWriteWorktreeListEscapesHostileWarnings(t *testing.T) {
	var output bytes.Buffer
	catalog := repository.Catalog{
		WorktreeRoot: "/home/alice/schooner",
		Repositories: []repository.Repository{},
		Warnings: []repository.Warning{{
			Path:    "bad\npath\x1b[31m",
			Message: "failed\rmessage\x1b[0m",
		}},
	}
	if err := writeWorktreeList(&output, "human", catalog); err != nil {
		t.Fatal(err)
	}
	result := output.String()
	if strings.ContainsRune(result, '\x1b') || strings.ContainsRune(result, '\r') || strings.Contains(result, "bad\npath") {
		t.Fatalf("unsafe warning output = %q", result)
	}
	if !strings.Contains(result, `bad\npath\x1b[31m`) || !strings.Contains(result, `failed\rmessage\x1b[0m`) {
		t.Fatalf("escaped warning output = %q", result)
	}
}

func TestWorktreeHumanOutputEscapesGitDerivedFormattingCharacters(t *testing.T) {
	unsafe := "spoof\u202eright"
	worktree := repository.Worktree{Path: "/root/" + unsafe, RelativePath: unsafe, GitDirectory: "/root/" + unsafe + "/.git", Kind: repository.Primary, Branch: unsafe, HEAD: unsafe}
	relation := repository.Repository{CommonDirectory: "/root/" + unsafe + "/.git", Origin: "ssh://" + unsafe + "/repo", Primary: &worktree, Linked: []repository.Worktree{}}
	for name, write := range map[string]func(*bytes.Buffer) error{
		"list": func(output *bytes.Buffer) error {
			return writeWorktreeList(output, "human", repository.Catalog{WorktreeRoot: "/root", Repositories: []repository.Repository{relation}, Warnings: []repository.Warning{}})
		},
		"inspect": func(output *bytes.Buffer) error {
			return writeWorktreeInspection(output, "human", repository.Inspection{WorktreeRoot: "/root", Repository: relation, Worktree: worktree, Warnings: []repository.Warning{{Path: unsafe, Message: unsafe}}})
		},
	} {
		t.Run(name, func(t *testing.T) {
			var output bytes.Buffer
			if err := write(&output); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(output.String(), "\u202e") || !strings.Contains(output.String(), `\u202e`) {
				t.Fatalf("unsafe output = %q", output.String())
			}
		})
	}
}

func TestWorktreeInspectionJSONCarriesWarnings(t *testing.T) {
	var output bytes.Buffer
	inspection := repository.Inspection{WorktreeRoot: "/root", Repository: repository.Repository{Linked: []repository.Worktree{}}, Warnings: []repository.Warning{{Path: "/root", Message: "partial"}}}
	if err := writeWorktreeInspection(&output, "json", inspection); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"warnings":[{"path":"/root","message":"partial"}]`) {
		t.Fatalf("output = %s", output.String())
	}
}

func TestLifecycleJSONOutputUsesDedicatedVersionedDocument(t *testing.T) {
	worktree := repository.Worktree{Path: "/root/repo", RelativePath: "repo", GitDirectory: "/root/repo/.git", Kind: repository.Primary, Branch: "main"}
	inspection := repository.Inspection{WorktreeRoot: "/root", Repository: repository.Repository{CommonDirectory: "/root/repo/.git", Primary: &worktree, Linked: []repository.Worktree{}}, Worktree: worktree}
	var output bytes.Buffer
	if err := writeLifecycleResult(&output, "json", repository.MutationResult{Action: "clone", Recovered: true, WorktreeRoot: "/root", Path: worktree.Path, Inspection: &inspection}); err != nil {
		t.Fatal(err)
	}
	result := output.String()
	for _, expected := range []string{`"schema_version":"1"`, `"action":"clone"`, `"recovered":true`, `"repository":`, `"worktree":`} {
		if !strings.Contains(result, expected) {
			t.Fatalf("output %s does not contain %s", result, expected)
		}
	}
}

func TestValidateRemoteWorktreeRootRejectsDrift(t *testing.T) {
	record := box.Record{Name: "work", WorktreeRoot: "/home/alice/schooner"}
	if err := validateRemoteWorktreeRoot(record, record.WorktreeRoot); err != nil {
		t.Fatal(err)
	}
	err := validateRemoteWorktreeRoot(record, "/home/alice/other")
	if box.ErrorCode(err) != "conflict" || !strings.Contains(err.Error(), "box setup work") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateDirectWorktreeRootRejectsCanonicalDrift(t *testing.T) {
	target := worktreeTarget{configured: config.Host{WorktreeRoot: "/home/alice/schooner"}, record: box.Record{Name: "work", WorktreeRoot: "/home/alice/schooner"}}
	if err := validateDirectWorktreeRoot(target, target.configured.WorktreeRoot); err != nil {
		t.Fatal(err)
	}
	err := validateDirectWorktreeRoot(target, "/home/alice/other")
	if box.ErrorCode(err) != "conflict" || !strings.Contains(err.Error(), "box setup work") {
		t.Fatalf("error = %v", err)
	}
}

func TestPublicRepositoryErrorMapsNotFound(t *testing.T) {
	cause := &repository.Error{Code: repository.CodeNotFound, Message: `worktree "missing" was not found`}
	err := publicRepositoryError(cause)
	if box.ErrorCode(err) != "not_found" || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("error = %v, code = %s", err, box.ErrorCode(err))
	}
}

func TestPublicRepositoryErrorMapsInvalidInput(t *testing.T) {
	cause := &repository.Error{Code: repository.CodeInvalidInput, Message: "worktree selector must be canonical"}
	err := publicRepositoryError(cause)
	if box.ErrorCode(err) != "invalid_input" {
		t.Fatalf("error = %v, code = %s", err, box.ErrorCode(err))
	}
}

func TestPublicRepositoryErrorMapsProtocolInvalidInput(t *testing.T) {
	cause := &hostruntime.Error{Code: hostruntime.CodeInvalidInput, Message: "worktree request selector is invalid"}
	err := publicRepositoryError(cause)
	if box.ErrorCode(err) != "invalid_input" {
		t.Fatalf("error = %v, code = %s", err, box.ErrorCode(err))
	}
}
