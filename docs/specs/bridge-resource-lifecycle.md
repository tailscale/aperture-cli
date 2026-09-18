# Bridge resource lifecycle

What a bridge creates, what removes it, and what is left behind today.

Creating a bridge produces three things. Removing one destroys one of them.
The other two are a tailnet device the user can see in their admin console and
a directory on their disk, and nothing in this repo has ever deleted either.

## The three resources

| Resource | Created by | First exists | Removed by |
|---|---|---|---|
| Tailnet device `aperture-cli-<bridge-id>` | `tsnet.Server` registering with the control plane | first successful `Activate` | nothing |
| `$UserConfigDir/aperture/bridges/<hex>` | tsnet, lazily, from `Server.Dir` | first `Activate`, successful or not | nothing |
| `config.Bridge` in settings | `Global.AddBridge` (`global.go:178-196`) | the moment the user types a name | `Global.RemoveBridge` (`global.go:219-239`) |

The device is persistent because the node is not ephemeral. `newNode`
(`manager.go:357-367`) builds:

```go
s := &tsnet.Server{
    Dir:      stateDir,
    Hostname: "aperture-cli-" + bridge.ID,
    UserLogf: userLogf,
}
```

There is no `Ephemeral` field set anywhere in `internal/bridges`, so the
control plane keeps the machine after the process exits, which is the point:
the same bridge reconnects next run without a login. The state directory is
the other half of that. `config.BridgeStateDir` (`settings.go:90-101`) returns
`$UserConfigDir/aperture/bridges/<id with the "bridge-" prefix stripped>`, and
its only non-test caller is `runningNode` (`manager.go:440`), which hands it to
`Server.Dir`. tsnet mkdirs it on start, so a bridge that has never been
activated has no directory.

`RemoveBridge` rewrites `Settings.Bridges` and calls `SaveSettings`. That is
all it does. `os.RemoveAll` appears four times in the repo, all of it client
installer cleanup, none of it bridge related. `Manager.Close`
(`manager.go:567-587`) closes proxies and nodes and never logs out, which is
correct for shutdown and is why nothing else has to be.

The one place that does log out is `SwitchTailnet` (`manager.go:528-566`), and
its comment already names the failure mode this document is about:

> A node that was never started this session is therefore brought up on the
> old tailnet first, which is also what leaves the device removed from it
> rather than orphaned.

That reasoning applies to removal at least as strongly as it applies to
switching. Removal skipped it.

## Where a bridge can be removed

Six places, all in `internal/tui`, none of them confirming, none of them
touching anything but settings.

| Site | What it removes | Note |
|---|---|---|
| `bridgesMenu` hidden `d` (`menus.go:213-227`) | the bridge | a second bridge-deleting UI, parallel to the picker's |
| `removeConnectionRow` default arm (`menus.go:482-495`) | the bridge, for a row with no endpoint | the "Remove bridge" the user sees |
| `removeConnection` (`menus.go:497-520`) | the endpoint, then cascades | |
| `dropOrphanBridge` (`menus.go:529-539`) | the bridge, once its last endpoint is gone | added in `b2a6bc3` |
| setup guide "Remove endpoint" (`menus.go:647-667`) | the endpoint, then cascades | duplicates `removeConnection`'s loop |
| `discardActivation` (`tui.go:471-494`) | the ephemeral endpoint only | leaves the bridge |

The last one is worth reading as a cause rather than a symptom. Cancelling a
connection to a freshly created bridge takes the endpoint back out and leaves
the bridge, which is exactly what makes a bare "Connect via" row appear in the
picker with no endpoint attached. So the row that `removeConnectionRow`'s
default arm deletes is usually the residue of an abandoned first login, and
deleting it is the one case where there is no device to clean up: the bridge
may never have registered at all.

`b2a6bc3` did not create the leak. It made it reachable from a single delete of
an endpoint, where before the user had to press `d` a second time on a
bridge-named row to get there.

## What cleanup needs

One operation on `bridges.Manager`, shaped like `SwitchTailnet` because it is
the same work minus the restart:

```go
func (m *Manager) Forget(ctx context.Context, bridge config.Bridge, emit func(connection.Event)) error
```

Order matters, and it is not the obvious one. Logout, close the node, evict it
from `nodes` and `tailnets`, `RemoveAll` the state directory, and only then let
the caller drop the settings entry. Settings last because settings is the only
record that the device exists: dropping it first and then failing the logout
leaves a registered machine the CLI can no longer name, which is strictly worse
than the leak we have now.

## Constraints that decide the design

**Logout needs a running node.** `(*tsnetNode).Logout` (`manager.go:335-341`)
goes through `server.LocalClient()`, so the credentials it clears live behind
the in-process LocalAPI. Closing a node without logging out reuses them next
start. There is no way to log out a bridge that is not up.

**Starting a node that never registered performs a full interactive login.**
`runningNode`'s own comment (`manager.go:471-474`) says `Up` blocks until the
node is Running, which for a bridge that has never logged in means blocking
until the user visits a link nothing has shown them yet. Routing removal
through `runningNode` unconditionally would create a device in order to delete
it, and would do it by asking the user to authorize a machine they just asked
to destroy. `Bridge.Tailnet != ""` is the persisted signal for "has ever
joined" (`SetBridgeTailnet`, `global.go:201-216`); a bridge without it skips
the logout entirely, and has no state directory to remove either.

**Removal is slow and failable.** `/machine/register` was observed hanging past
90 seconds on 2026-09-17, and logout is a control-plane round trip on the same
infrastructure. A delete that blocks the UI indefinitely is not shippable. The
operation needs a bounded wait and an escape that removes the local records
anyway and tells the user, in words, that a device named
`aperture-cli-<id>` is still in their tailnet and where to delete it.

**No removal site has a context or an event sink.** All six return
`menu.Result` synchronously. The house pattern for slow work is the one
`connectVia` uses (`menus.go:793-801`): do the fast fallible part inline,
return a `tea.Cmd` for the rest. The house pattern for showing progress
without a full connection attempt is the post-launch recheck
(`tui.go:773-782`), which reuses `stepPreflight` with a bare `activation` for
the label and clock plus its own result message.

**No removal path confirms today.** Every deletion above is one keypress. The
house confirm shape is `switchTailnetMenu` (`menus.go:544-576`): a menu whose
title is a question naming the subject, a preamble stating the current state
and the consequence, two items `{verb, y}` and `{Cancel, n}`, pushed with
`Next` so Esc also backs out.

## Out of scope

The cascade rule from `b2a6bc3` does not change: a bridge two endpoints reach
through is not an orphan and survives the removal of either one.

Deduplicating the six removal sites is not required to fix the leak, though
whatever lands should not make it seven.
