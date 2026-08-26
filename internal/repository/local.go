package repository

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// LocalCheckout is a live observation of the Git checkout containing the
// caller's current directory. It is deliberately not a Local Link: no
// relationship with a Box is persisted by observing it.
type LocalCheckout struct {
	TopLevel  string
	Origin    string
	OriginKey string
	// CloneSource is credential-sanitized but preserves an SSH username when
	// it is required for a usable clone URL.
	CloneSource string
	Branch      string
	Detached    bool
	HEAD        string
	Upstream    string
	Ahead       int
	Behind      int
	Status      Status
}

// InspectLocal returns nil when directory is not inside a Git worktree.
func InspectLocal(ctx context.Context, directory string) (*LocalCheckout, error) {
	return inspectLocal(ctx, directory, commandRunner{})
}

func inspectLocal(ctx context.Context, directory string, commands runner) (*LocalCheckout, error) {
	if directory == "" || !filepath.IsAbs(directory) {
		return nil, &Error{Code: CodeInvalidInput, Message: "local working directory must be absolute"}
	}
	directory, err := filepath.EvalSymlinks(filepath.Clean(directory))
	if err != nil {
		return nil, fmt.Errorf("resolve local working directory: %w", err)
	}
	marked, err := hasGitMarker(directory)
	if err != nil {
		return nil, fmt.Errorf("inspect local Git metadata: %w", err)
	}
	if !marked {
		return nil, nil
	}
	topOutput, err := git(ctx, commands, directory, "rev-parse", "--path-format=absolute", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("inspect local Git worktree: %w", err)
	}
	top := strings.TrimSuffix(string(topOutput), "\n")
	if top == "" || hasControl(top) {
		return nil, fmt.Errorf("Git returned malformed local worktree path")
	}
	top, err = filepath.EvalSymlinks(filepath.Clean(top))
	if err != nil {
		return nil, fmt.Errorf("resolve local Git worktree: %w", err)
	}
	if !filepath.IsAbs(top) {
		return nil, fmt.Errorf("Git returned a non-absolute local worktree path")
	}

	checkout := &LocalCheckout{TopLevel: top}
	if branch, branchErr := git(ctx, commands, top, "symbolic-ref", "--quiet", "--short", "HEAD"); branchErr == nil {
		checkout.Branch = strings.TrimSuffix(string(branch), "\n")
		if checkout.Branch == "" || hasControl(checkout.Branch) {
			return nil, fmt.Errorf("Git returned malformed local branch")
		}
	} else if exitCode(branchErr) == 1 {
		checkout.Detached = true
	} else {
		return nil, fmt.Errorf("read local branch: %w", branchErr)
	}
	if head, headErr := git(ctx, commands, top, "rev-parse", "--verify", "HEAD"); headErr == nil {
		checkout.HEAD = strings.TrimSpace(string(head))
	} else if exitCode(headErr) != 128 {
		return nil, fmt.Errorf("read local HEAD: %w", headErr)
	}
	status, err := git(ctx, commands, top, "status", "--porcelain=v2", "-z", "--untracked-files=all")
	if err != nil {
		return nil, fmt.Errorf("read local status: %w", err)
	}
	checkout.Status, err = parseStatus(status)
	if err != nil {
		return nil, err
	}
	if rawOrigin, originErr := git(ctx, commands, top, "remote", "get-url", "origin"); originErr == nil {
		raw := strings.TrimSpace(string(rawOrigin))
		checkout.Origin = sanitizeOrigin(raw)
		checkout.OriginKey = OriginKey(raw)
		checkout.CloneSource = sanitizeCloneSource(raw)
	} else if exitCode(originErr) != 2 && exitCode(originErr) != 128 {
		return nil, fmt.Errorf("read local origin: %w", originErr)
	}
	if checkout.Detached {
		return checkout, nil
	}
	upstream, upstreamErr := git(ctx, commands, top, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}")
	if upstreamErr != nil {
		if exitCode(upstreamErr) == 128 {
			return checkout, nil
		}
		return nil, fmt.Errorf("read local upstream: %w", upstreamErr)
	}
	checkout.Upstream = strings.TrimSpace(string(upstream))
	if checkout.Upstream == "" || hasControl(checkout.Upstream) {
		return nil, fmt.Errorf("Git returned malformed local upstream")
	}
	counts, countErr := git(ctx, commands, top, "rev-list", "--left-right", "--count", "HEAD...@{upstream}")
	if countErr != nil {
		return nil, fmt.Errorf("compare local checkout with upstream: %w", countErr)
	}
	fields := strings.Fields(string(counts))
	if len(fields) != 2 {
		return nil, fmt.Errorf("Git returned malformed ahead/behind counts")
	}
	checkout.Ahead, err = strconv.Atoi(fields[0])
	if err != nil || checkout.Ahead < 0 {
		return nil, fmt.Errorf("Git returned malformed ahead count")
	}
	checkout.Behind, err = strconv.Atoi(fields[1])
	if err != nil || checkout.Behind < 0 {
		return nil, fmt.Errorf("Git returned malformed behind count")
	}
	return checkout, nil
}

