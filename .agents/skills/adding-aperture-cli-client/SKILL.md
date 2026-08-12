---
name: adding-aperture-cli-client
description: "Add a new coding agent (a \"harness\", called a \"client\" in the code) to the aperture-cli launcher, so it appears in the menu, installs and uninstalls itself, routes through an Aperture endpoint, and replays on quick-select. Use when adding or modifying a client in the aperture-cli repo, or when debugging why a client does not appear in the menu or never offers quick select. To verify a client use testing-an-aperture-cli-client. For the Aperture server repo use aperture-dev."
---

# Adding a client to aperture-cli

`aperture-cli` is the launcher that starts coding agents preconfigured against an Aperture endpoint. Each agent is a **client**: a sub-package of `internal/clients` implementing the `clients.Client` interface. Users and vendors say "harness" or "agent"; the code says "client". Use "client" in code, comments, and commit messages.

This skill covers adding one command-line client. Desktop apps (Claude Cowork) live in `internal/profiles` behind an adapter and are out of scope.

## Before writing any Go

**Establish the harness contract first, and do not invent it.** This is the one input the codebase cannot give you, and getting it wrong produces a client that compiles, passes tests, and silently fails to route. Research the harness's own docs and source for:

- the env var it reads for the API base URL, and for the API key
- the env var for a default model, if any
- whether it requires a config file on disk, and that file's real schema
- the flag that skips permission prompts, if any
- which wire protocol(s) it speaks, mapped to compatibility keys
- whether it validates the base URL in a way that rejects `http://ai` — Gemini CLI does, see `validateHost` in `internal/clients/gemini/gemini.go`

Confirm the contract by launching the harness by hand with those values set before writing Go. If the harness has no way to accept a custom base URL, stop and say so: the task is infeasible, not a thing to work around.

