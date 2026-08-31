package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/thewelshrich/schooner/internal/box"
	"github.com/thewelshrich/schooner/internal/boxtarget"
	invsqlite "github.com/thewelshrich/schooner/internal/inventory/sqlite"
	"github.com/thewelshrich/schooner/internal/link"
	"github.com/thewelshrich/schooner/internal/repository"
	"github.com/thewelshrich/schooner/internal/source"
	"github.com/thewelshrich/schooner/internal/ui/prompts"
	uitheme "github.com/thewelshrich/schooner/internal/ui/theme"
	"github.com/thewelshrich/schooner/internal/workspacetransfer"
)

func newPushCommand(streams Streams, global *globalOptions, targets *boxtarget.Resolver) *cobra.Command {
	var explicitBox string
	var dryRun bool
	command := &cobra.Command{
		Use: "push [remote-worktree]", Short: "Put the current local workspace onto a Box", Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, target, linkCreated, err := runPush(cmd.Context(), streams, global, targets, explicitBox, firstArgument(args), dryRun)
			if err != nil {
				var usage usageError
				if errors.As(err, &usage) {
					return err
				}
				return executionError{cause: normalizePushError(err)}
			}
			return writePushResult(cmd.OutOrStdout(), global.output, result, target, linkCreated, dryRun, outputTheme(global, streams))
		},
	}
	command.Flags().StringVar(&explicitBox, "box", "", "box name (always uses OpenSSH)")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "inspect and show the push without changing the Box")
	return command
}

