# Roadmap

Schooner is an open-source, CLI-first development-machine workflow. This
roadmap describes intended direction, not a compatibility promise.

## Available now

- Adopt existing Ubuntu machines through OpenSSH.
- Provision and destroy DigitalOcean Droplets.
- Install, inspect, repair, and update the on-demand remote runtime.
- Install and upgrade the local CLI through Homebrew or the verified public
  release installer on supported macOS and Linux platforms, with ownership-aware
  local update checks and safe direct-install replacement.
- Discover, create, inspect, remove, and prune remote Git worktrees.
- Clone public and private GitHub repositories with Box-owned SSH identity
  recovery, while retaining explicit source connect and disconnect controls.
- Push the complete current state of a local checkout to a safe remote
  Worktree and persist a lightweight, revalidated Local Link for later
  `push`, `start`, and `resume` routing.
- Pull the complete current state of a remote Worktree into a safe local
  checkout, preserving ignored local files and refreshing the same Local Link.
- Start, list, resume, inspect, and stop tmux-backed sessions.
- Produce human-readable and versioned JSON output.

## Planned next

- Add Hetzner as the second built-in provider.
- Add optional coding-agent sessions.
- Open private development previews through OpenSSH forwarding.
- Complete recovery paths for interrupted setup and provider operations.
- Stabilize documented command behavior, JSON schemas, configuration, and
  local migrations.

## Not planned

- A hosted control plane, accounts, organizations, billing, or licensing.
- A persistent remote daemon.
- Generic remote command execution or `schooner run`.
- Continuous or implicit filesystem synchronization.
- A runtime plugin system or user-defined providers.
- A full-screen TUI.
- Public preview ingress or collaborative sessions.
- Windows client support or arbitrary Linux distribution support.

The durable product principles and technical boundaries behind this scope are
recorded for contributors in the [documentation index](README.md).
