// Package workcontext resolves high-level start and resume intent from live
// local Git, remote Git, and tmux observations. It performs no I/O and stores
// no relationship between the local and remote checkout.
package workcontext

import (
	"slices"
	"strings"
	"time"

	"github.com/thewelshrich/schooner/internal/repository"
	"github.com/thewelshrich/schooner/internal/session"
)

type StartMode string

const (
	StartUse         StartMode = "use"
	StartChoose      StartMode = "choose"
	StartClone       StartMode = "clone"
	StartUnavailable StartMode = "unavailable"
)

type WorktreeChoice struct {
	Origin          string
	CommonDirectory string
	Worktree        repository.Worktree
}

type StartPlan struct {
	Mode        StartMode
	Preferred   WorktreeChoice
	Choices     []WorktreeChoice
	CloneSource string
	Incomplete  bool
}

// PlanStart prefers the primary Worktree of the Repository matching local.
// When no match exists, a portable origin becomes a guided clone proposal;
// existing remote Worktrees remain available as a fallback.
func PlanStart(local *repository.LocalCheckout, catalog repository.Catalog) StartPlan {
	fallback := defaultChoices(catalog.Repositories)
	if local == nil || local.OriginKey == "" {
		return chooseStart(fallback)
	}
	matching := matchingRepositories(local.OriginKey, catalog.Repositories)
	if len(catalog.Warnings) != 0 {
		choices := fallback
		if len(matching) != 0 {
			choices = defaultChoices(matching)
		}
		sortWorktrees(choices)
		if len(choices) == 0 {
			return StartPlan{Mode: StartUnavailable, Incomplete: true}
		}
		return StartPlan{Mode: StartChoose, Choices: choices, Incomplete: true}
	}
	if len(matching) == 0 {
		if local.CloneSource == "" {
			return chooseStart(fallback)
		}
		return StartPlan{Mode: StartClone, Choices: fallback, CloneSource: local.CloneSource}
	}
	choices := defaultChoices(matching)
	return chooseStart(choices)
}

func chooseStart(choices []WorktreeChoice) StartPlan {
	sortWorktrees(choices)
	switch len(choices) {
	case 0:
		return StartPlan{Mode: StartUnavailable}
	case 1:
		return StartPlan{Mode: StartUse, Preferred: choices[0], Choices: choices}
	default:
		return StartPlan{Mode: StartChoose, Choices: choices}
	}
}

type ResumeMode string

const (
	ResumeUse         ResumeMode = "use"
	ResumeChoose      ResumeMode = "choose"
	ResumeUnavailable ResumeMode = "unavailable"
)

type ResumePlan struct {
	Mode       ResumeMode
	Preferred  session.Session
	Choices    []session.Session
	Incomplete bool
}

// PlanResume treats a detected local Repository as binding context and never
// falls back from it to an unrelated Session. Outside a local Repository, it
// selects the newest live, unambiguously associated managed Session.
func PlanResume(local *repository.LocalCheckout, catalog repository.Catalog, sessions session.Catalog) ResumePlan {
	managed := automaticSessions(sessions.Sessions)
	if local == nil {
		if len(managed) == 0 {
			return chooseResume(sessions.Sessions)
		}
		sortSessions(managed)
		return ResumePlan{Mode: ResumeUse, Preferred: managed[0]}
	}
	if local.OriginKey == "" {
		return ResumePlan{Mode: ResumeUnavailable}
	}

	matching := matchingRepositories(local.OriginKey, catalog.Repositories)
	common := make(map[string]struct{}, len(matching))
	for _, value := range matching {
		common[value.CommonDirectory] = struct{}{}
	}
	matched := make([]session.Session, 0)
	for _, value := range managed {
		if _, ok := common[value.RepositoryCommonDirectory]; ok {
			matched = append(matched, value)
		}
	}
	if len(matched) == 0 {
		return ResumePlan{Mode: ResumeUnavailable, Incomplete: len(catalog.Warnings) != 0}
	}
	sortSessions(matched)
	if len(catalog.Warnings) != 0 {
		return ResumePlan{Mode: ResumeChoose, Choices: matched, Incomplete: true}
	}
	if sessionsSpanRepositories(matched) {
		return ResumePlan{Mode: ResumeChoose, Choices: matched}
	}
	return ResumePlan{Mode: ResumeUse, Preferred: matched[0]}
}

func chooseResume(values []session.Session) ResumePlan {
	choices := resumableSessions(values)
	if len(choices) == 0 {
		return ResumePlan{Mode: ResumeUnavailable}
	}
	sortSessions(choices)
	return ResumePlan{Mode: ResumeChoose, Choices: choices}
}

func matchingRepositories(key string, values []repository.Repository) []repository.Repository {
	result := make([]repository.Repository, 0)
	for _, value := range values {
		if repository.OriginKey(value.Origin) == key {
			result = append(result, value)
		}
	}
	return result
}

func defaultChoices(values []repository.Repository) []WorktreeChoice {
	result := make([]WorktreeChoice, 0)
	for _, value := range values {
		if value.Primary != nil {
			result = append(result, WorktreeChoice{Origin: value.Origin, CommonDirectory: value.CommonDirectory, Worktree: *value.Primary})
		} else {
			for _, worktree := range value.Linked {
				result = append(result, WorktreeChoice{Origin: value.Origin, CommonDirectory: value.CommonDirectory, Worktree: worktree})
			}
		}
	}
	return result
}

func sessionsSpanRepositories(values []session.Session) bool {
	first := ""
	for _, value := range values {
		if first == "" {
			first = value.RepositoryCommonDirectory
			continue
		}
		if value.RepositoryCommonDirectory != first {
			return true
		}
	}
	return false
}

func automaticSessions(values []session.Session) []session.Session {
	result := make([]session.Session, 0)
	for _, value := range values {
		if value.Ownership == session.Managed && value.Association == session.AssociationLive && value.ID != "" {
			result = append(result, value)
		}
	}
	return result
}

func resumableSessions(values []session.Session) []session.Session {
	result := make([]session.Session, 0)
	for _, value := range values {
		if (value.Ownership == session.Managed && value.ID != "") || (value.Ownership == session.Unmanaged && value.TmuxID != "") {
			result = append(result, value)
		}
	}
	return result
}

func sortWorktrees(values []WorktreeChoice) {
	slices.SortFunc(values, func(left, right WorktreeChoice) int {
		if compared := strings.Compare(left.Worktree.RelativePath, right.Worktree.RelativePath); compared != 0 {
			return compared
		}
		return strings.Compare(left.Worktree.Path, right.Worktree.Path)
	})
}

func sortSessions(values []session.Session) {
	slices.SortStableFunc(values, func(left, right session.Session) int {
		if compared := compareTimeDescending(left.ActivityAt, right.ActivityAt); compared != 0 {
			return compared
		}
		if compared := compareTimeDescending(left.CreatedAt, right.CreatedAt); compared != 0 {
			return compared
		}
		return strings.Compare(left.TmuxID, right.TmuxID)
	})
}

func compareTimeDescending(left, right time.Time) int {
	if left.After(right) {
		return -1
	}
	if left.Before(right) {
		return 1
	}
	return 0
}
