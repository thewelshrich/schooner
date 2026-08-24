# ADR 0002: Deep modules and compiled-in extensibility

- Status: Accepted
- Date: 2026-08-24

## Context

Schooner should be easy for contributors to extend with commands, providers,
and product behavior. A runtime plugin system would add compatibility,
discovery, trust, distribution, and isolation requirements. Adding interfaces,
registries, and factories everywhere would instead create shallow abstractions
and make the repository harder to navigate.

## Decision

Schooner uses deep domain modules with interfaces at proven seams. Concrete
types are the default inside modules. Commands and adapters are compiled into
one Go executable and registered explicitly in one composition root.

There is no runtime plugin system, automatic registry, reflection-based
discovery, dependency-injection framework, or side-effect registration.
Interfaces are caller-owned and justified by multiple production adapters, a
meaningful test adapter, or a known architectural substitution.

## Consequences

- Contributors follow explicit recipes and conformance tests.
- Adding a feature is localized without making every package replaceable.
- Internal Go interfaces and layouts may evolve before they become external
  contracts.
- Generators are introduced only after repeated implementations establish a
  stable mechanical pattern.
- A future out-of-process plugin protocol remains possible but is not implied
  by internal Go interfaces.

