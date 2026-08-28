# ADR 0009: Contextual root commands for frequent work

- Status: Accepted
- Date: 2026-08-26

## Context

Starting and returning to remote work are Schooner's most frequent actions.
Requiring users to navigate resource-oriented command groups or repeatedly
select a Repository, Worktree, and Session makes the common workflow harder,
while silently persisting inferred local-to-remote relationships would create
state that can drift from Git and tmux.

## Decision

`schooner start` and `schooner resume` remain root commands with contextual
defaults. Exact Worktree paths and Session IDs remain explicit selectors for
advanced and automated use.

Without a selector, `start` observes the current local Git checkout and matches
its credential-free network-origin identity against live Repositories on the
selected Box. A match uses the remote primary Worktree as-is and never changes
its branch. When no match exists and the local origin is safely cloneable,
Schooner reviews local-only state and offers to clone the origin's default
branch before starting a managed Session.

Contextual clone uses the same durable source-aware recovery flow as the clone
command. Interactive human use retains the review and confirmation. A
non-interactive invocation may use already available Box or managed source
credentials, but never authorizes a Source Account or registers a new Box key.

Without a selector, `resume` treats a detected local Repository as binding
context. It chooses only a managed live Session associated with a matching
Repository, ordered by tmux activity, creation time, and tmux ID. If none
matches, it stops and suggests `schooner start`; it never falls back to an
unrelated Repository. Outside a local Repository, it chooses the newest
managed live Session on the selected Box. Unmanaged or uncertain Sessions
require an explicit selection.

These decisions use live observations only. They do not create a Local Link,
copy local files, synchronize commits, or imply `push` or `pull` behavior.

## Consequences

- The common workflow is `schooner start` and `schooner resume`, while exact
  selectors remain quietly available.
- Matching works across common HTTPS, SSH, Git, and SCP origin forms without
  retaining credentials.
- Local dirty files, detached HEADs, missing upstreams, and unpushed commits
  are warnings before cloning because the clone contains only origin state.
- Repository and Session ambiguity remains visible rather than being resolved
  through hidden persistent preferences.
- Repository context is trustworthy: bare `resume` cannot navigate from one
  local Repository to work for another Repository.
- Synchronization remains a separate future capability with explicit commands
  and semantics.
