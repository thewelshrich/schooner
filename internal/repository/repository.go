// Package repository discovers Git repositories and worktrees from live state.
package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/thewelshrich/schooner/internal/process"
)

const (
	maxDepth          = 8
	maxCandidates     = 500
	maxVisited        = 10_000
	maxWarnings       = 100
	maxOutputBytes    = 1 << 20
	maxCatalogBytes   = 3 << 18
	maxOriginBytes    = 32 << 10
	gitCommandTimeout = 30 * time.Second
)

var gitRepositoryEnvironment = []string{
	"GIT_ALTERNATE_OBJECT_DIRECTORIES",
	"GIT_COMMON_DIR",
	"GIT_CONFIG",
	"GIT_CONFIG_COUNT",
	"GIT_CONFIG_PARAMETERS",
	"GIT_DIR",
	"GIT_GRAFT_FILE",
	"GIT_IMPLICIT_WORK_TREE",
	"GIT_INDEX_FILE",
	"GIT_INTERNAL_SUPER_PREFIX",
	"GIT_NAMESPACE",
	"GIT_NO_REPLACE_OBJECTS",
	"GIT_OBJECT_DIRECTORY",
	"GIT_PREFIX",
	"GIT_REPLACE_REF_BASE",
	"GIT_SHALLOW_FILE",
	"GIT_TERMINAL_PROMPT",
	"GCM_INTERACTIVE",
	"GIT_SSH_COMMAND",
	"GIT_SSH_VARIANT",
	"GIT_WORK_TREE",
}

var errNotWorktree = errors.New("not a live Git worktree")

type WorktreeKind string

type Code string

const (
	Primary WorktreeKind = "primary"
	Linked  WorktreeKind = "linked"

	CodeNotFound     Code = "not_found"
	CodeInvalidInput Code = "invalid_input"
)

type Error struct {
	Code    Code
	Message string
	Cause   error
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.Cause }

func ErrorCode(err error) Code {
	var target *Error
	if errors.As(err, &target) {
		return target.Code
	}
	return ""
}

type Status struct {
	Staged     int `json:"staged"`
	Unstaged   int `json:"unstaged"`
	Untracked  int `json:"untracked"`
	Conflicted int `json:"conflicted"`
	// Ignored is used for local removal safety but intentionally remains out of
	// the version-1 wire shape, whose strict clients know only the four fields above.
	Ignored int `json:"-"`
}

type Worktree struct {
	Path         string       `json:"path"`
	RelativePath string       `json:"relative_path"`
	GitDirectory string       `json:"git_directory"`
	Kind         WorktreeKind `json:"kind"`
	Branch       string       `json:"branch,omitempty"`
	Detached     bool         `json:"detached"`
	HEAD         string       `json:"head,omitempty"`
	Status       Status       `json:"status"`
}

type Repository struct {
	CommonDirectory string     `json:"common_directory"`
	Origin          string     `json:"origin,omitempty"`
	Primary         *Worktree  `json:"primary,omitempty"`
	Linked          []Worktree `json:"linked"`
}

type Warning struct {
	Path    string `json:"path,omitempty"`
	Message string `json:"message"`
}

type Catalog struct {
	WorktreeRoot string       `json:"worktree_root"`
	Repositories []Repository `json:"repositories"`
	Warnings     []Warning    `json:"warnings"`
}

type Inspection struct {
	WorktreeRoot string     `json:"worktree_root"`
	Repository   Repository `json:"repository"`
	Worktree     Worktree   `json:"worktree"`
	Warnings     []Warning  `json:"warnings"`
}

type discoveryMetrics struct {
	Visited   int
	Inspected int
	Truncated bool
}

func Discover(ctx context.Context, worktreeRoot string) (Catalog, error) {
	return discover(ctx, worktreeRoot, commandRunner{})
}

func Inspect(ctx context.Context, worktreeRoot, selector string) (Inspection, error) {
	return inspect(ctx, worktreeRoot, selector, commandRunner{})
}

type runner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

type commandRunner struct{}

func (commandRunner) Run(ctx context.Context, name string, arguments ...string) ([]byte, error) {
	return process.RunWithoutEnvironment(ctx, maxOutputBytes, gitRepositoryEnvironment, name, arguments...)
}

type observation struct {
	repository string
	origin     string
	worktree   Worktree
}

