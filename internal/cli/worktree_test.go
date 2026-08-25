package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/thewelshrich/schooner/internal/box"
	"github.com/thewelshrich/schooner/internal/repository"
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
