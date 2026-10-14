# Testing

aperture-cli tests at three layers: unit tests inside each package, an e2e
suite that drives the built binary as a black box, and two black-box suites
behind the `tailnet` build tag. `make check` is the gate CI runs: lint,
build, then unit and e2e under the race detector.

## Unit tests

Unit tests live next to the code and test through the package's real
surface, not its internals. New functionality needs them; see
`internal/cli/launch_test.go` for the table style:

```go
{
    name: "a single-dash long flag in the tail is the client's own",
    in:   []string{"-config", "x.json", "--search"},
    sel:  launchSelection{},
    rest: []string{"-config", "x.json", "--search"},
},
```

Tests that touch settings point HOME at a temp dir. The config package has
`loadInto` (`internal/config/startup_test.go`) for a saved-settings fixture;
the cli package has `scratchHome` (`internal/cli/cli_test.go`).

## The e2e harness

`e2e/` builds the real binary once in `TestMain` and drives it. Nothing here
may touch the developer's real home, config or tailnet. The pieces:

- `run(t, bin, env, args...)` runs one invocation and returns stdout, stderr
  and the error. Use it for every non-interactive assertion.
- `spawn(t, bin, args, env)` starts the binary on a PTY and returns a
  `terminal` with `waitFor`, `send`, `screen` and `waitExit`. Use it for the
  TUI and for anything that needs a signal mid-run.
- `hermeticEnv(t, binDir)` builds an isolated HOME, XDG_CONFIG_HOME and PATH.
- `fakeAperture(t)` serves the `/v1/models` discovery contract, so a connect
  succeeds without a tailnet.
- `installStubPi(t, binDir)` installs a stub agent binary that records its
  argv and env, so a test can assert exactly what the client received.
- `installStubPiSleeper` is the same stub, but it sleeps, for signal tests.
- `waitForFile` polls for a file the stub or the binary writes.

A headless test through the whole path:

```go
func TestHeadlessLaunchPi(t *testing.T) {
	srv := fakeAperture(t)
	binDir := t.TempDir()
	recordDir := installStubPi(t, binDir)
	env := append(hermeticEnv(t, binDir), "APERTURE_E2E_RECORD="+recordDir)

	_, stderr, err := run(t, apertureBin, env, "pi", "--endpoint", srv.URL, "--", "--search")
	if err != nil {
		t.Fatalf("aperture pi: %v (%s)", err, stderr)
	}
	argv, _ := os.ReadFile(filepath.Join(recordDir, "argv"))
	if !strings.Contains(string(argv), "--search") {
		t.Errorf("stub argv = %q, want the passthrough flag in it", argv)
	}
}
```

A TUI test through a PTY:

```go
term := spawn(t, apertureBin, []string{"-endpoint", srv.URL}, env)
term.waitFor(t, "Which editor do you want to use?")
term.send("1")
waitForFile(t, filepath.Join(recordDir, "argv"))
term.send("q")
term.waitExit(t)
```

Row numbers shift with whatever clients the test box has installed. Read the
screen for the row instead of guessing, the way `compat_test.go` does with
`piRowPattern`.

A signal test (see `e2e/launch_signal_test.go`):

```go
cmd := exec.Command(apertureBin, "serve", "--bridge", "work")
cmd.Env = env
cmd.Start()
// wait for the slot lock, so the signal interrupts real work
cmd.Process.Signal(syscall.SIGTERM)
// expect a clean, fast exit
```

## The black-box suites (tailnet tag)

`make test-tailnet` runs two suites tagged `//go:build tailnet`, kept out of
the gate because they download the latest release from GitHub and, when
`TS_AUTHKEY` is set, log a bridge into the local tailnet. Run them on a dev
box.

**compat** (`e2e/compat_test.go`) downloads the latest released binary, has
it write settings, state and a bridge login, then runs this branch's binary
against all of it. This is the test that proves a real old install upgrades
cleanly. Without `TS_AUTHKEY` the bridge-login half skips.

**contract** (`e2e/contract_test.go`) pins the scripted surface byte for
byte: the columns a script parses, the stderr a user reads, the exit codes a
supervisor checks. Goldens live in `e2e/testdata/contract/`. A deliberate
formatting change regenerates them:

```sh
go test -tags tailnet ./e2e/ -run TestContract -update
```

Random output (bridge IDs) and run-specific paths (the run-log location) are
normalized by `normalizeGolden` in `e2e/blackbox.go` before comparison.

## Running against an installed binary

Every suite builds the binary it tests from source. To test what
`make install` or a release actually put on disk, point the harness at it:

```sh
APERTURE_E2E_BIN=$(which aperture) make test-tailnet
```
