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
`box list`, `box status`, and `box remove`. It supports Ubuntu 24.04 and 26.04 on amd64 and
arm64. See [DIRECTION.md](DIRECTION.md) for the wider V1 scope and exclusions.

## Adopt an existing machine

Run the guided, sequential form in a terminal:

```bash
schooner box add
```

Or provide the complete input for scripts and CI:

```bash
schooner box add work-api --ssh work-api --yes
schooner box list
schooner box status work-api
schooner box ssh work-api
schooner box remove work-api --yes
```

`box list` shows what Schooner already knows locally for SSH-adopted and
provider-created boxes alike: provider, region when recorded, last-known
reachability, and the time of the latest successful observation. It does not
probe machines; use `box status` for a live check.
The SSH destination may be an alias from `~/.ssh/config` or a normal
`user@host` destination. Schooner uses the system OpenSSH client and its
existing identities, agents, proxies, and `known_hosts`. For unattended first
contact, `--accept-new-host-key` explicitly enables OpenSSH's `accept-new`
policy; it never accepts a changed key.

`box ssh` hands the current terminal directly to the system OpenSSH client and
opens the remote user's normal login shell. It uses the connection details
recorded during `box add`, without launching another terminal or changing to
the project root. Supplying `--no-input` with a box name enables OpenSSH batch
mode, so authentication and host-trust prompts fail instead of blocking.

`box add` verifies the remote system, establishes a stable machine identity,
installs missing Git and tmux packages when `sudo -n` is available, and creates
the project root (default `~/schooner`). `box remove` only forgets local
inventory and never changes the remote machine.

Box commands that return structured results support `--output json`; the
interactive `box ssh` session supports human output only. Global interaction
controls include `--no-input`, `--accessible`, `--color auto|always|never`, and
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

## DigitalOcean

Connect a named DigitalOcean credential profile with an interactively entered
Personal Access Token:

```bash
schooner provider connect digitalocean personal --default
schooner provider list
```

For CI or another non-interactive environment, export
`DIGITALOCEAN_TOKEN`; Schooner never accepts the token as a command-line flag
and never saves an environment-provided token implicitly. A Full Access token
is the simplest option. Custom-scoped tokens need account and catalogue reads,
Droplet create/read/delete, SSH-key create/read/delete, VPC read, and tag create
permissions.

Run the guided add flow and choose DigitalOcean, or provide the complete
billable configuration explicitly:

```bash
schooner box add

schooner box add work-cloud \
  --provider digitalocean \
  --profile personal \
  --region fra1 \
  --size s-1vcpu-1gb \
  --image ubuntu-24-04-x64 \
  --yes \
  --accept-new-host-key
```

Schooner generates a dedicated local Ed25519 identity for provider-created
boxes. The guided flow separately offers public keys discovered beneath
`~/.ssh` and keys already registered with the DigitalOcean account. Selected
local public keys are registered only long enough for Droplet creation; private
keys are never read or uploaded. Creation is correlated and recoverable, so
retrying the same interrupted `box add` does not blindly create another
Droplet. Supplying only the previous uncompleted name resumes the recorded
selections:

```bash
schooner box add work-cloud
schooner box add work-cloud --yes --accept-new-host-key
schooner box ssh work-cloud
```

Local removal and infrastructure destruction remain deliberately separate:

```bash
schooner box remove work-cloud --yes   # local inventory only
schooner box destroy work-cloud --yes  # verified permanent Droplet deletion
```

Disconnecting a profile removes its locally stored secret but retains safe
metadata so referenced boxes can be reconnected without changing accounts:

```bash
schooner provider disconnect digitalocean/personal --yes
```

For local development, the active SQLite inventory can be destroyed without
opening or migrating it first:

```bash
schooner db destroy --yes
```

This forgets all locally recorded boxes, profiles, and recovery operations. It
does not delete DigitalOcean resources, revoke credentials, remove OS-keyring
entries, delete migration backups, or remove Schooner's SSH identity.