func discover(ctx context.Context, root string, commands runner) (Catalog, error) {
	canonicalRoot, err := canonicalDirectory(root)
	if err != nil {
		return Catalog{}, err
	}
	result := Catalog{WorktreeRoot: canonicalRoot, Repositories: []Repository{}, Warnings: []Warning{}}
	candidates, warnings, err := walkCandidates(ctx, canonicalRoot, commands)
	if err != nil {
		return Catalog{}, err
	}
	result.Warnings = append(result.Warnings, warnings...)
	groups := map[string]*Repository{}
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return Catalog{}, err
		}
		item, inspectErr := inspectCandidate(ctx, canonicalRoot, candidate, commands)
		if inspectErr != nil {
			appendWarning(&result.Warnings, candidate, inspectErr.Error())
			continue
		}
		group := groups[item.repository]
		if group == nil {
			group = &Repository{CommonDirectory: item.repository, Origin: item.origin, Linked: []Worktree{}}
			groups[item.repository] = group
		}
		if group.Origin == "" {
			group.Origin = item.origin
		}
		if item.worktree.Kind == Primary {
			copy := item.worktree
			group.Primary = &copy
		} else {
			group.Linked = append(group.Linked, item.worktree)
		}
	}
	for _, group := range groups {
		sort.Slice(group.Linked, func(i, j int) bool { return group.Linked[i].RelativePath < group.Linked[j].RelativePath })
		result.Repositories = append(result.Repositories, *group)
	}
	sort.Slice(result.Repositories, func(i, j int) bool {
		return firstPath(result.Repositories[i]) < firstPath(result.Repositories[j])
	})
	boundCatalog(&result)
	return result, nil
}

func inspect(ctx context.Context, root, selector string, commands runner) (Inspection, error) {
	canonicalRoot, err := canonicalDirectory(root)
	if err != nil {
		return Inspection{}, err
	}
	target, err := resolveSelector(canonicalRoot, selector)
	if err != nil {
		return Inspection{}, err
	}
	item, err := inspectCandidate(ctx, canonicalRoot, target, commands)
	if err != nil {
		if errors.Is(err, errNotWorktree) || errors.Is(err, os.ErrNotExist) || pathMissing(target) {
			return Inspection{}, worktreeNotFound(selector, err)
		}
		return Inspection{}, fmt.Errorf("inspect worktree %q: %w", selector, err)
	}
	catalog, err := discover(ctx, canonicalRoot, commands)
	if err != nil {
		return Inspection{}, err
	}
	repository := repositoryRelationship(catalog, item)
	latest, err := inspectCandidate(ctx, canonicalRoot, target, commands)
	if err != nil {
		if errors.Is(err, errNotWorktree) || errors.Is(err, os.ErrNotExist) || pathMissing(target) {
			return Inspection{}, worktreeNotFound(selector, err)
		}
		return Inspection{}, fmt.Errorf("revalidate worktree %q: %w", selector, err)
	}
	if latest.repository != item.repository {
		repository = repositoryRelationship(catalog, latest)
	}
	repository.Origin = latest.origin
	selectedPresent := false
	if repository.Primary != nil && repository.Primary.Path == latest.worktree.Path {
		copy := latest.worktree
		repository.Primary = &copy
		selectedPresent = true
	}
	for index := range repository.Linked {
		if repository.Linked[index].Path == latest.worktree.Path {
			repository.Linked[index] = latest.worktree
			selectedPresent = true
		}
	}
	if !selectedPresent {
		if latest.worktree.Kind == Primary {
			copy := latest.worktree
			repository.Primary = &copy
		} else {
			repository.Linked = append(repository.Linked, latest.worktree)
			sort.Slice(repository.Linked, func(i, j int) bool { return repository.Linked[i].RelativePath < repository.Linked[j].RelativePath })
		}
	}
	return Inspection{WorktreeRoot: canonicalRoot, Repository: repository, Worktree: latest.worktree, Warnings: catalog.Warnings}, nil
}

func repositoryRelationship(catalog Catalog, item observation) Repository {
	for _, candidate := range catalog.Repositories {
		if candidate.CommonDirectory == item.repository {
			return candidate
		}
	}
	repository := Repository{CommonDirectory: item.repository, Origin: item.origin, Linked: []Worktree{}}
	if item.worktree.Kind == Primary {
		copy := item.worktree
		repository.Primary = &copy
	} else {
		repository.Linked = append(repository.Linked, item.worktree)
	}
	return repository
}

