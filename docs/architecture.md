# Schooner Architecture

## Architectural style

Schooner is one Go application that runs locally and is installed on supported
remote Boxes. The local application provides the user-facing CLI and invokes
the remote application on demand through OpenSSH. The remote invocation exits
when its bounded operation completes; the initial release has no persistent
Schooner daemon.

The application is composed explicitly from deep domain modules and
infrastructure adapters. A **module** presents one **interface** to its callers.
The interface includes types, invariants, ordering constraints, errors,
configuration, and operational behavior. A module is **deep** when callers gain
substantial behavior through a small interface. A **seam** is a place where
behavior can vary; an **adapter** satisfies an interface at that seam.

Schooner does not create an interface for every implementation. A seam must be
justified by multiple production adapters, a meaningful test adapter, or a
known architectural substitution. Concrete types are the default inside a
module.

## Dependency direction

```text
Local machine                                Remote Box

+-------------------+                       +-----------------------+
| CLI and prompts   |                       | Schooner remote mode  |
+---------+---------+                       +-----------+-----------+
          |                                             |
+---------v---------------------------------------------v-----------+
| Deep domain modules and use cases                                |
| Box, Repository, Worktree, Local Link, Session, Agent            |
+----+-----------------+------------------+-------------------------+
     |                 |                  |
+----v-----+     +-----v-----+      +-----v-----------------+
| OpenSSH  |     | Providers |      | Local/remote storage  |
| adapter  |     | adapters  |      | Git and tmux adapters |
+----------+     +-----------+      +-----------------------+
```

Domain modules do not import Cobra, Huh, SQL drivers, provider SDKs, terminal
rendering, or operating-system process packages. The executable's composition
root creates concrete adapters, constructs modules, and registers local and
remote entry points. There is no dependency-injection framework, service
locator, or global registry.

## Concrete stack

| Concern | Choice |
| --- | --- |
| Language | Go 1.27, exact patch pinned in development and CI |
| CLI | Cobra |
| Focused prompts | Huh v2 behind a prompt adapter |
| Local database | SQLite through `database/sql` and `ncruces/go-sqlite3/driver` |
| Configuration | Strict typed TOML through `BurntSushi/toml` |
| Logging | Standard library `log/slog` |
| Local process execution | Standard library `os/exec` behind a deep process module |
| Remote transport | User's system `ssh` executable |
| Remote persistence | tmux for user-visible Sessions and their processes |
| Testing | `testing`, `go-cmp`, fuzzing, golden files, and handwritten fakes |
| Credential storage | `zalando/go-keyring` behind a provider-neutral secure-store seam |
| Dependency wiring | Explicit constructors in one composition root |

Dependencies are pinned deliberately. Provider SDKs are selected inside their
adapters and do not escape into domain interfaces.

## Repository shape

The repository uses one Go module and one application entry point:

```text
cmd/schooner/                 local and remote composition roots
internal/cli/                 user-facing Cobra command adapter
internal/ui/prompts/          focused Huh prompt adapter
internal/boxtarget/           Box selection and bound direct/SSH execution
internal/box/                 Box identity, inspection, and lifecycle
internal/acquisition/         adopted and provider-created acquisition
internal/repository/          live Git Repository and Worktree inspection
internal/source/              source identity, authorization, and reconciliation
internal/source/github/       bounded GitHub device-flow and SSH-key API adapter
internal/source/boxgit/       Box-owned key files and strict managed SSH adapter
internal/link/                lightweight local-to-remote Worktree routing
internal/workspacetransfer/   implemented push and directional-transfer contracts
internal/session/             tmux-backed Sessions and optional Agents
internal/runtime/             typed remote-operation contracts
internal/runtime/ssh/         system-OpenSSH transport adapter
internal/artifact/            verified remote-application artifacts and cache
internal/selfupdate/          verified local update ownership and promotion
internal/provider/            provider contracts and catalogue
internal/provider/digitalocean/
internal/provider/hetzner/
internal/inventory/           local persistence interface and behavior
internal/inventory/sqlite/    SQLite adapter and migrations
internal/credentials/         resolution, profiles, redaction, and storage
internal/secretstore/         operating-system credential-store seam
internal/process/             bounded local process execution
internal/output/              human and versioned JSON presentation
internal/config/              typed configuration and precedence
```

