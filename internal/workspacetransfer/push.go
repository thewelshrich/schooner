// Package workspacetransfer implements explicit, directional workspace
// movement without synchronization history.
package workspacetransfer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/thewelshrich/schooner/internal/repository"
)

type Action string

const (
	ActionWouldPush Action = "would_push"
	ActionPushed    Action = "pushed"
	ActionWouldPull Action = "would_pull"
	ActionPulled    Action = "pulled"
	ActionNoChange  Action = "no_change"
	ActionConflict  Action = "conflict"
)

type Code string

const (
	CodeConflict          Code = "conflict"
	CodeInsufficientSpace Code = "insufficient_space"
)

type Error struct {
	Code      Code
	Operation string
	Message   string
	Context   map[string]string
	Cause     error
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.Cause }

type PushRequest struct {
	LocalWorktree      string
	RemoteWorktree     string
	Staging            string
	DryRun             bool
	Remote             Remote
	CreatedDestination *repository.CheckoutState
}

type ApplyRequest struct {
	OperationID                 string
	RemoteWorktree              string
	ExpectedStateDigest         string
	PayloadSize                 int64
	PayloadSHA256               string
	SourceState                 repository.CheckoutState
	OperationCreatedDestination bool
	OperationCreatedBranch      string
}

type ApplyResult struct {
	State            repository.CheckoutState
	BytesTransferred int64
}

type PreflightResult struct {
	ExistingFiles int
	MatchingFiles int
}

// Remote is the real direct/SSH seam. Both adapters inspect and apply through
// the same repository checkout implementation.
type Remote interface {
	ObservePushDestination(context.Context, string) (*repository.CheckoutState, error)
	PreflightPushDestination(context.Context, string, repository.CheckoutState, bool) (PreflightResult, error)
	ApplyPush(context.Context, ApplyRequest, io.Reader) (ApplyResult, error)
}

type PushResult struct {
	Action           Action
	Source           repository.CheckoutState
	Destination      *repository.CheckoutState
	RemoteWorktree   string
	FilesChanged     int
	BytesTransferred int64
	Created          bool
}

