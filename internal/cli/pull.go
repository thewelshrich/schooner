package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/thewelshrich/schooner/internal/box"
	"github.com/thewelshrich/schooner/internal/boxtarget"
	invsqlite "github.com/thewelshrich/schooner/internal/inventory/sqlite"
	"github.com/thewelshrich/schooner/internal/link"
	"github.com/thewelshrich/schooner/internal/repository"
	"github.com/thewelshrich/schooner/internal/ui/prompts"
	uitheme "github.com/thewelshrich/schooner/internal/ui/theme"
	"github.com/thewelshrich/schooner/internal/workspacetransfer"
)

func newPullCommand(streams Streams, global *globalOptions, targets *boxtarget.Resolver) *cobra.Command {
	var explicitBox string
	var dryRun bool
	command := &cobra.Command{
		Use: "pull [remote-worktree]", Short: "Bring a Box workspace into the current local checkout", Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var progress box.Progress
			if global.output == "human" {
				renderer := newProgressRenderer(cmd.Context(), streams.Err, streams.ErrIsTerminal && !global.accessible, terminalTheme(global, streams))
				defer renderer.Close()
				progress = renderer.Event
			}
			result, target, linkCreated, err := runPull(cmd.Context(), streams, global, targets, explicitBox, firstArgument(args), dryRun, progress)
			if err != nil {
				var usage usageError
				if errors.As(err, &usage) {
					return err
				}
				return executionError{cause: normalizePullError(err)}
			}
			return writePullResult(cmd.OutOrStdout(), global.output, result, target, linkCreated, dryRun, outputTheme(global, streams))
		},
	}
	command.Flags().StringVar(&explicitBox, "box", "", "box name (always uses OpenSSH)")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "inspect and show the pull without changing the local checkout")
	return command
}

func runPull(ctx context.Context, streams Streams, global *globalOptions, targets *boxtarget.Resolver, explicitBox, remoteSelector string, dryRun bool, progress box.Progress) (workspacetransfer.PullResult, boxtarget.Target, bool, error) {
	local, err := inspectCurrentCheckout(ctx)
	if err != nil {
		return workspacetransfer.PullResult{}, boxtarget.Target{}, false, err
	}
	if local == nil {
		return workspacetransfer.PullResult{}, boxtarget.Target{}, false, usageError{cause: fmt.Errorf("schooner pull must run inside a Git Worktree")}
	}
	statePath, err := invsqlite.DefaultPath()
	if err != nil {
		return workspacetransfer.PullResult{}, boxtarget.Target{}, false, err
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
		return workspacetransfer.PullResult{}, boxtarget.Target{}, false, err
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
			linked = false
		} else if link.ErrorCode(err) != link.CodeNotFound {
			return workspacetransfer.PullResult{}, boxtarget.Target{}, false, err
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
		return workspacetransfer.PullResult{}, boxtarget.Target{}, false, err
	}
	useLink := linked && target.BoxID() == localLink.BoxID && remoteSelector == ""
	if useLink && target.BoxIdentity() != localLink.ExpectedBoxIdentity {
		return workspacetransfer.PullResult{}, target, false, &link.Error{Code: link.CodeStale, Message: "the Local Link's Box identity no longer matches the selected Box"}
	}
	remoteWorktree := ""
	if useLink {
		remoteWorktree = localLink.RemoteWorktree
		inspection, inspectErr := target.InspectWorktree(ctx, remoteWorktree)
		if box.ErrorCode(inspectErr) == "not_found" {
			return workspacetransfer.PullResult{}, target, false, &link.Error{Code: link.CodeStale, Message: "the linked remote Worktree no longer exists"}
		}
		if inspectErr != nil {
			return workspacetransfer.PullResult{}, target, false, inspectErr
		}
		identity := repository.OriginKey(inspection.Repository.Origin)
		if localLink.RepositoryIdentity != "" && identity != localLink.RepositoryIdentity {
			return workspacetransfer.PullResult{}, target, false, &link.Error{Code: link.CodeStale, Message: "the linked remote Worktree no longer has the expected Repository identity"}
		}
	} else if remoteSelector != "" {
		remoteWorktree, err = resolveRemoteWorktreePath(target.WorktreeRoot(), remoteSelector)
		if err != nil {
			return workspacetransfer.PullResult{}, target, false, err
		}
		inspection, inspectErr := target.InspectWorktree(ctx, remoteWorktree)
		if inspectErr != nil {
			return workspacetransfer.PullResult{}, target, false, inspectErr
		}
		remoteWorktree = inspection.Worktree.Path
	} else {
		remoteWorktree, err = resolvePullWorktree(ctx, streams, global, target, local)
		if err != nil {
			return workspacetransfer.PullResult{}, target, false, err
		}
	}
	staging := filepath.Join(filepath.Dir(statePath), "operations", "workspace")
	locks, err := repository.DefaultWorktreeLockStateDirectory()
	if err != nil {
		return workspacetransfer.PullResult{}, target, false, err
	}
	message := "Pulling workspace from " + targetBoxLabel(target)
	if dryRun {
		message = "Inspecting workspace on " + targetBoxLabel(target)
	}
	if progress != nil {
		progress(box.Event{Step: box.StepVerify, State: box.EventStarted, Message: message})
	}
	result, err := workspacetransfer.Pull(ctx, workspacetransfer.PullRequest{
		LocalWorktree: local.TopLevel, RemoteWorktree: remoteWorktree, Staging: staging,
		LockStateDirectory: locks, DryRun: dryRun, Source: target,
	})
	if progress != nil {
		state := box.EventCompleted
		if err != nil {
			state = box.EventFailed
		}
		progress(box.Event{Step: box.StepVerify, State: state, Message: message})
	}
	if err != nil {
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
		if result.Source.RepositoryIdentity == local.OriginKey {
			linkedIdentity = local.OriginKey
		}
		value := link.LocalLink{LocalWorktree: local.TopLevel, BoxID: target.BoxID(), ExpectedBoxIdentity: target.BoxIdentity(), RemoteWorktree: remoteWorktree, RepositoryIdentity: linkedIdentity, CreatedAt: created, UpdatedAt: now}
		if err = link.Save(ctx, store, value); err != nil {
			return result, target, false, fmt.Errorf("workspace was pulled but its Local Link could not be saved: %w", err)
		}
		linkCreated = !useLink
	}
	return result, target, linkCreated, nil
}

