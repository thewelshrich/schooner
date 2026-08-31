# ADR 0009: Contextual root commands for frequent work

- Status: Accepted
- Date: 2026-08-26

## Context

Starting and returning to remote work are Schooner's most frequent actions.
Requiring users to navigate resource-oriented command groups, or silently
persisting relationships inferred from a network-origin match, would make the
common workflow harder and create state that can drift from Git and tmux.

## Decision

`schooner start` and `schooner resume` are root commands with contextual
defaults. Exact Worktree paths and Session IDs remain explicit selectors.

Without a selector, both commands prefer a revalidated Local Link created by a
successful `push` or `pull`. They consume that route; they do not create it,
copy files, or transfer commits. A stale link fails instead of being followed
or replaced.

Without an applicable link, `start` may match the local checkout's
credential-free origin to a live Repository on the selected Box, or offer to
clone that origin after reviewing local-only state. `resume` stays inside the
detected local Repository, or chooses the newest managed live Session on the
Box when there is no local Repository. Unmanaged or uncertain Sessions require
an explicit selection.

JSON and non-interactive invocations never authorize a Source Account or
register a Box key.

## Consequences

- Everyday use is `schooner start` and `schooner resume`.
- Inferred origin matches remain live observations; only an explicit transfer
  persists a Local Link.
- Bare `resume` cannot navigate from one local Repository to work for another.
- Workspace transfer stays on `push` and `pull`. There is no `sync`.
