# ADR 0005: Dedicated provider SSH identity and correlation-based reconciliation

- Status: Accepted
- Date: 2026-08-24

## Context

Provider creation crosses two uncertain boundaries. Schooner must arrange SSH
access before a machine exists, and a lost provider response may leave a
billable machine whose identifier was never observed locally. Depending only on
an account SSH key would make provisioning depend on undiscoverable local
private-key state. Blindly repeating a create request could instead duplicate
infrastructure.

## Decision

Schooner maintains one local Ed25519 identity for provider-created boxes. Its
public key is temporarily registered with the provider for creation and its
private key remains directly usable through system OpenSSH. Users may also
embed selected provider-account keys for independent access.

Every provider creation receives a unique Schooner correlation identity. The
provider adapter reconciles that identity before creating, records the opaque
provider resource identifier when observed, and fails closed if reconciliation
is ambiguous. Destruction verifies both the resource identifier and correlation
identity before deleting infrastructure.

## Consequences

- Provisioning does not depend on guessing which local key matches a provider
  account key.
- Ordinary access still uses the user's system OpenSSH client and remains
  available without a running Schooner process.
- Interrupted creation can resume without duplicating billable resources.
- The local private key requires strict permissions and careful migration.
- Removing a provider correlation marker makes automated destruction
  unavailable until the marker is restored or the resource is handled manually.
