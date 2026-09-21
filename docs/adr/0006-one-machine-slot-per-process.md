# 0006. One Machine slot per concurrent process

Status: accepted
Date: 2026-09-21

## Why?

APT-330: five to ten agent sessions each running aperture in bridge mode leave
only the last-started one working; the rest hang and their clients get
"api error". Every process used the same state directory for the same Bridge,
so every process registered the same node key under the same hostname. The
control plane treats same-key connections as one node and hands the session to
the newest registrant; the older processes keep reporting themselves Running
locally while their dials silently reach nothing. The state directory was also
written concurrently, which risks corruption on top. The in-process rule "two
Machines for one Bridge would open the same state directory" never covered the
two-process case, and the failure mode it produced was silent.

## Decision

1. A Bridge's Machine identity is numbered by slot. Slot 1 keeps the existing
   state directory and hostname (`bridges/<suffix>`,
   `aperture-cli-<bridgeID>`); slots 2 and up get sibling directories
   `bridges/<suffix>-N` and hostnames `aperture-cli-<bridgeID>-N`. Existing
   users keep the device they already authorized.
2. A process claims the lowest free slot when it builds the node, and holds
   it until the Machine is closed or destroyed. The claim is an exclusive
   non-blocking lock on `bridges/locks/<suffix>-<slot>.lock` (flock where
   there is flock, LockFileEx on Windows), held open for the node's life.
   Lock files live outside the state directory so removal never deletes an
   open lock and a claimant's inode can never be deleted under it.
3. `Machines.Destroy` logs out every slot the bridge has on disk. A slot
   locked by a live process makes the whole removal fail before anything is
   logged out, naming the conflict, because evicting a running session is
   exactly the silent kill this ADR exists to remove.
4. Cap: 100 slots per bridge. Past that the process errors instead of
   probing forever.
5. Slot claiming emits no event and writes no settings. It is recorded in
   the contracts as deliberately silent.

## Consequences

Each concurrent process is its own device in the admin console: ten parallel
agent sessions are ten devices named `aperture-cli-<bridgeID>` through
`-10`. They accumulate when processes die unclosed (the devices go offline
and stay listed) and re-authorize only when a slot has never been authorized
before — once per slot, not once per launch. The failure mode for a
contended bridge moves from silent eviction to either a fresh slot (normal
case) or a named error (removal).

## Rejected

- One shared bridge daemon per machine, CLI instances dialing it over a local
  socket — the correct architecture and roughly what `tailscaled` already is;
  it costs a wire protocol, a trust boundary and a daemon lifecycle this CLI
  does not otherwise have, for a first iteration that needed to make parallel
  sessions work this week.
- A fresh ephemeral node per process — key expiry on ephemeral nodes means a
  browser login on every launch, which is worse than the bug for the reported
  workflow.
- Failing loudly on the second process with no sharing at all — converts
  silent breakage into loud breakage; the report asked for the sessions to
  work, not to be refused.

## Revisit when

The admin-console litter or the slot cap becomes the problem someone files,
or a daemon is wanted for its own reasons (credential renewal, one
connection to share). The lock files and slot layout are additive on top of
slot 1's legacy directory, so a daemon migration does not orphan existing
state.
