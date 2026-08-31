# Architecture decisions

These records capture accepted constraints on Schooner. They are for
contributors. User-facing behavior belongs in the [README](../../README.md)
and the [documentation index](../README.md).

Records begin at 0002. Write a new ADR before a consequential change; do not
amend an accepted decision in place.

| ADR | Decision |
| --- | --- |
| [0002](0002-deep-modules-and-compiled-in-extensibility.md) | Deep modules, compiled-in commands and providers, no plugin system |
| [0003](0003-local-state-and-secure-credentials.md) | SQLite inventory, TOML config, OS credential store for secrets |
| [0004](0004-box-acquisition-and-destruction.md) | Adopt or provision into one Box model; remove never destroys |
| [0005](0005-provider-ssh-identity-and-reconciliation.md) | Dedicated provider SSH identity and correlation-based create/destroy |
| [0006](0006-on-demand-remote-application-over-system-ssh.md) | On-demand remote application over system OpenSSH; no daemon |
| [0007](0007-default-box-resolution-and-host-maintenance.md) | Shared Box resolver; explicit setup and update |
| [0008](0008-git-owns-repository-and-worktree-identity.md) | Git and the filesystem own Repository and Worktree identity |
| [0009](0009-contextual-root-work-commands.md) | `start` and `resume` are contextual root commands |
| [0010](0010-box-owned-github-source-identities.md) | Local GitHub Source Account; Box-owned SSH identities |
| [0011](0011-direct-installation-ownership.md) | Direct installer and updater replace only receipted executables |
| [0012](0012-directional-workspace-transfer.md) | Explicit `push` and `pull`; no synchronization history |
