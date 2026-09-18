# 0004. Scope peer names, keep login links out of shells and logs, and join shutdown

Status: accepted
Date: 2026-09-18

## Why?

A shared-in peer named `ai` could receive requests intended for the selected
tailnet. A control-plane URL could become a Windows shell command, while the
normal diagnostic log retained machine-authorization links. A second quit
could also report shutdown complete while the first was still closing nodes.

## Decision

1. Match bare peer names only within the current Machine's MagicDNS suffix;
   retain explicit FQDN access to shared peers.
2. Open Windows login URLs through `ShellExecuteW`, using the existing
   `golang.org/x/sys/windows` dependency. Never pass them through `cmd.exe`.
3. Persist the login-required fact, not its URL, in Aperture's run log. Remove
   inputs from link-parse errors and redact raw bridge diagnostic URLs entering
   that log at every level.
4. Use `sync.OnceValue` so every shutdown caller joins the same cleanup and
   receives the same result.

The [model](../specs/connection-domain-model.md#security-correction) and
[contracts](../specs/connection-contracts.md#security-correction-contracts)
define the boundaries, failure cases and unchanged persistence model.

## Consequences

Unknown tailnet suffixes cannot supply short-name aliases. Diagnostics lose URL
detail; older run logs and the SDK's separate logtail still require care.
Repeated quit requests wait for cleanup instead of bypassing it. Native Windows
execution needs a Windows
test environment; cross-building alone cannot establish desktop behavior.

## Rejected

- Escape shell punctuation: maintains a second command-language parser where
  the operating system already accepts a URL directly.
- Keep links in owner-only logs: permissions do not follow diagnostics when
  users share them for support.
- Ignore repeated quit in the TUI: leaves other callers of `Close` with the
  same incorrect completion contract.

## Revisit when

Diagnostic URLs become necessary for support, or shutdown needs an explicit
force-exit operation. Define those permissions separately from normal logging
and successful cleanup.