func runPush(ctx context.Context, streams Streams, global *globalOptions, targets *boxtarget.Resolver, explicitBox, remoteSelector string, dryRun bool) (workspacetransfer.PushResult, boxtarget.Target, bool, error) {
	local, err := inspectCurrentCheckout(ctx)
	if err != nil {
		return workspacetransfer.PushResult{}, boxtarget.Target{}, false, err
	}
	if local == nil {
		return workspacetransfer.PushResult{}, boxtarget.Target{}, false, usageError{cause: fmt.Errorf("schooner push must run inside a Git Worktree")}
	}
	statePath, err := invsqlite.DefaultPath()
	if err != nil {
		return workspacetransfer.PushResult{}, boxtarget.Target{}, false, err
	}
	var store *invsqlite.Store
	if dryRun {
		var exists bool
		store, exists, err = invsqlite.OpenReadOnly(ctx, statePath)
		if err == nil && !exists {
			store = nil
		}
	} else {
		store, err = invsqlite.Open(ctx, statePath)
	}
	if err != nil {
		return workspacetransfer.PushResult{}, boxtarget.Target{}, false, err
	}
	if store != nil {
		defer store.Close()
	}
	var localLink link.LocalLink
	linked := false
	if store != nil {
		localLink, err = link.Find(ctx, store, local.TopLevel, local.OriginKey)
		if err == nil {
			linked = true
		} else if link.ErrorCode(err) == link.CodeStale && explicitBox != "" && remoteSelector != "" {
			// An exact Box and Worktree is the deliberately small repair path for a
			// stale routing record. A successful push replaces it below.
			linked = false
		} else if link.ErrorCode(err) != link.CodeNotFound {
			return workspacetransfer.PushResult{}, boxtarget.Target{}, false, err
		}
	}
	linkedBoxID := ""
	if linked {
		linkedBoxID = localLink.BoxID
	}
	target, err := resolveBoxExecutionTargetPolicy(ctx, streams, global, targets, explicitBox, linkedBoxID, dryRun)
	if err != nil {
		if linked && explicitBox == "" && box.ErrorCode(err) == "not_found" {
			err = &link.Error{Code: link.CodeStale, Message: "the Local Link refers to a Box that is no longer configured"}
		}
		return workspacetransfer.PushResult{}, boxtarget.Target{}, false, err
	}
	useLink := linked && target.BoxID() == localLink.BoxID && remoteSelector == ""
	if useLink && target.BoxIdentity() != localLink.ExpectedBoxIdentity {
		return workspacetransfer.PushResult{}, target, false, &link.Error{Code: link.CodeStale, Message: "the Local Link's Box identity no longer matches the selected Box"}
	}
	staging := filepath.Join(filepath.Dir(statePath), "operations", "workspace")
	if !useLink && remoteSelector == "" && !dryRun && local.CloneSource != "" {
		validation, captureErr := repository.CaptureCheckout(ctx, local.TopLevel, staging)
		if captureErr != nil {
			return workspacetransfer.PushResult{}, target, false, captureErr
		}
		validation.Release()
	}
	remoteWorktree := ""
	var createdDestination *repository.CheckoutState
	if useLink {
		remoteWorktree = localLink.RemoteWorktree
		observed, inspectErr := target.ObservePushDestination(ctx, remoteWorktree)
		if inspectErr != nil {
			return workspacetransfer.PushResult{}, target, false, inspectErr
		}
		if observed == nil {
			return workspacetransfer.PushResult{}, target, false, &link.Error{Code: link.CodeStale, Message: "the linked remote Worktree no longer exists; run push with an explicit Box to establish a new destination"}
		}
		if localLink.RepositoryIdentity != "" && observed.RepositoryIdentity != localLink.RepositoryIdentity {
			return workspacetransfer.PushResult{}, target, false, &link.Error{Code: link.CodeStale, Message: "the linked remote Worktree no longer has the expected Repository identity"}
		}
	} else if remoteSelector != "" {
		remoteWorktree, err = resolveRemotePushPath(target.WorktreeRoot(), remoteSelector)
		if err != nil {
			return workspacetransfer.PushResult{}, target, false, err
		}
		if !dryRun && local.CloneSource != "" {
			var observed *repository.CheckoutState
			observed, err = target.ObservePushDestination(ctx, remoteWorktree)
			if err != nil {
				return workspacetransfer.PushResult{}, target, false, err
			}
			if observed == nil {
				validation, captureErr := repository.CaptureCheckout(ctx, local.TopLevel, staging)
				if captureErr != nil {
					return workspacetransfer.PushResult{}, target, false, captureErr
				}
				validation.Release()
				clone, cloneErr := cloneWithRecovery(ctx, streams, global, target, repository.CloneRequest{Source: local.CloneSource, Destination: remoteWorktree}, "Cloning Repository on "+targetBoxLabel(target), nil)
				if cloneErr != nil {
					return workspacetransfer.PushResult{}, target, false, cloneErr
				}
				if clone.Path != remoteWorktree {
					return workspacetransfer.PushResult{}, target, false, fmt.Errorf("clone created %q instead of the requested remote Worktree %q", clone.Path, remoteWorktree)
				}
				createdDestination, err = observeCreatedPushDestination(ctx, target, clone, remoteWorktree, local.OriginKey)
				if err != nil {
					return workspacetransfer.PushResult{}, target, false, err
				}
			}
		}
	} else {
		remoteWorktree, createdDestination, err = resolvePushWorktree(ctx, streams, global, target, local, dryRun)
		if err != nil {
			return workspacetransfer.PushResult{}, target, false, err
		}
	}
	result, err := workspacetransfer.Push(ctx, workspacetransfer.PushRequest{LocalWorktree: local.TopLevel, RemoteWorktree: remoteWorktree, Staging: staging, DryRun: dryRun, Remote: target, CreatedDestination: createdDestination})
	if err != nil {
		if createdDestination != nil {
			markPushCreatedRemote(err)
		}
		return result, target, false, err
	}
	linkCreated := false
	if !dryRun && target.BoxID() != "" {
		now := time.Now().UTC()
		created := now
		if useLink {
			created = localLink.CreatedAt
		}
		linkedIdentity := ""
		if result.Destination != nil && result.Destination.RepositoryIdentity == local.OriginKey {
			linkedIdentity = local.OriginKey
		}
		value := link.LocalLink{LocalWorktree: local.TopLevel, BoxID: target.BoxID(), ExpectedBoxIdentity: target.BoxIdentity(), RemoteWorktree: remoteWorktree, RepositoryIdentity: linkedIdentity, CreatedAt: created, UpdatedAt: now}
		if err = link.Save(ctx, store, value); err != nil {
			return result, target, false, fmt.Errorf("workspace was pushed but its Local Link could not be saved: %w", err)
		}
		linkCreated = !useLink
	}
	return result, target, linkCreated, nil
}

func currentLocalLink(ctx context.Context, local *repository.LocalCheckout) (link.LocalLink, bool, error) {
	if local == nil {
		return link.LocalLink{}, false, nil
	}
	statePath, err := invsqlite.DefaultPath()
	if err != nil {
		return link.LocalLink{}, false, err
	}
	store, err := invsqlite.Open(ctx, statePath)
	if err != nil {
		return link.LocalLink{}, false, err
	}
	defer store.Close()
	value, err := link.Find(ctx, store, local.TopLevel, local.OriginKey)
	if link.ErrorCode(err) == link.CodeNotFound {
		return link.LocalLink{}, false, nil
	}
	return value, err == nil, err
}

