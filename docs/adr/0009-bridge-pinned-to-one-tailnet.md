# 0009. Pin a bridge to one tailnet

Status: accepted
Date: 2026-11-24

## Why?

People in more than one tailnet log bridges in to the wrong one, and it
happens a lot. The login link offers every tailnet the account is in.
Nobody notices until a dial fails. Scripts have it worst, because nobody
watches a `bridge add` then `serve` under a supervisor.

## Decision

1. `bridge add <name> --tailnet-id <id>` stores a Tailnet ID on the
   Bridge. Empty means any tailnet.
2. Each Machine of a pinned Bridge sets `tsnet.Server.Tailnet`, so
   registration asks control for `required:<id>`.
3. Bring-up fails a Running Machine unless its status reports that ID.
   Open closes the node when it fails.
4. `bridge add` and `Machines.For` refuse an ID with a comma, colon or
   space. Control parses the value as a list behind `required:`.
5. `tailscale.com` is pinned to the merge of
   [tailscale/tailscale#22606](https://github.com/tailscale/tailscale/pull/22606)
   on main. It's a pseudo version, so we don't release from the pin.

## Consequences

We own the bus watch (ADR 0001), so decision 3 copies an unexported
check from `tsnet.Server.Up`. Upstream changes to it get copied by hand.

Control doesn't hold an auth key to the pin. On 2026-11-24 a
`TS_AUTHKEY` from another tailnet came up Running despite
`required:<id>`. Decision 3 is the only guard there, and the refused
node stays registered until the bridge is deleted.

Existing bridges can't be pinned, so the user deletes and re-adds one.
The TUI can't set the pin. The ID comes from the admin console.

## Rejected

- A `--required-tailnet` flag per run. Forget it once and the bridge is
  back in the wrong tailnet. It also pins every bridge in the process.
- A syspolicy store on v1.102.3. It's process wide too, and that
  version's status has no tailnet ID to check.
- Pinning by tailnet name. A rename changes it, and control won't take
  one.

## Revisit when

A tagged `tailscale.com` release carries `Server.Tailnet`, control
starts holding an auth key to the pin, or someone needs to pin a bridge
they already have.
