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
commands. `pull` applies the same contract as `push` in the opposite direction. The
command's source is authoritative, but Schooner mutates the destination only
after proving that it contains no work unique to that destination. Conflicts
stop without automatic merging, last-writer-wins, timestamp authority, or a
force mode.

A Local Link stores only routing from a canonical local Worktree through a
stable Box record to one exact canonical remote Worktree. It stores no file
manifest, shared snapshot, merge base, or synchronization history.
The implemented commands create or refresh this link only after a successful,
non-dry-run transfer, including an already-identical destination.

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
lock while it revalidates and applies a prepared payload. Pull inspection is a
bounded, digest-pinned paged operation. Pull capture emits one validated JSON
header followed by exactly the declared payload bytes; retries recapture the
current remote state under a new operation ID rather than resuming old data.

Both directions apply to an existing destination through one repository-owned
transaction. The transaction locks and revalidates the destination, captures
rollback state, revalidates again, preflights filesystem and branch topology,
applies the payload, and verifies the result. A post-mutation failure restores
the captured destination. If restoration cannot be proven, the result is
`outcome_unknown`. Push alone retains the narrow branch-rewind exception for a
checkout created by that same first-push operation; pull never enables it.

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

The first slice implemented Local Links and `push`, including first-push clone
or direct Repository creation, remote safety checks, streaming upload, apply,
verification, dry-run, and contextual start/resume routing.

The second slice implements `pull`: remote bounded inspection and fixed-stream
capture, local destination dirtiness and ancestry protection, shared local
mutation locking and rollback, local apply and verification, dry-run,
human/JSON presentation, Local Link refresh, and direct/SSH conformance. It
does not add reconciliation, automatic merging, remote creation, or `sync`.
