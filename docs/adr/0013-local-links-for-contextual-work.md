# ADR 0013: Explicit transfers create Local Links for contextual work

- Status: Accepted
- Date: 2026-09-01
- Refines: ADR 0009

## Context

ADR 0009 chose live Repository-origin matching for contextual `start` and
`resume` because Schooner had no explicit local-to-remote workspace transfer.
ADR 0012 later introduced `push` and `pull`, whose successful outcome proves an
exact relationship between one canonical local checkout, one Box identity, and
one remote Worktree.

Recomputing that proven route from network origins would discard useful
information. Persisting routes inferred from observation would retain the drift
risk ADR 0009 rejected.

## Decision

A successful, non-dry-run `push` or `pull` creates or refreshes a Local Link.
The link stores routing only: canonical local checkout, stable Box record and
expected Box identity, remote Worktree, and Repository identity. It stores no
file manifest, shared snapshot, or synchronization history.

Contextual `push`, `pull`, `start`, and `resume` prefer an applicable Local Link
after revalidating the local checkout, Box identity, and live remote state. A
stale link fails explicitly rather than being followed or silently replaced.
When no link applies, ADR 0009's live origin-matching behavior remains the
fallback for `start` and `resume`.

Only an explicit successful transfer creates durable routing. Origin matching,
session selection, and contextual clone remain live observations and never
create a Local Link.

## Consequences

- Repeated transfers and session commands can return to the exact Box and
  Worktree selected by the user.
- Persisted routing has explicit provenance and can be revalidated without
  becoming workspace synchronization history.
- Stale or ambiguous state remains visible and recoverable through another
  explicit transfer.
- ADR 0009's prohibition on persisting inferred relationships remains intact;
  its no-Local-Link consequence is refined only for explicit transfers added
  later by ADR 0012.