func Push(ctx context.Context, request PushRequest) (PushResult, error) {
	if request.Remote == nil || request.LocalWorktree == "" || request.RemoteWorktree == "" || request.Staging == "" {
		return PushResult{}, fmt.Errorf("workspace push is not configured")
	}
	var capture repository.CheckoutCapture
	var source repository.CheckoutState
	var err error
	if request.DryRun {
		source, err = repository.ObserveCheckout(ctx, request.LocalWorktree)
	} else {
		capture, err = repository.CaptureCheckout(ctx, request.LocalWorktree, request.Staging)
		if err == nil {
			defer capture.Release()
			source = capture.State
		}
	}
	if err != nil {
		return PushResult{}, err
	}
	destination, err := request.Remote.ObservePushDestination(ctx, request.RemoteWorktree)
	if err != nil {
		return PushResult{}, err
	}
	result := PushResult{Source: source, Destination: destination, RemoteWorktree: request.RemoteWorktree, Created: destination == nil || request.CreatedDestination != nil}
	if request.CreatedDestination != nil {
		seed := request.CreatedDestination
		if destination == nil || destination.Worktree != request.RemoteWorktree || destination.RevalidationDigest != seed.RevalidationDigest {
			result.Action = ActionConflict
			return result, &Error{Code: CodeConflict, Message: "Push stopped: the newly cloned remote Worktree changed before the local workspace could be applied"}
		}
	}
	if destination != nil {
		if source.RepositoryIdentity != "" && destination.RepositoryIdentity != "" && source.RepositoryIdentity != destination.RepositoryIdentity {
			result.Action = ActionConflict
			return result, &Error{Code: CodeConflict, Message: "Push stopped: the local and remote Worktrees belong to different network repositories"}
		}
		if destination.Digest == source.Digest {
			result.Action = ActionNoChange
			return result, nil
		}
		if dirty(destination.Status) {
			result.Action = ActionConflict
			return result, &Error{Code: CodeConflict, Message: "Push stopped: the remote Worktree contains changes", Context: statusContext(destination.Status)}
		}
		if request.CreatedDestination == nil {
			safe, ancestorErr := repository.CommitIsAncestor(ctx, source.Worktree, destination.HEAD, source.HEAD)
			if ancestorErr != nil {
				return result, ancestorErr
			}
			if !safe {
				result.Action = ActionConflict
				return result, &Error{Code: CodeConflict, Message: "Push stopped: the remote Worktree contains commits not reachable from the local Worktree"}
			}
		}
		comparison := PreflightResult{}
		preflightFiles := make([]repository.CheckoutFile, 0, len(source.Files)+len(source.AbsentPaths))
		preflightFiles = append(preflightFiles, source.Files...)
		for _, path := range source.AbsentPaths {
			preflightFiles = append(preflightFiles, repository.CheckoutFile{Path: path, Kind: "absent", Tracked: true})
		}
		if len(preflightFiles) == 0 {
			comparison, err = request.Remote.PreflightPushDestination(ctx, request.RemoteWorktree, source, true)
			if err != nil {
				result.Action = ActionConflict
				return result, pushPreflightError(err)
			}
		}
		for offset := 0; offset < len(preflightFiles); {
			end, pageErr := preflightPageEnd(preflightFiles, offset)
			if pageErr != nil {
				return result, pageErr
			}
			page := source
			page.Files = preflightFiles[offset:end]
			pageComparison, preflightErr := request.Remote.PreflightPushDestination(ctx, request.RemoteWorktree, page, offset == 0)
			if preflightErr != nil {
				result.Action = ActionConflict
				return result, pushPreflightError(preflightErr)
			}
			comparison.ExistingFiles += pageComparison.ExistingFiles
			comparison.MatchingFiles += pageComparison.MatchingFiles
			offset = end
		}
		result.FilesChanged = source.FileCount + destination.FileCount - comparison.ExistingFiles - comparison.MatchingFiles
	} else {
		result.FilesChanged = source.FileCount
	}
	if request.DryRun {
		result.Action = ActionWouldPush
		return result, nil
	}
	payload, err := os.Open(capture.PayloadPath)
	if err != nil {
		return result, err
	}
	defer payload.Close()
	expected := ""
	if destination != nil {
		expected = destination.RevalidationDigest
	}
	operationID, err := newOperationID()
	if err != nil {
		return result, err
	}
	applied, err := request.Remote.ApplyPush(ctx, ApplyRequest{
		OperationID: operationID, RemoteWorktree: request.RemoteWorktree, ExpectedStateDigest: expected,
		PayloadSize: capture.PayloadSize, PayloadSHA256: capture.PayloadSHA256, SourceState: capture.State,
		OperationCreatedDestination: request.CreatedDestination != nil,
		OperationCreatedBranch:      operationCreatedBranch(request.CreatedDestination),
	}, payload)
	if err != nil {
		return result, err
	}
	if applied.State.Digest != source.Digest {
		return result, fmt.Errorf("remote workspace verification returned a different state")
	}
	result.Action = ActionPushed
	result.Destination = &applied.State
	result.BytesTransferred = applied.BytesTransferred
	return result, nil
}

func operationCreatedBranch(state *repository.CheckoutState) string {
	if state == nil || state.Detached {
		return ""
	}
	return state.Branch
}

func pushPreflightError(err error) error {
	if repository.ErrorCode(err) != repository.CodeConflict {
		return err
	}
	var repositoryError *repository.Error
	if errors.As(err, &repositoryError) {
		return &Error{Code: CodeConflict, Message: "Push stopped: " + repositoryError.Message, Context: repositoryError.Context, Cause: err}
	}
	return err
}

func preflightPageEnd(files []repository.CheckoutFile, start int) (int, error) {
	const maximumEncodedFiles = 768 << 10
	size := 0
	end := start
	for end < len(files) {
		encoded, err := json.Marshal(files[end])
		if err != nil {
			return 0, fmt.Errorf("encode workspace preflight manifest: %w", err)
		}
		if end > start && size+len(encoded)+1 > maximumEncodedFiles {
			break
		}
		size += len(encoded) + 1
		end++
	}
	return end, nil
}

func newOperationID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("create workspace operation ID: %w", err)
	}
	return hex.EncodeToString(value), nil
}

func dirty(value repository.Status) bool {
	return value.Staged != 0 || value.Unstaged != 0 || value.Untracked != 0 || value.Conflicted != 0
}

func statusContext(value repository.Status) map[string]string {
	return map[string]string{
		"staged": fmt.Sprint(value.Staged), "unstaged": fmt.Sprint(value.Unstaged),
		"untracked": fmt.Sprint(value.Untracked), "conflicted": fmt.Sprint(value.Conflicted),
	}
}
