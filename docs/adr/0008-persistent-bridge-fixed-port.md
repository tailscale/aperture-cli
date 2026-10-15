# 0008. A persistent bridge on a fixed port

Status: accepted
Date: 2026-10-13

## Why?

Developers run ts-unplug as a sidecar on a fixed port, then configure an
aperture endpoint to use that port. Two tools do one job, and the sidecar is
not the signed binary, which blocks team use cases aperture-cli's signing
covers. The fixed port is the point of the sidecar: a client config needs a
stable address to point at, and aperture's routes bind `127.0.0.1:0`, a
different port every run.

## Decision

1. `aperture serve --bridge <name>` runs one Machine and one Route on a fixed
   loopback port until SIGTERM or SIGINT. `serve` is its own verb. The
   `bridge` command group configures and never runs anything.
2. The port lives on the Bridge record, set by `bridge add --port`.
   `serve --port` overrides it for one run. A serve with neither a stored
   port nor the flag errors, naming both. A port already bound is a hard
   error, never a fallback to an ephemeral port.
3. Client launches through a bridge keep binding `127.0.0.1:0` even when the
   Bridge has a port. The fixed port belongs to serve, so a launch never
   collides with the standing listener.
4. Serve fronts the endpoint given by `--endpoint`, a name or a URL. The
   default is the well-known `http://ai`.
5. Serve is a foreground process. It installs nothing. The operator's
   supervisor (a systemd user unit, launchd) owns restart.

## Consequences

A serve holds its Machine slot for its whole life, normally slot 1. A client
launch through the same bridge claims a sibling slot and registers a sibling
device in the admin console, which is ADR 0006 working as designed.
`bridge delete` refuses while a serve holds the slot lock, because
`Machines.Destroy` already fails on a locked slot; the error names the
conflict. One serve fronts one endpoint. Two endpoints mean two serve
processes, two slots and two ports.

The fixed listener is loopback only, and nothing authenticates the requests
it forwards: the Route strips the client Authorization header because the
Machine's tailnet identity is the ingress auth. Every process on the host can
therefore reach the user's Aperture through a serve, and on a shared machine
so can every other user, since loopback is not per-UID. This is the same
exposure ts-unplug has, which serve replaces deliberately, and single-user
workstations are aperture's audience. A listener that demands a local token
is the revisit path if a multi-user deployment ever appears.

## Rejected

- A daemon mode that installs and manages a service unit. That is a daemon
  lifecycle, a wire protocol and a trust boundary the CLI does not otherwise
  have, the same shape ADR 0006 rejected. A sample unit in the README covers
  the reported workflow.
- Keeping ts-unplug for this. Two binaries where one is unsigned, and the
  signed one already knows how to run a Machine.
- A default-ephemeral serve. A random port re-creates the problem the
  sidecar solves: nothing stable to point a client at.
- Bare `bridge` with flags as the serve command. One word doing two jobs,
  and `aperture help bridge` would confuse them forever.

## Revisit when

Serve needs several endpoints on one port, which means a mux and a routing
rule, or a shared daemon is wanted for its own reasons. The second is ADR
0006's revisit condition and the two should reopen together.
