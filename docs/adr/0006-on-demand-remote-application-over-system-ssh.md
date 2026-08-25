# ADR 0006: On-demand remote application over system OpenSSH

- Status: Accepted
- Date: 2026-08-24

## Context

Schooner needs reliable, versioned behavior for Repositories, remote-first
Worktrees, Sessions, Agents, and Git-aware synchronization. Embedding an
expanding collection of shell programs in the local application would make
atomic behavior, structured inspection, compatibility, and testing harder to
maintain. A persistent daemon would add installation, supervision, availability,
and security obligations that the initial release does not require.

## Decision

Schooner installs the same application for the configured user on each
supported Box. The local application verifies a compatible installation and
invokes a private, typed remote mode on demand through the user's system
OpenSSH client. The remote invocation exits when its bounded operation
completes. There is no persistent remote daemon.

Raw remote command construction remains private to the OpenSSH transport.
Schooner exposes neither a generic runtime method nor a user-facing
`schooner run`. `box ssh` remains a separate interactive OpenSSH handoff.
OpenSSH host keys authenticate the Box; the Schooner Box identity provides
correlation only. tmux supplies persistence for user-visible Sessions and
optional coding Agents.

## Consequences

- Remote installation, compatibility negotiation, and recoverable updates are
  part of Box preparation.
- Local and remote operations share domain behavior and versioned protocols.
- SSH configuration, agents, proxying, host trust, and independent access stay
  under the user's control.
- Operations remain attached, bounded, idempotent, and checkpointed for later
  retry.
- Continuous behavior requires a separate future decision and cannot be added
  by silently turning the remote application into a daemon.
