// Package session owns Schooner-managed tmux Session lifecycle and observes
// unmanaged tmux sessions without claiming their lifecycle.
package session

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/thewelshrich/schooner/internal/process"
	"github.com/thewelshrich/schooner/internal/repository"
)

const (
	SchemaOption       = "@schooner_session_schema"
	IDOption           = "@schooner_session_id"
	KindOption         = "@schooner_session_kind"
	CreatedAtOption    = "@schooner_session_created_at"
	WorktreePathOption = "@schooner_worktree_path"

	SchemaVersion       = "2"
	LegacySchemaVersion = "1"
	KindShell           = "shell"
	DefaultLogLines     = 200
	MaxLogLines         = 2000
	MaxLogBytes         = 64 << 10
	maxMetadataBytes    = 1 << 20
	maxSessions         = 256
	maxPanes            = 4096
	tmuxSocketName      = "default"

	managedActionGranted = "schooner-managed-action-v1"
	managedActionRefused = "schooner-managed-action-refused-v1"
)

type Ownership string

const (
	Managed   Ownership = "managed"
	Unmanaged Ownership = "unmanaged"
	Invalid   Ownership = "invalid"
)

type AssociationState string

const (
	AssociationLive         AssociationState = "live"
	AssociationMissing      AssociationState = "missing"
	AssociationAmbiguous    AssociationState = "ambiguous"
	AssociationUnassociated AssociationState = "unassociated"
)

type Session struct {
	ID                        string           `json:"id,omitempty"`
	TmuxID                    string           `json:"tmux_id"`
	Name                      string           `json:"name"`
	Ownership                 Ownership        `json:"ownership"`
	Kind                      string           `json:"kind,omitempty"`
	CreatedAt                 time.Time        `json:"created_at"`
	ActivityAt                time.Time        `json:"activity_at"`
	AttachedClients           int              `json:"attached_clients"`
	WorktreePath              string           `json:"worktree_path,omitempty"`
	WorktreeRelativePath      string           `json:"worktree_relative_path,omitempty"`
	RepositoryCommonDirectory string           `json:"repository_common_directory,omitempty"`
	Association               AssociationState `json:"association"`
	SchoonerMetadata          bool             `json:"-"`
	LegacyMetadata            bool             `json:"-"`
	MetadataWorktreePath      string           `json:"-"`
}

type Catalog struct {
	WorktreeRoot string    `json:"worktree_root"`
	Sessions     []Session `json:"sessions"`
}

type StartResult struct {
	Session Session `json:"session"`
	Created bool    `json:"created"`
}

type LogsResult struct {
	SessionID string `json:"session_id"`
	Lines     int    `json:"lines"`
	Truncated bool   `json:"truncated"`
	Content   string `json:"content"`
}

type StopResult struct {
	SessionID string `json:"session_id"`
	Stopped   bool   `json:"stopped"`
}

type Attachment struct {
	Session             Session
	Path                string
	Args                []string
	ExcludedEnvironment []string
}

type commandRunner interface {
	Run(context.Context, int, string, ...string) (process.Result, error)
}

type osCommandRunner struct{}

func (osCommandRunner) Run(ctx context.Context, maximum int, name string, args ...string) (process.Result, error) {
	if name == "tmux" {
		args = append([]string{"-L", tmuxSocketName}, args...)
	}
	return process.RunCapturedWithoutEnvironment(ctx, maximum, []string{"TMUX", "TMUX_TMPDIR"}, []string{"LC_ALL=C", "LANG=C"}, name, args...)
}

type Service struct {
	root     string
	state    string
	commands commandRunner
	now      func() time.Time
	newID    func() (string, error)
}

