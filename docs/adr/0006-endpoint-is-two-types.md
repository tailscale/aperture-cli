# 0006. An Endpoint is one of two types, not a struct with an optional Bridge

Status: accepted
Date: 2026-09-21

## Why?

`Endpoint` was `{URL, BridgeID}` and a direct connection was the one with an
empty `BridgeID`. Every caller decided which kind it had by testing a string
for emptiness, thirty-six times outside tests, and the two concepts the
context map keeps apart shared one struct. Review on PR 41 called it what it
was: two different things, and an empty field standing in for a type.

## Decision

1. `Endpoint` is an interface with two implementations and no third:
   `DirectEndpoint` and `BridgeEndpoint`. An unexported method keeps the set
   closed.
2. The kind is the type. Callers that differ by kind type-switch; nothing
   asks whether a field is empty.
3. Values are comparable, so two Endpoints are the same when `==` says so.
   `SameEndpoint` is gone.
4. `settings.json` keeps its shape: `bridgeId` present or absent. The file
   predates the split and is not changing under existing users. Decoding
   picks the type from the record; each type encodes back to the record.
5. `WithURL` is on the interface: an edit or an inline override keeps its
   Bridge, and the caller does not need to know which kind it holds.

## Consequences

Constructors (`Direct`, `Bridged`) replace literals, in tests too. A nil
Endpoint is possible where a zero struct was not; the few places that meant
"no endpoint" now say `nil` and are guarded.

## Rejected

- **Exported fields with a differently named accessor.** A field and a method
  cannot share the name `URL`, and `Location` or `Address` would have put a
  second word for the same thing into the language table.
- **A `Kind` field on one struct.** The same smell with a nicer name.

## Revisit when

A third way to reach an Aperture exists.