func walkCandidates(ctx context.Context, root string, commands runner) ([]string, []Warning, error) {
	return walkCandidatesBounded(ctx, root, maxVisited, commands)
}

func walkCandidatesBounded(ctx context.Context, root string, visitLimit int, commands runner) ([]string, []Warning, error) {
	candidates, warnings, _, err := walkCandidatesMeasured(ctx, root, visitLimit, commands)
	return candidates, warnings, err
}

func walkCandidatesMeasured(ctx context.Context, root string, visitLimit int, commands runner) ([]string, []Warning, discoveryMetrics, error) {
	candidates := make([]string, 0)
	warnings := make([]Warning, 0)
	errCandidateLimit := errors.New("candidate limit reached")
	errVisitLimit := errors.New("filesystem entry limit reached")
	visited := 0
	inspected := 0
	depthLimited := false
	var walk func(string, int) error
	walk = func(path string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if visited == visitLimit {
			appendRequiredWarning(&warnings, root, fmt.Sprintf("filesystem entry limit of %d reached", visitLimit))
			return errVisitLimit
		}
		visited++
		if !utf8.ValidString(path) {
			appendWarning(&warnings, path, "directory path is not valid UTF-8")
			return nil
		}
		if depth > maxDepth {
			if !depthLimited {
				appendRequiredWarning(&warnings, root, fmt.Sprintf("discovery depth limit of %d reached", maxDepth))
				depthLimited = true
			}
			return nil
		}
		gitPath := filepath.Join(path, ".git")
		info, statErr := os.Lstat(gitPath)
		if statErr == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				appendWarning(&warnings, path, ".git symlink was ignored")
			}
			if info.IsDir() || info.Mode().IsRegular() {
				if inspected == maxCandidates {
					appendRequiredWarning(&warnings, root, fmt.Sprintf("checkout candidate limit of %d reached", maxCandidates))
					return errCandidateLimit
				}
				inspected++
				if validationErr := validateTopmostCandidate(ctx, root, path, commands); validationErr != nil {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					appendWarning(&warnings, path, validationErr.Error())
				} else {
					candidates = append(candidates, path)
					return nil
				}
			}
		} else if !errors.Is(statErr, os.ErrNotExist) {
			appendWarning(&warnings, path, statErr.Error())
		}
		directory, openErr := os.Open(path)
		if openErr != nil {
			appendWarning(&warnings, path, openErr.Error())
			return nil
		}
		defer func() { _ = directory.Close() }()
		for {
			remaining := visitLimit - visited
			if remaining == 0 {
				appendRequiredWarning(&warnings, root, fmt.Sprintf("filesystem entry limit of %d reached", visitLimit))
				return errVisitLimit
			}
			entries, readErr := directory.ReadDir(min(128, remaining))
			for _, entry := range entries {
				child := filepath.Join(path, entry.Name())
				if entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() {
					visited++
					continue
				}
				if err := walk(child, depth+1); err != nil {
					return err
				}
			}
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			if readErr != nil {
				appendWarning(&warnings, path, readErr.Error())
				return nil
			}
		}
	}
	err := walk(root, 0)
	metrics := discoveryMetrics{Visited: visited, Inspected: inspected, Truncated: errors.Is(err, errCandidateLimit) || errors.Is(err, errVisitLimit)}
	if errors.Is(err, errCandidateLimit) || errors.Is(err, errVisitLimit) {
		err = nil
	}
	return candidates, warnings, metrics, err
}

func validateTopmostCandidate(ctx context.Context, root, candidate string, commands runner) error {
	canonical, err := canonicalDirectory(candidate)
	if err != nil {
		return err
	}
	if !within(root, canonical) {
		return fmt.Errorf("worktree path escapes configured root")
	}
	paths, err := git(ctx, commands, canonical, "rev-parse", "--path-format=absolute", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("read Git top-level: %w", err)
	}
	top := strings.TrimSuffix(string(paths), "\n")
	if top == "" || hasControl(top) {
		return fmt.Errorf("Git returned malformed top-level")
	}
	resolvedTop, err := filepath.EvalSymlinks(filepath.Clean(top))
	if err != nil || resolvedTop != canonical {
		return fmt.Errorf("candidate is not the Git top-level worktree")
	}
	membershipOutput, err := git(ctx, commands, canonical, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return fmt.Errorf("list Git worktrees: %w", err)
	}
	members, err := parseWorktreeList(membershipOutput)
	if err != nil {
		return err
	}
	if _, found := membership(canonical, members); !found {
		return fmt.Errorf("checkout is not a live Git worktree member")
	}
	return nil
}