func observeCreatedPushDestination(ctx context.Context, target boxtarget.Target, clone repository.MutationResult, expectedPath, expectedIdentity string) (*repository.CheckoutState, error) {
	if clone.Inspection == nil || clone.Path != expectedPath || clone.Inspection.Worktree.Path != expectedPath {
		return nil, &repository.Error{Code: repository.CodeOutcomeUnknown, Message: "the newly cloned remote Worktree did not return an exact verified checkout"}
	}
	initial := clone.Inspection
	if pushStatusDirty(initial.Worktree.Status) {
		return nil, &workspacetransfer.Error{Code: workspacetransfer.CodeConflict, Message: "Push stopped: the newly cloned remote Worktree already contains checkout changes", Context: map[string]string{"remote_created": "true"}}
	}
	observed, err := target.ObservePushDestination(ctx, expectedPath)
	if err != nil {
		return nil, err
	}
	if observed == nil {
		return nil, &repository.Error{Code: repository.CodeOutcomeUnknown, Message: "the newly cloned remote Worktree disappeared before its workspace could be prepared"}
	}
	initialIdentity := repository.OriginKey(initial.Repository.Origin)
	if observed.Worktree != expectedPath || observed.HEAD != initial.Worktree.HEAD || observed.Branch != initial.Worktree.Branch || observed.Detached != initial.Worktree.Detached || pushStatusDirty(observed.Status) || observed.RepositoryIdentity != initialIdentity || (expectedIdentity != "" && observed.RepositoryIdentity != expectedIdentity) {
		return nil, &workspacetransfer.Error{Code: workspacetransfer.CodeConflict, Message: "Push stopped: the newly cloned remote Worktree changed before its workspace could be prepared", Context: map[string]string{"remote_created": "true"}}
	}
	return observed, nil
}

func pushStatusDirty(status repository.Status) bool {
	return status.Staged != 0 || status.Unstaged != 0 || status.Untracked != 0 || status.Conflicted != 0
}

func markPushCreatedRemote(err error) {
	var transfer *workspacetransfer.Error
	if !errors.As(err, &transfer) {
		return
	}
	contextValues := make(map[string]string, len(transfer.Context)+1)
	for key, value := range transfer.Context {
		contextValues[key] = value
	}
	contextValues["remote_created"] = "true"
	transfer.Context = contextValues
}

func resolvePushWorktree(ctx context.Context, streams Streams, global *globalOptions, target boxtarget.Target, local *repository.LocalCheckout, dryRun bool) (string, *repository.CheckoutState, error) {
	catalog, err := target.ListContextWorktrees(ctx)
	if err != nil {
		return "", nil, err
	}
	if len(catalog.Warnings) != 0 {
		return "", nil, &repository.Error{Code: repository.CodeConflict, Message: "remote Worktree discovery is incomplete; specify an exact remote Worktree after inspecting the Box", Context: map[string]string{"warnings": fmt.Sprint(len(catalog.Warnings))}}
	}
	var choices []repository.Worktree
	if local.OriginKey != "" {
		for _, relation := range catalog.Repositories {
			if repository.OriginKey(relation.Origin) != local.OriginKey {
				continue
			}
			if relation.Primary != nil {
				choices = append(choices, *relation.Primary)
			}
			choices = append(choices, relation.Linked...)
		}
	}
	switch len(choices) {
	case 1:
		return choices[0].Path, nil, nil
	case 0:
		name := filepath.Base(local.TopLevel)
		if identity, network, identityErr := source.RepositoryIdentityFor(local.CloneSource); identityErr == nil && network && identity.Repository != "" {
			name = identity.Repository
		}
		proposed := filepath.Join(target.WorktreeRoot(), name)
		if dryRun || local.CloneSource == "" {
			return proposed, nil, nil
		}
		result, cloneErr := cloneWithRecovery(ctx, streams, global, target, repository.CloneRequest{Source: local.CloneSource}, "Cloning Repository on "+targetBoxLabel(target), nil)
		if cloneErr != nil {
			return "", nil, cloneErr
		}
		path := result.Path
		if result.Inspection != nil {
			path = result.Inspection.Worktree.Path
		}
		seed, seedErr := observeCreatedPushDestination(ctx, target, result, path, local.OriginKey)
		return path, seed, seedErr
	default:
		if !interactionAllowed(streams, global) {
			return "", nil, usageError{cause: fmt.Errorf("multiple same-origin remote Worktrees are available; specify an exact remote Worktree")}
		}
		promptChoices := make([]prompts.Choice, 0, len(choices))
		for _, value := range choices {
			branch := value.Branch
			if value.Detached {
				branch = "detached"
			}
			promptChoices = append(promptChoices, prompts.Choice{Label: value.RelativePath + "  " + branch, Value: value.Path})
		}
		selected, pickErr := prompts.Pick(ctx, promptOptions(streams, global), "Choose a remote Worktree to push", promptChoices)
		if errors.Is(pickErr, prompts.ErrAborted) {
			return "", nil, abortError{cause: pickErr}
		}
		return selected, nil, pickErr
	}
}

