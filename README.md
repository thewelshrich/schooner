<p align="center">
  <img src="docs/assets/schooner-readme-banner.png" alt="A schooner crosses a dark sea beside the words: Your machines. Your tools. Your workflow." width="100%">
</p>

# Schooner

[![CI](https://github.com/thewelshrich/schooner/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/thewelshrich/schooner/actions/workflows/ci.yml)
[![License](https://img.shields.io/github/license/thewelshrich/schooner)](LICENSE)
[![Follow @RichDevLab on X](https://img.shields.io/badge/follow-%40RichDevLab-000000?logo=x&logoColor=white)](https://x.com/RichDevLab)

Schooner is an open-source CLI for creating and operating persistent development
machines with the tools you already use. It connects OpenSSH, Git worktrees,
and tmux sessions into one resumable workflow.

**No account. No hosted control plane. No loss of ordinary SSH access.**

> [!IMPORTANT]
> Schooner is currently a technical preview. The implemented workflows are
> usable, but commands and stored data may change before the first stable
> release.

## Why Schooner?

- **Keep your machines.** Adopt a server you already use or provision a
  DigitalOcean Droplet. Ordinary SSH access always remains available.
- **Keep work running.** Development sessions live in tmux and survive a lost
  connection or closed laptop.
- **Keep Git in charge.** Repositories and worktrees remain normal Git
  repositories rather than becoming records in a hosted service.
- **Keep automation possible.** Interactive flows have deterministic flags and
  structured JSON output.

## Quick start

Start with an Ubuntu machine you can already reach through SSH. An entry from
`~/.ssh/config` works well:

```sshconfig
Host work-api
    HostName 203.0.113.10
    User ubuntu
```

Then adopt the machine and start persistent work:

```bash
# Verify the local machine, then prepare the remote machine.
schooner doctor
schooner box add work-api --ssh work-api

# From a local Git checkout, start persistent work. If the repository is not
# on the box yet, Schooner reviews and offers to clone its network origin.
cd repository
schooner start --box work-api

# Disconnect whenever you like, then return to the same tmux session.
schooner resume --box work-api
```

`box add` verifies the remote system, installs or checks Git and tmux, and
installs the matching Schooner runtime for that SSH user. It does not install a
daemon or prevent you from continuing to use `ssh work-api` directly.

## Installation

### From source during the technical preview

You need:

- macOS 13 or later, or a contemporary Linux distribution;
- amd64 or arm64;
- the system OpenSSH client; and
- Go 1.27 or later.

Install the current development version from a source checkout and prepare its
Linux host runtimes:

```bash
git clone https://github.com/thewelshrich/schooner.git
cd schooner
go install ./cmd/schooner
schooner dev artifacts
schooner version
schooner doctor
```

Make sure Go's binary directory is on your `PATH`. It is normally `~/go/bin`.

Tagged binaries for macOS and Linux are published through
[GitHub Releases](https://github.com/thewelshrich/schooner/releases). A simpler
packaged installation path is planned before the first general release.

## Common workflows

### Adopt and inspect an existing machine

```bash
schooner box add
schooner box use work-api
schooner box list
schooner box status work-api
schooner box ssh work-api
```

The guided `box add` flow is intended for people. The complete non-interactive
form works in scripts and CI:

```bash
schooner box add work-api --ssh work-api --yes
```

For unattended first contact, add `--accept-new-host-key` to use OpenSSH's
`accept-new` policy. Schooner never accepts a changed host key.

### Work with remote Git worktrees

```bash
schooner clone git@github.com:owner/repository.git --box work-api
schooner worktree add repository repository-feature \
  --branch feature --box work-api
schooner worktree list --box work-api
schooner worktree inspect repository-feature --box work-api
schooner worktree remove repository-feature --box work-api
schooner worktree prune --box work-api
```

Schooner first uses the supplied URL and the Box user's existing Git and SSH
configuration. For GitHub it can then try canonical SSH, a connected Box Source
Identity, and anonymous HTTPS. Every Git process is non-interactive. The
credential-free supplied URL remains `remote.origin.url` even when another
transport completes the clone. Schooner does not copy local Git credentials to
a Box or require a repository configuration file.

### Connect a Box to private GitHub repositories

When the Box does not already have suitable GitHub credentials, connect a
dedicated Box-owned SSH identity:

```bash
schooner source connect github --box work-api
schooner source status --box work-api
schooner source disconnect github --box work-api --yes
```

Schooner authorizes one local GitHub account through a GitHub App and device
flow. Interactive connect first explains that the App can add this Box's public
SSH key, not read repositories, and will not copy laptop keys or store a token
on the Box. Tokens remain in the operating-system credential store, while each
Box generates and keeps its own Ed25519 private key. Schooner registers only the
public key, validates GitHub host keys from HTTPS metadata, and uses strict
managed SSH settings. JSON and non-interactive commands never launch
authorization; they return `authentication_required` if no stored credential can
satisfy the request. See the [source access guide](docs/source-access.md) for
permissions, status, recovery, and cleanup behavior.

An interactive `schooner clone` or contextual `schooner start` can offer a
dedicated Box key after a GitHub authentication failure, after explaining that
the Box's own Git and SSH configuration already failed. JSON and non-interactive
invocations never authorize an account or register a key; use explicit
`source connect` as the automation path.

### Start and resume persistent sessions

```bash
# Contextual defaults for everyday use.
cd /path/to/local/repository
schooner start --box work-api
schooner resume --box work-api

# Exact selectors remain available when you need them.
schooner start repository --box work-api
schooner sessions --box work-api
schooner resume repository --box work-api
schooner logs <session-id> --lines 200 --box work-api
schooner stop <session-id> --box work-api
schooner shell repository --box work-api
```

Each worktree has at most one Schooner-managed persistent shell session.
Unmanaged tmux sessions remain visible and independently usable.

Without a selector, `start` matches the current local checkout to a remote
repository by its network origin. It uses the remote primary worktree as-is;
it does not switch that worktree to the local branch. When no match exists,
Schooner can clone the origin's default branch after showing local dirty,
unpushed, detached-HEAD, or missing-upstream warnings. Local files and
unpushed commits are never copied by this flow.

Without a selector, `resume` chooses the most recently active managed live
session for the matching repository. If none matches, it suggests `schooner
start` instead of opening work for an unrelated repository. Outside a local
repository, it chooses the most recent managed live session on the selected
box. Unmanaged or uncertain tmux sessions require an explicit choice. These
decisions use live Git and tmux state; Schooner does not persist a
local-to-remote repository link.

### Provision with DigitalOcean

Schooner can also create and safely reconcile DigitalOcean Droplets:

```bash
schooner provider connect digitalocean personal --default
schooner box add
```

Provisioning creates billable infrastructure. See the
[DigitalOcean guide](docs/digitalocean.md) for credential permissions,
non-interactive use, recovery, and destruction semantics.

## How it works

```text
your terminal
    |
    | system OpenSSH
    v
your Linux machine
    |- Git owns repositories and worktrees
    |- tmux keeps sessions alive
    `- Schooner runs on demand and exits
```

Local inventory remembers how to reach a machine and records Schooner-owned
operational metadata. The machine's filesystem, Git, tmux, and live processes
remain authoritative.

Two safety boundaries are deliberate:

- `schooner box remove` forgets local inventory and never changes the machine.
- `schooner box destroy` is a separate command available only for supported
  provider-created infrastructure.

Source access is managed separately. Disconnect it before removing or
destroying a Box. Those Box commands never call GitHub, and retained local
source metadata permits later revocation by the former Box name.

Schooner does not expose generic remote command execution or a `schooner run`
escape hatch.

## Current scope

The technical preview currently includes:

- adopting existing Ubuntu machines over OpenSSH;
- provisioning and destroying DigitalOcean Droplets;
- remote runtime setup, status, repair, and updates;
- remote repository and Git worktree lifecycle;
- explicit Box-owned access to private GitHub repositories;
- persistent tmux session lifecycle; and
- human-readable and versioned JSON output.

The first general-release direction also includes Hetzner provisioning, local
links and explicit synchronization, optional coding-agent sessions, private
SSH previews, and recovery improvements. These are not all implemented yet.

Supported remote systems are Ubuntu 24.04 and 26.04 on amd64 and arm64.

## Documentation

- [Roadmap](docs/roadmap.md)
- [Domain language and lifecycle rules](docs/domain.md)
- [Architecture](docs/architecture.md)
- [Development guide](docs/development.md)
- [DigitalOcean guide](docs/digitalocean.md)
- [Private GitHub source access](docs/source-access.md)
- [Release process](docs/releasing.md)
- [Architecture decisions](docs/adr/)

Run `schooner --help` or `schooner <command> --help` for the complete command
reference.

## Contributing

Schooner is early, and focused bug reports and design feedback are welcome.
Please read [CONTRIBUTING.md](CONTRIBUTING.md) before opening a pull request.

## License

Schooner is licensed under the [Apache License 2.0](LICENSE).