func New(worktreeRoot, stateDirectory string) (*Service, error) {
	if worktreeRoot == "" || !filepath.IsAbs(worktreeRoot) || filepath.Clean(worktreeRoot) != worktreeRoot {
		return nil, &repository.Error{Code: repository.CodeInvalidInput, Message: "Session Worktree Root must be canonical and absolute"}
	}
	if stateDirectory == "" || !filepath.IsAbs(stateDirectory) || filepath.Clean(stateDirectory) != stateDirectory {
		return nil, &repository.Error{Code: repository.CodeInvalidInput, Message: "Session state directory must be canonical and absolute"}
	}
	return &Service{root: worktreeRoot, state: stateDirectory, commands: osCommandRunner{}, now: time.Now, newID: randomID}, nil
}

// TmuxUse is the narrow adapter used by repository removal. It deliberately
// shares the same fail-closed metadata parser as the full Session module.
type TmuxUse struct{ commands commandRunner }

func NewTmuxUse() TmuxUse { return TmuxUse{commands: osCommandRunner{}} }

func (use TmuxUse) ManagedSessions(ctx context.Context, worktreePath string) ([]string, error) {
	rows, err := listRows(ctx, use.commands)
	if err != nil {
		return nil, err
	}
	panes, err := listPanes(ctx, use.commands)
	if err != nil {
		return nil, err
	}
	result := make([]string, 0)
	for _, row := range rows {
		parsed := classifyRow(row)
		if parsed.Ownership == Invalid && row.hasSchoonerMetadata() {
			return nil, fmt.Errorf("tmux contains invalid Schooner Session metadata")
		}
		if parsed.Ownership == Managed && (parsed.WorktreePath == worktreePath || panesUsePath(panes[parsed.TmuxID], worktreePath)) {
			result = append(result, parsed.ID)
		}
	}
	return result, nil
}

func (s *Service) List(ctx context.Context) (Catalog, error) {
	rows, err := listRows(ctx, s.commands)
	if err != nil {
		return Catalog{}, err
	}
	panes, err := listPanes(ctx, s.commands)
	if err != nil {
		return Catalog{}, err
	}
	live, err := repository.Discover(ctx, s.root)
	if err != nil {
		return Catalog{}, err
	}
	worktrees := flattenWorktrees(live)
	result := Catalog{WorktreeRoot: live.WorktreeRoot, Sessions: make([]Session, 0, len(rows))}
	for _, row := range rows {
		value := classifyRow(row)
		switch value.Ownership {
		case Managed:
			associateManaged(&value, panes[value.TmuxID], worktrees)
		case Unmanaged:
			associateUnmanaged(&value, panes[value.TmuxID], worktrees)
		case Invalid:
			value.Association = AssociationUnassociated
		}
		result.Sessions = append(result.Sessions, value)
	}
	sort.SliceStable(result.Sessions, func(i, j int) bool {
		if result.Sessions[i].Ownership != result.Sessions[j].Ownership {
			return ownershipOrder(result.Sessions[i].Ownership) < ownershipOrder(result.Sessions[j].Ownership)
		}
		if !result.Sessions[i].ActivityAt.Equal(result.Sessions[j].ActivityAt) {
			return result.Sessions[i].ActivityAt.After(result.Sessions[j].ActivityAt)
		}
		return result.Sessions[i].TmuxID < result.Sessions[j].TmuxID
	})
	return result, nil
}

