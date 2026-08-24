# ADR 0003: Local state and secure credentials

- Status: Accepted
- Date: 2026-08-24

## Context

Schooner needs transactional inventory and recovery state while keeping live
machine and provider state authoritative externally. Provider credentials are
reusable across boxes and may belong to different accounts or projects. Plain
configuration files and SQLite are inappropriate secret stores.

## Decision

Operational local state uses SQLite through a cgo-free driver. Configuration is
strict typed TOML. Provider secrets use named credential profiles backed by the
operating-system credential store or documented environment variables. SQLite
and TOML store references and safe display metadata only.

The initial credential adapter uses `zalando/go-keyring`. It never silently
falls back to plaintext. If secure storage is unavailable, the credential may
be used in memory for the current invocation with an actionable warning.

## Consequences

- SQLite migrations are embedded, immutable, checksummed, and forward-only.
- Long external work does not hold database transactions.
- Credential rotation occurs once per profile rather than once per box.
- Headless Linux users can use provider environment variables without a desktop
  secret service.
- Native macOS Keychain integration tied to Schooner's signed code identity is
  a future hardening adapter.

