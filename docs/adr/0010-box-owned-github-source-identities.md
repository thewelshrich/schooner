# ADR 0010: Local Source Account with Box-owned GitHub identities

- Status: Accepted
- Date: 2026-08-27

## Context

Private GitHub repositories must be usable from persistent user-owned Boxes
without copying workstation SSH keys, storing OAuth tokens on Boxes, depending
on a hosted Schooner service, or making a transport URL into Repository
identity. Key creation and revocation cross local keyring, Box filesystem,
OpenSSH, and GitHub failure boundaries and therefore require durable,
authority-aware reconciliation.

## Considered options

- Rely only on each Box user's pre-existing Git and SSH configuration. This
  remains the first choice but provides no explicit recovery path for a fresh
  Box.
- Copy one local private key to every Box. This expands one credential's blast
  radius and violates Box ownership.
- Use repository deploy keys or HTTPS tokens. Deploy keys create
  repository-by-repository lifecycle work; HTTPS tokens would need to reach Git
  on the Box and broaden secret handling.
- Use a local GitHub Source Account to register one dedicated public key for
  each Box. This adds a narrow account authorization while keeping every
  private key on its owning machine.

## Decision

Schooner uses a GitHub App registration dedicated to the Go CLI, with device
flow, expiring user tokens, and only the account-level `Git SSH keys: write`
permission. One local Source Account is shared across Boxes. Its access token,
refresh token, and expiries are one versioned operating-system keyring envelope;
plaintext persistence, client secrets, repository Contents permission, and a
hosted control plane are excluded.

Each Box owns one dedicated Ed25519 Box Source Identity. Only its public key and
fingerprint cross typed direct/SSH operations. GitHub host trust comes from
fingerprint-validated HTTPS metadata and managed SSH fails closed with a
dedicated key, `known_hosts`, empty SSH config, and strict noninteractive
options.

SQLite stores safe account and key-correlation metadata plus lifecycle states
`connecting`, `connected`, `disconnecting`, and `cleanup_pending`. Fingerprints,
not display titles, establish identity. Connect reconciles ambiguous creation
by listing before retrying, and `connecting` remains verification-pending until
the Box confirms SSH access. A first authorization that cannot checkpoint any
Box binding is removed; zero-binding disconnect retries that cleanup after a
secure-store failure. Disconnect verifies and revokes GitHub authority before
deleting Box files; interrupted cleanup remains recoverable. Box removal and
destruction retain source metadata and make no GitHub calls.

Repository Identity is normalized host/owner/repository. HTTPS and SSH are
operation-scoped Transports and never define durable identity.

Repository clone version 2 moves GitHub transport selection behind the Source
module while the Repository lifecycle retains exclusive staging authority.
Candidates are the supplied transport, ambient canonical SSH, managed SSH, and
anonymous HTTPS. Only authentication-shaped failures advance. Durable clone
intent uses Repository Identity and checkpoints the first supplied origin so a
transport fallback resumes one operation without changing `remote.origin.url`.

Interactive clone and contextual start may offer to establish a Box Source
Identity after an authentication failure, verify the requested Repository, and
retry once. Existing managed identities require no prompt. JSON and
non-interactive flows never authorize or register a key.

## Consequences

- Compromise of one Box source key does not expose workstation keys or another
  Box's key.
- GitHub account authorization is centralized locally, while repository data
  access still occurs directly between the Box and GitHub over system tools.
- Secure-store outages permit invocation-only progress but cannot create a
  plaintext credential fallback.
- GitHub and Box outages can yield partial status or deferred cleanup without
  weakening revocation order.
- Removing a Box before disconnect may leave an inactive private key on that
  machine; the GitHub key remains revocable from retained local metadata.
- SAML SSO key authorization remains an explicit GitHub user action.