func resolvePullWorktree(ctx context.Context, streams Streams, global *globalOptions, target boxtarget.Target, local *repository.LocalCheckout) (string, error) {
	catalog, err := target.ListContextWorktrees(ctx)
	if err != nil {
		return "", err
	}
	if len(catalog.Warnings) != 0 {
		return "", &repository.Error{Code: repository.CodeConflict, Message: "remote Worktree discovery is incomplete; specify an exact remote Worktree after inspecting the Box"}
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
		return choices[0].Path, nil
	case 0:
		return "", &repository.Error{Code: repository.CodeNotFound, Message: "no matching remote Worktree exists; specify an exact remote Worktree"}
	default:
		if !interactionAllowed(streams, global) {
			return "", usageError{cause: fmt.Errorf("multiple same-origin remote Worktrees are available; specify an exact remote Worktree")}
		}
		promptChoices := make([]prompts.Choice, 0, len(choices))
		for _, value := range choices {
			branch := value.Branch
			if value.Detached {
				branch = "detached"
			}
			promptChoices = append(promptChoices, prompts.Choice{Label: value.RelativePath + "  " + branch, Value: value.Path})
		}
		selected, pickErr := prompts.Pick(ctx, promptOptions(streams, global), "Choose a remote Worktree to pull", promptChoices)
		if errors.Is(pickErr, prompts.ErrAborted) {
			return "", abortError{cause: pickErr}
		}
		return selected, pickErr
	}
}

func normalizePullError(err error) error {
	var transfer *workspacetransfer.Error
	if errors.As(err, &transfer) {
		return &box.Error{Code: string(transfer.Code), Message: transfer.Message, Context: transfer.Context, Cause: err}
	}
	var linkError *link.Error
	if errors.As(err, &linkError) {
		return guidanceError{cause: &box.Error{Code: string(linkError.Code), Message: linkError.Message, Cause: err}, guidance: "run `schooner pull <remote-worktree> --box <box>` after inspecting the intended source"}
	}
	var repositoryError *repository.Error
	if errors.As(err, &repositoryError) {
		if repositoryError.Code == repository.CodeConflict {
			transfer = &workspacetransfer.Error{Code: workspacetransfer.CodeConflict, Operation: "pull", Message: repositoryError.Message, Context: repositoryError.Context, Cause: err}
			return &box.Error{Code: string(transfer.Code), Message: transfer.Message, Context: transfer.Context, Cause: transfer}
		}
		return &box.Error{Code: string(repositoryError.Code), Message: repositoryError.Message, Context: repositoryError.Context, Cause: err}
	}
	var boxError *box.Error
	if errors.As(err, &boxError) && boxError.Code == "conflict" {
		transfer = &workspacetransfer.Error{Code: workspacetransfer.CodeConflict, Operation: "pull", Message: boxError.Message, Context: boxError.Context, Cause: err}
		return &box.Error{Code: boxError.Code, Message: boxError.Message, Context: boxError.Context, Cause: transfer}
	}
	return err
}

type pullDocument struct {
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
	Warnings           []string `json:"warnings"`
}

func writePullResult(writer io.Writer, output string, result workspacetransfer.PullResult, target boxtarget.Target, linkCreated, dryRun bool, theme *uitheme.Theme) error {
	if output == "json" {
		return json.NewEncoder(writer).Encode(pullDocument{SchemaVersion: "1", Operation: "pull", Action: string(result.Action), Box: targetBoxLabel(target), LocalWorktree: result.Destination.Worktree, RemoteWorktree: result.RemoteWorktree, RepositoryIdentity: result.Source.RepositoryIdentity, FilesChanged: result.FilesChanged, BytesTransferred: result.BytesTransferred, LinkCreated: linkCreated, Warnings: []string{}})
	}
	if output != "human" {
		return fmt.Errorf("unsupported output format %q", output)
	}
	title := "Pulled from " + targetBoxLabel(target)
	if result.Action == workspacetransfer.ActionNoChange {
		title = "Already up to date with " + targetBoxLabel(target)
	}
	if dryRun && result.Action != workspacetransfer.ActionNoChange {
		title = "Would pull from " + targetBoxLabel(target)
	}
	rows := []summaryRow{{Label: "Worktree", Value: result.RemoteWorktree}, {Label: "Files", Value: fmt.Sprintf("%d changed", result.FilesChanged)}}
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
