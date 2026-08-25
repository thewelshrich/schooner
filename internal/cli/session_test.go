package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	hostruntime "github.com/thewelshrich/schooner/internal/runtime"
	"github.com/thewelshrich/schooner/internal/runtime/host"
	"github.com/thewelshrich/schooner/internal/session"
)

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

func TestSessionOutputDistinguishesOwnershipAndAssociation(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	catalog := session.Catalog{WorktreeRoot: "/work", Sessions: []session.Session{
		{ID: "11111111-1111-4111-8111-111111111111", TmuxID: "$1", Name: "managed", Ownership: session.Managed, Association: session.AssociationLive, WorktreeRelativePath: "repo", CreatedAt: now, ActivityAt: now},
		{TmuxID: "$2", Name: "external", Ownership: session.Unmanaged, Association: session.AssociationUnassociated, CreatedAt: now, ActivityAt: now},
	}}
	var human bytes.Buffer
	if err := writeSessions(&human, "human", catalog); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"11111111-1111-4111-8111-111111111111", "tmux:$2", "managed", "unmanaged", "unassociated"} {
		if !strings.Contains(human.String(), expected) {
			t.Fatalf("human output missing %q: %s", expected, human.String())
		}
	}
	var jsonOutput bytes.Buffer
	if err := writeSessions(&jsonOutput, "json", catalog); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(jsonOutput.String(), `"schema_version":"1"`) || !strings.Contains(jsonOutput.String(), `"ownership":"unmanaged"`) {
		t.Fatalf("JSON output = %s", jsonOutput.String())
	}
}
