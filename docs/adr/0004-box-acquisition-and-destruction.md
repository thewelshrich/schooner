# ADR 0004: Separate box acquisition from box operation

- Status: Accepted
- Date: 2026-08-24

## Context

Schooner can adopt an existing SSH machine or create one through a provider.
Allowing provider concerns to shape Projects, Workspaces, and Sessions would
make adopted machines secondary and duplicate behavior across providers.
Treating all local removal as infrastructure deletion would be unsafe.

## Decision

Adoption and provider provisioning converge on one Box identity, inspection,
preparation, Project, Workspace, and Session model. Provider adapters acquire
and destroy infrastructure only.

`box remove` always changes local inventory only. `box destroy` is distinct and
available only for a verified provider-created resource with current provider
authorization and explicit confirmation.

Infrastructure lifecycle is represented as acquisition information and
capabilities, not a `managed by Schooner` boolean.

## Consequences

- Existing SSH machines are first-class.
- DigitalOcean, Hetzner, and future built-in providers share all post-acquisition
  behavior.
- Destroy recovery must distinguish failure, confirmed destruction, and an
  outcome-unknown provider request.
- Losing provider credentials disables destruction but never changes remove
  semantics.
