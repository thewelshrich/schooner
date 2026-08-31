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

`schooner push` is the first explicit, one-shot directional command. A future
`schooner pull` will apply the same contract in the opposite direction. The
command's source is authoritative, but Schooner mutates the destination only
after proving that it contains no work unique to that destination. Conflicts
stop without automatic merging, last-writer-wins, timestamp authority, or a
force mode.

A Local Link stores only routing from a canonical local Worktree through a
stable Box record to one exact canonical remote Worktree. It stores no file
manifest, shared snapshot, merge base, or synchronization history.
The implemented slice creates or updates this link only after a successful,
non-dry-run `push`.

Checkout state is observed deterministically for one operation. Git objects
reachable from source HEAD and the stage-zero index entries are transferred as
a pack. A validated payload carries branch or detached-HEAD metadata, portable
index entries, indexed paths absent from the working tree, and exact tracked
and untracked non-ignored regular files and symlinks. `.git`, ignored files,
credentials, hooks, stashes, and unrelated refs are excluded.

When first push creates a clone, the clone lifecycle's verified checkout and
an immediate deterministic observation form an operation-local seed. The
remote apply revalidates that exact digest under the Worktree mutation lock.
Only that operation-created seed may have its checked-out branch rewound to
the authoritative local HEAD; this permits a local checkout behind its origin
to complete first push without treating freshly cloned published history as
pre-existing remote work. Existing matching Worktrees never receive this
exception and retain the normal ancestry protection.

Large payloads use fixed, compiled-in streaming commands over the existing
system OpenSSH transport. Control operations remain typed and versioned. The
remote application remains on-demand and holds the existing Worktree mutation
lock while it revalidates and applies a prepared payload.

## Consequences

- Direction is explicit and does not grant permission to destroy unique work.
- Current observations, not persisted history, decide safety and verify the
  result.
- Ignored files remain destination-local and survive transfers unless they
  collide with an incoming synchronizable path.
- Unsupported Git or filesystem states fail explicitly rather than being
  approximated.
- There is no `schooner sync`, Sync Point, watcher, daemon, or background
  reconciliation workflow.

V1 rejects states whose exact behavior is not proven: unborn, shallow,
partial-clone or promisor repositories; replacement refs or grafts; unresolved
or in-progress Git operations; sparse checkout; submodules; non-stage-zero,
intent-to-add, skip-worktree, or assume-unchanged index entries; non-SHA-1
object formats; checkout conversion attributes or non-false `core.autocrlf`;
special filesystem files, unsafe symlink parents, and case-colliding paths; and
an incoming branch already owned by another Worktree. First creation also
requires the Schooner staging directory and Worktree Root to share a
filesystem so final promotion can be atomic.

## Delivery slices

The first slice implements Local Links and `push`, including first-push clone
or direct Repository creation, remote safety checks, streaming upload, apply,
verification, dry-run, and contextual start/resume routing.

A later slice will implement `pull` by mirroring the same contracts: remote
capture and fixed download preparation, local destination dirtiness and
ancestry protection, local mutation locking and revalidation, local apply and
verification, dry-run, human/JSON presentation, and direct/SSH end-to-end
tests. It will not add reconciliation, automatic merging, or `sync`.
