package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/thewelshrich/schooner/internal/box"
	"github.com/thewelshrich/schooner/internal/repository"
)

type lifecycleFailingWriter struct{ err error }

func (writer lifecycleFailingWriter) Write([]byte) (int, error) { return 0, writer.err }

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
	if err := writeWorktreeList(&output, "human", catalog, nil); err != nil {
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
			return writeWorktreeList(output, "human", repository.Catalog{WorktreeRoot: "/root", Repositories: []repository.Repository{relation}, Warnings: []repository.Warning{}}, nil)
		},
		"inspect": func(output *bytes.Buffer) error {
			return writeWorktreeInspection(output, "human", repository.Inspection{WorktreeRoot: "/root", Repository: relation, Worktree: worktree, Warnings: []repository.Warning{{Path: unsafe, Message: unsafe}}}, nil)
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
	if err := writeWorktreeInspection(&output, "json", inspection, nil); err != nil {
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
	if err := writeLifecycleResult(&output, "json", repository.MutationResult{Action: "clone", Recovered: true, WorktreeRoot: "/root", Path: worktree.Path, Inspection: &inspection}, nil); err != nil {
		t.Fatal(err)
	}
	result := output.String()
	for _, expected := range []string{`"schema_version":"1"`, `"action":"clone"`, `"recovered":true`, `"repository":`, `"worktree":`} {
		if !strings.Contains(result, expected) {
			t.Fatalf("output %s does not contain %s", result, expected)
		}
	}
}

func TestLifecycleHumanOutputReturnsWriteFailure(t *testing.T) {
	want := errors.New("write failed")
	if err := writeLifecycleResult(lifecycleFailingWriter{err: want}, "human", repository.MutationResult{Action: "clone", Path: "/root/repo"}, nil); !errors.Is(err, want) {
		t.Fatalf("write error = %v", err)
	}
}

func TestCloneCollisionAddsHumanGuidanceWithoutChangingStructuredError(t *testing.T) {
	err := withCloneCollisionGuidance(box.NewError("conflict", `clone destination "/root/schooner/repo" already exists`, nil))
	var human bytes.Buffer
	printError(&human, err, "human", nil)
	if !strings.Contains(human.String(), "Error: clone destination") || !strings.Contains(human.String(), "Next: run `schooner worktree list --box <box>`") {
		t.Fatalf("human error = %q", human.String())
	}
	var structured bytes.Buffer
	printError(&structured, err, "json", nil)
	if strings.Contains(structured.String(), "Next:") || !strings.Contains(structured.String(), `"code":"conflict"`) {
		t.Fatalf("structured error = %q", structured.String())
	}
}
