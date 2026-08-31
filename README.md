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
schooner doctor
schooner box add work-api --ssh work-api

cd repository
schooner push --box work-api
schooner start

schooner resume --box work-api
```

`box add` prepares Git, tmux, and the Schooner runtime for that SSH user. It
does not install a daemon. `ssh work-api` continues to work.

`push` is an explicit one-shot workspace transfer, not Git push. It copies the
current checkout to a remote Worktree and remembers that route for later
`push`, `pull`, `start`, and `resume` commands.

## Installation

Schooner supports macOS 13 or later and contemporary Linux distributions on
amd64 and arm64. The system OpenSSH client is required for Box connections.

Homebrew is the recommended path:

```bash
brew install thewelshrich/tap/schooner
schooner version
schooner doctor
```

Update with `brew upgrade thewelshrich/tap/schooner`. `schooner update` prints
that command for a Homebrew install and never replaces the executable beneath
it. `brew uninstall schooner` removes the package; it does not change local
inventory or any remote machine.

Without Homebrew, the public installer selects the matching signed or verified
release:

```bash
curl -fsSL https://raw.githubusercontent.com/thewelshrich/schooner/main/scripts/install.sh | bash
```

The default target is `~/.local/bin/schooner`. The installer does not use
`sudo` or edit shell profiles. Pass `--install-dir` or `--version` after `--`
to choose a directory or pin a release. `schooner update` replaces only a
direct installation it owns. To uninstall that default install:

```bash
rm -- "$HOME/.local/bin/schooner" \
  "$HOME/.local/bin/.schooner-install-receipt.json"
```

Disable the occasional update notice with `SCHOONER_NO_UPDATE_CHECK=1`. Tagged
binaries are also on
[GitHub Releases](https://github.com/thewelshrich/schooner/releases). Source
builds are for contributors; see the [development guide](docs/development.md).

## Common workflows

```bash
schooner box add
schooner box use work-api
schooner box list
schooner box status work-api
schooner box ssh work-api
```

The guided `box add` flow is for people. Scripts can pass the complete
non-interactive form, including `--yes` and `--accept-new-host-key` for
unattended first contact. Schooner never accepts a changed host key.

```bash
schooner clone git@github.com:owner/repository.git --box work-api
schooner worktree add repository repository-feature --branch feature --box work-api
schooner worktree list --box work-api
```

```bash
schooner source connect github --box work-api
schooner source status --box work-api
schooner source disconnect github --box work-api --yes
```

Each connected Box owns its own GitHub SSH key. Schooner does not copy laptop
keys or store a GitHub token on the Box. See the
[source access guide](docs/source-access.md) for permissions, status, and
cleanup.

```bash
cd /path/to/local/repository
schooner start --box work-api
schooner resume --box work-api
schooner pull --box work-api
```

`start` and `resume` pick up the Worktree remembered by a successful `push` or
`pull`. Exact selectors remain available: `schooner start repository`,
`schooner sessions`, `schooner logs`, `schooner stop`, and `schooner shell`.

Provisioning a DigitalOcean Droplet creates billable infrastructure:

```bash
schooner provider connect digitalocean personal --default
schooner box add
```

See the [DigitalOcean guide](docs/digitalocean.md) for credentials, recovery,
and destruction.

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

`schooner box remove` forgets local inventory and never changes the machine.
`schooner box destroy` is a separate command for supported provider-created
infrastructure only. Disconnect GitHub source access before either command.
Schooner does not expose generic remote command execution or a `schooner run`
escape hatch.

Supported remote systems are Ubuntu 24.04 and 26.04 on amd64 and arm64. The
[roadmap](docs/roadmap.md) lists what is available now and what is not planned.

## Documentation

User guides and the contributor map live in the
[documentation index](docs/README.md). Run `schooner --help` or
`schooner <command> --help` for the command reference.

## Contributing

Schooner is early, and focused bug reports and design feedback are welcome.
Please read [CONTRIBUTING.md](CONTRIBUTING.md) and the
[Code of Conduct](CODE_OF_CONDUCT.md) before opening a pull request.

## License

Schooner is licensed under the [Apache License 2.0](LICENSE).
