package repository

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/thewelshrich/schooner/internal/process"
)

const CheckoutSchemaVersion = "1"

type CheckoutFile struct {
	Path       string `json:"path"`
	Kind       string `json:"kind"`
	Executable bool   `json:"executable,omitempty"`
	Size       int64  `json:"size"`
	SHA256     string `json:"sha256"`
	Tracked    bool   `json:"tracked"`
}

// CheckoutIndexEntry is the portable, stage-zero part of Git's index needed
// to reproduce staged state without copying .git/index or creating tree
// objects while observing a checkout.
type CheckoutIndexEntry struct {
	Path   string `json:"path"`
	Mode   string `json:"mode"`
	Object string `json:"object"`
}

type CheckoutState struct {
	SchemaVersion      string               `json:"schema_version"`
	Worktree           string               `json:"worktree"`
	HEAD               string               `json:"head"`
	Branch             string               `json:"branch,omitempty"`
	Detached           bool                 `json:"detached"`
	IndexEntries       []CheckoutIndexEntry `json:"index_entries"`
	IndexCount         int                  `json:"index_count"`
	AbsentPaths        []string             `json:"absent_paths"`
	AbsentCount        int                  `json:"absent_count"`
	RepositoryIdentity string               `json:"repository_identity,omitempty"`
	CloneSource        string               `json:"clone_source,omitempty"`
	Status             Status               `json:"status"`
	Files              []CheckoutFile       `json:"files"`
	FileCount          int                  `json:"file_count"`
	Digest             string               `json:"digest"`
	Bytes              int64                `json:"bytes"`
}

type CheckoutCapture struct {
	State         CheckoutState
	PayloadPath   string
	PayloadSize   int64
	PayloadSHA256 string
}

func (capture CheckoutCapture) Release() { _ = os.Remove(capture.PayloadPath) }

type checkoutPayloadMetadata struct {
	SchemaVersion string        `json:"schema_version"`
	State         CheckoutState `json:"state"`
}

func ObserveCheckout(ctx context.Context, worktree string) (CheckoutState, error) {
	if worktree == "" || !filepath.IsAbs(worktree) {
		return CheckoutState{}, &Error{Code: CodeInvalidInput, Message: "workspace transfer requires an absolute Git Worktree root"}
	}
	// Reject promisor configuration before ordinary checkout inspection. Even
	// read-looking Git commands such as status may otherwise lazily fetch a
	// deliberately missing object before the repository can be rejected.
	if err := rejectPartialCloneConfiguration(ctx, worktree); err != nil {
		return CheckoutState{}, err
	}
	local, err := InspectLocal(ctx, worktree)
	if err != nil {
		return CheckoutState{}, err
	}
	if local == nil || local.TopLevel != worktree {
		return CheckoutState{}, &Error{Code: CodeInvalidInput, Message: "workspace transfer requires an exact canonical Git Worktree root"}
	}
	if local.HEAD == "" {
		return CheckoutState{}, &Error{Code: CodeUnsupported, Message: "workspace transfer does not support an unborn Repository"}
	}
	// Transfer state has a file-level manifest, so its status counts untracked
	// files individually even though ordinary contextual checkout observation
	// intentionally retains Git's compact directory count.
	statusOutput, err := git(ctx, commandRunner{}, worktree, "status", "--porcelain=v2", "-z", "--untracked-files=all")
	if err != nil {
		return CheckoutState{}, fmt.Errorf("read workspace transfer status: %w", err)
	}
	local.Status, err = parseStatus(statusOutput)
	if err != nil {
		return CheckoutState{}, err
	}
	if err = validateCheckoutSupport(ctx, local); err != nil {
		return CheckoutState{}, err
	}
	if !validGitObjectID(local.HEAD) {
		return CheckoutState{}, &Error{Code: CodeUnsupported, Message: "workspace transfer currently supports SHA-1 Git repositories only"}
	}
	indexEntries, err := checkoutIndexEntries(ctx, worktree)
	if err != nil {
		return CheckoutState{}, err
	}
	files, total, err := checkoutFiles(ctx, worktree)
	if err != nil {
		return CheckoutState{}, err
	}
	if err = validateCheckoutAttributes(ctx, worktree, files); err != nil {
		return CheckoutState{}, err
	}
	present := make(map[string]struct{}, len(files))
	for _, entry := range files {
		present[entry.Path] = struct{}{}
	}
	absent := make([]string, 0)
	for _, entry := range indexEntries {
		if _, exists := present[entry.Path]; !exists {
			absent = append(absent, entry.Path)
		}
	}
	state := CheckoutState{
		SchemaVersion: CheckoutSchemaVersion, Worktree: worktree, HEAD: local.HEAD,
		Branch: local.Branch, Detached: local.Detached, IndexEntries: indexEntries, IndexCount: len(indexEntries),
		AbsentPaths: absent, AbsentCount: len(absent),
		RepositoryIdentity: local.OriginKey, CloneSource: local.CloneSource, Status: local.Status, Files: files, FileCount: len(files), Bytes: total,
	}
	state.Digest, err = checkoutDigest(state)
	return state, err
}

