<p align="center">
  <img src="docs/assets/schooner-readme-banner.png" alt="A schooner crosses a dark sea beside the words: Your machines. Your tools. Your workflow." width="100%">
</p>

# Schooner

[![CI](https://github.com/thewelshrich/schooner/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/thewelshrich/schooner/actions/workflows/ci.yml)
[![License](https://img.shields.io/github/license/thewelshrich/schooner)](LICENSE)
[![Follow @RichDevLab on X](https://img.shields.io/badge/follow-%40RichDevLab-000000?logo=x&logoColor=white)](https://x.com/RichDevLab)

**Adopt a server. Get a persistent, resumable dev machine. Keep plain SSH
access the whole time.**

Schooner is an open-source CLI that turns a plain Ubuntu box into a
persistent development machine, using the tools you already trust: OpenSSH,
Git worktrees, and tmux. No account, no hosted control plane, no daemon
watching your machine, no loss of ordinary `ssh` access.

Schooner has no opinion about how you work once you're there. No required
TUI, no GUI client, no bundled agent harness, no wrapper you have to run
your coding agent through. It moves your workspace to a real machine and
gets out of the way; run whatever editor, shell, or agent you already use.

<p align="center">
  <img src="docs/assets/demo.gif" alt="Animated terminal walkthrough of schooner box add, schooner push, schooner start, and picking the same session back up over plain SSH, using real Schooner CLI wording." width="720">
</p>

## Contents

- [Why Schooner?](#why-schooner)
- [Quick start](#quick-start)
- [Installation](#installation)
- [Common workflows](#common-workflows)
- [How it works](#how-it-works)
- [Documentation](#documentation)
- [Contributing](#contributing)
- [License](#license)

## Why Schooner?

- **Keep your machines.** Adopt a server you already use or provision a
  DigitalOcean Droplet. Ordinary SSH access always remains available.
- **Keep work running.** Development sessions live in tmux and survive a lost
  connection or closed laptop.
- **Keep Git in charge.** Repositories and worktrees remain normal Git
  repositories rather than becoming records in a hosted service.
- **Keep automation possible.** Interactive flows have deterministic flags and
  structured JSON output.
- **Keep your stack.** Schooner is unopinionated about your editor, shell,
  and coding agent. It moves the workspace; it does not put a TUI, GUI, or
  harness between you and it.

Not sure how that's different from what you already do?

| Instead of&hellip; | Schooner |
| --- | --- |
| Hand-rolled `ssh` + `tmux new -s` aliases | The same OpenSSH and tmux, with one CLI that remembers boxes, worktrees, and sessions for you |
| Coder, Gitpod, or GitHub Codespaces | No hosted control plane, no account, no vendor lock-in &mdash; the machine is still just a machine you can `ssh` into directly |
| A remote-agent product with its own TUI, GUI, or required harness | No required interface at all &mdash; `push`, `start`, and `resume` hand you a plain shell on a real machine, and you run whatever editor or agent you want inside it |
| A daemon or agent running on the box at all times | Schooner runs on demand, over SSH, and exits |

## Quick start

Start with an Ubuntu machine you can already reach through SSH &mdash; any
`user@host` you'd normally pass to `ssh` works:

```bash
schooner doctor
schooner box add work-api --ssh ubuntu@203.0.113.10

cd repository
schooner push
schooner start

schooner resume
```

`box add` is how Schooner meets a machine: it prepares Git, tmux, and the
Schooner runtime for that SSH user and remembers it locally as a **Box**. It
does not install a daemon, and `ssh ubuntu@203.0.113.10` continues to work
exactly as before.

`push` is an explicit one-shot workspace transfer, not Git push. It copies the
current checkout to a checkout on the Box &mdash; Schooner calls that a
**Worktree**, matching Git's own term &mdash; and remembers that route for
later `push`, `pull`, `start`, and `resume` commands.

`start` opens a persistent tmux session on the Box for that Worktree. Close
your laptop, lose your Wi-Fi, whatever &mdash; `resume` reattaches to the same
session exactly where you left it.

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
`sudo`. In a terminal it uses the same heading as the CLI, shows the selected
release and target, then asks for confirmation. If the target directory is not
on `PATH`, it separately offers to add it to the active shell profile. Pass
`--install-dir`, `--version`, or `--yes` after `--` to choose a directory, pin
a release, or run unattended; unattended installs never edit shell profiles:

```bash
curl -fsSL https://raw.githubusercontent.com/thewelshrich/schooner/main/scripts/install.sh | bash -s -- --yes
```

`schooner update` replaces only a direct installation it owns. To uninstall
that default install:

```bash
rm -- "$HOME/.local/bin/schooner" \
  "$HOME/.local/bin/.schooner-install-receipt.json"
```

Disable the occasional update notice with `SCHOONER_NO_UPDATE_CHECK=1`. Tagged
binaries are also on
[GitHub Releases](https://github.com/thewelshrich/schooner/releases). Source
builds are for contributors; see the [development guide](docs/development.md).

## Common workflows

Commands below omit `--box`: Schooner falls back to your configured default
Box, your only Box, or an interactive prompt. Pass `--box <name>` on any of
them to target a specific Box instead.

**Manage Boxes.** The guided `box add` flow is for people; scripts can pass
the complete non-interactive form, including `--yes` and
`--accept-new-host-key` for unattended first contact. Schooner never accepts
a changed host key.

```bash
schooner box add
schooner box use work-api
schooner box list
schooner box status work-api
schooner box ssh work-api
```

**Clone and branch on the Box.** `worktree add` creates an additional
checkout next to the primary one, the same way `git worktree add` would.

```bash
schooner clone git@github.com:owner/repository.git
schooner worktree add repository repository-feature --branch feature
schooner worktree list
```

**Connect a private GitHub repository.** Each connected Box owns its own
GitHub SSH key; Schooner never copies your laptop's keys or stores a GitHub
token on the Box. See the [source access guide](docs/source-access.md) for
permissions, status, and cleanup.

```bash
schooner source connect github
schooner source status
schooner source disconnect github --yes
```

**Move work and pick it back up.** `start` and `resume` pick up the Worktree
remembered by a successful `push` or `pull`. Exact selectors remain
available: `schooner start repository`, `schooner sessions`, `schooner logs`,
`schooner stop`, and `schooner shell`.

```bash
cd /path/to/local/repository
schooner start
schooner resume
schooner pull
```

**Provision a machine instead of adopting one.** This creates billable
infrastructure. See the [DigitalOcean guide](docs/digitalocean.md) for
credentials, recovery, and destruction.

```bash
schooner provider connect digitalocean personal --default
schooner box add
```

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

Three words carry most of the vocabulary above:

- **Box** &mdash; a machine Schooner knows about, identified by its own
  verified identity, not by whichever SSH alias you typed.
- **Worktree** &mdash; a checkout on a Box. Same concept as `git worktree`,
  just tracked by Schooner so `push`, `pull`, `start`, and `resume` know
  which one to use.
- **Session** &mdash; the tmux session `start` opens for a Worktree.
  Ordinary tmux; `resume` just reattaches to it.

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
