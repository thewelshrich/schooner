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

Without a selector, `resume` first chooses the newest managed live Session
associated with the matching Repository, ordered by tmux activity, creation
time, and tmux ID. If none matches, it chooses the newest managed live Session
on the selected Box. Unmanaged or uncertain Sessions always require an
explicit selection.

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
- Synchronization remains a separate future capability with explicit commands
  and semantics.
