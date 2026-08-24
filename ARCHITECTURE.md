# Schooner Architecture

## Architectural style

Schooner is one local Go executable composed explicitly from deep domain
modules and infrastructure adapters.

A **module** presents one **interface** to its callers. The interface includes
types, invariants, ordering constraints, errors, configuration, and operational
behavior. A module is **deep** when callers gain substantial behavior through a
small interface. A **seam** is a place where behavior can vary; an **adapter**
satisfies an interface at that seam.

Schooner does not create an interface for every implementation. A seam must be
justified by multiple production adapters, a meaningful test adapter, or a
known architectural substitution. Concrete types are the default inside a
module.

## Dependency direction

```text
                  +-------------------+
                  | CLI and prompts   |
                  +---------+---------+
                            |
        +-------------------v-------------------+
        | Deep domain modules and use cases    |
        | box, acquisition, project, session   |
        +----+---------------+---------------+-+
             |               |               |
       +-----v-----+   +-----v-----+   +-----v------+
       | OpenSSH   |   | Providers |   | SQLite     |
       | adapter   |   | adapters  |   | adapter    |
       +-----------+   +-----------+   +------------+
```

Domain modules do not import Cobra, Huh, SQL drivers, provider SDKs, terminal
rendering, or operating-system process packages. The executable's composition
root creates concrete adapters, constructs modules, and registers commands.
There is no dependency-injection framework, service locator, or global
registry.

## Concrete stack

| Concern | Choice |
| --- | --- |
| Language | Go 1.27, exact patch pinned in development and CI |
| CLI | Cobra |
| Focused prompts | Huh v2 behind a prompt adapter |
| Local database | SQLite through `database/sql` and `ncruces/go-sqlite3/driver` |
| Configuration | Strict typed TOML through `pelletier/go-toml/v2` |
| Logging | Standard library `log/slog` |
| Local process execution | Standard library `os/exec` behind a deep process module |
| SSH | User's system `ssh` executable |
| Testing | `testing`, `go-cmp`, fuzzing, golden files, and handwritten fakes |
| Credential storage | `zalando/go-keyring` behind a credential-store seam |
| Dependency wiring | Explicit constructors in one composition root |

Dependencies are pinned deliberately. Provider SDKs are selected inside their
adapters and do not escape into domain interfaces.

## Repository shape

The initial repository uses one Go module and one executable entry point:

```text
cmd/schooner/                 composition root
internal/cli/                 Cobra command adapter
internal/ui/prompts/          Huh prompt adapter
internal/box/                 box identity, inspection, and lifecycle
internal/acquisition/         adopted and provider-created acquisition
internal/project/             project and worktree behavior
internal/session/             tmux-backed session behavior
internal/runtime/             typed remote-operation interface
internal/runtime/ssh/         system-OpenSSH adapter and embedded operations
internal/provider/            provider contracts and catalogue
internal/provider/digitalocean/
internal/provider/hetzner/
internal/inventory/           local persistence interface and behavior
internal/inventory/sqlite/    SQLite adapter and migrations
internal/credentials/         resolution, profiles, redaction, and storage
internal/process/             bounded local process execution
internal/output/              human and versioned JSON presentation
internal/config/              typed configuration and precedence
```

This is a map, not a requirement to create empty packages. A package is added
when it owns behavior. Package names may improve while the dependency rules and
domain language remain stable.

## Remote runtime

V1 installs no Schooner executable or daemon on a box. `runtime` is a deliberate
Go interface for bounded remote product operations, not a remote process and
not a generic command runner.

The OpenSSH adapter:

- invokes the user's system OpenSSH executable;
- respects SSH config aliases, identities, agents, proxies, and host-trust
  behavior;
- never implements a competing SSH stack or partial SSH-config parser;
- exposes typed operations to callers;
- keeps raw command execution private;
- embeds small, operation-specific, versioned shell programs;
- passes data separately from program source;
- uses POSIX `sh` for the minimal capability probe and Bash for non-trivial
  operations on the certified Ubuntu target;
- emits versioned JSON results on stdout and diagnostics on stderr;
- bounds captured output and preserves typed exit information.

Unknown hosts require explicit approval of the presented fingerprint. Known
hosts must match, and changed host keys fail closed with recovery guidance.
Schooner never disables strict host-key checking. The remote box identifier is
checked after SSH authentication for correlation and duplicate detection; it
does not replace host-key trust.

There is no exported `Run(command string)` method. User input is never
interpolated into shell program source. Argument encoding and environment
construction are centralized and fuzz-tested with hostile values.

Ordinary operations remain attached to SSH. They are idempotent and
checkpointed so a later invocation can inspect and resume after interruption.
tmux is reserved for user-visible development sessions, not hidden work.

Remote files and sessions belong to the configured SSH user. Schooner keeps its
hidden remote state separate from visible projects:

```text
~/.local/state/schooner/       identity and operation checkpoints
~/.local/share/schooner/       Schooner-owned durable data, if later needed
~/schooner/                    default configurable project root
```

The runtime resolves the remote home directory deliberately rather than
assuming interactive-shell environment variables. The stable box identifier is
stored beneath the state directory. It is a correlation identifier, not an
authentication secret.

