# 0001. Connection is one bounded context, and it publishes events, not log lines

Status: proposed
Date: 2026-09-17
Change size: large. Touches `internal/bridges`, the activation half of
`internal/tui`, and the `ApertureHost` boundary into `internal/clients`.

## Context

A bridge that had never been connected took 29 seconds to come up and the
screen showed nothing but tsnet's `NeedsLogin`. The goroutine dump puts the
node inside the control plane's first `POST /machine/register`
(`controlclient/direct.go:853`) with an empty follow-up URL, so no login link
existed yet. That is a distinct thing to be waiting on, and it is
indistinguishable on screen from waiting for the user to finish in the browser,
because both are `ipn.NeedsLogin`.

Three changes have now been aimed at that wait without the information to aim:

- `5ebdb59` opens the browser at the link.
- `80d679f` reads the link off the IPN bus instead of tsnet's 5s poll.
- `82fab61` resolves the target against the node's own peer map before dialing.

Each is correct. None could have shortened this particular wait, because none
of them is in the phase the wait was in. The forcing reason for this ADR is not
that the code is untidy: it is that four separate mechanisms carry progress
(a `func(string)` sink, a `chan bridgeLine`, `tsnet`'s own prose, an
`*ipnstate.Status` return) and none of them names what the attempt is waiting
on, so a fourth fix would be aimed the same way.

Two concrete defects fall out of the same shape:

- The TUI recovers the login link by string-matching two markers, one of which
  is a phrase inside tsnet's log text (`"or go to: "`, `browser.go:16`). A
  reworded upstream log line silently stops the browser from opening.
- The link travels on a 32-slot channel whose sink drops on overflow
  (`tui.go:557`). Under `--debug` the tsnet backend logger shares that channel,
  so a burst of chatter can discard the one line the user cannot proceed
  without.

Three IPN bus watchers run against one backend: tsnet's inside `Up`, ours
inside `WatchLogin`, and tsnet's `printAuthURLLoop`. `LocalBackend.sendToLocked`
iterates every watcher while holding `b.mu`, and the code comments there assume
one.

## Bounded contexts

One: **Connection**. It spans the whole flow, from the user picking an endpoint
to a client knowing where to send requests, including the `/v1/models` fetch
that follows bring-up. Splitting bring-up from the fetch is the reason nobody
owns "what is this attempt waiting on"; the user experiences one wait.

Neighbours and patterns are in
[the context map](../specs/connection-context-map.md).

## Ubiquitous language

Connection Attempt, Endpoint, Gateway, Route, Bridge, Crossing, Login Link,
Phase, Progress. Defined in the context map. Three of these resolve words that
currently mean two things: `Gateway` versus `Endpoint` splits `ApertureHost`,
`Crossing` versus `Bridge` splits the running node from the saved record, and
`Phase` takes over from `Status`.

## Domain objects and invariants

Full model in [the domain model](../specs/connection-domain-model.md). The
invariants this ADR is accountable for:

- An Attempt's `Trail` accounts for its whole wall clock with no gaps, so
  "where did 29 seconds go" has an answer.
- `AwaitingLoginLink` and `AwaitingAuthorization` are different phases despite
  being the same `ipn.State`.
- A `LoginLink` is parsed once, at the boundary, and is `https` with no
  whitespace. Nothing downstream re-derives it from text.
- Only `Noted` events may be dropped under backpressure.
- No vendor type crosses the context boundary.

## Anti-corruption layer

`internal/bridges` is the ACL and the only importer of `tsnet`, `ipn`,
`ipnstate` and `client/local`. The existing `tailnetNode` port leaks
`*ipnstate.Status` through `Up` and `Status`; the replacement port speaks
Connection's own types and publishes `Event`.

Taking ownership of the IPN bus watch means not calling `tsnet.Server.Up`, so
we take on what `Up` does beyond waiting for `ipn.Running`
(`tsnet/tsnet.go:533`):

| What `Up` does | How we do it |
|---|---|
| `s.LocalClient()`, which triggers `Start` | unchanged, we already call it |
| its own `lc.WatchIPNBus(NotifyInitialState)` | ours becomes the only one |
| fails on any `Notify.ErrMessage` | same, surfaced as `Failed` |
| `lc.Status` and a non-empty `TailscaleIPs` check | same call, we already have `Status` on the port |
| `resetServeStateOnce`: clear serve config and advertised services | skipped |