func (s *Service) Start(ctx context.Context, selector string) (StartResult, error) {
	inspection, err := repository.Inspect(ctx, s.root, selector)
	if err != nil {
		return StartResult{}, err
	}
	lock, err := repository.AcquireWorktreeMutationLock(s.state, inspection.Worktree.Path)
	if err != nil {
		return StartResult{}, err
	}
	defer func() { _ = lock.Close() }()
	inspection, err = repository.Inspect(ctx, s.root, inspection.Worktree.Path)
	if err != nil {
		return StartResult{}, err
	}
	catalog, err := s.List(ctx)
	if err != nil {
		return StartResult{}, err
	}
	for _, value := range catalog.Sessions {
		if value.Ownership == Invalid && value.SchoonerMetadata {
			return StartResult{}, &repository.Error{Code: repository.CodeOutcomeUnknown, Message: "tmux contains invalid Session metadata; creation was not attempted"}
		}
	}
	existing := managedForPath(catalog.Sessions, inspection.Worktree.Path)
	if len(existing) > 1 {
		return StartResult{}, &repository.Error{Code: repository.CodeConflict, Message: fmt.Sprintf("Worktree %q has multiple managed Sessions", inspection.Worktree.RelativePath)}
	}
	if len(existing) == 1 {
		return StartResult{Session: existing[0]}, nil
	}
	id, err := s.newID()
	if err != nil {
		return StartResult{}, fmt.Errorf("create Session identifier: %w", err)
	}
	if !validID(id) {
		return StartResult{}, fmt.Errorf("create Session identifier: generator returned an invalid UUID")
	}
	created := s.now().UTC().Truncate(time.Second)
	name := "schooner-" + compactID(id)[:12]
	args := []string{"new-session", "-d", "-s", name, "-c", inspection.Worktree.Path, "-P", "-F", "#{session_id}",
		";", "set-option", "-t", "=" + name, SchemaOption, SchemaVersion,
		";", "set-option", "-t", "=" + name, IDOption, id,
		";", "set-option", "-t", "=" + name, KindOption, KindShell,
		";", "set-option", "-t", "=" + name, CreatedAtOption, created.Format(time.RFC3339),
		";", "set-option", "-t", "=" + name, WorktreePathOption, inspection.Worktree.Path}
	result, runErr := s.commands.Run(ctx, maxMetadataBytes, "tmux", args...)
	if runErr != nil || result.Truncated {
		return StartResult{}, &repository.Error{Code: repository.CodeOutcomeUnknown, Message: "tmux Session creation outcome could not be verified", Cause: runErr}
	}
	tmuxID := strings.TrimSpace(string(result.Stdout))
	if !validTmuxID(tmuxID) {
		return StartResult{}, &repository.Error{Code: repository.CodeOutcomeUnknown, Message: "tmux Session creation returned an invalid target"}
	}
	rows, err := listRows(ctx, s.commands)
	if err != nil {
		return StartResult{}, &repository.Error{Code: repository.CodeOutcomeUnknown, Message: "tmux Session creation returned invalid metadata", Cause: err}
	}
	var value Session
	for _, row := range rows {
		if row.tmuxID == tmuxID {
			value = classifyRow(row)
			break
		}
	}
	associateManaged(&value, nil, flattenWorktrees(repository.Catalog{Repositories: []repository.Repository{inspection.Repository}}))
	if value.Ownership != Managed || value.ID != id || value.WorktreePath != inspection.Worktree.Path || value.Association != AssociationLive {
		return StartResult{}, &repository.Error{Code: repository.CodeOutcomeUnknown, Message: "tmux Session metadata was not committed safely"}
	}
	return StartResult{Session: value, Created: true}, nil
}

func (s *Service) Resolve(ctx context.Context, selector string) (Session, error) {
	if selector == "" || hasControl(selector) {
		return Session{}, &repository.Error{Code: repository.CodeInvalidInput, Message: "Session selector is required"}
	}
	catalog, err := s.List(ctx)
	if err != nil {
		return Session{}, err
	}
	var matches []Session
	if strings.HasPrefix(selector, "tmux:") {
		tmuxID := strings.TrimPrefix(selector, "tmux:")
		if !validTmuxID(tmuxID) {
			return Session{}, &repository.Error{Code: repository.CodeInvalidInput, Message: "unmanaged Session selector is invalid"}
		}
		for _, value := range catalog.Sessions {
			if value.TmuxID == tmuxID && value.Ownership == Unmanaged {
				matches = append(matches, value)
			}
		}
	} else {
		for _, value := range catalog.Sessions {
			if value.ID == selector && value.Ownership == Managed {
				matches = append(matches, value)
			}
		}
		if len(matches) == 0 {
			inspection, inspectErr := repository.Inspect(ctx, s.root, selector)
			if inspectErr != nil {
				return Session{}, inspectErr
			}
			for _, value := range catalog.Sessions {
				if value.Ownership == Managed && value.Association == AssociationLive && value.WorktreePath == inspection.Worktree.Path {
					matches = append(matches, value)
				}
			}
		}
	}
	if len(matches) == 0 {
		return Session{}, &repository.Error{Code: repository.CodeNotFound, Message: fmt.Sprintf("Session %q was not found", selector)}
	}
	if len(matches) > 1 {
		return Session{}, &repository.Error{Code: repository.CodeConflict, Message: fmt.Sprintf("Session selector %q is ambiguous", selector)}
	}
	return matches[0], nil
}

