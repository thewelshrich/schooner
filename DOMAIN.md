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

### Repository

A **Repository** is one Git common object and ref store. Git identifies it by
its canonical common directory; Schooner does not assign it another identity.

### Worktree

A **Worktree** is any checkout belonging to a Repository, including its primary
checkout. Git and the filesystem own its identity and lifecycle.

### Primary worktree

A **Primary worktree** is the normal checkout created with a non-bare clone.

### Linked worktree

A **Linked worktree** is an additional checkout registered with its Repository
through Git's worktree mechanism.

### Worktree Root

The **Worktree Root** is the configured parent directory beneath which Schooner
discovers and creates Worktrees on a Box.

### Local Link

A **Local Link** is an optional relationship between a local checkout and a
remote Worktree.

A local checkout may link to one Worktree, while a Worktree may be linked
from more than one local machine. The relationship lives in Schooner state,
not in a required repository configuration file. An explicit `push` or `pull`
may establish it.

### Session

A **Session** is a persistent tmux session associated with a revalidated
Worktree path.

The Session may outlive the invoking Schooner process and remains independently
usable through tmux. It is not an SSH connection or a hidden background-job
mechanism.

A managed tmux Session is identified by session-scoped
`@schooner_session_schema`, `@schooner_session_id`,
`@schooner_session_kind`, `@schooner_session_created_at`, and
`@schooner_worktree_path` metadata. A Worktree has at most one managed shell
Session; repeated starts reuse it. Worktree removal and Session creation share
the same per-Worktree mutation lock so neither can pass live validation while
the other is committing its effect. An ephemeral Worktree shell holds the same
lock for its lifetime.

New Sessions use metadata schema 2. The original schema 1 shape containing
schema, Session ID, and Worktree path remains readable and operable during
upgrade; new metadata fields are never silently assumed to exist on it.

Unmanaged tmux sessions remain outside Schooner lifecycle ownership. Schooner
may list and resume them by their live `tmux:$N` target, but never captures
their logs or stops them. Pane paths create a Worktree association only when
every pane maps unambiguously to the same live Worktree. Partial or malformed
Schooner metadata is never treated as unmanaged.

### Agent

An **Agent** is an optional coding-agent process occupying a Session.

A Session can exist without an Agent, and an Agent ending does not end the
Session or Worktree. Schooner's on-demand remote application is never called
an Agent.

### Sync Point

A **Sync Point** is the last verified shared state of a Local Link and remote
Worktree.

It is evidence for later comparison, not authority over either checkout and
not a promise that the two sides have remained unchanged.

## Synchronization language

- **Push** synchronizes from a local checkout to its remote Worktree.
- **Pull** synchronizes from a remote Worktree to its local checkout.
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
| Repository and Worktree identity and lifecycle | Git and the filesystem on the Box |
| Sessions, Agents, and capabilities | Live Box state |
| Local checkout contents | Local filesystem and Git repository |
| Local Links and Sync Points | Local Schooner state plus verified observations of both sides |
| Provider resource existence | Provider |
| Provider secret value | Environment or operating-system credential store |
| Operation recovery metadata | Local Schooner state plus verified external observations |
