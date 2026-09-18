# 0002. Removing a bridge destroys its Machine

Status: accepted
Date: 2026-09-18

## Why?

There are two dead `aperture-cli-bridge-*` machines in the maintainer's
tailnet. Deleting a bridge drops its settings entry (`RemoveBridge`,
`global.go:219`) and leaves the registered device and the tsnet state
directory behind. `SwitchTailnet` (`manager.go:528`) already worked out that
closing a node without logging out orphans the device; removal never got that
reasoning.

`b2a6bc3` made it ordinary. Before it, orphaning took a deliberate second `d`
on a bridge row. Now deleting an endpoint cascades into it, so the common path
silently abandons a machine on the user's tailnet.

Detail, including all six removal sites and the constraints:
[bridge resource lifecycle](../specs/bridge-resource-lifecycle.md).

## Decision

Destroying the last Bridge reference destroys its Machine.

1. `Machine` grows `destroy`: `Logout`, `Close`, then discard the state
   directory, which is the Machine's own persistence. `Manager.Destroy` is the
   way in, because the turn that serialises work on one Machine and the cache
   entry holding it are both `Manager` state, and a destroy that took neither
   could open the state directory a running attempt is still writing. `Manager`
   grows a method, not a field (ADR 0001, decision 6).
2. New invariant: a Bridge with no Endpoint has no Machine.
3. Settings last. It is the only record the device exists, so dropping it
   before a failed logout leaves a machine the CLI can no longer name.
4. A Bridge with no state directory never registered. It has no Machine to
   destroy and must not start one to find out. Not `Bridge.Tailnet`: that is a
   display hint, written after verification and cleared before a switch, so it
   is empty for machines that do exist.
5. Destruction confirms, naming the device and the tailnet.
6. The wait is bounded, and on timeout the local records go anyway and the
   user is told which device is still theirs to delete.

## Consequences

Delete stops being instant and infallible: logout is a control-plane round
trip, and that round trip was hanging past 90s on 2026-09-17. Point 6 is the
concession, so "removed" will sometimes mean "removed locally".

Removal is irreversible from the CLI, and ACL rules naming the old device stop
matching. Leftover devices are unpublished behaviour someone may depend on,
which is the argument for confirming loudly rather than for leaking quietly.

The six removal sites need a `context.Context` and an event sink they do not
have today. That is most of the work.

## Rejected

- **Leave it.** Invisible where it happens, visible later in a different
  product, and no longer rare.
- **Revert `b2a6bc3`.** Brings back deleting one row twice, and still leaks.
- **Ephemeral nodes.** Closes the leak completely and costs a new device and a
  new login every run, which is what persistent bridges exist to avoid.
- **Skip the logout, delete the rest.** Keeps delete fast, leaves the exact
  orphan `SwitchTailnet` exists to prevent, and destroys the credentials that
  would let us clean up later.

## Revisit when

tsnet can deregister a node without bringing it up, which is the only reason
`Destroy` is slow.