func hasGitMarker(directory string) (bool, error) {
	for current := directory; ; current = filepath.Dir(current) {
		if _, err := os.Lstat(filepath.Join(current, ".git")); err == nil {
			return true, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return false, nil
		}
	}
}

func sanitizeCloneSource(raw string) string {
	if raw == "" || hasControl(raw) {
		return ""
	}
	if !strings.Contains(raw, "://") {
		if index := strings.IndexAny(raw, "?#"); index >= 0 {
			raw = raw[:index]
		}
		colon := strings.IndexByte(raw, ':')
		if colon <= 0 || strings.ContainsRune(raw[:colon], '/') {
			return ""
		}
		return boundedOrigin(raw)
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Opaque != "" {
		return ""
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "https" && scheme != "http" && scheme != "ssh" && scheme != "git" {
		return ""
	}
	if scheme == "ssh" && parsed.User != nil {
		parsed.User = url.User(parsed.User.Username())
	} else {
		parsed.User = nil
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return boundedOrigin(parsed.String())
}

// OriginKey returns a credential-free identity suitable for matching the same
// network repository reached through common HTTPS, SSH, Git, or SCP syntax.
// Local filesystem origins intentionally have no cross-machine identity.
func OriginKey(origin string) string {
	if origin == "" || len(origin) > maxOriginBytes || hasControl(origin) {
		return ""
	}
	if !strings.Contains(origin, "://") {
		if index := strings.IndexAny(origin, "?#"); index >= 0 {
			origin = origin[:index]
		}
		colon := strings.IndexByte(origin, ':')
		if colon <= 0 || strings.ContainsRune(origin[:colon], '/') {
			return ""
		}
		authority := origin[:colon]
		username := ""
		host := authority
		if at := strings.LastIndexByte(authority, '@'); at >= 0 {
			username = originIdentityUsername(authority[:at])
			host = authority[at+1:]
			if username == "" && authority[:at] != "git" {
				return ""
			}
		}
		host = strings.ToLower(host)
		path := cleanOriginPath(origin[colon+1:])
		if host == "" || path == "" {
			return ""
		}
		return originIdentityAuthority(username, host) + "/" + path
	}
	parsed, err := url.Parse(origin)
	if err == nil && parsed.Scheme != "" && parsed.Opaque == "" {
		scheme := strings.ToLower(parsed.Scheme)
		if scheme != "https" && scheme != "http" && scheme != "ssh" && scheme != "git" {
			return ""
		}
		host := strings.ToLower(parsed.Hostname())
		path := cleanOriginPath(parsed.Path)
		if host == "" || path == "" {
			return ""
		}
		port := parsed.Port()
		if port != "" && !defaultOriginPort(scheme, port) {
			host = net.JoinHostPort(host, port)
		}
		username := ""
		if scheme == "ssh" && parsed.User != nil {
			username = originIdentityUsername(parsed.User.Username())
			if username == "" && parsed.User.Username() != "git" {
				return ""
			}
		}
		return originIdentityAuthority(username, host) + "/" + path
	}
	return ""
}

func cleanOriginPath(value string) string {
	value = strings.Trim(value, "/")
	value = strings.TrimSuffix(value, ".git")
	if value == "" || value == "." || value == ".." || hasControl(value) {
		return ""
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." {
			return ""
		}
	}
	return value
}

func originIdentityUsername(username string) string {
	// "git" is the conventional transport account used by hosted forges, so
	// omitting it keeps their HTTPS and SSH clone forms equivalent. Other SSH
	// usernames can select different repositories and remain part of identity.
	if username == "" || username == "git" || len(username) > 128 || hasControl(username) || strings.ContainsAny(username, "/@:") {
		return ""
	}
	return username
}

func originIdentityAuthority(username, host string) string {
	if username == "" {
		return host
	}
	return username + "@" + host
}

func defaultOriginPort(scheme, port string) bool {
	switch scheme {
	case "https":
		return port == "443"
	case "http":
		return port == "80"
	case "ssh":
		return port == "22"
	case "git":
		return port == "9418"
	default:
		return false
	}
}