This is a map, not a requirement to create empty packages. A package is added
only when it owns behavior. A future implementation may combine closely
coupled concepts behind a deeper module while preserving the domain language
and dependency rules.

## Remote application and OpenSSH transport

Schooner installs the same application on each supported Box for the configured
SSH user. Installation is versioned and recoverable. Box acquisition and
explicit setup repair the installation; explicit update targets the invoking
CLI's verified version. Observational commands such as `box status` report a
missing or incompatible runtime with maintenance guidance and do not mutate the
Box.

Bootstrap is the only shell-based part of the remote-application path. It
creates a unique staging file in `~/.local/bin`, streams the already verified
artifact over OpenSSH, verifies SHA-256 again on the Box, and executes the
staged binary's structured `host hello` handshake. Promotion to
`~/.local/bin/schooner` is a same-directory atomic rename followed by a fresh
handshake. Bootstrap commands invoke `/bin/sh` explicitly rather than relying
on the SSH user's login-shell grammar. Promotion holds a remote install lock and
compares the target with the fingerprint captured around the version check; a
changed target is reassessed before any retry. An interrupted attempt therefore
leaves either the previous runtime or the fully verified replacement at the
recorded path. Once present, product inspection uses fixed, hidden typed
operations rather than shell scripts.

The handshake negotiates a protocol version and sorted capabilities separately
from the Schooner release version. Setup may reuse compatible older or newer
executables. Update replaces a compatible older executable, reuses an equal
version, and retains a compatible newer executable. An incompatible newer
runtime is never silently downgraded; unidentifiable version skew fails with
recovery guidance.

The artifact module resolves a version and supported remote platform to one
verified executable. GitHub Releases is the canonical published source. A
required `SHA256SUMS` manifest supplies runtime integrity; GitHub build
provenance attestations supplement it at publication time. Verified downloads
are cached beneath the operating system's user cache directory at
`schooner/artifacts/<version>`. The `schooner dev artifacts` command prepares
both supported Linux development runtimes as one atomic generation in the
local `dev` artifact cache, or in `SCHOONER_ARTIFACT_DIR` when that override is
set. The override replaces both the release source and cache lookup when an
explicit artifact directory is needed, but it must contain the same binary and
checksum-manifest contract. Concurrent resolutions
may duplicate a download, but temporary files and checksum sidecars are stored
atomically so callers never receive a partially written artifact. Stable
artifact errors distinguish invalid versions, unsupported platforms,
unavailability, invalid manifests, checksum mismatches, and cache failures.

The local application invokes a private, machine-oriented remote mode at a
known Schooner-owned path through the user's system OpenSSH executable. The
transport:

- respects SSH config aliases, identities, agents, proxies, and host-trust
  behavior;
- never implements a competing SSH stack or partial SSH-config parser;
- exposes typed product operations to domain callers;
- keeps raw remote command construction private;
- passes structured input separately from the invoked operation;
- exchanges versioned results on stdout and diagnostics on stderr;
- bounds captured output and preserves typed exit information.

There is no exported `Run(command string)` method and no user-facing
`schooner run`. The private remote entry point accepts only registered bounded
operations; user input cannot select an arbitrary executable or shell program.
An explicit `box ssh` remains a separate user-owned interactive handoff to
OpenSSH and accepts no remote command.

Each bounded JSON operation has one compiled-in typed contract in the runtime
module. The contract owns its fixed command and capability, strict request
validation, versioned result envelope, identity correlation, and
operation-specific result invariants. The private host CLI and OpenSSH runtime
remain explicit adapters at that seam. Interactive Worktree shell and Session
resume operations retain their separate terminal-streaming path rather than
being forced through the bounded JSON contract.

