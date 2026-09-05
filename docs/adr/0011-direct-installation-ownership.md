# ADR 0011: Verified direct installation owns only receipted executables

- Status: Accepted
- Date: 2026-08-29

## Context

Schooner releases immutable executables through GitHub. Ordinary users need a
package-like installation path without an account or hosted control plane, and the
local updater needs to know whether it has authority to replace the running
file. Files installed by Homebrew, source builds, and manually copied binaries
remain owned by their respective users and tools.

An executable path alone cannot establish ownership. Receipts can become stale,
custom install directories can overlap package-manager prefixes, and an
interrupted promotion can leave a valid new executable beside old metadata. The
repository installer is Bash, so its persistent and locking contracts must also
be implemented by the Go updater.

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

The repository installer consumes deterministic release archives. The
local updater consumes the unchanged raw release executable. Both verify the
concrete release manifest and executable identity before promotion; macOS also
requires the Developer ID Application signature for Team ID `LDCWNW7T7K` and
identifier `app.schooner.cli` before the candidate runs.

A direct installation stores `.schooner-install-receipt.json` beside the
executable with mode `0600`. Schema version 1 is strict canonical JSON containing
the installation method, canonical executable path, installed version,
executable SHA-256, release-asset kind/name/SHA-256, and installation timestamp.
The installer writes `release_asset_kind` as `archive`; the updater writes
`raw`. A receipt grants authority only when it is a current-user-owned regular
file, is not group- or world-writable, names the running path, and its digest
matches the current executable bytes.

Package-manager ownership and symlinks take precedence over direct receipts.
Development, source, stale-receipt, and unknown installations are not replaced.
An explicit installer invocation may adopt an unreceipted target only when its
bytes already equal the fully verified candidate, because that operation changes
metadata without replacing user code.

Installer and updater operations coordinate through an adjacent exclusive
`.schooner-install.lock` directory. Its versioned owner record binds hostname,
PID, canonical target, and a random token. All existing locks fail closed,
including demonstrably dead local owners. Automatic retirement cannot safely
distinguish the inspected directory from a newer owner installed by another
reclaimer. Recovery requires stopping all installers and updaters before manually
removing the owner file and empty lock directory. The owner format is unchanged. Each operation
captures target and receipt fingerprints under the lock and rechecks them before
same-directory executable promotion. The receipt is promoted only after the
executable.

GitHub HTTPS, the concrete immutable release, and `SHA256SUMS` form the Linux
client trust boundary in this release. Build-provenance attestations are
published evidence but are not yet verified by installer clients.

## Consequences

- Schooner can replace only files installed or explicitly adopted through its
  direct installer.
- A manual edit, relocation, missing receipt, or mismatched digest revokes
  automatic replacement authority without deleting the executable.
- Failure after executable promotion but before receipt promotion leaves a valid
  but unowned binary. Re-running the same explicit version verifies identical
  bytes and repairs the receipt.
- The Bash installer and Go updater must preserve the version-1 receipt
  serialization and lock protocol until an explicit migration is designed.
- Package managers remain responsible for their own upgrades and uninstall
  behavior.
