package workcontext

import (
	"testing"
	"time"

	"github.com/thewelshrich/schooner/internal/repository"
	"github.com/thewelshrich/schooner/internal/session"
)

func TestPlanStartUsesMatchingPrimaryAsIs(t *testing.T) {
	primary := repository.Worktree{Path: "/remote/repo", RelativePath: "repo", Branch: "main"}
	linked := repository.Worktree{Path: "/remote/feature", RelativePath: "feature", Branch: "feature"}
	catalog := repository.Catalog{Repositories: []repository.Repository{
		{Origin: "git@github.com:else/other.git", CommonDirectory: "/other/.git", Primary: &repository.Worktree{Path: "/remote/other", RelativePath: "other"}},
		{Origin: "git@github.com:owner/repo.git", CommonDirectory: "/repo/.git", Primary: &primary, Linked: []repository.Worktree{linked}},
	}}
	local := &repository.LocalCheckout{Origin: "https://github.com/owner/repo", OriginKey: "github.com/owner/repo", Branch: "local-feature"}

	plan := PlanStart(local, catalog)
	if plan.Mode != StartUse || plan.Preferred.Worktree.Path != primary.Path {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestPlanStartMatchesNonDefaultSSHUsername(t *testing.T) {
	primary := repository.Worktree{Path: "/remote/repo", RelativePath: "repo", Branch: "main"}
	catalog := repository.Catalog{Repositories: []repository.Repository{
		{Origin: "ssh://bob@example.com/owner/repo", CommonDirectory: "/bob/.git", Primary: &repository.Worktree{Path: "/remote/bob", RelativePath: "bob"}},
		{Origin: "ssh://alice@example.com/owner/repo", CommonDirectory: "/alice/.git", Primary: &primary},
	}}
	local := &repository.LocalCheckout{OriginKey: "alice@example.com//owner/repo"}

	plan := PlanStart(local, catalog)
	if plan.Mode != StartUse || plan.Preferred.Worktree.Path != primary.Path {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestPlanStartOffersCloneAndRetainsExistingFallback(t *testing.T) {
	primary := repository.Worktree{Path: "/remote/existing", RelativePath: "existing"}
	local := &repository.LocalCheckout{Origin: "https://example.com/new/repo", OriginKey: "example.com/new/repo", CloneSource: "git@example.com:new/repo"}
	plan := PlanStart(local, repository.Catalog{Repositories: []repository.Repository{{Primary: &primary}}})
	if plan.Mode != StartClone || plan.CloneSource != local.CloneSource || len(plan.Choices) != 1 {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestPlanStartFallsBackToLinkedOnlyWhenNoPrimaryExists(t *testing.T) {
	linked := repository.Worktree{Path: "/remote/feature", RelativePath: "feature"}
	plan := PlanStart(nil, repository.Catalog{Repositories: []repository.Repository{{Linked: []repository.Worktree{linked}}}})
	if plan.Mode != StartUse || plan.Preferred.Worktree.Path != linked.Path {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestPlanStartIncludesLinkedOnlyRepositoryAlongsidePrimaryFallback(t *testing.T) {
	primary := repository.Worktree{Path: "/remote/primary", RelativePath: "primary"}
	linked := repository.Worktree{Path: "/remote/linked", RelativePath: "linked"}
	plan := PlanStart(nil, repository.Catalog{Repositories: []repository.Repository{
		{Primary: &primary},
		{Linked: []repository.Worktree{linked}},
	}})
	if plan.Mode != StartChoose || len(plan.Choices) != 2 {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestPlanStartDoesNotOfferCloneWithoutUsableSource(t *testing.T) {
	local := &repository.LocalCheckout{Origin: "/local/repo", OriginKey: "example.com/repo"}
	plan := PlanStart(local, repository.Catalog{})
	if plan.Mode != StartUnavailable {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestPlanResumePrefersNewestManagedLiveSessionForMatchingRepository(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	primary := repository.Worktree{Path: "/remote/repo"}
	local := &repository.LocalCheckout{OriginKey: "github.com/owner/repo"}
	repositories := repository.Catalog{Repositories: []repository.Repository{{Origin: "https://github.com/owner/repo", CommonDirectory: "/repo/.git", Primary: &primary}}}
	sessions := session.Catalog{Sessions: []session.Session{
		{ID: "global-newer", TmuxID: "$3", Ownership: session.Managed, Association: session.AssociationLive, RepositoryCommonDirectory: "/other/.git", ActivityAt: now.Add(time.Hour)},
		{ID: "matching-old", TmuxID: "$1", Ownership: session.Managed, Association: session.AssociationLive, RepositoryCommonDirectory: "/repo/.git", ActivityAt: now},
		{ID: "matching-new", TmuxID: "$2", Ownership: session.Managed, Association: session.AssociationLive, RepositoryCommonDirectory: "/repo/.git", ActivityAt: now.Add(time.Minute)},
	}}
	plan := PlanResume(local, repositories, sessions)
	if plan.Mode != ResumeUse || plan.Preferred.ID != "matching-new" || plan.Fallback {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestPlanResumeUsesDeterministicActivityFallback(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	local := &repository.LocalCheckout{OriginKey: "example.com/missing/repo"}
	sessions := session.Catalog{Sessions: []session.Session{
		{ID: "second", TmuxID: "$2", Ownership: session.Managed, Association: session.AssociationLive, ActivityAt: now, CreatedAt: now},
		{ID: "first", TmuxID: "$1", Ownership: session.Managed, Association: session.AssociationLive, ActivityAt: now, CreatedAt: now},
	}}
	plan := PlanResume(local, repository.Catalog{}, sessions)
	if plan.Mode != ResumeUse || plan.Preferred.ID != "first" || !plan.Fallback {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestPlanResumeRequiresPickerWhenDiscoveryIsPartial(t *testing.T) {
	local := &repository.LocalCheckout{OriginKey: "example.com/owner/repo"}
	repositories := repository.Catalog{Warnings: []repository.Warning{{Message: "catalog output limit reached"}}}
	sessions := session.Catalog{Sessions: []session.Session{
		{ID: "unrelated", TmuxID: "$1", Ownership: session.Managed, Association: session.AssociationLive},
		{ID: "possibly-matching", TmuxID: "$2", Ownership: session.Managed, Association: session.AssociationMissing},
	}}
	plan := PlanResume(local, repositories, sessions)
	if plan.Mode != ResumeChoose || len(plan.Choices) != 2 || !plan.Fallback {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestPlanResumeRequiresPickerAcrossDuplicateMatchingRepositories(t *testing.T) {
	local := &repository.LocalCheckout{OriginKey: "example.com/owner/repo"}
	repositories := repository.Catalog{Repositories: []repository.Repository{
		{Origin: "https://example.com/owner/repo", CommonDirectory: "/first/.git"},
		{Origin: "https://example.com/owner/repo", CommonDirectory: "/second/.git"},
	}}
	sessions := session.Catalog{Sessions: []session.Session{
		{ID: "first", TmuxID: "$1", Ownership: session.Managed, Association: session.AssociationLive, RepositoryCommonDirectory: "/first/.git"},
		{ID: "second", TmuxID: "$2", Ownership: session.Managed, Association: session.AssociationLive, RepositoryCommonDirectory: "/second/.git"},
	}}
	plan := PlanResume(local, repositories, sessions)
	if plan.Mode != ResumeChoose || len(plan.Choices) != 2 || plan.Fallback {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestPlanResumeRequiresPickerForUnmanagedOrUncertainSession(t *testing.T) {
	sessions := session.Catalog{Sessions: []session.Session{
		{TmuxID: "$1", Ownership: session.Unmanaged, Association: session.AssociationUnassociated},
		{ID: "missing", TmuxID: "$2", Ownership: session.Managed, Association: session.AssociationMissing},
		{ID: "invalid", TmuxID: "$3", Ownership: session.Invalid},
	}}
	plan := PlanResume(nil, repository.Catalog{}, sessions)
	if plan.Mode != ResumeChoose || len(plan.Choices) != 2 {
		t.Fatalf("plan = %+v", plan)
	}
}
