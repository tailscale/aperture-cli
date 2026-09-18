# 0001. Connection is one bounded context, and it publishes events, not log lines

Status: proposed
Date: 2026-09-17
Change size: large. Touches `internal/bridges`, the activation half of
`internal/tui`, and the `ApertureHost` boundary into `internal/clients`.

## Why?

A bridge that had never been connected took 29 seconds to come up and the
screen showed nothing but tsnet's `NeedsLogin`. The goroutine dump puts it
inside the first `POST /machine/register` (`controlclient/direct.go:853`) with
no follow-up URL, so no login link existed yet. That is a distinct thing to be
waiting on and it is indistinguishable on screen from waiting for the user to
finish in the browser, because both are `ipn.NeedsLogin`.

Three changes have already been aimed at that wait without the information to
aim: `5ebdb59` opens the browser at the link, `80d679f` reads the link off the
IPN bus, `82fab61` resolves the target against the node's peer map. Each is
correct and none could have shortened this wait, because none is in the phase
the wait was in. Four mechanisms carry progress (a `func(string)` sink, a
`chan bridgeLine`, tsnet's own prose, an `*ipnstate.Status` return) and not one
of them names what the attempt is waiting on, so a fourth fix would be aimed
the same way.

Two defects fall out of the same shape. The TUI recovers the login link by
matching a phrase inside tsnet's log text (`"or go to: "`, `browser.go:16`), so
a reworded upstream line silently stops the browser opening. And the link
travels on a 32-slot channel that drops on overflow (`tui.go:557`), shared
under `--debug` with the tsnet backend logger, so chatter can discard the one
line the user cannot proceed without.

Model, language and the anti-corruption layer:
[context map](../specs/connection-context-map.md),
[domain model](../specs/connection-domain-model.md),
[contracts](../specs/connection-contracts.md).

## Decision

1. Connection is one bounded context spanning bridge bring-up and the model
   fetch. The user experiences one wait.
2. The boundary out of `internal/bridges` becomes a typed `Event` stream.
   Delete `AuthLogPrefix`, `tsnetAuthURLMarker` and the marker scraping in
   `authURLFromLog`; keep its URL rules as `ParseLoginLink`.
3. The port stops returning `*ipnstate.Status`. `internal/bridges` is the only
   package importing tsnet and friends.
4. Own the IPN bus watch. Stop calling `tsnet.Server.Up`, absorb its
   `TailscaleIPs` check, skip `resetServeStateOnce`, silence `UserLogf`.
5. `Gateway` replaces `ApertureHost` at the boundary into `internal/clients`
   and `internal/profiles`.
6. `ConnectionAttempt` and `Machine` are their own types. `Manager` does not
   grow fields; `Machine` takes the node, its routes and its tailnet.

## Consequences

A wait has a name and a duration, so the next slow connection is diagnosable
from the screen rather than from a `SIGQUIT` dump. The browser opens because a
`LoginRequired` event arrived, the link cannot be dropped by debug chatter, and
one watcher runs inside `LocalBackend`'s lock instead of two.

The cost is that we own the bring-up loop, including `Notify.ErrMessage` and
the `TailscaleIPs` check, so a step added to `Up` upstream will not reach us.
Phase detection reads `Notify` fields whose ordering is upstream behaviour and
not contract. `printAuthURLLoop` still runs and still calls
`StatusWithoutPeers` every five seconds while a login is outstanding, and
nothing outside tsnet can stop it. Every `g.ApertureHost` reader changes.

## Rejected

- **Timestamp the log lines and stop there.** Already shipped (`53f2148`) and
  it is what made the phases visible enough to name. The TUI still parses prose
  to decide to open a browser, and an elapsed time against an unnamed line
  still does not say which `NeedsLogin` wait you are in.
- **Keep `tsnet.Server.Up` and add phases from the existing second watcher.**
  Smaller, and it keeps two watchers on a backend that assumes one. A lagging
  watcher is evicted with a terminal `ErrMessage` that `Up` reports as
  `IPN bus consumer fell behind`: a bridge failure unrelated to anything the
  user did.
- **Patch tsnet upstream to publish phases.** The right long-term answer and
  worth raising. It does not unblock this, and the ACL is what makes adopting
  it later a change in one package.

## Revisit when

Upstream exposes bring-up phases directly, or the CLI serves anything over a
bridge, which puts `resetServeStateOnce` back in scope.
