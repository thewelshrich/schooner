# ADR 0012: Directional workspace transfer without synchronization history

- Status: Accepted
- Date: 2026-08-30

## Context

Schooner must move the complete, current state of a Git checkout between a
local machine and a Box. Git remotes do not carry unpushed commits, index
state, working-tree changes, or untracked files. A bidirectional synchronizer
would add historical snapshots, reconciliation, and background lifecycle that
the product does not need.

## Decision

`schooner push` and `schooner pull` are explicit, one-shot directional
commands. The command's source is authoritative. Schooner mutates the
destination only after proving that it contains no work unique to that
destination. Conflicts stop without automatic merging, last-writer-wins, or a
force mode.

A Local Link stores only routing from a canonical local checkout through a
stable Box record to one exact remote Worktree. It stores no file manifest,
shared snapshot, or synchronization history. A successful, non-dry-run
transfer creates or refreshes the link.

Ignored files remain destination-local. Continuous watchers, implicit
background transfer, and bidirectional reconciliation are outside the product.

## Consequences

- Direction is explicit and does not grant permission to destroy unique work.
- Current observations, not persisted history, decide safety and verify the
  result.
- Unsupported Git or filesystem states fail explicitly rather than being
  approximated.
- There is no `schooner sync`, watcher, daemon, or background reconciliation
  workflow.