Skipping `resetServeStateOnce` is deliberate. It exists to clear serve config
and service advertisements persisted by an earlier run of a differently
configured program, and we call neither `SetServeConfig` nor set
`AdvertiseServices`, so there is nothing of ours in the bridge state dir for it
to clear. Both halves are reachable from exported API if that changes:
`lc.SetServeConfig` and `local.Client.EditPrefs` with `AdvertiseServicesSet`
(the unexported `s.lb.EditPrefs` that `Up` uses is equivalent). Revisit if the
CLI ever serves anything over a bridge.

`printAuthURLLoop` cannot be switched off: `go s.printAuthURLLoop()` is
unconditional in `start()` and no field or envknob guards it. Setting
`Server.UserLogf` to a no-op is the only way to stop its prose reaching us, and
that is what we do, because with a typed `LoginRequired` event its output is
not a source any more. So the watcher count goes three to two while a login is
outstanding, and to one after: `printAuthURLLoop` exits when the state leaves
`NeedsLogin`.

## Decision

1. Connection is one bounded context spanning bridge bring-up and the model
   fetch.
2. The boundary out of `internal/bridges` becomes a typed `Event` stream.
   Delete `bridges.AuthLogPrefix`, `tsnetAuthURLMarker` and the marker scraping
   in `authURLFromLog`; keep its URL rules as `ParseLoginLink`.
3. The port stops returning `*ipnstate.Status`. `internal/bridges` is the only
   package importing tsnet and friends.
4. Own the IPN bus watch. Stop calling `tsnet.Server.Up`, absorb its
   `TailscaleIPs` check, skip `resetServeStateOnce`, and silence `UserLogf`.
5. `Gateway` replaces `ApertureHost` at the boundary into `internal/clients`
   and `internal/profiles`.
6. `ConnectionAttempt` and `Crossing` are their own types. `Manager` does not
   grow fields; `Crossing` takes the node, its routes and its tailnet, which is
   most of what `Manager` holds today.

Artifacts land in `docs/specs/` and `docs/adr/`, and the conventions they
follow are recorded in the repo's `CLAUDE.md` rather than in any one person's
tooling.

## Consequences

Good:

- A wait has a name and a duration, so the next report of a slow connection is
  diagnosable from the screen rather than from a `SIGQUIT` dump.
- The browser opens because a `LoginRequired` event arrived, not because a log
  line matched a phrase in a vendored package.
- The login link cannot be dropped by debug chatter.
- One watcher instead of two inside `LocalBackend`'s lock.
- `internal/clients` stops receiving a field that means two things.

Bad, and accepted:

- We now own the bring-up loop, including `Notify.ErrMessage` handling and the
  `TailscaleIPs` check. If tsnet adds a step to `Up`, we will not get it.
- Phase detection reads several `Notify` fields (`State`, `BrowseToURL`,
  `LoginFinished`, `SelfChange`) whose exact ordering is upstream behaviour, not
  contract. `WatchIPNBus` is documented as unstable.
- `printAuthURLLoop` still runs and still calls `StatusWithoutPeers` every five
  seconds while a login is outstanding. Nothing we can do from outside tsnet.
- A migration: every `g.ApertureHost` reader changes.

## Alternatives considered

**Timestamp the log lines and stop there.** Already shipped (`53f2148`) and it
is what made the phases visible enough to name. It is not enough on its own:
the TUI still parses prose to decide to open a browser, the link still shares a
lossy channel, and an elapsed time against an unnamed line still does not say
which of two `NeedsLogin` waits you are in.

**Keep `tsnet.Server.Up` and add phases from the existing second watcher.**
Smaller diff, and it avoids owning the bring-up loop. Rejected because it keeps
two LocalAPI watchers on a backend that assumes one, and because a lagging
watcher is evicted with a terminal `ErrMessage`
(`closeLaggingWatchSessionLocked`) that `Up` converts into
`tsnet.Up: backend: IPN bus consumer fell behind`: a bridge failure with no
relationship to anything the user did.

**Patch tsnet upstream to publish phases.** The right long-term answer for
`printAuthURLLoop` and for phase signals generally, and worth raising. It does
not unblock this, and the ACL is what makes adopting it later a change in one
package.

## Revisit when

Upstream exposes bring-up phases directly, or the CLI starts serving anything
over a bridge (which puts `resetServeStateOnce` back in scope).
