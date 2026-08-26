package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/thewelshrich/schooner/internal/boxtarget"
	"github.com/thewelshrich/schooner/internal/repository"
	hostruntime "github.com/thewelshrich/schooner/internal/runtime"
	"github.com/thewelshrich/schooner/internal/runtime/host"
	"github.com/thewelshrich/schooner/internal/session"
	"github.com/thewelshrich/schooner/internal/workcontext"
)

func TestContextualCloneHonorsNoInput(t *testing.T) {
	var output bytes.Buffer
	_, err := confirmCloneForStart(t.Context(), Streams{Err: &output, InIsTerminal: true, OutIsTerminal: true, ErrIsTerminal: true}, &globalOptions{output: "human", noInput: true}, boxtarget.Target{}, &repository.LocalCheckout{TopLevel: "/repo"}, workcontext.StartPlan{Mode: workcontext.StartClone, CloneSource: "https://example.com/repo.git"})
	var usage usageError
	if !errors.As(err, &usage) || output.Len() != 0 {
		t.Fatalf("error = %v, output = %q", err, output.String())
	}
}

func TestIncompleteContextualStartForcesChoiceWithSingleWorktree(t *testing.T) {
	choice := workcontext.WorktreeChoice{Worktree: repository.Worktree{Path: "/remote/repo", RelativePath: "repo", Branch: "main"}}
	selected, err := pickWorktreeChoices(t.Context(), Streams{}, &globalOptions{output: "human", noInput: true}, "Choose", []workcontext.WorktreeChoice{choice})
	var usage usageError
	if selected != "" || !errors.As(err, &usage) {
		t.Fatalf("selected = %q, error = %v", selected, err)
	}
}

func TestContextualUnavailableIsAnExecutionFailureWithGuidance(t *testing.T) {
	err := contextualUnavailable("nothing available", "start something")
	var execution executionError
	var guidance guidanceError
	if !errors.As(err, &execution) || !errors.As(err, &guidance) || guidance.guidance != "start something" {
		t.Fatalf("error = %#v", err)
	}
}

func TestCloneStartWarningsExplainLocalOnlyState(t *testing.T) {
	warnings := cloneStartWarnings(&repository.LocalCheckout{
		Detached: true,
		Ahead:    3,
		Status:   repository.Status{Staged: 1, Unstaged: 2, Untracked: 1},
	})
	joined := strings.Join(warnings, "\n")
	for _, expected := range []string{"not copied", "4 changed", "detached HEAD", "origin default branch"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("warnings missing %q: %s", expected, joined)
		}
	}
}

func TestCloneStartWarningsReportAheadAndMissingUpstream(t *testing.T) {
	ahead := strings.Join(cloneStartWarnings(&repository.LocalCheckout{Upstream: "origin/main", Ahead: 2}), "\n")
	if !strings.Contains(ahead, "2 commit(s) ahead") {
		t.Fatalf("ahead warnings = %s", ahead)
	}
	missing := strings.Join(cloneStartWarnings(&repository.LocalCheckout{}), "\n")
	if !strings.Contains(missing, "no upstream") {
		t.Fatalf("missing-upstream warnings = %s", missing)
	}
}

func TestSessionCommandsExposeConsistentBoxSelection(t *testing.T) {
	home := t.TempDir()
	options := &globalOptions{hostRuntime: func() *host.Runtime { return host.NewAtHome(hostruntime.BuildInfo{}, home) }}
	root := newRootCommand(BuildInfo{}, Streams{}, options)
	for _, name := range []string{"start", "resume", "sessions", "logs", "stop", "shell"} {
		command, _, err := root.Find([]string{name})
		if err != nil || command == nil {
			t.Fatalf("find %s: %v", name, err)
		}
		if command.Flags().Lookup("box") == nil {
			t.Fatalf("%s has no --box flag", name)
		}
	}
	logs, _, _ := root.Find([]string{"logs"})
	if logs.Flags().Lookup("follow") != nil || logs.Flags().Lookup("lines") == nil {
		t.Fatal("logs flags do not match the bounded capture contract")
	}
}

func TestFirstArgumentPreservesExactSelector(t *testing.T) {
	if got := firstArgument([]string{"  repo  "}); got != "  repo  " {
		t.Fatalf("selector = %q", got)
	}
	if got := firstArgument([]string{""}); got != "" {
		t.Fatalf("explicit empty selector = %q", got)
	}
	if sessionSelectorOmitted([]string{""}) {
		t.Fatal("explicit empty selector was treated as omitted")
	}
	if !sessionSelectorOmitted(nil) {
		t.Fatal("missing selector was treated as provided")
	}
}

func TestSessionOutputDistinguishesOwnershipAndAssociation(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	catalog := session.Catalog{WorktreeRoot: "/work", Sessions: []session.Session{
		{ID: "11111111-1111-4111-8111-111111111111", TmuxID: "$1", Name: "managed", Ownership: session.Managed, Association: session.AssociationLive, WorktreeRelativePath: "repo", CreatedAt: now, ActivityAt: now},
		{TmuxID: "$2", Name: "external", Ownership: session.Unmanaged, Association: session.AssociationUnassociated, CreatedAt: now, ActivityAt: now},
	}}
	var human bytes.Buffer
	if err := writeSessions(&human, "human", catalog, nil); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"11111111-1111-4111-8111-111111111111", "tmux:$2", "managed", "unmanaged", "unassociated"} {
		if !strings.Contains(human.String(), expected) {
			t.Fatalf("human output missing %q: %s", expected, human.String())
		}
	}
	var jsonOutput bytes.Buffer
	if err := writeSessions(&jsonOutput, "json", catalog, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(jsonOutput.String(), `"schema_version":"1"`) || !strings.Contains(jsonOutput.String(), `"ownership":"unmanaged"`) {
		t.Fatalf("JSON output = %s", jsonOutput.String())
	}
}

func TestSessionOutputQuotesInvalidNames(t *testing.T) {
	catalog := session.Catalog{Sessions: []session.Session{{TmuxID: "$2", Name: "forged\tcolumn\n\x1b[31m", Ownership: session.Invalid, Association: session.AssociationUnassociated}}}
	var output bytes.Buffer
	if err := writeSessions(&output, "human", catalog, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "forged\tcolumn") || strings.Contains(output.String(), "\x1b[31m") || !strings.Contains(output.String(), `"forged\tcolumn\n\x1b[31m"`) {
		t.Fatalf("invalid name was not safely quoted: %q", output.String())
	}
}
