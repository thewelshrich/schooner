# Roadmap

Schooner is working toward a coherent first general release of its open-source,
CLI-first development-machine workflow. This roadmap describes intended scope,
not a compatibility promise.

## Available in the technical preview

- Adopt existing Ubuntu machines through OpenSSH.
- Provision and destroy DigitalOcean Droplets.
- Install, inspect, repair, and update the on-demand remote runtime.
- Discover, create, inspect, remove, and prune remote Git worktrees.
- Clone public and private GitHub repositories with Box-owned SSH identity
  recovery, while retaining explicit source connect and disconnect controls.
- Start, list, resume, inspect, and stop tmux-backed sessions.
- Produce human-readable and versioned JSON output.

## Before the first general release

- Package straightforward installation and upgrades for supported local
  platforms.
- Add Hetzner as the second built-in provider.
- Add explicit local links and one-shot `push`, `pull`, and `sync` workflows.
- Add optional coding-agent sessions.
- Open private development previews through OpenSSH forwarding.
- Complete recovery paths for interrupted setup and provider operations.
- Stabilize documented command behavior, JSON schemas, configuration, and
  local migrations.

## Not planned for the initial release

- A hosted control plane, accounts, organizations, billing, or licensing.
- A persistent remote daemon.
- Generic remote command execution or `schooner run`.
- Continuous or implicit filesystem synchronization.
- A runtime plugin system or user-defined providers.
- A full-screen TUI.
- Public preview ingress or collaborative sessions.
- Windows client support or arbitrary Linux distribution support.

The durable product principles and technical boundaries behind this scope are
documented in the [domain model](domain.md), [architecture](architecture.md),
and [architecture decisions](adr/).
