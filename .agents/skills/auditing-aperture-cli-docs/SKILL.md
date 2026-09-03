---
name: auditing-aperture-cli-docs
description: "Audit the aperture-cli repo's docs and its own in-repo skills against the code, reporting drift ranked by severity with a tier label proving each claim: static citations, executed commands, live routing. Use when asked to audit or fact-check the docs, to check whether README.md, docs/adding-a-client.md, or .agents/skills/*/SKILL.md still match the code, or after a change touching a client package or the client interface. Read-only by default. To write a client use adding-aperture-cli-client; to verify one use testing-an-aperture-cli-client."
---

# Auditing aperture-cli docs against the code

The repo documents itself three times over: `README.md` for users, `docs/adding-a-client.md` as the step-by-step guide, and `.agents/skills/*/SKILL.md` as the agent-facing orientation layer. All three make checkable claims about code that moves underneath them. This skill finds the ones that have stopped being true.

**Audit the skills, not just the prose.** The skills are the most drift-prone documents in the repo and the least likely to be read by a human who would notice. They cite line numbers, name unexported helpers, describe which clients follow which convention, and cross-reference sections of the guide by title. Every one of those is a claim that can rot. A skill that confidently misdescribes the codebase is worse than no skill, because an agent will act on it.

## The corpus

Audit exactly these, and enumerate rather than assume — a new skill directory is easy to miss:

```bash
ls README.md docs/*.md .agents/skills/*/SKILL.md
```

## Three tiers, and every finding must name the one that proved it

The tier is not decoration. It tells the reader how much to trust the finding and how much work reproducing it costs. A claim "verified" at the wrong tier is the failure mode this skill exists to prevent: green unit tests and resolving line anchors both look like proof and neither touches routing.

| Tier | Question it answers | Cost | Can it fail spuriously? |
| --- | --- | --- | --- |
| 1. Static | Does the cited thing exist and still say this? | Free | No |
| 2. Executed | Does the promised command produce the promised output? | Seconds | Only on a broken tree |
| 3. Live | Does the documented routing actually route? | Minutes, needs endpoint + harness | Yes — missing prerequisites are **not** a pass |

### Tier 1: static

The guide carries 40+ `path/file.go:NN` anchors. Check that each file exists, the line is in range, **and the line still says what the surrounding prose claims** — an anchor that drifted onto a neighbouring function resolves fine and is still wrong. Read the citing sentence, then the cited line, and compare meaning.

```bash
python3 - <<'PY'
import re, os
corpus = ["README.md"] + [os.path.join("docs", f) for f in os.listdir("docs") if f.endswith(".md")]
corpus += [os.path.join(r, "SKILL.md") for r, _, fs in os.walk(".agents/skills") if "SKILL.md" in fs]
pat = re.compile(r'`([A-Za-z0-9_./-]+\.go):(\d+)`')
for doc in corpus:
    for ln, line in enumerate(open(doc), 1):
        for m in pat.finditer(line):
            path, n = m.group(1), int(m.group(2))
            if not os.path.exists(path):
                print(f"MISSING-FILE {doc}:{ln} -> {path}:{n}"); continue
            src = open(path).read().splitlines()
            if n > len(src):
                print(f"OUT-OF-RANGE {doc}:{ln} -> {path}:{n} (file has {len(src)})"); continue
            print(f"OK {doc}:{ln} -> {path}:{n} | {src[n-1].strip()[:90]}")
PY
```

Then check the claims that carry no line number, which are the ones that actually break:

- **Cross-document section references.** A doc naming a section of another doc (say, a skill pointing at `its "Test it end to end" section` of the guide) must match a real heading. Extract quoted section names and grep the target's headings — `grep -n '^#\{1,3\} ' docs/adding-a-client.md`. That exact reference was a real finding, fixed at 51de975; see Known drift for how it read. Beware the self-reference: grepping the corpus for a dangling title will match this skill's own prose discussing it. Exclude the auditing skill from that grep, or expect the hit — anything auditing a corpus it belongs to has to account for itself.
- **Counts and enumerations.** "The interface has nine methods" against `grep -cE '^\t[A-Z][A-Za-z]*\(' internal/clients/registry.go`. The README's `Supported agents` list against `ls -d internal/clients/*/` plus the desktop adapters in `internal/profiles` — the list is legitimately longer than the client count, because Claude Cowork is a profile, not a client.
- **"Client X does this, client Y does not" claims.** These are the fastest-rotting sentences in the corpus, because a new client silently joins or breaks the pattern. Verify each side: `grep -rn "TrimRight" internal/clients/`.
- **Named symbols, flags, paths, make targets.** Every backticked identifier should resolve: `grep -rn "func validateHost" internal/clients/`, flags against `grep -n 'flag\.' cmd/aperture/main.go`, `make <target>` against the `Makefile`, and version claims against `go.mod`.
- **CI claims.** "CI runs exactly two things" is a claim about `.github/workflows/`. Check the trigger blocks, not just the file count: there are three workflows, and `govulncheck.yaml` fires only on a schedule and on changes to itself, so it is not a PR gate. Read `on:` before calling this drift.

### Tier 2: executed

Run the read-only commands the docs promise and diff real output against documented output. Never predict.

```bash
gofmt -l .        # documented as printing nothing
go vet ./...      # documented as passing clean repo-wide
make test
make build
curl -s http://ai/api/providers
```

For `/api/providers`, verify the shape the docs describe, and note that **some providers report `models: null`, not `[]`** — any snippet indexing models without an `or []` guard is a real bug in the doc, not a nitpick.

