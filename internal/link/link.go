// Package link owns lightweight local-to-remote Worktree routing state.
package link

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"
)

type Code string

const (
	CodeNotFound     Code = "not_found"
	CodeInvalidInput Code = "invalid_input"
	CodeStale        Code = "stale_link"
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

// LocalLink is routing state. It deliberately contains no checkout snapshot
// or synchronization history.
type LocalLink struct {
	LocalWorktree       string
	BoxID               string
	ExpectedBoxIdentity string
	RemoteWorktree      string
	RepositoryIdentity  string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

func (value LocalLink) Validate() error {
	if !canonicalAbsolute(value.LocalWorktree) {
		return &Error{Code: CodeInvalidInput, Message: "Local Link local Worktree must be a canonical absolute path"}
	}
	if value.BoxID == "" || value.ExpectedBoxIdentity == "" {
		return &Error{Code: CodeInvalidInput, Message: "Local Link Box identity is required"}
	}
	if !canonicalAbsolute(value.RemoteWorktree) {
		return &Error{Code: CodeInvalidInput, Message: "Local Link remote Worktree must be a canonical absolute path"}
	}
	if value.CreatedAt.IsZero() || value.UpdatedAt.IsZero() || value.UpdatedAt.Before(value.CreatedAt) {
		return &Error{Code: CodeInvalidInput, Message: "Local Link timestamps are invalid"}
	}
	return nil
}

func canonicalAbsolute(value string) bool {
	return value != "" && filepath.IsAbs(value) && filepath.Clean(value) == value
}

type Store interface {
	FindLocalLink(context.Context, string) (LocalLink, error)
	SaveLocalLink(context.Context, LocalLink) error
}

func Find(ctx context.Context, store Store, localWorktree, currentRepositoryIdentity string) (LocalLink, error) {
	if store == nil || !canonicalAbsolute(localWorktree) {
		return LocalLink{}, &Error{Code: CodeInvalidInput, Message: "local Worktree must be a canonical absolute path"}
	}
	value, err := store.FindLocalLink(ctx, localWorktree)
	if err != nil {
		return LocalLink{}, err
	}
	if err = value.Validate(); err != nil {
		return LocalLink{}, &Error{Code: CodeStale, Message: "the Local Link is invalid; run a successful push with an explicit Box and Worktree to replace it", Cause: err}
	}
	if value.LocalWorktree != localWorktree {
		return LocalLink{}, &Error{Code: CodeStale, Message: "the Local Link no longer matches this Worktree"}
	}
	if value.RepositoryIdentity != "" && value.RepositoryIdentity != currentRepositoryIdentity {
		return LocalLink{}, &Error{Code: CodeStale, Message: "the Local Link belongs to a different Repository at this local Worktree"}
	}
	return value, nil
}

func Save(ctx context.Context, store Store, value LocalLink) error {
	if store == nil {
		return fmt.Errorf("Local Link store is not configured")
	}
	if err := value.Validate(); err != nil {
		return err
	}
	return store.SaveLocalLink(ctx, value)
}
