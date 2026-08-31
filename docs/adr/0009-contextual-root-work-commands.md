# ADR 0009: Contextual root commands for frequent work

- Status: Accepted
- Date: 2026-08-26

## Context

Starting and returning to remote work are Schooner's most frequent actions.
Requiring users to navigate resource-oriented command groups or repeatedly
select a Repository, Worktree, and Session makes the common workflow harder,
while silently persisting relationships inferred from a network-origin match
would create state that can drift from Git and tmux. ADR 0012 introduces a
narrow Local Link created by an explicit successful `push`; contextual commands
can consume that deliberate routing without turning inference into state.

## Decision

`schooner start` and `schooner resume` remain root commands with contextual
defaults. Exact Worktree paths and Session IDs remain explicit selectors for
advanced and automated use.

Without a selector, `start` first checks the current local Worktree for a Local
Link. With no explicit Box, the link participates in standard Box selection.
When the selected Box is the linked Box, Schooner revalidates the recorded Box
identity, exact remote Worktree, and Repository identity before starting there.
A stale link fails with guidance instead of being followed or silently
replaced.

When no Local Link applies to the selected Box, `start` matches the local
checkout's credential-free network-origin identity against live Repositories
on that Box. A match uses the remote primary Worktree as-is and never changes
its branch. When no match exists and the local origin is safely cloneable,
Schooner reviews local-only state and offers to clone the origin's default
branch before starting a managed Session.

Contextual clone uses the same durable source-aware recovery flow as the clone
command. Interactive human use retains the review and confirmation. A
non-interactive invocation may use already available Box or managed source
credentials, but never authorizes a Source Account or registers a new Box key.

Without a selector, `resume` uses an applicable, revalidated Local Link as
binding context and chooses only a managed live Session on its exact remote
Worktree. If none exists, it stops and suggests `schooner start`. Without an
applicable link, a detected local Repository remains binding context: `resume`
chooses only a managed live Session associated with a matching Repository,
ordered by tmux activity, creation time, and tmux ID. It never falls back to an
unrelated Repository. Outside a local Repository, it chooses the newest managed
live Session on the selected Box. Unmanaged or uncertain Sessions require an
explicit selection.

Only a successful, non-dry-run `push` creates or updates a Local Link and
transfers checkout state. `start` and `resume` revalidate and consume an
applicable link but do not create one, copy local files, or transfer commits.
Origin-based fallback decisions remain live observations only.

## Consequences

- The common workflow is `schooner start` and `schooner resume`, while exact
  selectors remain quietly available.
- Matching works across common HTTPS, SSH, Git, and SCP origin forms without
  retaining credentials.
- A successful `push` can establish an exact route for later `start` and
  `resume`; inferred origin matches never persist that route.
- Local dirty files, detached HEADs, missing upstreams, and unpushed commits are
  warnings before fallback cloning because that clone contains only origin
  state.
- Repository and Session ambiguity remains visible rather than being resolved
  through hidden persistent preferences.
- Repository context is trustworthy: bare `resume` cannot navigate from one
  local Repository to work for another Repository.
- Workspace transfer remains explicit: `push` is implemented, `pull` is a
  future directional command, and there is no reconciliation-style `sync`.
