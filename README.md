<p align="center">
  <img src="docs/assets/schooner-readme-banner.png" alt="A schooner crosses a dark sea beside the words: Your machines. Your tools. Your workflow." width="100%">
</p>

# Schooner

[![Release](https://img.shields.io/github/v/release/thewelshrich/schooner)](https://github.com/thewelshrich/schooner/releases/latest)
[![CI](https://github.com/thewelshrich/schooner/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/thewelshrich/schooner/actions/workflows/ci.yml)
[![License](https://img.shields.io/github/license/thewelshrich/schooner)](LICENSE)

**Persistent remote development, on machines you control.**

Schooner moves the current state of your local Git workspace to a remote Linux
machine, opens a persistent tmux-backed development session, and brings the
result home when you are done — all over ordinary SSH.

**No account. No daemon. No hosted control plane. Ordinary SSH keeps working.**

```console
cd repository

schooner push --box work-api   # put this workspace on the Box
schooner start                 # start persistent remote work
schooner resume                # come back after disconnecting
schooner pull                  # bring the result home
```

The first successful `push` remembers the relationship between the local
checkout, Box, and remote Worktree. After that, commands such as `start`,
`resume`, `push`, and `pull` can use the current repository as context instead
of making you repeatedly specify where the work lives.

## Why Schooner?

SSH gives you a remote shell. Git gives you repositories. tmux gives you
persistent terminal sessions. Schooner connects those primitives into one
coherent development workflow without putting a service between you and your
machine.

- **Work on infrastructure you control.** Adopt a server you already use or
  provision a DigitalOcean Droplet. The machine remains yours and ordinary SSH
  access remains available.
- **Move the workspace, not only the commit history.** `schooner push`
  transfers the current checkout state, including synchronizable uncommitted
  and untracked work, into a safely validated remote Worktree.
- **Leave without stopping.** Sessions live in tmux and survive a lost
  connection, closed terminal, or closed laptop. Return with `schooner resume`.
- **Keep Git in charge.** Repositories and worktrees remain normal Git
  repositories. Schooner does not turn them into records in a hosted service.
- **Make movement explicit.** `push` and `pull` are deliberate one-shot
  transfers. There is no watcher, daemon, or hidden continuous filesystem sync.
- **Automate when you need to.** Interactive workflows also expose
  deterministic flags and versioned JSON output.

## Installation

Schooner supports macOS 13 or later and contemporary Linux distributions on
amd64 and arm64. The system OpenSSH client is required for Box connections.

Install with Homebrew:

```bash
brew install thewelshrich/tap/schooner
```

Or install the matching signed or verified release directly:

```bash
curl -fsSL https://raw.githubusercontent.com/thewelshrich/schooner/main/scripts/install.sh | bash
```

Then check the local environment:

```bash
schooner version
schooner doctor
```

Homebrew installations update with:

```bash
brew upgrade thewelshrich/tap/schooner
```

Direct installations can update themselves with:

```bash
schooner update
```

The direct installer defaults to `~/.local/bin/schooner`, does not use `sudo`,
and does not edit shell profiles. Tagged binaries are also available from
[GitHub Releases](https://github.com/thewelshrich/schooner/releases).

## Quick start

Start with an Ubuntu machine you can already reach through SSH. An entry in
`~/.ssh/config` works well:

```sshconfig
Host work-api
    HostName 203.0.113.10
    User ubuntu
```

Adopt it as a Schooner Box:

```bash
schooner box add work-api --ssh work-api
```

`box add` prepares Git, tmux, and the on-demand Schooner runtime for that SSH
user. It does not install a daemon, and `ssh work-api` continues to work as
normal.

Now move an existing local workspace onto the Box:

```bash
cd /path/to/repository
schooner push --box work-api
schooner start
```

Work normally in the remote terminal. You can disconnect at any time. Later,
from the same local repository:

```bash
schooner resume
```

When you want the remote workspace state back locally:

```bash
schooner pull
```

`push` and `pull` are workspace transfers, not aliases for Git network
operations. Schooner validates both sides before changing them and stops rather
than silently merging, overwriting, or reconciling ambiguous state.

## Bring your own machine — or create one

An existing SSH-accessible Ubuntu machine is enough to use Schooner.

If you want Schooner to provision a development machine for you, connect a
DigitalOcean account and run the same `box add` flow:

```bash
schooner provider connect digitalocean personal --default
schooner box add
```

Provisioning creates billable infrastructure. See the
[DigitalOcean guide](docs/digitalocean.md) for credentials, recovery, removal,
and destruction.

## Private GitHub repositories

A Box can be connected directly to GitHub for private repository access:

```bash
schooner source connect github --box work-api
```

Each connected Box owns its own SSH key. Schooner does not copy your laptop SSH
keys to the server or store a GitHub token on the Box.

Once connected, clone normally through Schooner:

```bash
schooner clone git@github.com:owner/repository.git --box work-api
```

See the [source access guide](docs/source-access.md) for permissions, status,
recovery, and cleanup.

## Worktrees and persistent sessions

Schooner keeps repositories, worktrees, and tmux sessions visible rather than
hiding them behind a proprietary workspace format.

```bash
schooner worktree add repository repository-feature --branch feature --box work-api
schooner worktree list --box work-api

schooner sessions --box work-api
schooner logs --box work-api
schooner stop --box work-api
schooner shell --box work-api
```

Exact selectors remain available when repository context is not enough. Run
`schooner --help` or `schooner <command> --help` for the complete command
reference.

## How it works

```text
your laptop
    |
    | schooner push / pull
    | system OpenSSH
    v
your Linux machine
    |- Git owns repositories and worktrees
    |- tmux keeps development sessions alive
    `- Schooner runs on demand and exits
```

Schooner is deliberately not a cloud IDE, hosted development service, generic
remote command runner, or continuous synchronization system. It is a thin
workflow layer over machines and tools you already control.

`schooner box remove` forgets local inventory and never changes the machine.
`schooner box destroy` is a separate command for supported provider-created
infrastructure only. Schooner does not expose generic remote command execution
or a `schooner run` escape hatch.

Supported remote systems are Ubuntu 24.04 and 26.04 on amd64 and arm64. The
[roadmap](docs/roadmap.md) lists what is available now, what is planned, and
what is intentionally out of scope.

## Documentation

The [documentation index](docs/README.md) contains user guides, architecture,
domain language, contributor documentation, ADRs, and maintainer runbooks.

Useful starting points:

- [DigitalOcean](docs/digitalocean.md)
- [Private GitHub source access](docs/source-access.md)
- [Roadmap](docs/roadmap.md)
- [Support](SUPPORT.md)
- [Security policy](SECURITY.md)

## Contributing

Schooner is early, and focused bug reports and design feedback are welcome.
Please read [CONTRIBUTING.md](CONTRIBUTING.md) and the
[Code of Conduct](CODE_OF_CONDUCT.md) before opening a pull request.

Follow [@RichDevLab](https://x.com/RichDevLab) for project updates.

## License

Schooner is licensed under the [Apache License 2.0](LICENSE).