func (s *Service) Attachment(ctx context.Context, selector string, insideTmux bool) (Attachment, error) {
	value, err := s.Resolve(ctx, selector)
	if err != nil {
		return Attachment{}, err
	}
	command := "attach-session"
	if insideTmux {
		command = "switch-client"
	}
	args := []string{"-L", tmuxSocketName, command, "-t", value.TmuxID}
	if value.Ownership == Managed {
		action := fmt.Sprintf("%s -t %s", command, value.TmuxID)
		refusal := "display-message -p 'Schooner refused attachment because Session ownership changed.' ; run-shell \"exit 76\""
		args = append([]string{"-L", tmuxSocketName}, managedActionArguments(value, action, refusal)...)
	}
	excluded := []string(nil)
	if !insideTmux {
		excluded = []string{"TMUX", "TMUX_TMPDIR"}
	}
	return Attachment{Session: value, Path: "tmux", Args: args, ExcludedEnvironment: excluded}, nil
}

func (s *Service) Logs(ctx context.Context, id string, lines int) (LogsResult, error) {
	if !validManagedID(id) {
		return LogsResult{}, &repository.Error{Code: repository.CodeInvalidInput, Message: "logs require a managed Session ID"}
	}
	if lines == 0 {
		lines = DefaultLogLines
	}
	if lines < 1 || lines > MaxLogLines {
		return LogsResult{}, &repository.Error{Code: repository.CodeInvalidInput, Message: fmt.Sprintf("log lines must be between 1 and %d", MaxLogLines)}
	}
	value, err := s.resolveManagedID(ctx, id)
	if err != nil {
		return LogsResult{}, err
	}
	if value.Ownership != Managed {
		return LogsResult{}, &repository.Error{Code: repository.CodeConflict, Message: "logs are available only for managed Sessions"}
	}
	action := fmt.Sprintf("display-message -p %s ; capture-pane -p -J -t %s -S -%d", managedActionGranted, value.TmuxID, lines)
	result, err := s.runManagedAction(ctx, value, MaxLogBytes, action)
	if err != nil {
		if repository.ErrorCode(err) == repository.CodeConflict || repository.ErrorCode(err) == repository.CodeOutcomeUnknown {
			return LogsResult{}, err
		}
		return LogsResult{}, &repository.Error{Code: repository.CodeNotFound, Message: "managed Session is no longer running", Cause: err}
	}
	content, lineTruncated := trimLastLines(string(result.Stdout), lines)
	return LogsResult{SessionID: id, Lines: lines, Truncated: result.Truncated || lineTruncated, Content: content}, nil
}

func (s *Service) Stop(ctx context.Context, id string) (StopResult, error) {
	if !validManagedID(id) {
		return StopResult{}, &repository.Error{Code: repository.CodeInvalidInput, Message: "stop requires a managed Session ID"}
	}
	value, err := s.resolveManagedID(ctx, id)
	if err != nil {
		return StopResult{}, err
	}
	if value.Ownership != Managed {
		return StopResult{}, &repository.Error{Code: repository.CodeConflict, Message: "only managed Sessions may be stopped"}
	}
	action := fmt.Sprintf("display-message -p %s ; kill-session -t %s", managedActionGranted, value.TmuxID)
	result, err := s.runManagedAction(ctx, value, 64<<10, action)
	if err != nil {
		if repository.ErrorCode(err) == repository.CodeConflict || repository.ErrorCode(err) == repository.CodeOutcomeUnknown {
			return StopResult{}, err
		}
		return StopResult{}, &repository.Error{Code: repository.CodeNotFound, Message: "managed Session is no longer running", Cause: errors.Join(err, errors.New(string(result.Stderr)))}
	}
	return StopResult{SessionID: id, Stopped: true}, nil
}

