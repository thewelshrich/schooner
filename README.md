# Schooner

Schooner is an open-source, CLI-first application for creating and operating
persistent, user-owned development machines.

It adopts existing machines through OpenSSH or provisions them through a
supported provider, then applies the same box, project, worktree, and session
model regardless of acquisition path. Live state remains on the machine, and
users retain independent SSH access.

This repository is currently establishing the V1 implementation foundation.

## Authoritative documents

- [Product direction](DIRECTION.md)
- [Domain language and lifecycle rules](DOMAIN.md)
- [Architecture and concrete stack](ARCHITECTURE.md)
- [Contributor patterns](CONTRIBUTING.md)
- [Architecture decisions](docs/adr/)

The first implementation milestone is the adopted-box path: `box add`,
`box status`, and `box remove`. It supports Ubuntu 24.04 and 26.04 on amd64 and
arm64. See [DIRECTION.md](DIRECTION.md) for the wider V1 scope and exclusions.

## Adopt an existing machine

Run the guided, sequential form in a terminal:

```bash
schooner box add
```

Or provide the complete input for scripts and CI:

```bash
schooner box add work-api --ssh work-api --yes
schooner box status work-api
schooner box remove work-api --yes
```

The SSH destination may be an alias from `~/.ssh/config` or a normal
`user@host` destination. Schooner uses the system OpenSSH client and its
existing identities, agents, proxies, and `known_hosts`. For unattended first
contact, `--accept-new-host-key` explicitly enables OpenSSH's `accept-new`
policy; it never accepts a changed key.

`box add` verifies the remote system, establishes a stable machine identity,
installs missing Git and tmux packages when `sudo -n` is available, and creates
the project root (default `~/schooner`). `box remove` only forgets local
inventory and never changes the remote machine.

All box commands support `--output json`. Global interaction controls include
`--no-input`, `--accessible`, `--color auto|always|never`, and
`--theme auto|light|dark`. The automatic theme uses the terminal's own
foreground and ANSI palette, making it independent of background detection;
the explicit modes apply Schooner's exact light or dark palette. `NO_COLOR` is
also respected.

## Core constraints

- Use the user's system OpenSSH client.
- Keep remote machine state authoritative.
- Preserve independent SSH, Git, tmux, terminal, and editor use.
- Never expose generic remote command execution.
- Never let local removal destroy infrastructure.
- Add remote helpers or daemons only when demonstrated requirements justify
  their lifecycle and security cost.

## Development

Schooner requires Go 1.27.0. The Go command can download the pinned toolchain
automatically when `GOTOOLCHAIN=auto` is enabled.

Build and verify the CLI directly with the Go toolchain:

```bash
go build ./cmd/schooner
go test ./...
go vet ./...
```

The initial scaffold intentionally has no task runner or release automation.
