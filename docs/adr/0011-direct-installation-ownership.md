# ADR 0011: Verified direct installation owns only receipted executables

- Status: Accepted
- Date: 2026-08-29

## Context

Schooner releases immutable executables through GitHub. Ordinary users need a
package-like installation path without an account or hosted control plane, and
the local updater needs to know whether it has authority to replace the running
file. Homebrew, source builds, and manually copied binaries remain owned by
their respective users and tools.

## Considered options

- Replace any writable `schooner` executable. This is convenient but silently
  takes authority from package managers and source-build workflows.
- Infer ownership only from the executable path. Homebrew layouts are detectable
  in common cases, but custom prefixes and manually copied binaries make path
  inference insufficient.
- Store one local direct-install receipt bound to the canonical path and exact
  executable digest. This adds a small persistent contract but permits safe,
  explicit replacement and fails closed after external changes.

## Decision

The repository installer and the local updater verify a concrete GitHub release
before promotion. On macOS they also require the expected Developer ID
Application signature. Direct installation writes a receipt beside the
executable. That receipt grants replacement authority only while it names the
running path and matches the current executable bytes.

Package-manager ownership and symlinks take precedence over direct receipts.
Development, source, stale-receipt, and unknown installations are not replaced.
GitHub HTTPS, the immutable release, and `SHA256SUMS` form the Linux client
trust boundary in this release. Build-provenance attestations are published
evidence but are not yet verified by installer clients.

## Consequences

- Schooner can replace only files installed or explicitly adopted through its
  direct installer.
- A manual edit, relocation, missing receipt, or mismatched digest revokes
  automatic replacement authority without deleting the executable.
- Package managers remain responsible for their own upgrades and uninstall
  behavior.
