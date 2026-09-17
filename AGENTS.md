# aperture-cli

## Design

This project is being taken toward domain-driven design. New work that
introduces or reshapes a domain concept is modelled before it is written.

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
- `docs/specs/<context>-contracts.md` — every domain event to 100%, and every
  aggregate transition traced through them. A contract this project does not
  have (there is no service API and no relational store) is recorded as absent
  with its reason, never left blank.
- `docs/adr/NNNN-<slug>.md` — the decision and the forcing reason.

The discipline is the `ddd` plugin's `domain-driven-design` and
`defining-contracts` skills; these paths are the project's, not the tool's.

Mermaid diagrams in those files are rendered before the commit that adds them.

Current: [Connection](docs/adr/0001-connection-bounded-context.md).

## Conventions

- Commit prefixes match the package touched: `tui:`, `bridges:`, `config:`.
- `make test` is the gate.