// runManagedAction keeps the final ownership check and the protected tmux
// operation in one server-side command queue. A stale tmux $N target can
// therefore fail or be refused, but it cannot be reused as an unmanaged target
// between validation and the capture or kill.
func (s *Service) runManagedAction(ctx context.Context, value Session, maximum int, action string) (process.Result, error) {
	refusal := "display-message -p " + managedActionRefused
	prefix := []byte(managedActionGranted + "\n")
	result, err := s.commands.Run(ctx, maximum+len(prefix), "tmux", managedActionArguments(value, action, refusal)...)
	if err != nil {
		return result, err
	}
	if string(result.Stdout) == managedActionRefused+"\n" {
		return result, &repository.Error{Code: repository.CodeConflict, Message: "managed Session ownership changed; operation was not performed"}
	}
	if !strings.HasPrefix(string(result.Stdout), string(prefix)) {
		return result, &repository.Error{Code: repository.CodeOutcomeUnknown, Message: "tmux did not confirm managed Session ownership"}
	}
	result.Stdout = result.Stdout[len(prefix):]
	return result, nil
}

func (s *Service) resolveManagedID(ctx context.Context, id string) (Session, error) {
	catalog, err := s.List(ctx)
	if err != nil {
		return Session{}, err
	}
	matches := make([]Session, 0, 1)
	for _, value := range catalog.Sessions {
		if value.Ownership == Managed && value.ID == id {
			matches = append(matches, value)
		}
	}
	if len(matches) == 0 {
		return Session{}, &repository.Error{Code: repository.CodeNotFound, Message: fmt.Sprintf("managed Session %q was not found", id)}
	}
	if len(matches) > 1 {
		return Session{}, &repository.Error{Code: repository.CodeConflict, Message: fmt.Sprintf("managed Session ID %q is ambiguous", id)}
	}
	return matches[0], nil
}

func managedActionArguments(value Session, action, refusal string) []string {
	return []string{"if-shell", "-F", "-t", value.TmuxID, managedSessionCondition(value), action, refusal}
}

func managedSessionCondition(value Session) string {
	metadataWorktreePath := value.MetadataWorktreePath
	if metadataWorktreePath == "" {
		metadataWorktreePath = value.WorktreePath
	}
	checks := []string{
		fmt.Sprintf("#{==:#{%s},%s}", SchemaOption, SchemaVersion),
		fmt.Sprintf("#{==:#{%s},%s}", IDOption, tmuxFormatLiteral(value.ID)),
		fmt.Sprintf("#{==:#{%s},%s}", KindOption, KindShell),
		fmt.Sprintf("#{==:#{%s},%s}", CreatedAtOption, value.CreatedAt.UTC().Format(time.RFC3339)),
		fmt.Sprintf("#{==:#{%s},%s}", WorktreePathOption, tmuxFormatLiteral(metadataWorktreePath)),
	}
	if value.LegacyMetadata {
		checks = []string{
			fmt.Sprintf("#{==:#{%s},%s}", SchemaOption, LegacySchemaVersion),
			fmt.Sprintf("#{==:#{%s},%s}", IDOption, tmuxFormatLiteral(value.ID)),
			fmt.Sprintf("#{==:#{%s},}", KindOption),
			fmt.Sprintf("#{==:#{%s},}", CreatedAtOption),
			fmt.Sprintf("#{==:#{%s},%s}", WorktreePathOption, tmuxFormatLiteral(metadataWorktreePath)),
		}
	}
	condition := checks[len(checks)-1]
	for index := len(checks) - 2; index >= 0; index-- {
		condition = "#{&&:" + checks[index] + "," + condition + "}"
	}
	return condition
}

func tmuxFormatLiteral(value string) string {
	value = strings.ReplaceAll(value, "#", "##")
	value = strings.ReplaceAll(value, ",", "#,")
	return strings.ReplaceAll(value, "}", "#}")
}