```bash
curl -s http://ai/api/providers | python3 -c "
import json,sys
for p in json.load(sys.stdin):
    ck=[k for k,v in (p.get('compatibility') or {}).items() if v]
    m=p.get('models')
    print(f\"{p['id']:26} | {','.join(ck):58} | models={'null' if m is None else len(m)}\")
"
```

Cross-check documented compat keys against that live list plus the in-repo declarations (`grep -rn 'compatKey\|compatKeys' internal/clients/`). A key in the docs that no provider sets is worth reporting; a key in the code that no provider sets is an Aperture-side gap, not doc drift.

### Tier 3: live

For routing claims — base URL shapes, `/v1` suffixes, model reference formats — nothing below this tier is evidence. Emit configs through **the client's own builders**, never by hand, then send a real request per protocol and a tool-calling check.

Add a throwaway in-package test gated on an env var, named to sort last, and delete it when done:

```bash
E2E_DIR=$(mktemp -d "${TMPDIR:-/tmp}/audit-e2e-XXXXXX")
E2E_OUT="$E2E_DIR" E2E_HOST=http://ai go test ./internal/clients/pi/ \
  -run TestAuditE2EEmitConfigs -v
```

**Populate `ProviderInfo.Models` from the live endpoint.** A synthetic `config.ProviderInfo{ID: "anthropic"}` emits `"models": []`, and pi then rejects the model reference with `Model "..." not found` — which looks exactly like a routing failure and is not one. Fetch the real model list and pass it in, or you will chase a bug you created.

Read the emitted artifact before running it: check for a doubled `//`, a missing or doubled `/v1`, a literal `null`, and whether model IDs still carry a `provider/` prefix. Then drive the harness:

```bash
pi -e "$E2E_DIR/anthropic.ts" --model "<ref>" -p --no-session "Reply with exactly: PONG"
echo "pipestatus=${pipestatus[1]}"   # zsh; bash uses ${PIPESTATUS[1]}

pi -e "$E2E_DIR/anthropic.ts" --model "<ref>" -p --no-session -t bash \
  "Run the shell command 'echo tool-ok' and tell me its output"
```

Note the model reference is `aperture-<provider>/<model>` — `piModelRef` namespaces the provider ID so a registration cannot overwrite one of pi's built-ins. A bare `provider/model` fails.

**Do not prefix these with `timeout`.** It is not a zsh builtin: the whole command becomes an unfound command name whose exit status still reads 0, which looks like a pass.

**If the endpoint is unreachable or the harness is missing, report the claim as UNVERIFIED.** Not "should work", not silently omitted. An untested protocol is untested, and saying so is the whole value of the tier label.

Clean up, then re-run the gates so the tree you hand off is the tree you tested:

```bash
rm -f internal/clients/*/zz_audit_e2e_test.go && rm -rf "$E2E_DIR"
gofmt -l . && go vet ./... && make test
```

## Known drift: found at 51de975, both since fixed

Both were verified present at Tier 1 when this skill was written, and both were fixed in the commits immediately after. They are recorded here as the worked examples of what this skill is looking for, not as open findings — confirm rather than inherit either direction, since the corpus keeps moving.

1. **The dangling cross-reference.** `.agents/skills/adding-aperture-cli-client/SKILL.md:76` sent the reader to a "Test it end to end" section of `docs/adding-a-client.md` that did not exist. The section was written and then reverted, and the reverted guide is what got committed, so no blob in any commit carried the title — it was unrecoverable, not misplaced. The guide's closest heading is Step 11, "Register the client, test it, and update the README", which covers the CI gates and the interactive walk-through but not the headless per-protocol request, so the pointer was wrong in scope as well as in name. It now names Step 11 and its actual scope. **This remains the smoke test for this skill:** an audit that would not have caught it is not working. Re-derive it from the commit rather than trusting this entry.
2. **The undiscoverable contributor docs.** `README.md` had no pointer to `docs/` or `.agents/skills/` anywhere. Its nav listed only Supported agents, Installation, Usage, and Development, leaving a 62KB contributor guide and three skills unreachable from the front door. It now carries a `Contributing` section naming both, wired into the nav row.

## Fix policy: read-only by default

Report first, always. After the report, offer to apply **only** mechanical, unambiguous drift, and show a diff before touching anything:

- stale line anchors where the symbol clearly moved within the same file
- renamed or moved paths with one obvious successor
- wrong counts and enumerations
- broken cross-references where exactly one real heading matches

Everything judgment-based stays out of the auto-fix set and goes into the handoff prompt: whether a missing section should be written or the pointer to it removed, how to restructure navigation, whether a stale convention claim means fixing the doc or fixing the code. Those are decisions, and a doc audit does not get to make them silently.

If the tree is dirty or another session is editing the corpus, check before writing — `git status --porcelain` and file mtimes. Concurrent edits to `.agents/skills/` do happen; do not clobber them, and do not attribute them to yourself.

## Output contract

Three parts, in order:

1. **Findings, ranked by severity.** Each one: `file:line`, the claim as written, the contradicting evidence, the tier that proved it, and a proposed edit. Rank by what would mislead a reader into broken work — a wrong routing fact outranks a stale line anchor, and a dangling cross-reference that costs a contributor twenty minutes outranks a cosmetic count. State the corpus coverage and which tiers ran.
2. **The mechanical fixes, offered.** With a diff, as one batch the user can accept or decline.
3. **A copy-pasteable prompt for a fresh session** covering everything judgment-based, with enough context to act without re-auditing: the finding, what was verified, and what the open decision is.
