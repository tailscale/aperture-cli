# 0003. Commit endpoint edits after verification and serialize each Machine

Status: accepted
Date: 2026-09-18

## Why?

Editing the active URL rewrote settings before the model check; Escape returned
to the agent menu with the replacement host and the previous host's providers.
An inline override could also reuse a node while the cancelled attempt was
closing it. Tailnet switching left shared endpoints launchable after closing
their proxies and required authorization before it could log out.

## Decision

1. Keep the original endpoint through an edit, retry and override. Commit the
   verified replacement and removal of the original in one settings write.
2. A Machine grants one cancellable operation at a time, including startup and
   cleanup. Its cache entry is not evidence of readiness.
3. Switching the active bridge invalidates its runtime before dispatch. Only
   successful verification restores launch actions, including after Escape.
4. Logout initializes the LocalAPI without awaiting `ipn.Running`.

Fields, states and contracts: [model](../specs/connection-domain-model.md#lifecycle-correction-implemented-in-this-pass)
and [contracts](../specs/connection-contracts.md#lifecycle-correction-contracts).

## Consequences

A failed edit retains its candidate alongside the working endpoint for retry.
An overlapping activation waits for the previous node's cleanup. Cancelling
a switch before logout starts may still require reconnecting: it is not proof
that the old gateway survived.

## Rejected

- Restore settings after failure: leaves an unverified host active during the
  attempt and requires a second fallible write to undo it.
- Lock the entire manager through startup: one login would block other bridges
  and prevent shutdown from cancelling the wait.
- Await authorization before logout: requires joining the network being left.

## Revisit when

Machines gain a persistent event stream or multiple independent subscribers;
then move runtime invalidation from switch intent to a durable lifecycle event.