func inspectCandidate(ctx context.Context, root, candidate string, commands runner) (observation, error) {
	canonical, err := canonicalDirectory(candidate)
	if err != nil {
		return observation{}, err
	}
	if !within(root, canonical) {
		return observation{}, fmt.Errorf("worktree path escapes configured root")
	}
	paths, err := git(ctx, commands, canonical, "rev-parse", "--path-format=absolute", "--show-toplevel", "--absolute-git-dir", "--git-common-dir")
	if err != nil {
		if exitCode(err) >= 0 {
			return observation{}, fmt.Errorf("%w: read Git paths: %v", errNotWorktree, err)
		}
		return observation{}, fmt.Errorf("read Git paths: %w", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(paths), "\n"), "\n")
	if len(lines) != 3 || hasControl(lines[0]) || hasControl(lines[1]) || hasControl(lines[2]) {
		return observation{}, fmt.Errorf("Git returned malformed paths")
	}
	top, err := filepath.EvalSymlinks(filepath.Clean(lines[0]))
	if err != nil || top != canonical {
		return observation{}, fmt.Errorf("%w: candidate is not the Git top-level worktree", errNotWorktree)
	}
	gitDirectory, err := canonicalPath(lines[1])
	if err != nil {
		return observation{}, fmt.Errorf("resolve Git directory: %w", err)
	}
	commonDirectory, err := canonicalPath(lines[2])
	if err != nil {
		return observation{}, fmt.Errorf("resolve Git common directory: %w", err)
	}
	membershipOutput, err := git(ctx, commands, canonical, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return observation{}, fmt.Errorf("list Git worktrees: %w", err)
	}
	members, err := parseWorktreeList(membershipOutput)
	if err != nil {
		return observation{}, err
	}
	kind, found := membership(canonical, members)
	if !found {
		return observation{}, fmt.Errorf("%w: checkout is not a live Git worktree member", errNotWorktree)
	}
	relative, err := filepath.Rel(root, canonical)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return observation{}, fmt.Errorf("worktree path escapes configured root")
	}
	worktree := Worktree{Path: canonical, RelativePath: filepath.ToSlash(relative), GitDirectory: gitDirectory, Kind: kind}
	branch, branchErr := git(ctx, commands, canonical, "symbolic-ref", "--quiet", "--short", "HEAD")
	if branchErr == nil {
		worktree.Branch = strings.TrimSuffix(string(branch), "\n")
		if worktree.Branch == "" || hasControl(worktree.Branch) {
			return observation{}, fmt.Errorf("Git returned malformed branch")
		}
	} else if exitCode(branchErr) == 1 {
		worktree.Detached = true
	} else {
		return observation{}, fmt.Errorf("read branch: %w", branchErr)
	}
	head, headErr := git(ctx, commands, canonical, "rev-parse", "--verify", "HEAD")
	if headErr == nil {
		worktree.HEAD = strings.TrimSpace(string(head))
	} else if exitCode(headErr) != 128 {
		return observation{}, fmt.Errorf("read HEAD: %w", headErr)
	}
	status, err := git(ctx, commands, canonical, "status", "--porcelain=v2", "-z", "--untracked-files=all", "--ignored=matching")
	if err != nil {
		return observation{}, fmt.Errorf("read worktree status: %w", err)
	}
	worktree.Status, err = parseStatus(status)
	if err != nil {
		return observation{}, err
	}
	origin := ""
	if rawOrigin, originErr := git(ctx, commands, canonical, "remote", "get-url", "origin"); originErr == nil {
		raw := strings.TrimSpace(string(rawOrigin))
		origin = sanitizeOrigin(raw)
	} else if exitCode(originErr) != 2 {
		return observation{}, fmt.Errorf("read origin: %w", originErr)
	}
	return observation{repository: commonDirectory, origin: origin, worktree: worktree}, nil
}

type member struct {
	path     string
	primary  bool
	bare     bool
	prunable bool
	locked   bool
}