func CaptureCheckout(ctx context.Context, worktree, stagingDirectory string) (CheckoutCapture, error) {
	if stagingDirectory == "" || !filepath.IsAbs(stagingDirectory) {
		return CheckoutCapture{}, &Error{Code: CodeInvalidInput, Message: "workspace staging directory must be absolute"}
	}
	if err := os.MkdirAll(stagingDirectory, 0o700); err != nil {
		return CheckoutCapture{}, fmt.Errorf("create workspace staging directory: %w", err)
	}
	state, err := ObserveCheckout(ctx, worktree)
	if err != nil {
		return CheckoutCapture{}, err
	}
	pack, err := os.CreateTemp(stagingDirectory, ".objects-*.pack")
	if err != nil {
		return CheckoutCapture{}, fmt.Errorf("create Git pack staging file: %w", err)
	}
	packPath := pack.Name()
	defer os.Remove(packPath)
	var revisions strings.Builder
	revisions.WriteString(state.HEAD)
	revisions.WriteByte('\n')
	for _, entry := range state.IndexEntries {
		revisions.WriteString(entry.Object)
		revisions.WriteByte('\n')
	}
	result, runErr := process.RunStreamingWithoutEnvironment(ctx, worktree, gitRepositoryEnvironment, gitSafeEnvironment, strings.NewReader(revisions.String()), pack, "git", "--no-optional-locks", "--no-replace-objects", "-c", "core.fsmonitor=false", "pack-objects", "--stdout", "--revs")
	closeErr := pack.Close()
	if runErr != nil {
		return CheckoutCapture{}, gitTransferError("create Git object pack", result, runErr)
	}
	if closeErr != nil {
		return CheckoutCapture{}, fmt.Errorf("close Git pack staging file: %w", closeErr)
	}
	payload, err := os.CreateTemp(stagingDirectory, ".push-*.tar")
	if err != nil {
		return CheckoutCapture{}, fmt.Errorf("create workspace payload: %w", err)
	}
	payloadPath := payload.Name()
	failed := true
	defer func() {
		_ = payload.Close()
		if failed {
			_ = os.Remove(payloadPath)
		}
	}()
	hash := sha256.New()
	tw := tar.NewWriter(io.MultiWriter(payload, hash))
	checkoutRoot, err := os.OpenRoot(worktree)
	if err != nil {
		return CheckoutCapture{}, fmt.Errorf("open workspace root: %w", err)
	}
	defer checkoutRoot.Close()
	metadata, err := json.Marshal(checkoutPayloadMetadata{SchemaVersion: CheckoutSchemaVersion, State: state})
	if err == nil {
		err = writeTarBytes(tw, "metadata.json", 0o600, metadata)
	}
	if err == nil {
		err = writeTarFile(tw, "objects.pack", packPath, 0o600)
	}
	for _, entry := range state.Files {
		if err != nil {
			break
		}
		err = writeCheckoutTarEntry(tw, checkoutRoot, entry)
	}
	if closeTarErr := tw.Close(); err == nil {
		err = closeTarErr
	}
	if syncErr := payload.Sync(); err == nil {
		err = syncErr
	}
	if closePayloadErr := payload.Close(); err == nil {
		err = closePayloadErr
	}
	if err != nil {
		return CheckoutCapture{}, fmt.Errorf("write workspace payload: %w", err)
	}
	info, err := os.Stat(payloadPath)
	if err != nil {
		return CheckoutCapture{}, err
	}
	latest, err := ObserveCheckout(ctx, worktree)
	if err != nil {
		return CheckoutCapture{}, err
	}
	if latest.Digest != state.Digest {
		return CheckoutCapture{}, &Error{Code: CodeConflict, Message: "the source Worktree changed while its workspace was being captured"}
	}
	failed = false
	return CheckoutCapture{State: state, PayloadPath: payloadPath, PayloadSize: info.Size(), PayloadSHA256: hex.EncodeToString(hash.Sum(nil))}, nil
}

