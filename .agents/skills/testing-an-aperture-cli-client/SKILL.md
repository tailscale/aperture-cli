---
name: testing-an-aperture-cli-client
description: "Test a client (coding agent harness) in the aperture-cli launcher end to end: the CI gates, a headless live request through every wire protocol, tool calling, and the interactive TUI checks a human must do. Use when verifying a new or modified client in internal/clients, when asked whether a harness routes correctly, when a client passes tests but fails at runtime, or before opening a PR touching a client package. To write the client use adding-aperture-cli-client."
---

# Testing a client in aperture-cli

Four layers, cheapest first. Each catches a class of failure the one before it cannot see.

| Layer | Catches | Automatable |
| --- | --- | --- |
| 1. CI gates | Compile errors, formatting, unit-level logic | Yes |
| 2. Headless live request | Wrong base URL, wrong protocol, bad config schema | Yes |
| 3. Tool calling | Missing/null fields the harness only needs mid-loop | Yes |
| 4. Interactive TUI | Menu wiring, replay, cleanup on exit | No — human at a terminal |

**Green unit tests prove almost nothing about routing.** They exercise the strings a client builds, not whether a harness accepts them. Layers 2 and 3 are where clients actually fail, and they are scriptable, so run them before declaring anything done.

## Layer 1: the CI gates

CI runs exactly two things — `gofmt -l .` (fails on **any** output) and `make test`. Run all four and report real output, never a prediction:

```bash
gofmt -l .        # must print nothing
go vet ./...
make test
make build
```

`go vet` is not in CI but currently passes clean repo-wide, so any vet failure you see is yours.

## Layer 2: a headless live request per protocol

Generate the exact artifact the client produces, then feed it to the real harness against a live Aperture. Do not hand-write the config or the arguments — call the client's own unexported builders, or a bug in them is exactly what you fail to catch.

Confirm the endpoint first and pick a real provider and model for each protocol the client supports:

```bash
curl -s http://ai/api/providers | python3 -c "
import json,sys
for p in json.load(sys.stdin):
    ck=[k for k,v in (p['compatibility'] or {}).items() if v]
    m=p.get('models') or []
    print(f\"{p['id']:28} | {','.join(ck):45} | {len(m):3} models | {m[:2]}\")
"
```

Note some providers report `models: null`, not `[]` — hence the `or []`. Pick the cheapest capable model per protocol; a smoke test does not need a frontier model.

**For a config-file or extension client**, add a temporary in-package test that writes the real artifact for each backend and logs the real argv. Gate it on an env var so it skips in CI, name it so it sorts last, and delete it when you are done:

```go
// zz_manual_e2e_test.go — throwaway, delete after use.
func TestManualE2EEmitConfigs(t *testing.T) {
	out := os.Getenv("E2E_OUT")
	if out == "" {
		t.Skip("set E2E_OUT to emit")
	}
	host := os.Getenv("E2E_HOST")
	// one case per (provider, backend) pair the client supports
	for _, c := range cases {
		b, ok := backendByID(c.bID)
		if !ok {
			t.Fatalf("no backend %q", c.bID)
		}
		src, err := extensionSource(c.prov.ID, buildProvider(host, c.prov, b))
		if err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(out, c.file)
		if err := os.WriteFile(p, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s -> %s  args=%v", c.bID, p, buildArgs(p, c.prov.ID, c.model))
	}
}
```

```bash
E2E_DIR=$(mktemp -d "${TMPDIR:-/tmp}/<name>-e2e-XXXXXX")
E2E_OUT="$E2E_DIR" E2E_HOST=http://ai go test ./internal/clients/<name>/ \
  -run TestManualE2EEmitConfigs -v
```

Read the emitted file before running anything. Check the base URL for a doubled `//`, a missing or doubled `/v1`, and that model IDs carry no `provider/` prefix. Then grep for `null` — a Go zero value marshals to `0`/`""`/`null` and reaches the harness literally.

Now drive the harness with each artifact, using its non-interactive flag:

```bash
for b in <backend-ids>; do
  echo "===== $b"
  <harness> <load-config-flag> "$E2E_DIR/$b.<ext>" --model "<ref>" \
    -p --no-session "Reply with exactly: PONG" 2>&1 | tail -6
  echo "--- pipestatus=${pipestatus[1]}"   # zsh; bash uses ${PIPESTATUS[1]}
done
```

Non-interactive flags differ per harness: pi and Claude Code use `-p`/`--print`, OpenCode uses `opencode run`. Check `--help`. Do not prefix these with `timeout` in zsh — it is not a builtin and the whole command becomes an unfound command name whose exit status still reads 0, which looks like a pass.

**For an env-vars-only client**, skip the artifact and set the same variables the client sets, read off `./.build/aperture -debug`.

## Layer 3: tool calling

Text generation and a tool loop exercise different fields. Run this for every protocol the client supports:

```bash
<harness> <load-config-flag> "$E2E_DIR/$b.<ext>" --model "<ref>" \
  -p --no-session -t bash "Run the shell command 'echo tool-ok' and tell me its output"
```

Expect `tool-ok` in the output. This is the check to re-run after **any** edit to config or extension generation. Pi's own extension code documents why: an omitted `maxTokens` reaches Anthropic as a literal `null` and is rejected, and an omitted `input` list crashes pi's model formatter outright — see the `piModel` comment in `internal/clients/pi/extension.go`. Neither shows up in a text-only smoke test.

## Layer 4: the interactive TUI

Layers 1–3 all bypass the menu. These need a human:

```bash
./.build/aperture -debug
```

1. The client appears in the root menu when installed, or under `[i] Install agents` when not.
2. Walking provider → backend → model reaches a launch. One option should auto-descend without an Enter press; zero compatible providers should show an error, not an empty menu.
3. `-debug` prints the resolved env and args to stderr before exec — the fastest way to spot a wrong variable name or a doubled slash.
4. The harness starts and completes a real request.
5. Quitting the harness lands back on the root menu.
6. `[0] Quick select` now names the client, with the provider, backend, and model from `QuickSelectLabel`.
7. The per-launch config file is **gone**. Check `$HOME/Library/Application Support/aperture/clients/<name>/` (macOS) or `${XDG_CONFIG_HOME:-$HOME/.config}/aperture/clients/<name>/` (Linux). A leftover file means `Cleanup` was not passed through to `LaunchSpec`.

Replay is only testable this way, and only against a launcher state that names your client. Read what was actually persisted:

```bash
cat "$HOME/Library/Application Support/aperture/launcher.json"
```

If `lastClientName` names a different client, quick-select will not exercise yours — launch yours once first. Note `resolveReplay`-style helpers exist precisely so staleness logic is unit-testable; `Replay` itself returns `nil` at `!IsInstalled()` before any other check, so a test driving `Replay` on a machine without the harness passes vacuously.

## Cleaning up

Delete the throwaway test and the emitted configs, then re-run the gates so the tree you hand off is the tree you tested:

```bash
rm -f internal/clients/<name>/zz_manual_e2e_test.go
rm -rf "$E2E_DIR"
gofmt -l . && go vet ./... && make test
```

Never leave the e2e test committed. It needs a live endpoint and a real installed harness, so it would fail or vacuously skip in CI.

## Reporting

State which layers ran and which did not. Layer 4 is frequently impossible in an agent session — say so plainly rather than implying full coverage, and give the exact commands for a human to finish. A protocol you did not exercise is untested, not "should work": report it per protocol, with the provider and model used, since a client that works on Anthropic Messages can fail on Vertex over the same code path.
