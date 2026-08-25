package cli

import (
	"bytes"
	"strings"
	"testing"

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
