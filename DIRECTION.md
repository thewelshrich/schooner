# Schooner Direction

## Status

This document defines the initial product direction for the CLI-first Schooner
repository. When historical Schooner material conflicts with this document,
this repository wins.

## Product

Schooner is an open-source command-line application for creating and operating
persistent, user-owned development machines.

A Schooner Box is a supported Linux machine that the user controls and can
access independently through OpenSSH. A Box may be adopted through an existing
SSH destination or provisioned through a supported provider. Acquisition only
determines which infrastructure lifecycle operations are available. Every Box
otherwise converges on the same preparation, Project, Workspace, Session, and
recovery model.

Schooner is installed on the local machine and on supported remote Boxes. The
local application invokes the remote application on demand through the user's
system OpenSSH client. There is no persistent remote daemon in the initial
release; tmux supplies persistence for development Sessions and their
processes.

Schooner does not replace SSH, Git, tmux, terminals, editors, coding agents,
source hosts, or cloud providers. It has no accounts or Schooner-operated
backend. Live Workspace and Session state is authoritative on the Box. Local
inventory supports access, optional Local Links, synchronization, and recovery
without becoming a hosted control plane.

## Product principles

1. **Workspaces are remote-first.** A Workspace is a first-class remote
   checkout. It does not depend on a local checkout, Local Link, or Agent.
2. **Existing machines are first-class.** Adoption through OpenSSH is the
   foundational path, not an import or compatibility route.
3. **Acquisition is separate from operation.** Provider adapters obtain or
   destroy infrastructure; they do not define Projects, Workspaces, Sessions,
   or Box behavior.
4. **Independent access is preserved.** Schooner uses the user's system
   OpenSSH client and never makes ordinary SSH access depend on Schooner.
5. **Remote authority stays remote.** Projects, Workspaces, Sessions,
   installed tools, and capabilities are observed from the Box.
6. **Synchronization is explicit.** `push`, `pull`, and `sync` are one-shot,
   Git-aware operations. Schooner never runs a continuous file synchronizer or
   silently chooses a winner when states conflict.
7. **Destruction is explicit.** Removing local inventory never destroys a
   machine. Infrastructure destruction is a distinct, capability-gated action.
8. **No generic remote execution.** Product behavior is expressed as typed,
   bounded operations. Schooner does not expose a user-facing `schooner run`.
9. **Automation and interaction are peers.** Every interactive flow has a
   deterministic non-interactive form. Structured output is a supported
   contract from the beginning.
10. **Extensibility is for contributors.** The initial release has no runtime
    plugin system. New built-in commands, providers, and domain behavior follow
    explicit repository patterns and are compiled into Schooner.

## Initial release

The initial release is a coherent vertical product, not every feature Schooner
may eventually support.

It includes:

- adopting an existing OpenSSH destination;
- provisioning through DigitalOcean and Hetzner;
- explicit SSH host trust and changed-key failure behavior;
- establishing and verifying a stable Box identity;
- installing and invoking the same Schooner application on supported Ubuntu
  Boxes;
- installing or verifying Git and tmux;
- discovering and creating Projects and remote-only Workspaces;
- creating a Local Link through an explicit `push` or `pull`;
- explicit, one-shot, Git-aware `push`, `pull`, and `sync` operations;
- discovering, starting, and resuming tmux-backed Sessions;
- optionally starting or resuming a coding Agent in a Session;
- opening private previews through OpenSSH forwarding;
- forgetting a Box without affecting the machine;
- destroying only provider-created infrastructure;
- named, securely stored provider credential profiles;
- recoverable setup and provisioning operations;
- human-readable and versioned JSON output.

Git operations use the configured local or remote user and that user's existing
Git credential and SSH setup. Schooner does not store, broker, copy, or
manufacture source-host credentials in the initial release. A repository does
not need a Schooner configuration file.

The certified initial matrix is:

| Concern | Support |
| --- | --- |
| Local operating systems | macOS 13+ and contemporary Linux |
| Remote operating systems | Current Ubuntu LTS releases |
| Architectures | amd64 and arm64 |
| SSH | User's system OpenSSH client |
| Providers | DigitalOcean and Hetzner |
| Required remote tools | Schooner, Git, and tmux |

## Explicit initial-release exclusions

The initial release does not include:

- a persistent remote daemon;
- a runtime plugin system;
- a full-screen TUI;
- generic remote command execution or a user-facing `schooner run`;
- continuous or implicit filesystem synchronization;
- public preview ingress;
- collaborative or shared Sessions;
- a hosted control plane, accounts, organizations, billing, or licensing;
- Windows client support;
- arbitrary Linux distribution support;
- a generalized package catalogue;
- user-defined providers or package definitions.

## Implementation sequence

The first milestone establishes a trusted Box and installs the remote
application:

```text
box add
  -> resolve an OpenSSH destination
  -> verify host identity
  -> inspect Ubuntu capabilities
  -> establish stable Box identity
  -> install a compatible Schooner application, Git, and tmux
  -> persist local inventory
  -> render a human or JSON result

box status
  -> reconnect
  -> verify Box and remote-application identity
  -> report live capabilities

box remove
  -> remove local inventory only
```

After this path is complete:

1. DigitalOcean proves provider acquisition and destruction.
2. Hetzner proves that the provider seam is deep rather than provider-shaped.
3. Project and remote-only Workspace creation prove the remote-first model.
4. Session and optional Agent resumption prove tmux-backed persistence.
5. Local Links, Sync Points, and explicit push/pull/sync prove safe directional
   synchronization without making local state authoritative.
6. Additional behavior is added only through these established modules.