Unknown hosts require explicit approval of the presented fingerprint. Known
hosts must match, and changed host keys fail closed with recovery guidance.
Schooner never disables strict host-key checking. The remote Box identifier is
checked after SSH authentication for correlation and duplicate detection; it
does not replace host-key trust.

The remote application runs only for the duration of an invocation. Operations
are idempotent and checkpointed so a later invocation can inspect and resume
after interruption. tmux, not a Schooner daemon, supplies persistence for
Sessions and optional coding Agents.

Remote files and processes belong to the configured SSH user. Schooner keeps
its own state separate from visible Worktrees:

```text
~/.local/bin/schooner          installed on-demand application
~/.local/state/schooner/       Box identity and operation checkpoints
~/.local/state/schooner/source/github.com/
                               Box-owned GitHub key and managed host trust
~/schooner/                    default configurable Worktree root
```

The runtime resolves the remote home directory deliberately rather than
assuming interactive-shell environment variables. Ordinary Repository, Worktree,
Session, Agent, and workspace-transfer operations do not require elevation.
Explicit Box setup may use `sudo` after capability inspection and user consent.
Schooner does not create a dedicated Unix user or install broad sudo rules.

## Repository and Worktree model

A Repository is one Git common object/ref store. A Worktree is any checkout
registered with it: the normal primary checkout or an additional linked
worktree. Canonical Git paths and `git worktree list --porcelain -z` are the
live authority. Schooner does not persist Repository or Worktree IDs, aliases,
inventory rows, ownership flags, or lifecycle state.

The Repository module exposes discovery and targeted inspection while hiding
bounded filesystem traversal, fixed Git invocation, porcelain parsing, origin
sanitization, grouping, and path confinement. Results are observations and may
become stale immediately. A Session or Operation may annotate a canonical
Worktree path, but must revalidate it against live Git state before use.

Clone lifecycle version 2 delegates network transport selection to the Source
module while retaining staging authority in the Repository module. Durable
intent uses normalized Repository Identity plus destination and ref, and saves
the first credential-free supplied origin separately. A source adapter can ask
the lifecycle to reset its operation-owned stage before a candidate, but cannot
remove another path. The host protocol remains version 1 and advertises
`repository.clone.v1` and `repository.clone.v2` independently; SSH callers use
v2 when the live handshake advertises it and otherwise retain v1 compatibility.

## Source access

`internal/source` is the deep module for source-provider authorization and Box
identity lifecycle. Its small interface owns account resolution, token refresh
and rotation, GitHub key reconciliation, local and remote observations,
authority-first disconnect, persisted lifecycle checkpoints, and per-Box
locking. GitHub HTTP behavior and Box filesystem behavior remain private
adapters behind that module.

One local GitHub Source Account is shared across Boxes. Its versioned token
envelope is stored under a source-specific operating-system credential service;
SQLite stores only the opaque reference, GitHub account ID and login, expiry
metadata, and status. If secure storage is unavailable, the token is held only
for the invoking process and a warning is returned. A different GitHub account
cannot replace the Source Account while any Box binding remains.

Every connected Box owns a dedicated Ed25519 key beneath
`~/.local/state/schooner/source/github.com/`. Direct and SSH target adapters
implement the same bounded host operations for inspecting, ensuring, removing,
and verifying that identity. Key generation is staged and atomically promoted;
managed directories are private, the private key is mode `0600`, symlinks are
rejected, and only the public key and fingerprint cross the protocol.

GitHub's HTTPS `/meta` response supplies the managed `known_hosts` data. Each
key is decoded and matched to an advertised SHA-256 fingerprint before the Box
writes a dedicated file. Managed SSH ignores user SSH configuration, uses the
dedicated identity with `BatchMode` and `IdentitiesOnly`, and requires strict
host-key checking. Missing, malformed, or changed trust fails closed.

Key titles are display metadata (`Schooner / <box-name>`); fingerprints establish
identity. A lost create response is reconciled by listing keys and matching the
fingerprint before any retry. Disconnect lists and verifies the recorded
fingerprint, revokes GitHub authority first, and only then removes Box files. A
failed post-revocation Box cleanup succeeds with a security warning and remains
`cleanup_pending` for a later status or disconnect invocation.