func CommitIsAncestor(ctx context.Context, worktree, ancestor, descendant string) (bool, error) {
	if ancestor == "" || descendant == "" {
		return false, nil
	}
	if _, err := git(ctx, commandRunner{}, worktree, "cat-file", "-e", ancestor+"^{commit}"); err != nil {
		return false, nil
	}
	_, err := git(ctx, commandRunner{}, worktree, "merge-base", "--is-ancestor", ancestor, descendant)
	if err == nil {
		return true, nil
	}
	if exitCode(err) == 1 {
		return false, nil
	}
	return false, err
}

func checkoutFiles(ctx context.Context, worktree string) ([]CheckoutFile, int64, error) {
	trackedRaw, err := git(ctx, commandRunner{}, worktree, "ls-files", "-z", "--cached")
	if err != nil {
		return nil, 0, fmt.Errorf("list tracked workspace files: %w", err)
	}
	untrackedRaw, err := git(ctx, commandRunner{}, worktree, "ls-files", "-z", "--others", "--exclude-standard")
	if err != nil {
		return nil, 0, fmt.Errorf("list untracked workspace files: %w", err)
	}
	tracked := parseNULPaths(trackedRaw)
	untracked := parseNULPaths(untrackedRaw)
	values := make(map[string]bool, len(tracked)+len(untracked))
	for _, path := range tracked {
		values[path] = true
	}
	for _, path := range untracked {
		if _, exists := values[path]; !exists {
			values[path] = false
		}
	}
	paths := make([]string, 0, len(values))
	for path := range values {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	if err = rejectCaseCollisions(paths); err != nil {
		return nil, 0, err
	}
	if err = rejectCheckoutPathPrefixes(paths); err != nil {
		return nil, 0, err
	}
	root, err := os.OpenRoot(worktree)
	if err != nil {
		return nil, 0, fmt.Errorf("open workspace root: %w", err)
	}
	defer root.Close()
	files := make([]CheckoutFile, 0, len(paths))
	var total int64
	for _, relative := range paths {
		if err = validateCheckoutPath(relative); err != nil {
			return nil, 0, err
		}
		info, statErr := root.Lstat(filepath.FromSlash(relative))
		if errors.Is(statErr, os.ErrNotExist) && values[relative] {
			continue
		}
		if statErr != nil {
			return nil, 0, &Error{Code: CodeUnsupported, Message: fmt.Sprintf("workspace path %q cannot be inspected without leaving the Worktree", relative), Cause: statErr}
		}
		if err = validateCheckoutSourceParents(root, relative); err != nil {
			return nil, 0, err
		}
		entry, err := checkoutFileAtRoot(root, relative, values[relative], info)
		if err != nil {
			return nil, 0, err
		}
		total += entry.Size
		files = append(files, entry)
	}
	return files, total, nil
}

func rejectCheckoutPathPrefixes(paths []string) error {
	known := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		known[path] = struct{}{}
	}
	for _, path := range paths {
		for parent := filepath.ToSlash(filepath.Dir(filepath.FromSlash(path))); parent != "."; parent = filepath.ToSlash(filepath.Dir(filepath.FromSlash(parent))) {
			if _, collision := known[parent]; collision {
				return &Error{Code: CodeUnsupported, Message: fmt.Sprintf("workspace paths %q and %q cannot both be transferred safely", parent, path)}
			}
		}
	}
	return nil
}

