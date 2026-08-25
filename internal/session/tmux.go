// Package session observes Schooner-owned tmux Session metadata.
package session

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/thewelshrich/schooner/internal/process"
)

const (
	SchemaOption       = "@schooner_session_schema"
	IDOption           = "@schooner_session_id"
	WorktreePathOption = "@schooner_worktree_path"
)

type TmuxUse struct{}

func NewTmuxUse() TmuxUse { return TmuxUse{} }

func (TmuxUse) ManagedSessions(ctx context.Context, worktreePath string) ([]string, error) {
	format := "#{" + SchemaOption + "}\t#{" + IDOption + "}\t#{" + WorktreePathOption + "}"
	result, err := process.RunCapturedWithoutEnvironment(ctx, 64<<10, nil, nil, "tmux", "list-sessions", "-F", format)
	if err != nil {
		message := strings.ToLower(string(result.Stderr))
		if process.ExitCode(err) == 1 && (strings.Contains(message, "no server running") || strings.Contains(message, "no sessions")) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("list tmux Sessions: %w", err)
	}
	if result.Truncated {
		return nil, fmt.Errorf("tmux Session metadata exceeded 64 KiB")
	}
	return parseManagedSessions(result.Stdout, worktreePath), nil
}

func parseManagedSessions(output []byte, worktreePath string) []string {
	resultIDs := make([]string, 0)
	for _, line := range strings.Split(strings.TrimSuffix(string(output), "\n"), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) != 3 || fields[0] != "1" || !safeID(fields[1]) {
			continue
		}
		if filepath.IsAbs(fields[2]) && filepath.Clean(fields[2]) == fields[2] && fields[2] == worktreePath {
			resultIDs = append(resultIDs, fields[1])
		}
	}
	return resultIDs
}

func safeID(value string) bool {
	if value == "" || len(value) > 256 || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}
