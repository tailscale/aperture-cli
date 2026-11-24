# 0009. A bridge can be pinned to one tailnet

Status: accepted
Date: 2026-11-24

## Why?

Users who belong to several tailnets log a bridge in to the wrong one,
and it happens often. The login link lets them pick any tailnet their
account is in, and nothing tells them they picked wrong until a dial
fails. Scripted setups have it worst: `bridge add` then `serve` under a
supervisor, with nobody watching which tailnet the link landed in.
tailscaled solves this with the `Tailnet` system policy set to
`required:<stable-tailnet-id>`, and customers have asked for the same
on aperture.

## Decision

1. A Bridge carries an optional Tailnet ID, set once by
   `bridge add <name> --tailnet-id <id>`. Empty means unpinned, today's
   behaviour.
2. Every Machine of a pinned Bridge sets `tsnet.Server.Tailnet`, so its
   registration asks control for `required:<id>` and control refuses a
   login to any other tailnet.
3. Bring-up fails a Running Machine of a pinned Bridge unless the status
   reports that Tailnet ID. A missing ID fails too. The failed Open shuts
   the node down, so nothing dials through it.
4. `bridge add` refuses an ID containing whitespace, a comma or a colon.
   Control reads the value as a comma separated list with a `required:`
   prefix, so those characters would change what is asked for.
5. `tailscale.com` is pinned to the head of tailscale/tailscale#22606
   until a release carries `Server.Tailnet`. No aperture release ships
   from the pin.

## Consequences

Bring-up repeats `tsnet.Server.Up`'s tailnet check because we own the
bus watch instead of calling `Up` (ADR 0001, decision 4). The check is
unexported upstream, so it is a second copy, and a change to its rule
upstream has to be followed here by hand.

The pin cannot be added to an existing bridge. A user with a bridge
already in the wrong tailnet deletes it and adds it pinned. A pinned
bridge whose saved state is in another tailnet fails every Open until
then. Switching tailnet from the picker also fixes it: the logout
clears the state and the next login is pinned.

The TUI cannot set the pin. `bridge add` is the only way in, which
covers the scripted case that asked for it.

The user finds the ID in the admin console. Aperture does not look it
up or offer a list.

## Rejected

- A process wide `--required-tailnet` flag. It is easy to forget on one
  invocation, and forgetting it once is the mistake this exists to
  stop. It would also pin every bridge in the process to one tailnet.
- A syspolicy store on v1.102.3, which already sends a policy Tailnet on
  register. tsnet has no per server policy client, so the store is
  process wide with the same problem, and v1.102.3's status carries no
  stable tailnet ID to check against after login.
- Pinning by tailnet name, the existing `Bridge.Tailnet`. A rename
  changes it, and control does not accept a name as a required tailnet.

## Revisit when

#22606 merges in a different shape (its review floated a list of
suggested tailnets), or users ask to pin a bridge they already have.