Ordinary inspection, project, and session operations do not require elevation.
Explicit setup operations may use `sudo` after capability inspection and user
consent. V1 does not create a dedicated Schooner Unix user, install broad sudo
rules, or execute ordinary work as root.

An on-demand helper requires demonstrated shell brittleness around atomicity,
cancellation, or structured inspection. A daemon additionally requires
continuous or independently durable behavior while no CLI is connected.

## Commands and interaction

Cobra is used directly by the CLI adapter; Schooner does not wrap it in a
generic command framework. A command constructor receives explicit
dependencies and returns a `*cobra.Command`. Commands resolve input and call
domain modules but contain no business rules.

Input precedence is explicit. For ordinary settings:

```text
flags > documented environment variables > TOML > defaults
```

Interactive prompts occur only when interaction is allowed and the relevant
streams are terminals. Non-interactive, JSON, remote-operation, and test paths
never initialize Huh. Every prompt-backed action has a complete flag or input
equivalent.

Human results use stdout and progress or diagnostics use stderr. JSON mode uses
dedicated, versioned presentation types; it never serializes domain structs,
database rows, or SDK types directly. Errors are structured on stderr and
paired with a nonzero documented exit status. JSON Lines is reserved for a
future command that genuinely streams events.

## Errors

Domain errors have stable codes, structured safe context, and wrapped internal
causes. The CLI maps them to human guidance, JSON errors, and a small set of
process exit statuses.

Initial error categories include:

```text
invalid_input
not_found
conflict
authentication_required
permission_denied
unsupported
connection_failed
host_identity_changed
operation_in_progress
outcome_unknown
internal
```

Secrets, authorization headers, raw provider responses, and unsafe remote
output are redacted before entering error context or logs.

## Local persistence

SQLite stores inventory, credential-profile references, cached observations,
schema version, and operation recovery metadata. It does not become authority
for live remote or provider state.

The SQLite adapter uses WAL, a bounded busy timeout, short transactions, and
explicit logical ownership for conflicting long mutations. It never holds a
database transaction across SSH or provider network calls.

Migrations are numbered, immutable, forward-only SQL files embedded in the
binary. The migration module records version and checksum, runs one transaction
per migration, rejects altered history, and refuses a database created by a
newer application. Risky migrations require a backup. Shipped commands never
automatically run down-migrations.

Configuration and state use platform conventions:

```text
macOS: ~/Library/Application Support/Schooner/
       ~/Library/Caches/Schooner/

Linux: $XDG_CONFIG_HOME/schooner/
       $XDG_STATE_HOME/schooner/
       $XDG_CACHE_HOME/schooner/
```

Documented XDG fallbacks apply. `SCHOONER_CONFIG` may select a configuration
file; individual internal paths are not separately configurable.

## Credentials

Provider credentials are named profiles. Resolution order is:

```text
documented provider environment variable
-> explicitly selected stored profile
-> configured default stored profile
-> interactive hidden prompt
```

An interactively entered token is validated before Schooner offers to store it.
The prompt names the actual destination, such as macOS Keychain or Secret
Service. Environment-provided credentials are never saved implicitly, and
secrets are never accepted as ordinary command-line arguments.

If secure storage is unavailable, Schooner uses the secret in memory for the
current operation, explains that it was not stored, and never falls back to a
plaintext credential file. SQLite and TOML contain only opaque references and
safe display metadata.

The initial macOS keyring adapter may use the system `security` executable via
`zalando/go-keyring`. A native, code-identity-bound adapter becomes a hardening
candidate once macOS artifacts are consistently signed and notarized.

Provider credential profiles are distinct from source-host credentials. Git
operations run as the configured remote SSH user and use that user's existing
Git and SSH setup. V1 does not copy local Git credentials to a box or persist
source-host tokens.

## Extensibility

Extensibility means that a contributor has one obvious, verified path for a
change. It does not mean runtime plugins or speculative interfaces.

- Registration is explicit in the composition root; there are no `init()`
  registries, side-effect imports, or reflection-based discovery.
- A new command constructs Cobra commands directly and delegates to a domain
  module.
- A new provider implements the acquisition seam and its conformance suite;
  provider-specific types stay inside the adapter.
- New functionality belongs in an existing deep module or justifies a new one
  through distinct invariants and behavior.
- Generators are introduced only after at least three implementations reveal
  stable mechanical repetition.
- Internal interfaces may evolve. Only persisted or externally consumed
  contracts receive compatibility guarantees.

## Verification

Tests observe behavior through module interfaces. They use real local
substitutes where practical, handwritten fakes at caller-owned seams, and
provider mocks only for true external APIs.

The repository enforces:

- import direction and forbidden framework/SDK imports in domain packages;
- runtime and provider conformance suites;
- command-tree and external-output golden tests;
- duplicate command, provider, profile, and migration identifiers;
- migration history and checksum integrity;
- fuzz tests for shell argument encoding, configuration, identifiers, and
  remote JSON decoding;
- integration tests using the actual `ssh` process adapter against controlled
  fixtures before cloud tests;
- narrowly scoped live-provider tests that are opt-in and resource-cleaning.
