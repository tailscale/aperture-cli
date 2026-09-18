# Bridge resource lifecycle

Creating a bridge produces three things. Removing one destroys one of them.
The survivors are a device in the user's admin console and a directory on
their disk. Decision: [ADR 0002](../adr/0002-bridge-removal-destroys-the-machine.md).

## What a bridge creates

| Resource | Created by | First exists | Removed by |
|---|---|---|---|
| Machine `aperture-cli-<bridge-id>` | `tsnet.Server` registering | first successful `Activate` | nothing |
| `$UserConfigDir/aperture/bridges/<hex>` | tsnet, from `Server.Dir` | first `Activate`, successful or not | nothing |
| `config.Bridge` | `AddBridge` (`global.go:178`) | the moment a name is typed | `RemoveBridge` (`global.go:219`) |

The device outlives the process because `newNode` (`manager.go:357`) sets no
`Ephemeral`, which is the point: the same bridge reconnects next run without a
login. The directory is the other half of that, and tsnet mkdirs it lazily, so
a bridge that never connected has none. `RemoveBridge` writes settings and
nothing else; `os.RemoveAll` appears four times in the repo, all of it client
installer cleanup.

`SwitchTailnet` (`manager.go:528`) is the only caller of `Logout`, and its
comment already names the failure mode: a close without a logout leaves the
device orphaned rather than removed.

## Where a bridge can be removed

Six sites, all in `internal/tui`. Five of them now describe the delete as a
`bridgeRemoval` and hand it to `remove` (`removal.go`), which is the only place
that decides whether a machine has to be destroyed first.

| Site | Removes |
|---|---|
| `bridgesMenu` hidden `d` | the bridge, from a second delete UI parallel to the picker's |
| `removeRow` bridge arm | the bridge, for a row with no endpoint |
| `removeRow` endpoint arm | the endpoint, then cascades |
| `dropOrphanBridge` | the bridge, once its last endpoint goes (`b2a6bc3`) |
| setup guide "Remove endpoint" | the endpoint, then cascades |
| `discardActivation` (`tui.go`) | the ephemeral endpoint, leaving the bridge |

The last is the exception and is a cause rather than a symptom: abandoning the
first connection to a new bridge is what leaves a bare "Connect via" row with
no endpoint. So the row the second site deletes is usually the residue of a
login nobody finished, and is the one case with no device to clean up.

## What destroying it needs

`Machine.destroy`, on the aggregate that owns the node
([domain model](connection-domain-model.md#machine)): `Logout`, `Close`, then
discard the state directory, which is the Machine's own persistence.
`Manager.Destroy` is the entry point, because the Machine's turn and its cache
entry are `Manager` state and destruction has to hold the turn like every other
operation on that node.

The state directory goes last and only when the logout succeeded: it holds the
node key, which is what a later attempt would need to deregister the device.

Order matters and is not the obvious one. Settings goes last, after `Destroy`
returns, because settings is the only record that the device exists: dropping
it first and then failing the logout leaves a registered machine the CLI can no
longer name.

## Constraints

**Logout needs an initialized LocalAPI, not an authorized node.** `Logout`
goes through `server.LocalClient()`, which calls `Start` without waiting for
`Running`. `SwitchTailnet` now uses this path; see
[ADR 0003](../adr/0003-preserve-verified-connections.md). A closed node keeps
its credentials, but requiring `Up` before logout would unnecessarily demand
authorization of the identity being left.

**Initialization can begin registration; `Up` waits for it.** Destruction must
not require an interactive login to remove a bridge, so it initializes the node
and never brings it up. `Bridge.Tailnet` is a display hint, not proof that no
machine exists when empty: it is saved only after endpoint verification and
cleared before a switch. The state directory is the durable evidence, which is
what `bridges.HasMachine` reads.

**Destruction is slow and failable.** `/machine/register` was hanging past 90
seconds on 2026-09-17 and logout is a round trip to the same place. The escape
has to name the surviving device, not just report a timeout.

**No removal site had a context or an event sink.** They return `menu.Result`
synchronously, so the wait goes where every other slow bridge operation goes:
`destroyBridgeCmd` reuses `stepPreflight`, the bridge log tail and
`bridgeRemovedMsg`, in the shape of the post-launch recheck. The attempt keeps
no cancel handle, so Esc cannot abandon a logout half way through and leave the
record disagreeing with the device.

**No removal path confirmed.** `removeBridgeMenu` follows `switchTailnetMenu`,
the house confirm shape.

## Out of scope

The `b2a6bc3` cascade rule stands: a bridge two endpoints reach through is not
an orphan. Deduplicating the six sites is not required to fix the leak, though
whatever lands should not make it seven.
