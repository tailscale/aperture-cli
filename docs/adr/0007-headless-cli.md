# 0007. Headless CLI with a cobra command tree

Status: accepted
Date: 2026-10-13

## Why?

Every launch walks the TUI: pick the client, pick the provider, pick the
model. A developer who wants `alias codex='aperture codex'` cannot have it,
because aperture has no verb for "launch codex". Configuration has the same
shape: adding a bridge or an endpoint is menu navigation, so nothing about an
aperture setup can live in a script, a Makefile or a teammate's paste. The
existing `-endpoint` and `-bridge` flags resolve a destination and then still
open the TUI. The failure is not missing flags; it is that the TUI is the
only interface, and menus do not compose with a shell.

## Decision

1. The command tree is cobra. No arguments starts the TUI. `bridge`,
   `endpoint` and `serve` are subcommand groups. Each registered client gets
   a command named by its binary (`aperture codex`).
2. Flags take GNU form through pflag: `--endpoint`, `--bridge`, `--provider`,
   `--model`, `--port`, `--config`, `--debug`. The single-dash long forms
   (`-endpoint`) stop parsing. The environment variables are unchanged.
3. A client command parses its own tail. Aperture consumes its known flags
   wherever they appear. The first unknown flag, or `--`, starts verbatim
   passthrough to the client. The parser is a pure function, not cobra.
4. Commands address endpoints and bridges by Name. An endpoint value that
   parses as a URL is an inline endpoint instead. Names are unique per kind
   at add time. Old settings without names load unchanged.
5. A headless launch takes provider and model from `LastLaunch` when they are
   still valid for that client. Otherwise it takes the first compatible
   provider and its default model. `--provider` and `--model` override.
6. `--config <path>` and `$APERTURE_CONFIG` select an alternate
   settings.json. The schema does not change; the flag picks the file.
7. Scripted surfaces stay scriptable: data goes to stdout, diagnostics and
   login links go to stderr, failures exit non-zero. The login link never
   enters the run log, in CLI mode as in the TUI.

## Consequences

cobra and pflag join go.mod. Single-dash long flags break; the binary is
still B0 and the env vars cover containers and units, so the break is
announced rather than shimmed. Each of the nine client packages grows a
headless launch entry next to its menu flow. The TUI and the CLI edit the
same settings file, so a change made in one is visible in the other, and the
name-uniqueness rule applies to both.

## Rejected

- stdlib `flag` with hand-rolled dispatch. Fine for the four flags today. The
  grammar is ten commands plus a dynamic client set, and cobra's help,
  usage and flag-error behavior would be re-implemented by hand.
- Viper. settings.json is a domain-owned schema with atomic writes and
  load-time defaults. The only file feature needed is a path flag. Viper's
  file, env and flag merging would put a second source of truth next to the
  first, and its dependency tree is heavy for that.
- Requiring `--provider` and `--model` when more than one fits. Predictable
  for scripts, hostile to the alias case, which is the common case.
- No names on endpoints. The smallest diff, and the one that has the user
  typing URLs or bridge IDs every day.

## Revisit when

A client ships a flag that collides with an aperture flag and `--` proves too
awkward to escape it, or configuration grows enough knobs that file, env and
flag precedence needs a real merger. The second is Viper's actual use case.
