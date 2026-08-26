# Contributing to Schooner

Thank you for taking the time to improve Schooner. The project is still early,
so opening an issue before beginning a large change is the best way to avoid
duplicated or out-of-direction work.

## Before you start

- Use a bug report for reproducible incorrect behavior.
- Use a feature request to describe a user problem before proposing a large
  implementation.
- Do not open a public issue for a suspected security vulnerability. Follow
  the security policy once it is published.
- Keep pull requests focused. Unrelated cleanup is easier to review separately.

## Local setup

Schooner requires Go 1.27.0. Clone the repository and run the local verification
commands before making a change:

```bash
go build ./cmd/schooner
go test ./...
go vet ./...
```

See the [development guide](docs/development.md) for remote-runtime artifacts
and the Ubuntu SSH smoke test.

## Pull requests

1. Create a focused branch from `main`.
2. Add or update tests for observable behavior.
3. Run formatting, tests, and vet locally.
4. Update user documentation when command behavior changes.
5. Use a conventional commit title, such as `feat:`, `fix:`, `docs:`, or
   `refactor:`.
6. Explain the user-visible outcome and important design choices in the pull
   request description.

By submitting a contribution, you agree that it is licensed under the
[Apache License 2.0](LICENSE).

## Design standard

Schooner favors deep modules: substantial behavior behind a small interface at
a clean seam. Before adding an abstraction, ask:

1. What behavior varies today?
2. Where is the seam at which it varies?
3. Does the interface hide complexity or merely repeat the implementation?
4. Is there a second production adapter or a meaningful test adapter?
5. Would deleting the module make complexity reappear across multiple callers?

Use concrete types by default. Interfaces are owned by callers at proven seams.
Do not add factories, registries, hooks, or extension points for hypothetical
future use.

## Dependency rules

- Domain modules do not import Cobra, Huh, SQL drivers, provider SDKs,
  rendering packages, or `os/exec`.
- Commands contain input resolution and presentation, not product rules.
- Infrastructure adapters translate external systems into domain concepts.
- Construction and registration are explicit in `cmd/schooner`.
- Avoid package globals and side-effecting `init()` functions.
- Accept dependencies through constructors; validate required dependencies
  during construction.
- Return results and typed errors rather than logging inside domain behavior.

## Adding a command

1. Identify the domain use case the command invokes. Add behavior to the deep
   domain module before adding CLI orchestration.
2. Create a command constructor receiving explicit dependencies and returning
   `*cobra.Command`.
3. Define flags and non-interactive input before prompts.
4. Add focused Huh prompts only through the prompt adapter.
5. Add human and JSON presentation types without serializing domain values
   directly.
6. Register the command explicitly in the composition root.
7. Add command-tree, argument, non-TTY, JSON, cancellation, and error-mapping
   tests.

Do not build a Schooner command framework on top of Cobra.

## Adding a provider

1. Confirm the provider can satisfy the existing acquisition outcomes without
   leaking provider-specific concepts into callers.
2. Implement the provider adapter inside `internal/provider/<name>`.
3. Keep SDK request, response, pagination, retry, and authentication types
   inside the adapter.
4. Declare provider capabilities honestly; never fake an unsupported lifecycle
   operation.
5. Return a provider-neutral resource reference and connection handoff.
6. Implement the shared provider conformance suite and adapter-specific tests.
7. Register the provider explicitly in the composition root.
8. Add opt-in live tests with unique resource names, bounded cost, and reliable
   cleanup.

Provider adapters acquire infrastructure. They do not install tools, discover
Repositories or Worktrees, manage Sessions, or define Box behavior.

## Adding remote behavior

1. Add a typed product operation to the runtime interface only when a caller
   needs it; never add generic command execution.
2. Implement the operation in the shared application and register it in the
   private remote entry point.
3. Keep the operation cohesive enough to hide Git, tmux, filesystem, and tool
   version details.
4. Pass structured input separately from remote invocation and executable
   selection.
5. Emit a versioned result on stdout and diagnostics on stderr.
6. Bound output, classify exits, redact unsafe values, and respect cancellation.
7. Test idempotency, interruption, unsupported capabilities, protocol
   compatibility, and hostile input.
8. Add the operation to the remote-runtime conformance suite.

Treat the remote box identifier as correlation data, never as authentication.
Tests covering identity must establish OpenSSH host trust separately and must
exercise the duplicate-ID case caused by a cloned machine image.

The remote Schooner application is invoked on demand and exits with its bounded
operation. Do not introduce a persistent service, worker, or daemon without a
separate architectural decision demonstrating behavior that must continue
without a connected CLI. tmux owns persistence for user-visible Sessions and
optional coding Agents.

## Adding Repository, Worktree, or synchronization behavior

1. Treat Git's canonical common directory as Repository identity and Git's
   registered checkout paths as Worktree identity.
2. Never add Repository/Worktree IDs, aliases, inventory records, managed flags,
   or a parallel lifecycle.
3. Use fixed Git operations and revalidate live state before acting on a path.
4. Store only Schooner-owned Session and Operation metadata outside repositories;
   do not require a repository configuration file.
5. Keep `push` local-to-remote, `pull` remote-to-local, and `sync` a comparison
   of both sides against their Sync Point.
6. Run synchronization only through explicit one-shot commands and update the
   Sync Point only after verifying the shared result.
7. Return conflicts instead of silently choosing a winning checkout.

## Adding a module

A new module must own a coherent set of invariants and provide leverage to more
than one caller or behavior. Document:

- its interface and invariants;
- why its seam is located where it is;
- which adapters or tests justify the seam;
- its error and concurrency behavior;
- which source of truth it observes or owns.

Do not create empty packages to match the architecture map. Do not split a
module into `handler`, `service`, and `repository` packages by convention.

## Testing

- Test through module interfaces and observable outcomes.
- Prefer table tests and `go-cmp` semantic diffs.
- Use `t.TempDir` and real SQLite databases for inventory tests.
- Use handwritten fakes for small caller-owned interfaces.
- Use golden files only for genuine external contracts.
- Fuzz parsers, argument encoding, identifiers, and untrusted external data.
- Keep live cloud tests opt-in and separate from the deterministic suite.
- Delete tests that merely duplicate implementation after a module is deepened.

## Compatibility

Internal package layouts and Go interfaces are not public contracts. Changes to
the following require explicit compatibility and migration review:

- CLI command behavior and documented exit statuses;
- JSON output schemas;
- TOML configuration;
- SQLite schema and migration history;
- remote identity and metadata files;
- remote-operation protocols and result schemas;
- remove and destroy semantics;
- SSH host-trust behavior;
- credential storage and redaction behavior.

Consequential changes should be recorded in `docs/adr/` before implementation.
