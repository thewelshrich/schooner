# ADR 0008: Git owns Repository and Worktree identity

- Status: Accepted
- Date: 2026-08-26

## Context

Schooner needs to discover and operate repositories and worktrees without
creating a second identity or lifecycle system that can drift from Git and the
filesystem.

## Decision

Git's common directory and registered checkout paths are the only Repository
and Worktree identities. The filesystem and live
`git worktree list --porcelain -z` output remain authoritative.

Schooner does not persist alternate IDs, aliases, inventory, ownership flags,
or lifecycle state for repositories and worktrees. It orchestrates fixed Git
operations and may annotate only Schooner-owned Sessions and Operations with
paths that are revalidated before use.

## Consequences

- Remote discovery and ordinary Git commands observe the same state.
- Schooner cannot silently retain a repository or worktree that Git no longer
  recognizes.
- Selection and display must use live paths rather than stable Schooner IDs.
- Session and operation paths must be revalidated before use.
- Dirty-worktree protection remains grounded in Git's current status.
