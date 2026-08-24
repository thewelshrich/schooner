# ADR 0001: Agentless system-SSH runtime

- Status: Accepted
- Date: 2026-08-24

## Context

Schooner must inspect and operate user-owned machines without weakening
independent SSH access or acquiring unnecessary remote lifecycle obligations.
Persistent agents simplify continuous observation and durable work but require
installation, updates, service supervision, compatibility negotiation, and a
larger remote security surface.

V1 has no demonstrated requirement for continuous behavior while the local CLI
is disconnected.

## Decision

V1 uses the user's system OpenSSH client and standard Ubuntu primitives through
a typed runtime interface. The SSH adapter implements bounded product
operations with small embedded scripts and structured results. It exposes no
generic remote execution method.

Schooner installs no remote binary or daemon. Operations are attached,
idempotent, and checkpointed for safe retry.

OpenSSH host keys authenticate hosts. A stable Schooner identifier stored in
the remote user's state directory supports correlation and duplicate detection
but never substitutes for host-key verification.

## Consequences

- SSH config, agents, proxying, and host trust continue to work independently.
- Schooner has no remote update or daemon-supervision lifecycle in V1.
- Shell behavior requires disciplined input separation, versioned result
  schemas, hostile-input testing, and a narrow certified OS matrix.
- An on-demand helper is considered only after repeated evidence that shell
  orchestration cannot safely provide atomicity, cancellation, or inspection.
- A daemon additionally requires continuous or independently durable behavior.