func parseWorktreeList(data []byte) ([]member, error) {
	fields := bytes.Split(data, []byte{0})
	result := make([]member, 0)
	current := member{}
	first := true
	flush := func() error {
		if current.path == "" && !current.bare {
			return nil
		}
		current.primary = first && !current.bare
		first = false
		result = append(result, current)
		current = member{}
		return nil
	}
	for _, raw := range fields {
		field := string(raw)
		if field == "" {
			if err := flush(); err != nil {
				return nil, err
			}
			continue
		}
		key, value, _ := strings.Cut(field, " ")
		switch key {
		case "worktree":
			if value == "" || hasControl(value) {
				return nil, fmt.Errorf("Git returned malformed worktree membership")
			}
			current.path = filepath.Clean(value)
		case "bare":
			current.bare = true
		case "prunable":
			current.prunable = true
		case "locked":
			current.locked = true
		}
	}
	if current.path != "" || current.bare {
		_ = flush()
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("Git returned no worktree membership")
	}
	return result, nil
}

func membership(candidate string, members []member) (WorktreeKind, bool) {
	for _, item := range members {
		if item.bare || item.prunable || item.path != candidate {
			continue
		}
		if item.primary {
			return Primary, true
		}
		return Linked, true
	}
	return "", false
}

func parseStatus(data []byte) (Status, error) {
	var result Status
	fields := bytes.Split(data, []byte{0})
	for index := 0; index < len(fields); index++ {
		field := fields[index]
		if len(field) == 0 {
			continue
		}
		switch field[0] {
		case '?':
			result.Untracked++
		case 'u':
			result.Conflicted++
		case '1', '2':
			parts := bytes.SplitN(field, []byte{' '}, 3)
			if len(parts) < 2 || len(parts[1]) != 2 {
				return Status{}, fmt.Errorf("Git returned malformed status")
			}
			if parts[1][0] != '.' {
				result.Staged++
			}
			if parts[1][1] != '.' {
				result.Unstaged++
			}
			if field[0] == '2' {
				if index+1 >= len(fields) || len(fields[index+1]) == 0 {
					return Status{}, fmt.Errorf("Git returned malformed rename status")
				}
				index++ // rename/copy records carry the original path as the next NUL field
			}
		case '!':
			result.Ignored++
		default:
			return Status{}, fmt.Errorf("Git returned unknown status record")
		}
	}
	return result, nil
}

func resolveSelector(root, selector string) (string, error) {
	if selector == "" || hasControl(selector) {
		return "", invalidSelector("worktree selector is required")
	}
	var target string
	if filepath.IsAbs(selector) {
		if filepath.Clean(selector) != selector {
			return "", invalidSelector("absolute worktree selector must be canonical")
		}
		target = selector
	} else {
		if filepath.Clean(selector) != selector || selector == ".." || strings.HasPrefix(selector, ".."+string(filepath.Separator)) {
			return "", invalidSelector("worktree selector must be an exact root-relative path")
		}
		target = filepath.Join(root, selector)
	}
	canonical, err := canonicalDirectory(target)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || pathIsNotDirectory(target) {
			return "", worktreeNotFound(selector, err)
		}
		return "", err
	}
	if !within(root, canonical) {
		return "", invalidSelector("worktree selector escapes configured root")
	}
	if canonical != target {
		return "", invalidSelector("worktree selector must not resolve through symlinks")
	}
	return canonical, nil
}

func worktreeNotFound(selector string, cause error) error {
	return &Error{Code: CodeNotFound, Message: fmt.Sprintf("worktree %q was not found", selector), Cause: cause}
}

func invalidSelector(message string) error {
	return &Error{Code: CodeInvalidInput, Message: message}
}

func pathMissing(path string) bool {
	_, err := os.Stat(path)
	return errors.Is(err, os.ErrNotExist)
}

func pathIsNotDirectory(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func canonicalDirectory(path string) (string, error) {
	if path == "" || !filepath.IsAbs(path) {
		return "", fmt.Errorf("worktree root must be an absolute path")
	}
	clean := filepath.Clean(path)
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", clean, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("inspect %s: %w", clean, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", clean)
	}
	return filepath.Clean(resolved), nil
}

func canonicalPath(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}

