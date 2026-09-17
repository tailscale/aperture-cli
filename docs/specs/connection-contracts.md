# Connection contracts

Pass 1. Contracts for the objects in
[the domain model](connection-domain-model.md).

Two of the three contracts are deliberately absent, with reasons, rather than
left blank:

| Contract | Status | Reason |
|---|---|---|
| API | Absent | The CLI exposes no service surface. It has no callers, so there are no caller classes to enumerate, no authentication and no authorization rule. Its outbound calls are `GET /v1/models` on an Aperture we do not own and the in-process tsnet LocalAPI, both other people's contracts. The nearest thing we define is the `Machine` port, which is covered by the domain model's behaviours. |
| DDL | Absent | No relational store. Persistence is `settings.json`, a document rewritten whole. See "Persisted facts" below for the one place the DDL rules still bite. |
| Domain events | Defined below, 100% | The refactor's whole point is replacing a `func(string)` log sink with typed events, so these are the contract that exists. |

## Domain events

Delivery is the same for all six and stated once rather than repeated per row:
a single in-process buffered channel per Connection Attempt, consumed by the
bubbletea update loop. One producer, one consumer, ordered, no replay, no
persistence, closed when the Attempt ends. At-least-once does not arise; the
risk here is loss, not duplication, and the rule is in the per-event rows.

| Name | Emitting aggregate | Emitting transition | Payload | Consumers | Delivery | Boundary | Domain service |
|---|---|---|---|---|---|---|---|
| `PhaseEntered` | ConnectionAttempt | every `Enter(Phase)`, including into terminal phases | `Phase Phase`, `Progress Progress` | connect screen (label, elapsed, spinner) | never dropped; a lost phase breaks the `Trail` gap-free invariant | internal to Connection | none; `ConnectionAttempt.Enter` is the whole reaction |
| `LoginRequired` | Machine | `Starting → NeedsLogin`, on the first `Notify.BrowseToURL` | `Link LoginLink` | ConnectionAttempt (`Authorize`), connect screen (footer, ctrl+y copy, browser open) | never dropped; this is the event whose loss strands the user | internal to Connection | none; `ConnectionAttempt.Authorize` is a single aggregate method |
| `TailnetJoined` | Machine | `Joining → Open`, when the netmap carries a tailnet name | `Tailnet string` | ConnectionAttempt, Settings (`Bridge.Tailnet`) | never dropped; losing it silently un-labels the bridge in the picker | published, crosses into Settings | **missing.** See below. |
| `Noted` | Machine | none; not a transition | `Text string` | connect screen log pane only | droppable. The only droppable event, and the reason the others can state that they are not | internal to Connection | none |
| `Failed` | ConnectionAttempt | any phase `→ Failed` | `Err error` | connect screen, endpoint menu | never dropped; terminal | internal to Connection | none |
| `Ready` | ConnectionAttempt | `AskingForModels → Ready` | `Gateway Gateway`, `Providers []config.ProviderInfo` | Client Launch, connect screen, Settings (active endpoint) | never dropped; terminal | published, crosses into Client Launch | **missing.** See below. |

### Two events whose reaction has no home

The domain service column is the anti-anemia check, and it found two holes.
Both are cross-aggregate rules currently living in the TUI, which is an
application service and does not count.

`TailnetJoined` spans Machine and Bridge: "the Bridge records the tailnet its
Machine joined, so the picker can name it before the Machine exists again".
Today that is `model.recordBridgeTailnet` (`tui.go:421`), which reaches into
`Manager.Tailnet(bridgeID)` and then `g.SetBridgeTailnet`. The TUI is loading,
calling and committing, which is orchestration, but it is also deciding the
rule, which is not.

`Ready` spans ConnectionAttempt and whatever Client Launch reads: "a client
launched after a successful Attempt uses that Attempt's Gateway". Today that
is the assignment `m.g.ApertureHost = msg.host` (`tui.go:681`) into a shared
mutable global that five client packages read whenever they happen to run.
There is no object that owns "which Gateway is current", which is why the field
could come to mean two things without anyone deciding that it should.

Both need a home before the events are implemented. Naming them is out of scope
for this pass; that they are unowned is the finding.

### What shipped, and what the table is still describing

Three of the six are in `internal/connection` (`event.go`). The other three are
not, and the gap is deliberate rather than unfinished:

