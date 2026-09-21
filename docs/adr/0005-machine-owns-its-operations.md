# 0005. Machine owns its operations, Machines holds them, Attempt owns its transitions

Status: accepted
Date: 2026-09-21

## Why?

`Manager` grew from four fields on `main` to eight on this branch the day
after [ADR 0001](0001-connection-bounded-context.md) decision 6 said it would
not grow. Every behaviour the domain model gives `Machine` (`Open`, `RouteTo`,
`LeaveTailnet`, `Close`) was a `Manager` method taking `rt *Machine` as an
argument, and `Machine` as built shared no field with `Machine` as modelled.
The model document described two different objects under one name. The lock
around a Machine's operations was named `acquire` and documented as granting
"a cancellable turn", words in neither the code's nor the model's vocabulary,
and the phrase had reached ADR 0003 and the contracts before anyone asked
what it meant.

Meanwhile the TUI decided domain rules it should only have displayed: whether
a removal destroys a device, when the Bridge record goes, when the tailnet a
Machine joined is recorded on its Bridge, when an endpoint edit commits, which
candidate an abandoned attempt takes back out. Twenty-eight sites, two of
which the model already listed as "the TUI orchestrates but should not
decide".

## Decision

1. `Machine` has its modelled behaviours: `Open`, `RouteTo`, `LeaveTailnet`,
   `Destroy`, `Close`, `Tailnet`. The one-operation-at-a-time rule is its
   private detail; nothing outside it takes a lock.
2. `Machines` is a collection: creates a Machine per Bridge on first use,
   closes them all once. It does no network work.
3. `Attempt` (the model's ConnectionAttempt) lives in `internal/bridges` and
   owns its transitions: `BeginAttempt`, `Run`, `Commit`, `Abandon`,
   `Retarget`. Commit rather than Succeed, because it persists a result Run
   already produced and decides nothing. The TUI's `activation` holds
   presentation state only.
4. Removing a Bridge is the one transition no aggregate owns, and it gets
   functions named for the nouns it acts on rather than a process object:
   `DestroysMachine`, `Machines.Destroy`, `ForgetBridge`. No service type.
   The first attempt at this was a `Bridging` service and a `Removal` value,
   both names for activities rather than things, and both went in review.
5. Every operation that waits on the network is split from the one that
   writes settings. `Run` and `Machines.Destroy` may run anywhere and write
   nothing; `BeginAttempt`, `Commit`, `Abandon`, `DestroysMachine` and
   `ForgetBridge` run on the update loop.
6. `Manager` is deleted. No compatibility wrapper.

## Consequences

The two-phase API is imposed by bubbletea, not chosen: commands run off the
update loop and `config.Global` has no lock, so a settings write in `Run`
would race every view that reads it. The race detector is the gate here
(`make check`), so the split is what keeps the gate honest. It costs each
caller two calls where it made one.

A superseded attempt could commit if its result arrives before the newer
attempt's; the TUI's id check still discards it, and `Commit` runs only for
the attempt on screen. Unchanged from before.

`Bridge.Tailnet` is still written by the service on verification rather than
on join, because writing on join would happen in `Run`. `Machines.Tailnet`
covers the gap by preferring what the running Machine reports.

The two rules the model still leaves unowned stay in the TUI as a display
flag: whether the active destination is verified. `InvalidatesActive` and
`TargetsActive` on the Attempt are the rule's answer; the TUI keeps the bit. Naming the object that owns "the
current Gateway and whether it is verified" is the next modelling step.

## Rejected

- **Rename `acquire` and move on.** Fixes the word, not the shape. The
  operations would still live on a role-named object with the entity passed
  in as an argument.
- **Leave it for APT-330, which deletes `Manager` anyway.** APT-330 stacks on
  this branch and its spec warns that `Sessions` "must remain a collection,
  not a renamed manager". Landing a larger manager for it to remove makes its
  rebase bigger and leaves the model wrong on `main` in the meantime.
- **Write settings from the goroutine and lock `Global`.** Every read in the
  view would need the lock too; `Global` is read on nearly every frame.

## Revisit when

APT-330 lands: `Machines` and `Machine` become one launcher's view of a shared
helper, and `Attempt` should survive that with its signatures. Or an object
owning the current Gateway exists, at which point `connected` leaves the TUI
and `InvalidatesActive` and `TargetsActive` lose their reason to exist.