func validateCheckoutSourceParents(root *os.Root, relative string) error {
	parent := filepath.Dir(filepath.FromSlash(relative))
	if parent == "." {
		return nil
	}
	current := ""
	for _, component := range strings.Split(parent, string(filepath.Separator)) {
		if current == "" {
			current = component
		} else {
			current = filepath.Join(current, component)
		}
		info, err := root.Lstat(current)
		if err != nil {
			return &Error{Code: CodeUnsupported, Message: fmt.Sprintf("workspace path %q has an unreadable parent", relative), Cause: err}
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return &Error{Code: CodeUnsupported, Message: fmt.Sprintf("workspace path %q has a symlink or non-directory parent", relative)}
		}
	}
	return nil
}

func checkoutIndexEntries(ctx context.Context, worktree string) ([]CheckoutIndexEntry, error) {
	raw, err := git(ctx, commandRunner{}, worktree, "ls-files", "--stage", "-z")
	if err != nil {
		return nil, fmt.Errorf("read workspace index: %w", err)
	}
	entries := make([]CheckoutIndexEntry, 0)
	paths := make([]string, 0)
	seen := make(map[string]struct{})
	for _, record := range bytes.Split(raw, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		header, pathValue, found := bytes.Cut(record, []byte{'\t'})
		fields := bytes.Fields(header)
		path := string(pathValue)
		if !found || len(fields) != 3 || string(fields[2]) != "0" || !validIndexMode(string(fields[0])) || !validGitObjectID(string(fields[1])) {
			return nil, &Error{Code: CodeUnsupported, Message: "the Git index cannot be represented safely"}
		}
		if err = validateCheckoutPath(path); err != nil {
			return nil, err
		}
		if _, duplicate := seen[path]; duplicate {
			return nil, &Error{Code: CodeUnsupported, Message: "the Git index contains duplicate paths"}
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
		entries = append(entries, CheckoutIndexEntry{Path: path, Mode: string(fields[0]), Object: string(fields[1])})
	}
	if err = rejectCaseCollisions(paths); err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

func validIndexMode(value string) bool {
	return value == "100644" || value == "100755" || value == "120000"
}

func validGitObjectID(value string) bool {
	if len(value) != 40 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validateCheckoutSupport(ctx context.Context, local *LocalCheckout) error {
	if local.Status.Conflicted != 0 {
		return &Error{Code: CodeUnsupported, Message: "workspace transfer does not support unresolved merge conflicts"}
	}
	replacements, replacementsErr := git(ctx, commandRunner{}, local.TopLevel, "for-each-ref", "--format=%(refname)", "refs/replace/")
	if replacementsErr != nil {
		return replacementsErr
	}
	if strings.TrimSpace(string(replacements)) != "" {
		return &Error{Code: CodeUnsupported, Message: "workspace transfer does not support Git replacement refs"}
	}
	commonOutput, commonErr := git(ctx, commandRunner{}, local.TopLevel, "rev-parse", "--git-common-dir")
	if commonErr != nil {
		return commonErr
	}
	commonDirectory := strings.TrimSpace(string(commonOutput))
	if !filepath.IsAbs(commonDirectory) {
		commonDirectory = filepath.Join(local.TopLevel, commonDirectory)
	}
	commonDirectory, commonErr = filepath.EvalSymlinks(filepath.Clean(commonDirectory))
	if commonErr != nil {
		return &Error{Code: CodeUnsupported, Message: "workspace transfer could not safely inspect the common Git directory", Cause: commonErr}
	}
	if _, statErr := os.Lstat(filepath.Join(commonDirectory, "info", "grafts")); statErr == nil {
		return &Error{Code: CodeUnsupported, Message: "workspace transfer does not support Git grafts"}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return &Error{Code: CodeUnsupported, Message: "workspace transfer could not safely inspect Git grafts", Cause: statErr}
	}
	for _, name := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "REBASE_HEAD"} {
		value, err := git(ctx, commandRunner{}, local.TopLevel, "rev-parse", "--git-path", name)
		if err != nil {
			return err
		}
		gitPath := strings.TrimSpace(string(value))
		if !filepath.IsAbs(gitPath) {
			gitPath = filepath.Join(local.TopLevel, gitPath)
		}
		if _, statErr := os.Lstat(gitPath); statErr == nil {
			return &Error{Code: CodeUnsupported, Message: "workspace transfer does not support an in-progress Git merge, rebase, cherry-pick, or revert"}
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
	}
	for _, name := range []string{"rebase-merge", "rebase-apply"} {
		value, err := git(ctx, commandRunner{}, local.TopLevel, "rev-parse", "--git-path", name)
		if err == nil {
			gitPath := strings.TrimSpace(string(value))
			if !filepath.IsAbs(gitPath) {
				gitPath = filepath.Join(local.TopLevel, gitPath)
			}
			if _, statErr := os.Lstat(gitPath); statErr == nil {
				return &Error{Code: CodeUnsupported, Message: "workspace transfer does not support an in-progress Git rebase"}
			}
		}
	}
	if value, _ := git(ctx, commandRunner{}, local.TopLevel, "rev-parse", "--is-shallow-repository"); strings.TrimSpace(string(value)) == "true" {
		return &Error{Code: CodeUnsupported, Message: "workspace transfer does not support shallow repositories"}
	}
	if err := rejectPartialCloneConfiguration(ctx, local.TopLevel); err != nil {
		return err
	}
	if value, _ := git(ctx, commandRunner{}, local.TopLevel, "config", "--bool", "core.sparseCheckout"); strings.TrimSpace(string(value)) == "true" {
		return &Error{Code: CodeUnsupported, Message: "workspace transfer does not support sparse checkout"}
	}
	for _, setting := range []struct{ key, unsupported string }{
		{"core.filemode", "false"}, {"core.symlinks", "false"},
	} {
		value, configErr := git(ctx, commandRunner{}, local.TopLevel, "config", "--bool", "--get", setting.key)
		if configErr != nil && exitCode(configErr) != 1 {
			return configErr
		}
		if strings.TrimSpace(string(value)) == setting.unsupported {
			return &Error{Code: CodeUnsupported, Message: fmt.Sprintf("workspace transfer does not support %s=%s", setting.key, setting.unsupported)}
		}
	}
	autocrlf, configErr := git(ctx, commandRunner{}, local.TopLevel, "config", "--get", "core.autocrlf")
	if configErr != nil && exitCode(configErr) != 1 {
		return configErr
	}
	if value := strings.ToLower(strings.TrimSpace(string(autocrlf))); value != "" && value != "false" {
		return &Error{Code: CodeUnsupported, Message: fmt.Sprintf("workspace transfer does not support core.autocrlf=%s", value)}
	}
	stages, err := git(ctx, commandRunner{}, local.TopLevel, "ls-files", "--stage", "-z")
	if err != nil {
		return err
	}
	for _, record := range bytes.Split(stages, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		fields := bytes.Fields(record)
		if len(fields) < 4 {
			return fmt.Errorf("Git returned malformed index entries")
		}
		if string(fields[0]) == "160000" {
			return &Error{Code: CodeUnsupported, Message: "workspace transfer does not support submodules"}
		}
		if string(fields[2]) != "0" {
			return &Error{Code: CodeUnsupported, Message: "workspace transfer does not support unmerged index entries"}
		}
	}
	flags, err := git(ctx, commandRunner{}, local.TopLevel, "ls-files", "-v", "-z")
	if err != nil {
		return err
	}
	for _, record := range bytes.Split(flags, []byte{0}) {
		if len(record) > 0 && record[0] != 'H' {
			return &Error{Code: CodeUnsupported, Message: "workspace transfer does not support skip-worktree or assume-unchanged index entries"}
		}
	}
	status, err := git(ctx, commandRunner{}, local.TopLevel, "status", "--porcelain=v2", "-z", "--untracked-files=normal")
	if err != nil {
		return err
	}
	for _, record := range bytes.Split(status, []byte{0}) {
		if len(record) > 4 && record[0] == '1' && record[2] == '.' && record[3] == 'A' {
			return &Error{Code: CodeUnsupported, Message: "workspace transfer does not support intent-to-add entries"}
		}
	}
	attributes, err := git(ctx, commandRunner{}, local.TopLevel, "check-attr", "-z", "--all", "--", ".")
	if err == nil && (bytes.Contains(attributes, []byte("filter\x00")) || bytes.Contains(attributes, []byte("working-tree-encoding\x00"))) {
		return &Error{Code: CodeUnsupported, Message: "workspace transfer does not support clean/smudge filters or working-tree encodings"}
	}
	return nil
}

func rejectPartialCloneConfiguration(ctx context.Context, worktree string) error {
	for _, arguments := range [][]string{
		{"config", "--local", "--get", "extensions.partialClone"},
		{"config", "--local", "--get-regexp", `^remote\..*\.promisor$`},
		{"config", "--local", "--get-regexp", `^remote\..*\.partialclonefilter$`},
	} {
		value, err := git(ctx, commandRunner{}, worktree, arguments...)
		if err == nil && strings.TrimSpace(string(value)) != "" {
			return &Error{Code: CodeUnsupported, Message: "workspace transfer does not support partial-clone or promisor repositories"}
		}
		// Exit 1 is a missing config key. Exit 128 also covers a directory
		// that is not a Repository; normal checkout inspection owns that
		// diagnostic after this transfer-specific early guard.
		if err != nil && exitCode(err) != 1 && exitCode(err) != 128 {
			return err
		}
	}
	return nil
}

func checkoutDigest(state CheckoutState) (string, error) {
	state.Digest = ""
	state.Worktree = ""
	state.CloneSource = ""
	state.RepositoryIdentity = ""
	encoded, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// CheckoutSummary preserves comparison and safety metadata while removing the
// potentially unbounded file manifest from bounded control responses.
func CheckoutSummary(state CheckoutState) CheckoutState {
	state.Files = nil
	state.IndexEntries = nil
	state.AbsentPaths = nil
	return state
}

func checkoutFileAt(absolute, relative string, tracked bool, info os.FileInfo) (CheckoutFile, error) {
	entry := CheckoutFile{Path: relative, Tracked: tracked}
	hash := sha256.New()
	switch {
	case info.Mode().IsRegular():
		entry.Kind = "file"
		entry.Executable = info.Mode().Perm()&0o111 != 0
		file, err := os.Open(absolute)
		if err != nil {
			return CheckoutFile{}, fmt.Errorf("read workspace path %q: %w", relative, err)
		}
		written, copyErr := io.Copy(hash, file)
		closeErr := file.Close()
		if copyErr != nil {
			return CheckoutFile{}, fmt.Errorf("read workspace path %q: %w", relative, copyErr)
		}
		if closeErr != nil {
			return CheckoutFile{}, fmt.Errorf("close workspace path %q: %w", relative, closeErr)
		}
		entry.Size = written
	case info.Mode()&os.ModeSymlink != 0:
		entry.Kind = "symlink"
		target, err := os.Readlink(absolute)
		if err != nil {
			return CheckoutFile{}, fmt.Errorf("read workspace path %q: %w", relative, err)
		}
		entry.Size = int64(len(target))
		_, _ = hash.Write([]byte(target))
	default:
		return CheckoutFile{}, &Error{Code: CodeUnsupported, Message: fmt.Sprintf("workspace path %q is not a regular file or symlink", relative)}
	}
	entry.SHA256 = hex.EncodeToString(hash.Sum(nil))
	return entry, nil
}

func checkoutFileAtRoot(root *os.Root, relative string, tracked bool, info os.FileInfo) (CheckoutFile, error) {
	entry := CheckoutFile{Path: relative, Tracked: tracked}
	hash := sha256.New()
	path := filepath.FromSlash(relative)
	switch {
	case info.Mode().IsRegular():
		entry.Kind = "file"
		entry.Executable = info.Mode().Perm()&0o111 != 0
		file, err := root.Open(path)
		if err != nil {
			return CheckoutFile{}, fmt.Errorf("read workspace path %q: %w", relative, err)
		}
		opened, statErr := file.Stat()
		if statErr != nil || !opened.Mode().IsRegular() {
			_ = file.Close()
			return CheckoutFile{}, &Error{Code: CodeConflict, Message: fmt.Sprintf("source path %q changed while it was being observed", relative), Cause: statErr}
		}
		written, copyErr := io.Copy(hash, file)
		closeErr := file.Close()
		if copyErr != nil {
			return CheckoutFile{}, fmt.Errorf("read workspace path %q: %w", relative, copyErr)
		}
		if closeErr != nil {
			return CheckoutFile{}, fmt.Errorf("close workspace path %q: %w", relative, closeErr)
		}
		entry.Size = written
	case info.Mode()&os.ModeSymlink != 0:
		entry.Kind = "symlink"
		target, err := root.Readlink(path)
		if err != nil {
			return CheckoutFile{}, fmt.Errorf("read workspace path %q: %w", relative, err)
		}
		entry.Size = int64(len(target))
		_, _ = hash.Write([]byte(target))
	default:
		return CheckoutFile{}, &Error{Code: CodeUnsupported, Message: fmt.Sprintf("workspace path %q is not a regular file or symlink", relative)}
	}
	entry.SHA256 = hex.EncodeToString(hash.Sum(nil))
	return entry, nil
}

func validateCheckoutAttributes(ctx context.Context, worktree string, files []CheckoutFile) error {
	for start := 0; start < len(files); start += 128 {
		end := min(start+128, len(files))
		arguments := []string{"check-attr", "-z", "filter", "working-tree-encoding", "text", "eol", "ident", "--"}
		for _, entry := range files[start:end] {
			arguments = append(arguments, entry.Path)
		}
		output, err := git(ctx, commandRunner{}, worktree, arguments...)
		if err != nil {
			return err
		}
		fields := bytes.Split(output, []byte{0})
		for index := 0; index+2 < len(fields); index += 3 {
			attribute, value := string(fields[index+1]), string(fields[index+2])
			if value != "unspecified" && value != "unset" && value != "" {
				return &Error{Code: CodeUnsupported, Message: fmt.Sprintf("workspace path %q uses unsupported Git attribute %s=%s", fields[index], attribute, value)}
			}
		}
	}
	return nil
}

func writeTarBytes(writer *tar.Writer, name string, mode int64, contents []byte) error {
	header := &tar.Header{Name: name, Mode: mode, Size: int64(len(contents)), ModTime: time.Unix(0, 0), Typeflag: tar.TypeReg, Format: tar.FormatPAX}
	if err := writer.WriteHeader(header); err != nil {
		return err
	}
	_, err := writer.Write(contents)
	return err
}

func writeTarFile(writer *tar.Writer, name, source string, mode int64) error {
	file, err := os.Open(source)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if err = writer.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: info.Size(), ModTime: time.Unix(0, 0), Typeflag: tar.TypeReg, Format: tar.FormatPAX}); err != nil {
		return err
	}
	_, err = io.Copy(writer, file)
	return err
}

func writeCheckoutTarEntry(writer *tar.Writer, root *os.Root, entry CheckoutFile) error {
	name := "files/" + entry.Path
	path := filepath.FromSlash(entry.Path)
	if entry.Kind == "symlink" {
		target, err := root.Readlink(path)
		if err != nil {
			return err
		}
		digest := sha256.Sum256([]byte(target))
		if int64(len(target)) != entry.Size || hex.EncodeToString(digest[:]) != entry.SHA256 {
			return &Error{Code: CodeConflict, Message: fmt.Sprintf("source path %q changed while its workspace was being captured", entry.Path)}
		}
		return writer.WriteHeader(&tar.Header{Name: name, Linkname: target, Mode: 0o777, ModTime: time.Unix(0, 0), Typeflag: tar.TypeSymlink, Format: tar.FormatPAX})
	}
	mode := int64(0o644)
	if entry.Executable {
		mode = 0o755
	}
	file, err := root.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() {
		return &Error{Code: CodeConflict, Message: fmt.Sprintf("source path %q changed while its workspace was being captured", entry.Path), Cause: err}
	}
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() != entry.Size || !info.Mode().IsRegular() || (info.Mode().Perm()&0o111 != 0) != entry.Executable {
		return &Error{Code: CodeConflict, Message: fmt.Sprintf("source path %q changed while its workspace was being captured", entry.Path)}
	}
	if err = writer.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: entry.Size, ModTime: time.Unix(0, 0), Typeflag: tar.TypeReg, Format: tar.FormatPAX}); err != nil {
		return err
	}
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(writer, hash), file)
	if err != nil {
		return err
	}
	if written != entry.Size || hex.EncodeToString(hash.Sum(nil)) != entry.SHA256 {
		return &Error{Code: CodeConflict, Message: fmt.Sprintf("source path %q changed while its workspace was being captured", entry.Path)}
	}
	return nil
}