| Event | State | Why |
|---|---|---|
| `PhaseEntered` | Built, payload reduced to `Phase` | `Progress` is derivable: the connect screen already stamps every line with elapsed time from the Attempt's start, so carrying a duration in the event would be a second copy of the same clock, computed earlier and able to disagree. Add it when something off-screen needs the number. |
| `LoginRequired` | Built as specified | |
| `Noted` | Built as specified | |
| `TailnetJoined` | Not built | Blocked on the Bridge/tailnet ownership decision above. `Manager.Tailnet` and `recordBridgeTailnet` still carry it. |
| `Ready`, `Failed` | Not built | Both already travel as `endpointActivationResult` on the same channel, typed, with the same single consumer. Converting them buys nothing until the Gateway owner exists, and `Ready`'s payload is that owner's to define. |

Six `Phase` values are built, not nine. `Ready`, `Failed` and `Cancelled` are
in the domain model because they are real states of an Attempt, but nothing
emits a `PhaseEntered` for them today, and a constant no producer writes is a
constant a reader has to go and check. They arrive with the Attempt aggregate.

## Persisted facts

No DDL, but the schema rules still apply to `settings.json` and one of them
bites.

`Bridge.Tailnet` is empty until a Machine joins one, which is the JSON-document
form of a nullable `joined_at` on the primary row: a field about something that
has not happened, sitting empty on every bridge the user has created and not
yet connected. Under the rule it should be its own fact, keyed by bridge id,
with presence meaning joined.

Recorded exception, and the reason, next to the thing it applies to: the store
is a single document rewritten whole, there is exactly one tailnet per bridge
at a time, and nothing wants the history. Splitting it would add a collection
to a settings file to model an absence that the picker already renders as
"tailnet not known yet". Revisit if a Bridge ever needs to remember more than
its current tailnet, at which point it needs a real store anyway.

Everything else persisted is unconditional: `Endpoint.URL`, `Endpoint.BridgeID`
(empty means direct, which is a real value and not an absence), `Bridge.ID`,
`Bridge.Name`.

## Cross-check

With no API and no DDL, the cross-check reduces to: every aggregate transition
emits an event, or is recorded here as deliberately silent.

| Transition | Event | Note |
|---|---|---|
| ConnectionAttempt → `StartingMachine` | `PhaseEntered` | |
| → `AwaitingLoginLink` | `PhaseEntered` | the phase that did not exist |
| → `AwaitingAuthorization` | `PhaseEntered`, preceded by `LoginRequired` | |
| → `JoiningTailnet` | `PhaseEntered` | |
| → `FindingEndpoint` | `PhaseEntered` | |
| → `AskingForModels` | `PhaseEntered` | |
| → `Ready` | `PhaseEntered`, `Ready` | |
| → `Failed` | `PhaseEntered`, `Failed` | |
| → `Cancelled` | `PhaseEntered` only | Deliberately silent beyond the phase. Cancellation is initiated by the consumer, so an event telling it what it just did carries nothing. The `Trail` still records it, which is what a later "why was this slow" question needs. |
| Machine `Starting → NeedsLogin` | `LoginRequired` | |
| Machine `Starting → Joining` | none | Deliberately silent. The credentials-on-disk path has nothing to tell the user and no cross-aggregate reaction; the Attempt's own `PhaseEntered` covers the screen. |
| Machine `NeedsLogin → Joining` | none | Same. The authorization that caused it is already on screen. |
| Machine `Joining → Open` | `TailnetJoined` | |
| Machine `→ Closed` via `Close` | none | Deliberately silent. Process teardown; there is no consumer left to react. |
| Machine `→ Closed` via `LeaveTailnet` | `Noted` | Weak, and knowingly so. The user asked to switch tailnets and wants to see it happen, but nothing reacts to it, so it does not earn a typed event yet. Promote it if Settings ever needs to clear `Bridge.Tailnet` on logout, which it arguably already does. |

Two invariants from the model have no enforcement point outside application
code, which the skill flags and no constraint layer here can fix:

- "Exactly one terminal outcome" and "phase only moves forward" are enforced by
  `ConnectionAttempt`'s own methods and nothing below them. With no database
  there is no check constraint to back them, so the constructor and the
  unexported fields are the whole guarantee. That makes "no exported fields, no
  setters" load-bearing rather than stylistic.
- "At most one Machine per Bridge" is enforced by a map keyed on bridge id
  under a mutex. Same situation.

## Next pass

Triggered by either of the two unowned reactions finding a home, or by the
model changing under implementation. Contract work is expected to loop back
into the model; this pass sent two findings there.