Two answers shape everything downstream. **Routing style:** env-vars-only gives a short client (see `copilot`), a config file means writing one per launch and pointing the harness at it with one env var (see `opencode`, `codex`, `gemini`). **Protocol count:** one protocol needs a single `compatKey` const (`codex`); several with a user choice needs a `backend` struct and a `backendStep` (`copilot`, `gemini`); several resolved automatically needs a resolver (`opencode`'s `pickSDK`).

## The full procedure

The repo ships a step-by-step guide covering every method, snippet, and checkpoint: **`docs/adding-a-client.md`**, at the root of the aperture-cli checkout. Read it and follow it. It was independently verified against the code — both routing variants were built from scratch and confirmed to pass every CI gate — so trust it over your own recollection of Go idiom here.

`internal/clients/pi` is the most complete worked example: a four-protocol client that routes through a generated per-launch extension rather than env vars, with an in-package test file covering every unexported helper.

This skill is the orientation layer: the traps below are the ones that cost real time, and several are invisible to the compiler.

## Non-obvious facts about this codebase

**The interface has nine methods, all on pointer receivers.** `Name`, `BinaryName`, `CommonPaths`, `IsInstalled`, `Install`, `Uninstall`, `Menu`, `Replay`, `QuickSelectLabel` — defined in `internal/clients/registry.go`. A `Client` receiver instead of `*Client` fails the interface check with a confusing message.

**Registration is a side effect, and forgetting it fails silently.** Your `init()` calls `clients.Register(&Client{})`, but `init()` only runs if the package is linked, which is what the blank-identifier import block in `cmd/aperture/main.go` is for. A missing import produces no error at all — the client just does not exist.

**You cannot choose your menu position.** Registration order is display order, and it follows that import block's order — which gofmt sorts alphabetically. Insert your import in correct alphabetical position; appending it leaves the file unformatted and fails CI. Position is decided by package name, full stop. Wanting a different order means adding a real ordering mechanism to `internal/clients`, not hand-editing imports.

**`Install.Run` and `Uninstall.Run` take different shapes.** `Install.Run` passes one string to `/bin/sh -c`, so pipes work. `Uninstall.Run` has no shell, so you must split the command into separate arguments yourself. Passing the whole command as one argument compiles and then fails at runtime with `fork/exec ...: no such file or directory`. No build or test step catches this.

**Trim the trailing slash before appending a path.** Use `strings.TrimRight(g.ApertureHost, "/")`. Users do configure `http://ai/`, and preflight tolerates it, so untrimmed concatenation yields `http://ai//v1`. `copilot` and `gemini` trim; `codex` and `opencode` do not and are inconsistent — follow the ones that trim.

**Strip the provider prefix out of model names.** The launcher displays models as `provider_id/model_id`; harnesses want the bare ID. Leaving the prefix on breaks path-based routing and produces a puzzling 404 rather than a clear error.

**Empty provider list is an error, one option auto-descends.** Every client follows this: zero compatible providers returns an error result rather than an empty menu, exactly one descends straight to the next step without making the user press Enter, more than one shows a submenu as `Result.Next`. Zero *models* is not an error — pass an empty model string and let the harness pick.

**Compat keys are not centrally defined.** Each client declares its own. `opencode`'s `compatKeys` is the longest list but is not complete: `gemini` uses `experimental_gemini_cli_vertex_compat`, absent from it. Grep `compatKey\|compatKeys` across `internal/clients/` for the real set, and confirm against a live `GET /api/providers`.

**Use `config.ClientConfigDir`, not a hand-built path.** It returns `<UserConfigDir>/aperture/clients/<name>` and creates it `0o700`. Write files `0o600`. Config files hold the endpoint URL, which reveals a tailnet hostname, and per-launch files need the `Cleanup` closure passed through to `LaunchSpec` or they accumulate.

**Credentials are always placeholders.** Aperture authenticates over Tailscale, so the harness's key check has nothing to validate. Existing clients use `not-needed`, `not-required`, or a bare `-`. Never add a path that reads a real key from the environment and forwards it into a file the launcher writes. Note `-debug` dumps the whole env map to stderr.

## Testing

Tests live **in-package** (not `_test`) so they reach unexported helpers. Set `testHost = "http://ai.example.com"` — never a real endpoint. Any test touching config paths must `t.Setenv("HOME", tmp)` and `t.Setenv("XDG_CONFIG_HOME", ...)` to a `t.TempDir()`, because `os.UserConfigDir()` otherwise resolves to the developer's real user config directory.

**Do not copy the `TestReplay_StaleProvider` shape from `codex_test.go`.** It passes vacuously: `Replay` returns `nil` at the `!IsInstalled()` check before ever reaching the provider lookup, so on any machine without the harness installed — including CI — it would still pass if the provider check were deleted. Test the unexported helpers directly instead: pull env construction into a `buildEnv` function and assert on the returned map (as `copilot_test.go` does), and exercise `providerMatches` and `fqnModels` on their own.

Cover: the env or config produced for each protocol, the provider filter, the backend filter if you have one, install and uninstall hints, and at least one replay path.

## Verification gates

CI runs exactly two things: `gofmt -l .` (fails on **any** output) and `make test`. Before declaring done, run all four and report real output:

```bash
gofmt -l .        # must print nothing
go vet ./...
make test
make build
```

**Green gates do not mean the client routes.** Unit tests check the strings the client builds, not whether the harness accepts them, so a client can pass everything above and fail on its first real request. Follow **testing-an-aperture-cli-client** for the rest: a headless live request per protocol, a tool-calling check, and the interactive TUI steps only a human can do. `docs/adding-a-client.md` covers the same ground in its "Test it end to end" section.

Finally, add the harness to the `Supported agents` list in `README.md`, and lead the commit message with the touched path: `internal/clients: add <Name> client`.

## Debugging a client that misbehaves

**Not in the root menu at all.** Check `[i] Install agents` first — if it is there, discovery is the problem, so verify `binaryName` matches the executable and that `commonBinaryPaths` returns full paths to the binary, not directories. If it is in neither list, the package is not linked: confirm the blank import. If it registers and is installed but still absent, check that `Menu()` sets `Action` — the root menu skips items with a nil `Action`.

**"No providers support ..."** The compat key does not match the endpoint. Fetch `GET /api/providers` and compare keys literally. If nothing sets your key, the client is behaving correctly and the gap is Aperture-side.

**Starts but every request fails.** Run `-debug`. Check the base URL for a missing or doubled `/v1`, then check whether the model still carries its `provider/` prefix.

**Quick select never appears.** `Replay` returned `nil`. Walk its checks in order, and read what the launcher actually persisted — `statePath` in `internal/config/state.go` resolves it through `os.UserConfigDir()`, giving `$HOME/Library/Application Support/aperture/launcher.json` on macOS and `$HOME/.config/aperture/launcher.json` on Linux. A `name` constant that changed since the launch was recorded will never match.
