# Schooner Domain

## Purpose

This document defines Schooner's ubiquitous language and lifecycle invariants.
Names in commands, code, persistence, documentation, and tests follow these
definitions.

## Core language

### Box

A **Box** is a remote Linux machine controlled by the user.

An SSH alias, hostname, address, or username is a connection locator, not Box
identity. Schooner correlates a local Box record with a stable identity on the
machine; OpenSSH host-key verification remains the authentication mechanism.

### Project

A **Project** is the identity and shared Git object store of a repository on a
Box.

A Project may have multiple Workspaces. It is not a checkout, directory name,
local repository, source-host account, or provider project.

### Workspace

A **Workspace** is one concrete remote checkout or Git worktree belonging to a
Project.

A Workspace is first-class and remote-first. It may exist without a local
checkout, Local Link, Session, or Agent. **Worktree** is Git implementation
language; **Workspace** is the Schooner domain term.

### Local Link

A **Local Link** is an optional relationship between a local checkout and a
remote Workspace.

A local checkout may link to one Workspace, while a Workspace may be linked
from more than one local machine. The relationship lives in Schooner state,
not in a required repository configuration file. An explicit `push` or `pull`
may establish it.

### Session

A **Session** is a persistent tmux session associated with a Workspace.

The Session may outlive the invoking Schooner process and remains independently
usable through tmux. It is not an SSH connection or a hidden background-job
mechanism.

### Agent

An **Agent** is an optional coding-agent process occupying a Session.

A Session can exist without an Agent, and an Agent ending does not end the
Session or Workspace. Schooner's on-demand remote application is never called
an Agent.

### Sync Point

A **Sync Point** is the last verified shared state of a Local Link and remote
Workspace.

It is evidence for later comparison, not authority over either checkout and
not a promise that the two sides have remained unchanged.

## Synchronization language

- **Push** synchronizes from a local checkout to its remote Workspace.
- **Pull** synchronizes from a remote Workspace to its local checkout.
- **Sync** compares both sides with their Sync Point and reconciles only a
  safely determined result.

Push, Pull, and Sync are explicit, one-shot, Git-aware operations. They update
the Sync Point only after verifying the shared result. A conflict is reported
for explicit resolution; Schooner never silently chooses one side or runs a
continuous synchronizer.

## Supporting language

### Acquisition

**Acquisition** is how Schooner obtains access to a Box:

- **Adopted**: the machine already exists and Schooner is given an OpenSSH
  destination.
- **Provisioned**: a provider adapter creates the infrastructure and returns a
  provider resource reference plus connection information.

Acquisition does not change how Schooner operates the resulting Box.

### Remove and destroy

- **Remove** forgets the Box from local Schooner inventory. It never changes or
  destroys the remote machine.
- **Destroy** asks the provider to permanently destroy recorded infrastructure.
  It is available only for a verified provider-created resource and requires
  explicit confirmation.

An adopted Box cannot be destroyed by Schooner. Losing provider authorization
makes destruction unavailable; it does not turn destruction into a local
operation.

### Credential Profile

A **Credential Profile** is a named reference to provider credentials, scoped
by provider and external account or provider project. Secret values live in an
operating-system credential store or the current process environment; local
configuration and inventory store only references and safe metadata.

Provider credential profiles are not source-host credentials. Git uses the
configured local or remote user's existing credentials and SSH setup.

### Provider Resource Reference

A **Provider Resource Reference** identifies infrastructure acquired for one
Box. It contains the provider, opaque resource identifier, Credential Profile
reference, and Schooner acquisition correlation identity.

It is durable verification and recovery data, not authorization.

### Capability

A **Capability** is an observed fact about what a Box can support, such as its
operating system, architecture, privilege path, or availability of Schooner,
Git, and tmux. Absence of evidence is not readiness.

### Operation

An **Operation** is a user-initiated mutation that may cross a local, SSH, or
provider failure boundary. It records enough intent, identity, progress, and
outcome to make retry safe.

An Operation may finish as succeeded, failed before an external effect, failed
after a confirmed external effect, or outcome unknown. The initial release has
no background worker; later CLI invocations resume checkpointed Operations.

## Authority

| Concern | Authority |
| --- | --- |
| Box connection inventory and preferences | Local Schooner state |
| SSH authentication and host trust | User's OpenSSH environment and explicit approval |
| Remote Box identity | Identity stored on the Box |
| Projects, Workspaces, Sessions, Agents, and capabilities | Live Box state |
| Local checkout contents | Local filesystem and Git repository |
| Local Links and Sync Points | Local Schooner state plus verified observations of both sides |
| Provider resource existence | Provider |
| Provider secret value | Environment or operating-system credential store |
| Operation recovery metadata | Local Schooner state plus verified external observations |