func parseNULPaths(value []byte) []string {
	parts := bytes.Split(value, []byte{0})
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part) != 0 {
			result = append(result, string(part))
		}
	}
	return result
}

func validateCheckoutPath(value string) error {
	if value == "" || !utf8.ValidString(value) || hasControl(value) || strings.ContainsRune(value, '\\') {
		return &Error{Code: CodeUnsupported, Message: "workspace contains an unsupported path"}
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(value)))
	if clean != value || strings.HasPrefix(value, "/") || value == "." || value == ".." || strings.HasPrefix(value, "../") || containsGitMetadataComponent(value) {
		return &Error{Code: CodeUnsupported, Message: fmt.Sprintf("workspace path %q is unsafe", value)}
	}
	return nil
}

func containsGitMetadataComponent(value string) bool {
	for _, component := range strings.Split(value, "/") {
		if strings.EqualFold(component, ".git") {
			return true
		}
	}
	return false
}

func rejectCaseCollisions(paths []string) error {
	seen := make(map[string]string, len(paths))
	for _, value := range paths {
		folded := strings.ToLower(value)
		if former, ok := seen[folded]; ok && former != value {
			return &Error{Code: CodeUnsupported, Message: fmt.Sprintf("workspace paths %q and %q collide on a case-insensitive filesystem", former, value)}
		}
		seen[folded] = value
	}
	return nil
}

func gitTransferError(action string, _ process.Result, err error) error {
	// Git diagnostics can echo credential-bearing origins or helper output.
	// Workspace transfer reports only the fixed operation label and process
	// failure; the checkout state and payload never need remote diagnostics.
	return fmt.Errorf("%s: %w", action, err)
}
