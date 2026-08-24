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
`box status`, and `box remove`. See [DIRECTION.md](DIRECTION.md) for its precise
scope and the V1 exclusions.

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