func within(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func git(ctx context.Context, commands runner, worktree string, arguments ...string) ([]byte, error) {
	return gitWithTimeout(ctx, commands, worktree, gitCommandTimeout, arguments...)
}

func gitWithTimeout(ctx context.Context, commands runner, worktree string, timeout time.Duration, arguments ...string) ([]byte, error) {
	commandContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	fixed := make([]string, 0, len(arguments)+5)
	fixed = append(fixed, "--no-optional-locks", "-c", "core.fsmonitor=false", "-C", worktree)
	fixed = append(fixed, arguments...)
	output, err := commands.Run(commandContext, "git", fixed...)
	if errors.Is(commandContext.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
		return nil, fmt.Errorf("Git command timed out after %s", timeout)
	}
	return output, err
}

func sanitizeOrigin(raw string) string {
	if raw == "" || hasControl(raw) {
		return ""
	}
	parsed, parseErr := url.Parse(raw)
	if parseErr == nil && parsed.Scheme != "" {
		if parsed.Opaque != "" {
			return ""
		}
		if strings.EqualFold(parsed.Scheme, "ssh") && parsed.User != nil {
			parsed.User = url.User(parsed.User.Username())
		} else {
			parsed.User = nil
		}
		parsed.RawQuery = ""
		parsed.Fragment = ""
		parsed.Path = strings.TrimSuffix(parsed.Path, ".git")
		return boundedOrigin(parsed.String())
	}
	if parseErr != nil && strings.Contains(raw, "://") {
		return ""
	}
	if index := strings.IndexAny(raw, "?#"); index >= 0 {
		raw = raw[:index]
	}
	if colon := scpPathSeparator(raw); colon > 0 && !strings.ContainsRune(raw[:colon], '/') {
		if at := strings.LastIndexByte(raw[:colon], '@'); at >= 0 {
			username := raw[:at]
			if originIdentityUsername(username) == "" && username != "git" {
				return ""
			}
		}
		return boundedOrigin(strings.TrimSuffix(raw, ".git"))
	}
	return boundedOrigin(strings.TrimSuffix(raw, ".git"))
}

func boundedOrigin(origin string) string {
	if len(origin) > maxOriginBytes {
		return ""
	}
	return origin
}

func boundCatalog(catalog *Catalog) {
	encoded, err := json.Marshal(catalog)
	if err != nil || len(encoded) <= maxCatalogBytes {
		return
	}
	limitWarning := Warning{Path: catalog.WorktreeRoot, Message: fmt.Sprintf("catalog output limit of %d bytes reached", maxCatalogBytes)}
	catalog.Warnings = []Warning{limitWarning}
	encoded, err = json.Marshal(catalog)
	if err == nil && len(encoded) <= maxCatalogBytes {
		return
	}
	for len(catalog.Repositories) != 0 {
		last := len(catalog.Repositories) - 1
		repository := &catalog.Repositories[last]
		switch {
		case len(repository.Linked) != 0:
			repository.Linked = repository.Linked[:len(repository.Linked)-1]
		case repository.Primary != nil:
			repository.Primary = nil
		default:
			catalog.Repositories = catalog.Repositories[:last]
		}
		if repository.Primary == nil && len(repository.Linked) == 0 && last < len(catalog.Repositories) {
			catalog.Repositories = catalog.Repositories[:last]
		}
		encoded, err = json.Marshal(catalog)
		if err == nil && len(encoded) <= maxCatalogBytes {
			return
		}
	}
}

func appendWarning(warnings *[]Warning, path, message string) {
	if len(*warnings) >= maxWarnings {
		return
	}
	*warnings = append(*warnings, Warning{Path: warningValue(path), Message: warningValue(message)})
}

func appendRequiredWarning(warnings *[]Warning, path, message string) {
	warning := Warning{Path: warningValue(path), Message: warningValue(message)}
	if len(*warnings) < maxWarnings {
		*warnings = append(*warnings, warning)
		return
	}
	(*warnings)[len(*warnings)-1] = warning
}

func warningValue(value string) string {
	if utf8.ValidString(value) {
		return value
	}
	quoted := strconv.QuoteToASCII(value)
	return strings.TrimSuffix(strings.TrimPrefix(quoted, `"`), `"`)
}

func firstPath(repository Repository) string {
	if repository.Primary != nil {
		return repository.Primary.RelativePath
	}
	if len(repository.Linked) != 0 {
		return repository.Linked[0].RelativePath
	}
	return repository.CommonDirectory
}

func exitCode(err error) int {
	return process.ExitCode(err)
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
