# Schooner Direction

## Status

This document defines the initial product direction for the CLI-first Schooner
repository. When historical Schooner material conflicts with this document,
this repository wins.

## Product

Schooner is an open-source command-line application for creating and operating
persistent, user-owned development machines.

A Schooner box is a compatible Linux machine that the user controls and can
access independently through OpenSSH. A box may be adopted through an existing
SSH destination or provisioned through a supported provider. Acquisition only
determines which infrastructure lifecycle operations are available. All boxes
otherwise converge on the same identity, inspection, preparation, project,
session, and recovery model.

Schooner is not a hosted control plane and does not replace SSH, Git, terminals,
editors, coding agents, source hosts, or cloud providers. Live machine state is
authoritative on the machine. Local inventory is an aid to access and recovery,
not a competing source of truth.

## Product principles

1. **Existing machines are first-class.** Adoption through OpenSSH is the
   foundational path, not an import or compatibility route.
2. **Acquisition is separate from operation.** Provider adapters obtain or
   destroy infrastructure; they do not define projects, sessions, or box
   behavior.
3. **Independent access is preserved.** Schooner uses the user's system
   OpenSSH client and never makes ordinary SSH access depend on Schooner.
4. **Remote authority stays remote.** Projects, worktrees, sessions, installed
   tools, and capabilities are observed from the box.
5. **Destruction is explicit.** Removing local inventory never destroys a
   machine. Infrastructure destruction is a distinct, capability-gated action.
6. **No generic remote execution.** Product behavior is expressed as typed,
   bounded operations. Schooner does not expose a user-facing remote `run`.
7. **Agentless until proven otherwise.** V1 uses OpenSSH and standard Linux
   primitives. An on-demand helper or daemon requires evidence that the simpler
   model cannot meet a concrete requirement.
8. **Automation and interaction are peers.** Every interactive flow has a
   deterministic non-interactive form. Structured output is a supported
   contract from the beginning.
9. **Extensibility is for contributors.** V1 has no runtime plugin system.
   New built-in commands, providers, and domain behavior follow explicit
   repository patterns and are compiled into Schooner.

## V1

V1 is a coherent vertical product, not every feature Schooner may eventually
support.

It includes:

- adopting an existing OpenSSH destination;
- provisioning through DigitalOcean and Hetzner;
- explicit SSH host trust and changed-key failure behavior;
- establishing and verifying a stable box identity;
- inspecting and preparing supported Ubuntu machines;
- installing or verifying Git and tmux;
- discovering and cloning Git projects;
- discovering Git worktrees;
- discovering, starting, and resuming tmux-backed sessions;
- forgetting a box without affecting the machine;
- destroying only provider-created infrastructure;
- named, securely stored provider credential profiles;
- recoverable setup and provisioning operations;
- human-readable and versioned JSON output.

V1 clone operations use Git as the configured remote SSH user and therefore
use that user's existing Git credential and SSH setup. Schooner does not store,
broker, or manufacture source-host credentials in V1.

The certified V1 matrix is:

| Concern | Support |
| --- | --- |
| Local operating systems | macOS 13+ and contemporary Linux |
| Remote operating systems | Current Ubuntu LTS releases |
| Architectures | amd64 and arm64 |
| SSH | User's system OpenSSH client |
| Providers | DigitalOcean and Hetzner |
| Required remote tools | Git and tmux |

## Explicit V1 exclusions

V1 does not include:

- a persistent remote agent or daemon;
- an on-demand remote binary helper;
- a runtime plugin system;
- a full-screen TUI;
- generic remote command execution;
- local/remote filesystem synchronization;
- wrappers around Git push or pull;
- public preview ingress;
- collaborative or shared sessions;
- a hosted control plane, accounts, organizations, billing, or licensing;
- Windows client support;
- arbitrary Linux distribution support;
- a generalized package catalogue;
- user-defined providers or package definitions.

Private SSH port forwarding may be added after the core V1 if it remains a thin
use of OpenSSH. It must not delay the release or introduce hosted authority.

## Implementation sequence

The first milestone proves the architecture without creating cloud resources:

```text
box add
  -> resolve an OpenSSH destination
  -> verify host identity
  -> inspect Ubuntu capabilities
  -> establish stable box identity
  -> persist local inventory
  -> render a human or JSON result

box status
  -> reconnect
  -> verify box identity
  -> report live capabilities

box remove
  -> remove local inventory only
```

After this path is complete:

1. DigitalOcean proves provider acquisition and destruction.
2. Hetzner proves that the provider seam is deep rather than provider-shaped.
3. Project discovery and cloning prove filesystem and Git observation.
4. Worktree and tmux discovery prove persistent development resumption.
5. Additional V1 behavior is added only through these established modules.
