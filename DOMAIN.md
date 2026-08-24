# Schooner Domain

## Purpose

This document defines Schooner's ubiquitous language and lifecycle invariants.
Names in commands, code, persistence, documentation, and tests should follow
these definitions.

## Box

A **Box** is a compatible Linux machine the user controls and can access through
OpenSSH.

A local box record contains:

- a local Schooner ID and name;
- an OpenSSH destination, such as an SSH config alias or `user@host`;
- the stable identity established on the remote machine;
- acquisition information;
- optional provider resource and credential-profile references;
- cached observations and operation recovery state.

An SSH alias, address, hostname, or username is a connection locator, not box
identity. Bootstrap establishes a persistent random identity on the machine so
that address changes and multiple SSH aliases do not create false machines.
This identifier is for correlation and duplicate detection; it is not proof of
host authenticity. OpenSSH host-key verification remains the authentication
mechanism. A copied machine image containing the same identifier is an identity
conflict that requires explicit recovery, never silent reassignment.

### Acquisition

**Acquisition** is how Schooner obtains access to a box:

- **Adopted**: the machine already exists and Schooner is given an OpenSSH
  destination.
- **Provisioned**: a provider adapter creates the infrastructure and returns a
  provider resource reference plus connection information.

Acquisition does not change how Schooner operates the resulting box.

### Infrastructure lifecycle capability

Infrastructure ownership is not represented by a `managed` boolean. A box
instead records its acquisition and available lifecycle capability.

- An adopted box can be inspected and configured but cannot be destroyed by
  Schooner.
- A provisioned box may be destroyed only while Schooner can authenticate to
  the recorded provider resource and verify the target.
- Losing provider credentials does not turn destruction into a local action;
  it makes destruction unavailable until authorization is restored.

### Remove and destroy

These verbs are permanent product invariants:

- **Remove** forgets the box from the local Schooner inventory. It never
  changes or destroys the remote machine.
- **Destroy** asks the provider to permanently destroy recorded infrastructure.
  It is available only for a verified provider-created resource and requires
  explicit confirmation.

Provider destruction happens before final local removal. Recovery records must
distinguish a failed request, an outcome-unknown request, and confirmed
destruction so a retry cannot accidentally target a different resource.

## Project

A **Project** is the identity of a Git repository present on a box. It captures
repository identity and groups its worktrees; it is not merely a directory
name.

Schooner may clone a project or discover one that already exists. The box
filesystem remains authoritative.

V1 invokes Git as the configured SSH user and relies on that user's existing
Git credential and SSH configuration. Source-host authentication is not a
Schooner credential profile.

## Worktree

A **Worktree** is one concrete Git checkout path belonging to a project. It has
observed branch, head, and working-tree state. A normal clone begins with one
worktree; additional Git worktrees are discovered as siblings under the same
project.

Schooner does not use **Workspace** as a persisted V1 domain object. The term is
too easily confused with a repository, checkout, directory, or session. It may
be introduced later only if a distinct concept earns the name.

## Session

A **Session** is a persistent tmux session associated with a worktree. Schooner
can discover, start, attach to, and resume sessions. tmux remains independently
usable; Schooner does not replace it or use hidden tmux sessions as a background
job system.

## Credential profile

A **Credential Profile** is a named reference to provider credentials, scoped
by provider and account or project, for example:

```text
digitalocean/personal
digitalocean/acme
hetzner/side-project
```

The secret value lives in an operating-system credential store or the current
process environment. SQLite and TOML store only the profile reference and safe
display metadata. A box may refer to the credential profile used to provision
it so later inspection or destruction can resolve current credentials.

A credential profile is bound to the provider's stable external account or
project identity when that provider exposes one. Reconnecting a profile with a
credential for a different account is a conflict, not credential rotation.
For DigitalOcean, the credential is called a **Personal Access Token**, not an
API key, and the stable account identity is the team UUID.

## Provider resource reference

A **Provider Resource Reference** identifies infrastructure acquired for one
box. It contains the provider, the provider's opaque resource identifier, the
credential-profile reference, and Schooner's acquisition correlation identity.

It is durable recovery and verification data, not authorization. A resource
may be destroyed only after current provider credentials resolve the reference
and the provider still reports the expected correlation identity. A missing or
ambiguous correlation is a conflict that requires explicit recovery.

## Capability

A **Capability** is an observed fact about what a box can support, such as its
operating system, architecture, privilege path, Git availability, or tmux
availability. Absence of evidence is not readiness.

Compatibility is decided from explicit capabilities and a certified support
matrix, not by loosely matching a distribution name.

## Operation

An **Operation** is a user-initiated mutation that may cross a local, SSH, or
provider failure boundary. Operations that can outlive one atomic local write
record enough intent, remote/provider identity, progress, and outcome to make a
retry safe.

An operation may finish as:

- succeeded;
- failed before an external effect;
- failed after a confirmed external effect;
- outcome unknown.

V1 has no background worker. Operations remain attached to the invoking CLI,
use idempotent steps and checkpoints, and resume through a later invocation.

## Authority

| Concern | Authority |
| --- | --- |
| Box connection inventory and preferences | Local Schooner state |
| SSH authentication and host trust | User's OpenSSH environment and explicit approval |
| Remote box identity | Identity stored on the box |
| Projects, worktrees, sessions, and capabilities | Live box state |
| Provider resource existence | Provider |
| Provider secret value | Environment or operating-system credential store |
| Operation recovery metadata | Local Schooner state plus verified external observations |