func resolveRemotePushPath(root, selector string) (string, error) {
	value := selector
	if !filepath.IsAbs(value) {
		value = filepath.Join(root, filepath.FromSlash(value))
	}
	value = filepath.Clean(value)
	relative, err := filepath.Rel(root, value)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", usageError{cause: fmt.Errorf("remote Worktree must be beneath the Box Worktree Root")}
	}
	return value, nil
}

func normalizePushError(err error) error {
	var transfer *workspacetransfer.Error
	if errors.As(err, &transfer) {
		return &box.Error{Code: string(transfer.Code), Message: transfer.Message, Context: transfer.Context, Cause: err}
	}
	var linkError *link.Error
	if errors.As(err, &linkError) {
		return guidanceError{cause: &box.Error{Code: string(linkError.Code), Message: linkError.Message, Cause: err}, guidance: "run `schooner push <remote-worktree> --box <box>` after inspecting the intended destination"}
	}
	var repositoryError *repository.Error
	if errors.As(err, &repositoryError) {
		return &box.Error{Code: string(repositoryError.Code), Message: repositoryError.Message, Context: repositoryError.Context, Cause: err}
	}
	return err
}

type pushDocument struct {
	SchemaVersion      string   `json:"schema_version"`
	Operation          string   `json:"operation"`
	Action             string   `json:"action"`
	Box                string   `json:"box"`
	LocalWorktree      string   `json:"local_worktree"`
	RemoteWorktree     string   `json:"remote_worktree"`
	RepositoryIdentity string   `json:"repository_identity,omitempty"`
	FilesChanged       int      `json:"files_changed"`
	BytesTransferred   int64    `json:"bytes_transferred"`
	LinkCreated        bool     `json:"link_created"`
	RemoteCreated      bool     `json:"remote_created"`
	CreationMethod     string   `json:"creation_method,omitempty"`
	Warnings           []string `json:"warnings"`
}

func writePushResult(writer io.Writer, output string, result workspacetransfer.PushResult, target boxtarget.Target, linkCreated, dryRun bool, theme *uitheme.Theme) error {
	if output == "json" {
		return json.NewEncoder(writer).Encode(pushDocument{SchemaVersion: "1", Operation: "push", Action: string(result.Action), Box: targetBoxLabel(target), LocalWorktree: result.Source.Worktree, RemoteWorktree: result.RemoteWorktree, RepositoryIdentity: result.Source.RepositoryIdentity, FilesChanged: result.FilesChanged, BytesTransferred: result.BytesTransferred, LinkCreated: linkCreated, RemoteCreated: result.Created, CreationMethod: pushCreationMethod(result), Warnings: []string{}})
	}
	if output != "human" {
		return fmt.Errorf("unsupported output format %q", output)
	}
	title := "Pushed to " + targetBoxLabel(target)
	if result.Action == workspacetransfer.ActionNoChange {
		title = "Already up to date on " + targetBoxLabel(target)
	}
	if dryRun && result.Action != workspacetransfer.ActionNoChange {
		title = "Would push to " + targetBoxLabel(target)
	}
	rows := []summaryRow{{Label: "Worktree", Value: result.RemoteWorktree}, {Label: "Files", Value: fmt.Sprintf("%d changed", result.FilesChanged)}}
	if result.Created {
		label := "Created"
		if dryRun {
			label = "Plan"
		}
		value := "normal Git Repository from the local checkout"
		if result.Source.CloneSource != "" {
			value = "clone Repository, then overlay the local workspace"
		}
		rows = append(rows, summaryRow{Label: label, Value: value})
	}
	if !dryRun {
		rows = append(rows, summaryRow{Label: "Transferred", Value: humanBytes(result.BytesTransferred)})
	}
	if err := writeReadySummary(writer, theme, title, rows); err != nil {
		return err
	}
	if linkCreated {
		_, err := fmt.Fprintf(writer, "Linked to %s:%s\n", targetBoxLabel(target), result.RemoteWorktree)
		return err
	}
	return nil
}

func pushCreationMethod(result workspacetransfer.PushResult) string {
	if !result.Created {
		return ""
	}
	if result.Source.CloneSource != "" {
		return "clone"
	}
	return "direct_repository"
}

func humanBytes(value int64) string {
	if value < 1024 {
		return fmt.Sprintf("%d B", value)
	}
	if value < 1024*1024 {
		return fmt.Sprintf("%.1f KB", float64(value)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(value)/(1024*1024))
}