type tmuxRow struct {
	tmuxID, name, created, activity, attached  string
	schema, id, kind, managedCreated, worktree string
}

func (row tmuxRow) hasSchoonerMetadata() bool {
	return row.schema != "" || row.id != "" || row.kind != "" || row.managedCreated != "" || row.worktree != ""
}

func listRows(ctx context.Context, commands commandRunner) ([]tmuxRow, error) {
	result, err := commands.Run(ctx, maxMetadataBytes, "tmux", "list-sessions", "-F", sessionListFormat())
	if err != nil {
		message := strings.ToLower(string(result.Stderr))
		if process.ExitCode(err) == 1 && (strings.Contains(message, "no server running") || strings.Contains(message, "no sessions") || strings.Contains(message, "no such file or directory")) {
			return []tmuxRow{}, nil
		}
		return nil, fmt.Errorf("list tmux Sessions: %w", err)
	}
	if result.Truncated {
		return nil, &repository.Error{Code: repository.CodeOutcomeUnknown, Message: "tmux Session metadata exceeded 1 MiB"}
	}
	return parseRows(result.Stdout)
}

func sessionListFormat() string {
	fields := []string{"session_id", "session_name", "session_created", "session_activity", "session_attached", SchemaOption, IDOption, KindOption, CreatedAtOption, WorktreePathOption}
	framed := make([]string, 0, len(fields)*2)
	for _, field := range fields {
		framed = append(framed, "#{n:"+field+"}", "#{"+field+"}")
	}
	return strings.Join(framed, "\t")
}

func parseRows(output []byte) ([]tmuxRow, error) {
	rows := make([]tmuxRow, 0)
	for len(output) > 0 {
		if len(rows) >= maxSessions {
			return nil, &repository.Error{Code: repository.CodeOutcomeUnknown, Message: "tmux Session count exceeded 256"}
		}
		fields := make([]string, 10)
		for index := range fields {
			separator := bytes.IndexByte(output, '\t')
			if separator < 0 {
				return nil, errors.New("tmux returned malformed Session metadata")
			}
			length, err := strconv.Atoi(string(output[:separator]))
			start := separator + 1
			if err != nil || length < 0 || length > maxMetadataBytes || start+length >= len(output) {
				return nil, errors.New("tmux returned malformed Session metadata")
			}
			fields[index] = string(output[start : start+length])
			delimiter := byte('\t')
			if index == len(fields)-1 {
				delimiter = '\n'
			}
			if output[start+length] != delimiter {
				return nil, errors.New("tmux returned malformed Session metadata")
			}
			output = output[start+length+1:]
		}
		rows = append(rows, tmuxRow{fields[0], fields[1], fields[2], fields[3], fields[4], fields[5], fields[6], fields[7], fields[8], fields[9]})
	}
	return rows, nil
}

func classifyRow(row tmuxRow) Session {
	value := Session{TmuxID: row.tmuxID, Name: row.name, Ownership: Unmanaged, Association: AssociationUnassociated, SchoonerMetadata: row.hasSchoonerMetadata()}
	created, createdErr := unixTime(row.created)
	activity, activityErr := unixTime(row.activity)
	attached, attachedErr := strconv.Atoi(row.attached)
	if !validTmuxID(row.tmuxID) || !safeLabel(row.name, 256) || createdErr != nil || activityErr != nil || attachedErr != nil || attached < 0 {
		value.Ownership = Invalid
		return value
	}
	value.CreatedAt, value.ActivityAt, value.AttachedClients = created, activity, attached
	if !value.SchoonerMetadata {
		return value
	}
	if row.schema == LegacySchemaVersion && validLegacyID(row.id) && row.kind == "" && row.managedCreated == "" && canonicalAbsolute(row.worktree) {
		value.Ownership, value.ID, value.Kind, value.WorktreePath = Managed, row.id, KindShell, row.worktree
		value.MetadataWorktreePath = row.worktree
		value.LegacyMetadata = true
		return value
	}
	managedCreated, err := time.Parse(time.RFC3339, row.managedCreated)
	if row.schema != SchemaVersion || !validID(row.id) || row.kind != KindShell || err != nil || !canonicalAbsolute(row.worktree) {
		value.Ownership = Invalid
		value.WorktreePath = row.worktree
		return value
	}
	value.Ownership, value.ID, value.Kind, value.CreatedAt, value.WorktreePath = Managed, row.id, row.kind, managedCreated.UTC(), row.worktree
	value.MetadataWorktreePath = row.worktree
	return value
}

