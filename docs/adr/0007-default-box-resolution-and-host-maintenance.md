# ADR 0007: Default Box resolution and explicit host maintenance

- Status: Accepted
- Date: 2026-08-25

## Context

Product commands need one predictable answer to which Box they operate on, but
repeated prompts are heavy and arbitrary non-interactive selection is unsafe.
The on-demand remote application also needs explicit recovery and update routes
that preserve identity, compatibility, and no-downgrade guarantees.

## Decision

Schooner stores at most one default Box in local SQLite state. A shared resolver
uses an explicit Box, the current Local Link, the configured default, the sole
configured Box, or an interactive selector in that order. Non-interactive
ambiguity fails deterministically. Removal and destruction do not implicitly
use the default.

`box status` is observational. `box setup` repairs the runtime, prerequisites,
and workspace root after verifying the recorded Box identity. `box update`
requires an existing runtime and targets the invoking local CLI's verified
artifact. It updates older compatible runtimes, reuses equal versions, retains
newer compatible versions, and never silently downgrades.

## Consequences

- Removing the default clears the preference without selecting a replacement.
- Adding the first Box does not persist it as the default; sole-Box fallback is
  resolved dynamically.
- Setup invocation is explicit consent for its bounded runtime and prerequisite
  repairs.
- Missing runtimes route to setup; version maintenance routes to update.