For a GitHub Repository Identity, clone transport precedence is the supplied
URL with ambient Box configuration, canonical SSH with ambient configuration,
managed SSH with the Box Source Identity, then anonymous HTTPS with credential
helpers disabled. Only authentication-shaped failures advance. Network,
filesystem, invalid-source, cancellation, host-trust, and integrity failures
stop immediately. Every Git invocation disables terminal and credential-manager
prompts. Managed SSH additionally uses the dedicated key and `known_hosts`, an
empty SSH config, `BatchMode`, `IdentitiesOnly`, and strict host-key checking.

Operation-scoped Git URL rewriting lets a transport candidate perform the
fetch while the first supplied credential-free URL remains
`remote.origin.url`. Interactive clone recovery may connect and verify a new
managed identity before one retry. JSON and non-interactive clone or start
operations never authorize or register source access.

## Local Links and workspace transfer

A Local Link relates one canonical local checkout, one stable Box record, and
one exact canonical remote Worktree. It is lightweight local routing state;
deleting it does not delete or rewrite either checkout. The remote Worktree
remains usable without the link.

Workspace transfer is explicit and attached to the invoking CLI. The current
slice implements `push`; `pull` is planned against the same directional
contract:

```text
push: local checkout  -> remote Worktree
pull: remote Worktree -> local checkout
```

Each directional operation observes current repository, ref, index, and
working-tree state without consulting historical synchronization state. The
source named by the verb is authoritative. Ahead or divergent destination
commits, destination working-tree changes, ignored-path collisions, and
concurrent destination changes return a conflict before application. No
automatic merge or force mode exists.

Git objects and portable stage-zero index entries travel with a deterministic
archive of tracked and untracked non-ignored files. Indexed paths absent from
the working tree are represented explicitly. Fixed typed control operations
and fixed streaming commands run through system OpenSSH. Ignored files remain
destination-local. Continuous watchers, implicit background transfer, and
bidirectional reconciliation are outside the product.

## Commands and interaction

Cobra is used directly by the CLI adapter; Schooner does not wrap it in a
generic command framework. A command constructor receives explicit dependencies
and returns a `*cobra.Command`. Commands resolve input and call domain modules
but contain no business rules.

Input precedence is explicit:

```text
flags > documented environment variables > TOML > defaults
```

Repository, Worktree, and Session commands resolve one immutable Box execution
target through `internal/boxtarget`. The module owns direct-host detection,
inventory-backed Box resolution, direct-versus-SSH adapter binding, Worktree
Root drift checks, and stable error normalization. Command modules provide
domain intent and retain prompting and presentation; they do not construct
remote protocol envelopes or branch on the selected adapter.

Source commands use the same target seam. Interactive device authorization
always presents GitHub's URL and user code and may best-effort open the system
browser without a shell. JSON and non-interactive commands may use or refresh a
stored credential but never prompt, open a browser, or begin authorization.

Box resolution is a shared domain policy rather than command-specific logic:
an explicit Box wins over the current Local Link, configured default, sole
configured Box, and interactive selection, in that order. Ambiguity is an error
when interaction is unavailable. Destructive Box commands require their own
explicit or interactive target and do not consume the configured default.

Interactive prompts occur only when interaction is allowed and relevant
streams are terminals. Non-interactive, JSON, remote-operation, and test paths
never initialize Huh. Every prompt-backed action has a complete flag or input
equivalent. Focused prompts are allowed; a full-screen TUI is not part of the
initial release.

Human results use stdout and progress or diagnostics use stderr. JSON mode uses
dedicated, versioned presentation types; it never serializes domain structs,
database rows, or SDK types directly. Errors are structured on stderr and
paired with a nonzero documented exit status.

Private previews may use explicit OpenSSH forwarding. Public ingress, public
preview URLs, and a Schooner-operated relay or backend are outside the initial
release.

## Errors

Domain errors have stable codes, structured safe context, and wrapped internal
causes. The CLI maps them to human guidance, JSON errors, and a small set of
process exit statuses.

