# 0002. Removing a bridge destroys the machine it registered

Status: proposed
Date: 2026-09-18
Change size: medium. One new method on `bridges.Manager`, a confirmation menu,
and a rework of the six removal sites in `internal/tui` so they route through
it.

## Context

There are two dead `aperture-cli-bridge-*` machines in the maintainer's tailnet
right now. Neither was deliberately abandoned. Both are what is left of a
bridge that was created in the CLI, connected once, and then deleted from the
CLI, which removed the settings entry and nothing else.

`config.RemoveBridge` (`global.go:219-239`) rewrites `Settings.Bridges` and
calls `SaveSettings`. The node it registered is not ephemeral
(`manager.go:357-367` sets `Dir` and `Hostname` and no `Ephemeral`), so the
control plane keeps the device; tsnet mkdirs
`$UserConfigDir/aperture/bridges/<hex>` and nothing in the repo removes it.
`Manager.Close` never logs out. `SwitchTailnet` (`manager.go:528-566`) is the
only code path that has ever called `Logout`, and it is there because `e22c442`
worked out that closing a node without logging it out leaves the device
orphaned rather than removed. That reasoning was applied to switching tailnets
and not to deleting the bridge outright, which is the larger case.

The forcing reason is not that this is untidy. It is that the leak became
ordinary. Before `b2a6bc3` a bridge was orphaned only by a deliberate second
`d` on a bridge-named row, which few users would reach. After it, deleting an
endpoint takes its bridge with it, so the common path now silently registers a
machine on the user's tailnet and silently abandons it. `b2a6bc3` was the right
fix for the row that moved to the bottom instead of disappearing; it also
turned a rare leak into the default one.

The full inventory of resources and removal sites is in
[the bridge resource lifecycle spec](../specs/bridge-resource-lifecycle.md).

A note on evidence. The specs under `docs/specs/` landed on 2026-09-17
(`dceb2aa`) and describe the code as it already was, so this ADR does not treat
them as prior constraint. What it rests on is `manager.go` as written in
`e22c442` on 2026-09-16, and two machines that exist.

## Decision

Removing the last reference to a bridge destroys the machine it registered.

1. `bridges.Manager` grows `Forget(ctx, bridge, emit)`, shaped like
   `SwitchTailnet`: bring the node up if it is not already, log out, close it,
   evict it from `nodes` and `tailnets`, then `RemoveAll` the state directory.
2. The settings entry goes last, after `Forget` returns. Settings is the only
   record that the device exists, so dropping it first and then failing the
   logout leaves a registered machine the CLI can no longer name.
3. A bridge with no recorded tailnet (`Bridge.Tailnet == ""`) skips the logout.
   It has never registered, so there is no device, and bringing it up to delete
   it would make the user authorize a machine in order to destroy it.
4. Removal confirms first, in the `switchTailnetMenu` shape
   (`menus.go:544-576`), and the confirmation names the device and the tailnet
   it will be removed from.
5. The wait is bounded. On timeout or error the local records go anyway and the
   user is told, by name, which device is still in their tailnet.
6. All six removal sites route through one path. The parallel `d` on the
   Bridges settings menu (`menus.go:213-227`) stops being a second way to
   delete a bridge without confirmation.

## Consequences

Good:

- Deleting a bridge leaves nothing behind, which is what the user already
  believes is happening.
- The admin console stops accumulating a machine per abandoned first login.
- One removal path instead of six near-copies, and one confirmation covering
  all of them.

Bad, and accepted:

- Delete becomes slow and failable where it is currently instant and
  infallible. Logout is a control-plane round trip on the same infrastructure
  where `/machine/register` was observed hanging past 90 seconds on
  2026-09-17. Point 5 is the concession, and it means "removed" sometimes means
  "removed locally, go finish this in the admin console".
- Removal needs a `context.Context` and an event sink at call sites that today
  are synchronous `menu.Result` functions. That is the bulk of the diff.
- Destruction is now irreversible from the CLI. A user who deletes a bridge and
  wants it back logs in again as a new machine, with a new device name and
  whatever ACL grants applied to the old name no longer matching.
- Leftover devices are unpublished behaviour that someone may have come to
  depend on: an ACL rule naming `aperture-cli-bridge-abc123`, a route, a
  tagged group. That is an argument for confirming loudly, not for continuing
  to leak. It is why point 4 names the device rather than asking "remove this
  bridge?".

## Alternatives considered

**Leave it.** The status quo. Cheapest, and defensible while the leak was rare.
It is not rare now, and the failure is invisible at the point it happens and
visible only later, in a different product, to a user who has no way to tell
which of their `aperture-cli-*` machines are live.

**Go back to the two-step delete.** Revert `b2a6bc3`, so deleting an endpoint
never cascades and a bridge can only be removed deliberately. That restores
"when I delete an endpoint, it just moves to the bottom, I have to do it
twice" for something the picker presents as one row, and it does not stop the
leak, it only makes the user press `d` twice to cause it.

**Make bridges ephemeral.** Set `Ephemeral: true` on the `tsnet.Server` and let
the control plane reap the device when the node disconnects. It solves the leak
completely and costs the thing bridges exist for: every reconnect becomes a new
device and a new interactive login. Trading a cleanup bug for a login on every
run is a worse trade, particularly given how slow that login currently is.

**Delete the settings entry and the state directory, skip the logout.** Avoids
the control-plane round trip, so delete stays fast. It leaves exactly the
orphan `SwitchTailnet` was written to avoid, and it destroys the credentials
that would let us clean up later, so it converts a recoverable leak into a
permanent one.

## Revisit when

tsnet offers a way to deregister a node without bringing it up first, which
would remove the only reason `Forget` is slow and would let removal go back to
being synchronous. Or if bridges stop being long-lived, in which case the
ephemeral option becomes the right answer instead.