func listPanes(ctx context.Context, commands commandRunner) (map[string][]string, error) {
	result, err := commands.Run(ctx, maxMetadataBytes, "tmux", "list-panes", "-a", "-F", "#{session_id}\t#{n:pane_current_path}\t#{pane_current_path}")
	if err != nil {
		message := strings.ToLower(string(result.Stderr))
		if process.ExitCode(err) == 1 && (strings.Contains(message, "no server running") || strings.Contains(message, "no sessions") || strings.Contains(message, "no such file or directory")) {
			return map[string][]string{}, nil
		}
		return nil, fmt.Errorf("list tmux panes: %w", err)
	}
	if result.Truncated {
		return nil, &repository.Error{Code: repository.CodeOutcomeUnknown, Message: "tmux pane metadata exceeded 1 MiB"}
	}
	return parsePanes(result.Stdout)
}

func parsePanes(output []byte) (map[string][]string, error) {
	grouped := map[string][]string{}
	for count := 0; len(output) > 0; count++ {
		if count >= maxPanes {
			return nil, &repository.Error{Code: repository.CodeOutcomeUnknown, Message: "tmux pane count exceeded 4096"}
		}
		first := bytes.IndexByte(output, '\t')
		if first < 0 {
			return nil, errors.New("tmux returned malformed pane metadata")
		}
		secondRelative := bytes.IndexByte(output[first+1:], '\t')
		if secondRelative < 0 {
			return nil, errors.New("tmux returned malformed pane metadata")
		}
		second := first + 1 + secondRelative
		tmuxID := string(output[:first])
		length, err := strconv.Atoi(string(output[first+1 : second]))
		pathStart := second + 1
		if err != nil || length < 0 || length > 4096 || pathStart+length >= len(output) || output[pathStart+length] != '\n' {
			return nil, errors.New("tmux returned malformed pane metadata")
		}
		path := string(output[pathStart : pathStart+length])
		if !validTmuxID(tmuxID) || !canonicalPanePath(path) {
			return nil, errors.New("tmux returned malformed pane metadata")
		}
		grouped[tmuxID] = append(grouped[tmuxID], path)
		output = output[pathStart+length+1:]
	}
	return grouped, nil
}

type liveWorktree struct {
	path, relative, common string
}

func flattenWorktrees(catalog repository.Catalog) []liveWorktree {
	result := make([]liveWorktree, 0)
	for _, relation := range catalog.Repositories {
		if relation.Primary != nil {
			result = append(result, liveWorktree{relation.Primary.Path, relation.Primary.RelativePath, relation.CommonDirectory})
		}
		for _, worktree := range relation.Linked {
			result = append(result, liveWorktree{worktree.Path, worktree.RelativePath, relation.CommonDirectory})
		}
	}
	sort.Slice(result, func(i, j int) bool { return len(result[i].path) > len(result[j].path) })
	return result
}

func associateManaged(value *Session, panes []string, worktrees []liveWorktree) {
	for _, worktree := range worktrees {
		if value.WorktreePath == worktree.path {
			value.WorktreeRelativePath = worktree.relative
			value.RepositoryCommonDirectory = worktree.common
			value.Association = AssociationLive
			return
		}
	}
	if len(panes) != 0 {
		moved := Session{Ownership: Managed}
		associateUnmanaged(&moved, panes, worktrees)
		if moved.Association == AssociationLive {
			value.WorktreePath = moved.WorktreePath
			value.WorktreeRelativePath = moved.WorktreeRelativePath
			value.RepositoryCommonDirectory = moved.RepositoryCommonDirectory
			value.Association = AssociationLive
			return
		}
	}
	value.Association = AssociationMissing
}