Initial error categories include:

```text
invalid_input
not_found
conflict
box_selection_ambiguous
authentication_required
permission_denied
unsupported
connection_failed
host_identity_changed
host_runtime_missing
host_runtime_incompatible
host_runtime_install_failed
artifact_unavailable
checksum_mismatch
operation_in_progress
outcome_unknown
internal
```

Secrets, device codes after authorization, managed paths, authorization
headers, raw provider responses, and unsafe remote output are redacted before
entering output, error context, or logs. Typed operation errors may include a
bounded stable `reason`, including `credentials_missing`, `github_saml_sso`,
and `host_key_changed`.

## Persistence and authority

SQLite stores Box inventory and its single optional default, Credential Profile
and Source Account references, Box Source Identity fingerprints and GitHub key
correlation metadata, Local Links, cached observations, schema
version, and operation recovery metadata.
It does not become authority for live remote or provider state.

Source lifecycle rows use `connecting`, `connected`, `disconnecting`, and
`cleanup_pending` checkpoints. Tokens, public keys, private keys, and managed
filesystem paths never enter SQLite. Source rows deliberately do not cascade
with Box removal so revocation remains possible after local inventory changes.

The SQLite adapter uses WAL, a bounded busy timeout, short transactions, and
explicit logical ownership for conflicting long mutations. It never holds a
database transaction across SSH or provider network calls.

Migrations are numbered, immutable, forward-only SQL files embedded in the
binary. The migration module records version and checksum, runs one transaction
per migration, rejects altered history, and refuses a database created by a
newer application. Risky migrations require a backup. Shipped commands never
automatically run down-migrations.

This pre-release domain rebaseline is the one deliberate exception: the
unreleased migration history is hard-cut from `workspace_root` to
`worktree_root`, and development databases carrying the earlier checksums must
be reset explicitly by their owners. Schooner never deletes them automatically.
Once this baseline is released, migration immutability applies normally.

Configuration and state use platform conventions:

```text
macOS: ~/Library/Application Support/Schooner/
       ~/Library/Caches/Schooner/

Linux: $XDG_CONFIG_HOME/schooner/
       $XDG_STATE_HOME/schooner/
       $XDG_CACHE_HOME/schooner/
```

Documented XDG fallbacks apply. `SCHOONER_CONFIG` may select the strict host
configuration file containing schema version 1 and one canonical
`worktree_root`. Repositories and Worktrees require no repository-local
Schooner configuration or metadata.

## Credentials

Provider credentials are named profiles. Resolution order is:

```text
documented provider environment variable
-> explicitly selected stored profile
-> configured default stored profile
-> interactive hidden prompt
```

An interactively entered token is validated before Schooner offers to store it.
Environment-provided credentials are never saved implicitly, secrets are never
accepted as ordinary command-line arguments, and plaintext fallback files are
not used.

Provider Credential Profiles and Source Accounts use the same generic
secure-store seam but separate service namespaces and domain lifecycles. A
Source Account token authorizes account lookup and Git SSH-key management only;
it is never copied to a Box or exposed to Git. Box Source Identity private keys
remain Box-local.

## Extensibility and verification

Extensibility means that a contributor has one obvious, verified path for a
change. Registration is explicit in the composition root; there are no
`init()` registries, side-effect imports, reflection-based discovery, runtime
plugins, or user-defined providers in the initial release.

The repository enforces:

- import direction and forbidden framework or SDK imports in domain packages;
- remote-runtime and provider conformance suites;
- direct/SSH source-operation conformance and private-material boundary tests;
- command-tree and external-output golden tests;
- duplicate command, provider, profile, and migration identifiers;
- migration history and checksum integrity;
- fuzz tests for remote input encoding, configuration, identifiers, and JSON
  decoding;
- integration tests using the actual `ssh` process adapter against controlled
  fixtures before cloud tests;
- narrowly scoped live-provider tests that are opt-in and resource-cleaning;
- terminology checks that keep Repository, Worktree, Local Link, Session, and
  Agent aligned with [`domain.md`](domain.md).
