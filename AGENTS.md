# aperture-cli

## Design

Domain-driven design is the primary design paradigm for this project. New work
that introduces or reshapes a domain concept is modelled before it is written.

The domain model, the data model and the contracts are one first step, not
three stages. A domain model without its contracts is a description; contracts
without a model have nothing to be complete about. Define them together, before
the code, and let each correct the other.

- Bounded contexts are sized by language, not by responsibility. Splitting a
  context because two halves feel like different jobs is the usual mistake;
  if the user experiences one thing, it is one context.
- `domain` is never a package name. Packages and types are named after the
  thing they are, by what they do in this program rather than by their
  technical role. `Crossing`, not `NodeManager`.
- Every domain object is classified entity, value object or enumeration, and
  every field is listed. Behaviour lives on the object.
- Vendor types never appear in domain signatures. `tsnet`, `ipn` and
  `ipnstate` are confined to `internal/bridges`, which is the anti-corruption
  layer for the tailnet.

Artifacts, written before the code:

- `docs/specs/<context>-context-map.md` — ubiquitous language, contexts,
  relationships, ambiguous terms.
- `docs/specs/<context>-domain-model.md` — one section per object, with
  fields, behaviours, invariants, states and relationships.
- `docs/specs/<context>-contracts.md` — the API, the domain events and the data
  model, each to 100% and cross-checked against each other, so every aggregate
  transition can be traced through all three. A contract this project does not
  have is recorded as absent with its reason, never left blank.
- `docs/adr/NNNN-<slug>.md` — the decision and the forcing reason.

Mermaid diagrams in those files are rendered before the commit that adds them.

Current: [Connection](docs/adr/0001-connection-bounded-context.md).

## Conventions

- Commit prefixes match the package touched: `tui:`, `bridges:`, `config:`.
- `make test` is the gate.
