# Headless CLI and persistent serve

A command grammar for aperture alongside the TUI: config subcommands that
mutate settings.json, direct client launch with argument passthrough, and
`serve`, a persistent fixed-port bridge that replaces the ts-unplug sidecar.
No arguments keeps the TUI. Specs and ADRs land before code.

Decisions are [ADR 0007](../docs/adr/0007-headless-cli.md) (grammar, cobra,
passthrough, naming, `--config`) and
[ADR 0008](../docs/adr/0008-persistent-bridge-fixed-port.md) (`serve`, fixed
port on the Bridge record, no daemon).

## Grammar

```
aperture                                   # TUI, unchanged
aperture bridge add <name> [--port N]
aperture bridge list
aperture bridge describe <name>
aperture bridge delete <name>
aperture endpoint add <name> [--bridge <name>] <url>
aperture endpoint list
aperture endpoint delete <name>
aperture serve --bridge <name> [--port N] [--endpoint <name|url>]
aperture <client> [--endpoint <name|url>] [--bridge <name>] [--provider p] [--model m] [-- client args...]
aperture --config <path> <command>          # or APERTURE_CONFIG
```

Endpoints and bridges are addressed by Name, with raw-URL fallback for
endpoints. In a client command, aperture consumes its own flags anywhere in
the tail; the first unknown flag or `--` starts verbatim passthrough, so
`alias codex='aperture codex'` works.

## Steps

1. ADR 0007 and ADR 0008.
2. Specs: context map (Name and Port language rows, `bridge` command
   disambiguation), domain model (Route fixed-port invariant), contracts
   (command line becomes the API section; persisted facts gain Endpoint.Name
   and Bridge.Port).
3. config: `endpointRecord` gains `name,omitempty`; lookup by name with URL
   fallback; `Bridge` gains `Port int`; settings path override for
   `--config`. Round-trip tests: old files load unchanged.
4. internal/cli: cobra command tree (cobra + pflag are the one new
   dependency; Viper is rejected in ADR 0007). Root with no args runs the
   TUI. Pure passthrough parser with table tests. Every command's `--help`
   carries example invocations, with a test holding them to the contract's
   grammar.
5. bridge and endpoint command groups. Delete goes through
   `Machines.Destroy`. Name uniqueness enforced at add.
6. serve: `RouteTo` gains a listen port (0 keeps current behavior). Bind
   failure errors fast. Login link prints to stderr. SIGTERM closes Machines.
7. clients: one headless launch entry on the Client interface; provider and
   model empty means LastLaunch, then first compatible. Nine mechanical
   implementations over the existing launch builders. Registry gains lookup
   by binary name.
8. main.go: hand os.Args to the cobra root instead of flag.Parse.
9. e2e on the existing harness: command round-trips, headless launch of a
   stub binary with passthrough asserted, serve on a fixed port.
10. README: the grammar, the alias recipe, a sample systemd user unit for
    serve (documentation only).

## Watch

- pflag does not parse single-dash long flags. `-endpoint` users move to
  `--endpoint`; the env vars are unchanged. Recorded in ADR 0007.
- A bound serve port is a hard error, never a fallback to ephemeral.
- Login links go to stderr, never the run log. The redaction contract holds.
- `bridge delete` refuses while a serve holds the slot lock. That is
  `Machines.Destroy`'s existing rule and the right one.
- `make check` is the gate.
