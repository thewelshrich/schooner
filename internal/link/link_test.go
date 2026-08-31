package link

import (
	"context"
	"testing"
	"time"
)

type lookupStore struct {
	value LocalLink
}

func (store lookupStore) FindLocalLink(context.Context, string) (LocalLink, error) {
	return store.value, nil
}

func (lookupStore) SaveLocalLink(context.Context, LocalLink) error { return nil }

func TestFindRevalidatesStoredRepositoryIdentity(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	base := LocalLink{
		LocalWorktree:       "/local/repo",
		BoxID:               "box-1",
		ExpectedBoxIdentity: "remote-1",
		RemoteWorktree:      "/remote/repo",
		CreatedAt:           now,
		UpdatedAt:           now,
	}
	tests := []struct {
		name            string
		storedIdentity  string
		currentIdentity string
		wantStale       bool
	}{
		{name: "matching identity", storedIdentity: "github.com/owner/repo", currentIdentity: "github.com/owner/repo"},
		{name: "different identity", storedIdentity: "github.com/owner/repo", currentIdentity: "github.com/other/repo", wantStale: true},
		{name: "current origin missing", storedIdentity: "github.com/owner/repo", wantStale: true},
		{name: "origin-less link with current identity", currentIdentity: "github.com/owner/repo"},
		{name: "origin-less link without current identity"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := base
			value.RepositoryIdentity = test.storedIdentity
			got, err := Find(t.Context(), lookupStore{value: value}, value.LocalWorktree, test.currentIdentity)
			if test.wantStale {
				if ErrorCode(err) != CodeStale {
					t.Fatalf("Find() error = %v, code = %q; want %q", err, ErrorCode(err), CodeStale)
				}
				if got != (LocalLink{}) {
					t.Fatalf("Find() = %+v; want no reusable Local Link", got)
				}
				return
			}
			if err != nil || got != value {
				t.Fatalf("Find() = %+v, %v; want %+v, nil", got, err, value)
			}
		})
	}
}