func panesUsePath(panes []string, worktreePath string) bool {
	for _, pane := range panes {
		relative, err := filepath.Rel(worktreePath, pane)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func trimLastLines(content string, maximum int) (string, bool) {
	if content == "" {
		return "", false
	}
	trailingNewline := strings.HasSuffix(content, "\n")
	text := strings.TrimSuffix(content, "\n")
	lines := strings.Split(text, "\n")
	if len(lines) <= maximum {
		return content, false
	}
	trimmed := strings.Join(lines[len(lines)-maximum:], "\n")
	if trailingNewline {
		trimmed += "\n"
	}
	return trimmed, true
}

func associateUnmanaged(value *Session, panes []string, worktrees []liveWorktree) {
	if len(panes) == 0 {
		value.Association = AssociationUnassociated
		return
	}
	var selected *liveWorktree
	for _, pane := range panes {
		current := worktreeForPath(pane, worktrees)
		if current == nil {
			value.Association = AssociationAmbiguous
			return
		}
		if selected != nil && selected.path != current.path {
			value.Association = AssociationAmbiguous
			return
		}
		selected = current
	}
	if selected == nil {
		value.Association = AssociationUnassociated
		return
	}
	value.WorktreePath, value.WorktreeRelativePath, value.RepositoryCommonDirectory = selected.path, selected.relative, selected.common
	value.Association = AssociationLive
}

func worktreeForPath(path string, worktrees []liveWorktree) *liveWorktree {
	for index := range worktrees {
		relative, err := filepath.Rel(worktrees[index].path, path)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return &worktrees[index]
		}
	}
	return nil
}

func managedForPath(values []Session, path string) []Session {
	result := make([]Session, 0)
	for _, value := range values {
		if value.Ownership == Managed && value.WorktreePath == path {
			result = append(result, value)
		}
	}
	return result
}

func ownershipOrder(value Ownership) int {
	switch value {
	case Managed:
		return 0
	case Unmanaged:
		return 1
	default:
		return 2
	}
}

func randomID() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	data[6] = data[6]&0x0f | 0x40
	data[8] = data[8]&0x3f | 0x80
	encoded := hex.EncodeToString(data[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:], nil
}

func compactID(value string) string { return strings.ReplaceAll(value, "-", "") }

func validID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' || value[14] != '4' {
		return false
	}
	compact := compactID(value)
	decoded, err := hex.DecodeString(compact)
	return err == nil && len(decoded) == 16 && decoded[8]&0xc0 == 0x80
}

func validLegacyID(value string) bool { return safeLabel(value, 256) }

func validManagedID(value string) bool { return validID(value) || validLegacyID(value) }

func validTmuxID(value string) bool {
	if len(value) < 2 || value[0] != '$' {
		return false
	}
	_, err := strconv.ParseUint(value[1:], 10, 64)
	return err == nil
}

func unixTime(value string) (time.Time, error) {
	seconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(seconds, 0).UTC(), nil
}

func canonicalAbsolute(value string) bool {
	return value != "" && filepath.IsAbs(value) && filepath.Clean(value) == value && len(value) <= 4096 && !hasControl(value)
}

func canonicalPanePath(value string) bool {
	return value != "" && filepath.IsAbs(value) && filepath.Clean(value) == value && len(value) <= 4096
}

func safeLabel(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && utf8.ValidString(value) && !hasControl(value)
}

func hasControl(value string) bool {
	if !utf8.ValidString(value) {
		return true
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return true
		}
	}
	return false
}

func InsideTmux() bool {
	socket, _, found := strings.Cut(os.Getenv("TMUX"), ",")
	return found && filepath.Base(socket) == tmuxSocketName && filepath.Dir(filepath.Dir(socket)) == "/tmp"
}
