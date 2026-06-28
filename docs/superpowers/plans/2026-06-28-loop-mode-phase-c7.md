# Loop Mode — Phase C-7: The `loop` Command

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship `at-cove loop [<name>] [--once] [--keep] [--interval <dur>]` —
auto-create a loop's sandbox,
drain-then-poll its check and run the agent on each trigger,
with graceful signal stop and auto-destroy —
plus remove the shared image when the last instance is torn down.

**Architecture:** `doLoop` resolves the loop config,
holds a per-instance shared lock,
keeps the host awake,
creates (or with `--keep` reuses) the instance via `createLoopInstance`,
dials it,
resolves secrets (requiring `ANTHROPIC_API_KEY` to actually resolve),
and drives `loop.Run` with a stop-aware `tick` (`fresh?`→seed→check→agent) and a signal-aware `sleep`.
A `done` channel closed by a SIGINT/SIGTERM watcher makes both the drain and the poll exit gracefully.
`doDestroyInstance` gains last-instance detection so the shared image is removed only when nothing else needs it.
The integration glue mirrors `doConnect` (not hermetically unit-tested);
coverage is a dry-run + argv/flag-parsing tests over the already-tested mechanics.

**Tech Stack:** Go 1.22, standard library only (`os/signal`, `syscall`, `time`).

## Global Constraints

- Go version floor `go 1.22`; standard library only — no new dependencies.
- `loop [<name>] [kit-dir]`: first positional is the loop name (default `default`); kit discovered from cwd or the optional second positional. `--once`, `--keep`, `--interval <dur>` flags. `--interval` overrides the config interval.
- Unattended auth: secrets resolved once; `ANTHROPIC_API_KEY` must resolve to a non-empty value or the loop aborts before running.
- Graceful stop: SIGINT/SIGTERM let the in-flight tick finish, then the loop exits; the drain and poll both observe a closed `done` channel.
- `--keep` reuses an existing instance and skips teardown; without `--keep`, refuse if an instance exists, else auto-create and auto-destroy on exit.
- A running loop holds the per-instance shared lock (`AcquireSharedFor`), so destroy/recreate of its own instance refuses while it runs.
- Removing the shared image happens only when the last instance (no interactive, no other loop) is destroyed.
- Hermetic tests where feasible (last-instance logic, dry-run, flag parsing); the dial/scheduler/signal glue follows `doConnect`'s untested-glue pattern.

---

### Task 1: Remove the shared image when the last instance is destroyed

**Files:**
- Modify: `internal/state/state.go` (add `OtherLoopInstancesExist`)
- Modify: `main.go` (`doDestroyInstance` last-instance logic)
- Test: `internal/state/state_test.go`, `main_test.go`

**Interfaces:**
- Consumes: `state.Dir`, `Instance.file` (existing).
- Produces: `func state.OtherLoopInstancesExist(kitDir string, except Instance) bool`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/state/state_test.go`:

```go
func TestOtherLoopInstancesExist(t *testing.T) {
	dir := t.TempDir()
	if OtherLoopInstancesExist(dir, LoopInstance("foo")) {
		t.Fatal("no dir yet: false")
	}
	if err := SaveFor(dir, LoopInstance("foo"), State{Name: "x", Backend: "colima", Container: "x-loop-foo"}); err != nil {
		t.Fatal(err)
	}
	if OtherLoopInstancesExist(dir, LoopInstance("foo")) {
		t.Fatal("only foo exists, excluding foo: false")
	}
	if err := SaveFor(dir, LoopInstance("bar"), State{Name: "x", Backend: "colima", Container: "x-loop-bar"}); err != nil {
		t.Fatal(err)
	}
	if !OtherLoopInstancesExist(dir, LoopInstance("foo")) {
		t.Fatal("bar exists besides foo: true")
	}
}
```

Append to `main_test.go`:

```go
func TestDestroyLastLoopInstanceRemovesImage(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir) // config only — NO interactive instance state
	if err := state.SaveFor(kitDir, state.LoopInstance("foo"), state.State{
		Name: "box", Backend: "colima", Container: "box-loop-foo", Image: "at-cove-for-box",
	}); err != nil {
		t.Fatal(err)
	}
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"destroy", "--loop", "foo", kitDir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut.String())
	}
	var rmi bool
	for _, c := range f.Calls {
		if c.Name == "docker" && len(c.Args) > 0 && c.Args[0] == "rmi" {
			rmi = true
		}
	}
	if !rmi {
		t.Fatal("destroying the LAST instance (no interactive, no other loop) must remove the shared image")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `/usr/local/go/bin/go test ./internal/state/ ./ -run 'TestOtherLoopInstancesExist|TestDestroyLastLoopInstanceRemovesImage'`
Expected: FAIL — `OtherLoopInstancesExist` undefined; and the loop teardown currently always clears the image (no `rmi`).

- [ ] **Step 3: Add `OtherLoopInstancesExist`**

In `internal/state/state.go`, add (near `HasLoopInstances`):

```go
// OtherLoopInstancesExist reports whether any loop instance other than `except`
// has a state file in the kit. Used so destroying the last instance can reclaim
// the shared kit image.
func OtherLoopInstancesExist(kitDir string, except Instance) bool {
	entries, err := os.ReadDir(Dir(kitDir))
	if err != nil {
		return false
	}
	exceptFile := except.file()
	for _, e := range entries {
		n := e.Name()
		if strings.HasPrefix(n, "loop-") && strings.HasSuffix(n, ".json") && n != exceptFile {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Use it in `doDestroyInstance`**

In `main.go`, replace the image-guard block in `doDestroyInstance`:

```go
	bi := instanceFromState(st)
	if inst != state.Interactive {
		bi.Image = "" // loop instances share the kit image; never remove it on teardown
	} else if state.HasLoopInstances(kitDir) {
		bi.Image = "" // loop instances still depend on the shared kit image; keep it
	}
```

with:

```go
	bi := instanceFromState(st)
	if inst != state.Interactive {
		// A loop instance shares the kit image; keep it unless this is the last
		// instance overall (no interactive, no other loop), in which case reclaim it.
		last := !state.Exists(kitDir) && !state.OtherLoopInstancesExist(kitDir, inst)
		if !last {
			bi.Image = ""
		}
	} else if state.HasLoopInstances(kitDir) {
		bi.Image = "" // loop instances still depend on the shared kit image; keep it
	}
```

- [ ] **Step 5: Run the tests, build, vet, gofmt**

Run: `/usr/local/go/bin/go test ./... && /usr/local/go/bin/go build ./... && /usr/local/go/bin/go vet ./... && /usr/local/go/bin/gofmt -l internal/state/ main.go`
Expected: PASS (new tests; plus the existing `TestDestroyLoopInstancePreservesImage`, which has an interactive instance present, so destroying the loop is NOT last → image preserved; and `TestDestroyRemovesContainerImageAndState`); no vet output; clean gofmt.

- [ ] **Step 6: Commit**

```bash
git add internal/state/state.go main.go internal/state/state_test.go main_test.go
git commit -m "feat(cli): reclaim the shared image when the last loop instance is destroyed"
```

---

### Task 2: The `loop` command

**Files:**
- Modify: `main.go` (`--once`/`--keep`/`--interval` flags; loop positional parsing; `loop` dispatch; `doLoop`; help text)
- Test: `main_test.go`

**Interfaces:**
- Consumes: `createLoopInstance`, `doDestroyInstance`, `kit.Load`, `state.{LoadFor,ExistsFor,LoopInstance,AcquireSharedFor}`, `usersecret.Load`, `secret.{Spec,Resolve}`, `keys.Ensure`, `getBackend`, `sshargs.Target`, `awake.New`, `loop.Run`, `connect.{RunCheck,SeedLoopWorkspace,ResetLoopWorkspace,RunAgent}` (all landed).
- Produces: the `at-cove loop` command.

- [ ] **Step 1: Write the failing tests**

Append to `main_test.go` (imports already include what's needed plus `state`/`runner`/`bytes`/`os`/`strings`):

```go
func TestDryRunLoop(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeLoopKit(t, dir)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"--dry-run", "loop", "default", kitDir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut.String())
	}
	if len(f.Calls) != 0 {
		t.Fatalf("dry-run executed commands: %+v", f.Calls)
	}
	s := out.String()
	if !strings.Contains(s, "would run loop \"default\"") || !strings.Contains(s, "5m0s") {
		t.Fatalf("dry-run message = %q", s)
	}
}

func TestDryRunLoopIntervalOverride(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeLoopKit(t, dir)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"--dry-run", "--interval", "30s", "loop", kitDir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut.String())
	}
	// name omitted => "default"; interval overridden to 30s.
	if !strings.Contains(out.String(), "30s") {
		t.Fatalf("--interval should override; msg=%q", out.String())
	}
}

func TestLoopUnknownNameErrors(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeLoopKit(t, dir)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"--dry-run", "loop", "nope", kitDir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code == 0 {
		t.Fatal("unknown loop name must error")
	}
}

func TestLoopBadIntervalErrors(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeLoopKit(t, dir)
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"--interval", "nonsense", "loop", kitDir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code == 0 {
		t.Fatal("bad --interval must error")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `/usr/local/go/bin/go test ./ -run 'TestDryRunLoop|TestLoopUnknown|TestLoopBadInterval'`
Expected: FAIL — `loop` is an unknown command / flags unparsed.

- [ ] **Step 3: Parse `--once`/`--keep`/`--interval` and the loop positional**

In `main.go`, in `run(...)`, add the variables next to the others:

```go
	once := false
	keep := false
	intervalStr := ""
```

Add cases to the argument-parsing `switch` (after `--loop`):

```go
		case a == "--once":
			once = true
		case a == "--keep":
			keep = true
		case a == "--interval":
			if i+1 >= len(argv) {
				fmt.Fprintln(stderr, "at-cove: --interval requires a duration")
				return 2
			}
			i++
			intervalStr = argv[i]
```

Replace the existing positional/kit-dir resolution:

```go
	// Resolve the kit directory (explicit arg or discovery).
	start := "."
	if len(rest) == 1 {
		start = rest[0]
	} else if len(rest) > 1 {
		fmt.Fprintf(stderr, "at-cove: %s takes at most one kit-dir\n", cmd)
		return 2
	}
```

with a version that special-cases `loop`'s `[<name>] [kit-dir]`:

```go
	// Resolve positionals. Most commands take an optional kit-dir; `loop` takes
	// an optional loop name first, then an optional kit-dir.
	start := "."
	loopArg := ""
	if cmd == "loop" {
		switch len(rest) {
		case 0:
		case 1:
			loopArg = rest[0]
		case 2:
			loopArg, start = rest[0], rest[1]
		default:
			fmt.Fprintln(stderr, "at-cove: loop takes [<name>] [kit-dir]")
			return 2
		}
	} else if len(rest) == 1 {
		start = rest[0]
	} else if len(rest) > 1 {
		fmt.Fprintf(stderr, "at-cove: %s takes at most one kit-dir\n", cmd)
		return 2
	}
```

Parse the interval override (after `kitDir` is resolved, before the dispatch `switch`):

```go
	var intervalOverride time.Duration
	if intervalStr != "" {
		d, err := time.ParseDuration(intervalStr)
		if err != nil || d <= 0 {
			fmt.Fprintf(stderr, "at-cove: --interval must be a positive duration: %q\n", intervalStr)
			return 2
		}
		intervalOverride = d
	}
```

Add the dispatch case (in the `switch cmd`):

```go
	case "loop":
		err = doLoop(kitDir, r, loopArg, once, keep, intervalOverride, dryRun, stdout, stderr)
```

- [ ] **Step 4: Implement `doLoop`**

In `main.go`, add (near `doConnect`):

```go
// maxDrain caps consecutive triggers in a single drain, a safety valve so an
// unattended loop whose check never clears cannot spin forever; it resets each
// poll.
const maxDrain = 1000

// doLoop runs a scheduled, unattended agent loop against a dedicated sandbox: it
// auto-creates the loop instance (or reuses one with --keep), drains the loop's
// check — running the agent on each trigger — then polls every interval, until
// SIGINT/SIGTERM. It holds the host awake for the duration and, unless --keep,
// destroys the instance on exit.
func doLoop(kitDir string, r runner.Runner, loopName string, once, keep bool, intervalOverride time.Duration, dryRun bool, stdout, stderr io.Writer) error {
	cfg, err := kit.Load(kitDir)
	if err != nil {
		return err
	}
	if loopName == "" {
		loopName = "default"
	}
	lp, ok := cfg.Loops[loopName]
	if !ok {
		return fmt.Errorf("no loop %q defined in config.yml", loopName)
	}
	interval := lp.ParsedInterval()
	if intervalOverride > 0 {
		interval = intervalOverride
	}
	if dryRun {
		fmt.Fprintf(stdout, "would run loop %q every %s (once=%v, keep=%v): check %q, prompt %q\n",
			loopName, interval, once, keep, lp.Check, lp.Prompt)
		return nil
	}

	inst := state.LoopInstance(loopName)
	exists := state.ExistsFor(kitDir, inst)
	if exists && !keep {
		return fmt.Errorf("loop %q already has an instance; run `at-cove destroy --loop %s` or pass --keep to reuse it", loopName, loopName)
	}

	// Hold the host awake for the whole loop.
	if release, err := awake.New().Inhibit(); err != nil {
		fmt.Fprintf(stderr, "at-cove: warning: could not prevent host sleep: %v\n", err)
	} else {
		defer release()
	}

	var st state.State
	if exists {
		st, err = state.LoadFor(kitDir, inst)
	} else {
		st, err = createLoopInstance(kitDir, r, cfg, loopName, stdout)
	}
	if err != nil {
		return err
	}
	if !keep {
		defer func() { _ = doDestroyInstance(kitDir, r, inst, false, io.Discard) }()
	}

	// Block destroy/recreate of this instance while the loop runs.
	lock, err := state.AcquireSharedFor(kitDir, inst)
	if err != nil {
		return err
	}
	defer lock.Release()

	// Resolve secrets once for the run; ANTHROPIC_API_KEY must actually resolve.
	demanded := make([]secret.Spec, len(st.Secrets))
	for i, s := range st.Secrets {
		demanded[i] = secret.Spec{Name: s.Name, Command: s.Command}
	}
	store, err := usersecret.Load(filepath.Join(configDir(), "secrets.yml"))
	if err != nil {
		return err
	}
	specs, _ := store.Plan(demanded)
	env, err := secret.Resolve(r, specs)
	if err != nil {
		return err
	}
	if env["ANTHROPIC_API_KEY"] == "" {
		return fmt.Errorf("loop %q: ANTHROPIC_API_KEY did not resolve to a value (declare it in config.yml and provide it via the resolver command or secrets.yml)", loopName)
	}

	priv, _, err := keys.Ensure(r, configDir())
	if err != nil {
		return err
	}
	b, err := getBackend(st.Backend, r)
	if err != nil {
		return err
	}
	ep, cleanup, err := b.Dial(st.Container)
	if err != nil {
		return err
	}
	defer cleanup()
	knownHostsDir := filepath.Join(configDir(), "known_hosts.d")
	if err := os.MkdirAll(knownHostsDir, 0o700); err != nil {
		return err
	}
	tgt := sshargs.Target{
		Host:           ep.Host,
		User:           ep.User,
		Port:           ep.Port,
		IdentityFile:   priv,
		KnownHostsFile: filepath.Join(knownHostsDir, st.Container),
	}

	// Graceful stop: a watcher closes done on SIGINT/SIGTERM; both the drain and
	// the poll observe it.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)
	done := make(chan struct{})
	go func() {
		<-sig
		fmt.Fprintf(stderr, "loop %q: stopping after the current tick\n", loopName)
		close(done)
	}()
	stopped := func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}
	sleep := func(d time.Duration) bool {
		select {
		case <-done:
			return false
		case <-time.After(d):
			return true
		}
	}

	triggers := 0
	tick := func() bool {
		if stopped() || triggers >= maxDrain {
			return false
		}
		if lp.FreshWorkspace {
			if err := connect.ResetLoopWorkspace(r, tgt); err != nil {
				fmt.Fprintf(stderr, "loop %q: reset: %v\n", loopName, err)
				return false
			}
		}
		if err := connect.SeedLoopWorkspace(r, tgt, env, st.Setup); err != nil {
			fmt.Fprintf(stderr, "loop %q: seed: %v\n", loopName, err)
			return false
		}
		triggered, err := connect.RunCheck(r, tgt, env, lp.Check)
		if err != nil {
			fmt.Fprintf(stderr, "loop %q: check: %v\n", loopName, err)
			return false
		}
		if !triggered {
			triggers = 0
			return false
		}
		triggers++
		fmt.Fprintf(stdout, "loop %q: triggered, running agent\n", loopName)
		if err := connect.RunAgent(r, tgt, env, lp.Prompt); err != nil {
			fmt.Fprintf(stderr, "loop %q: agent: %v\n", loopName, err)
		}
		return true
	}
	// reset the drain cap on each poll.
	sleepReset := func(d time.Duration) bool {
		triggers = 0
		return sleep(d)
	}

	loop.Run(once, interval, tick, sleepReset)
	return nil
}
```

Add the needed imports to `main.go`: `"os/signal"`, `"syscall"`, `"time"` (likely present), `"github.com/aethons-tools/cove/internal/connect"` (present), `"github.com/aethons-tools/cove/internal/loop"`, `"github.com/aethons-tools/cove/internal/secret"` (present), `"github.com/aethons-tools/cove/internal/sshargs"` (present), `"github.com/aethons-tools/cove/internal/usersecret"` (present), `"github.com/aethons-tools/cove/internal/keys"` (present), `"github.com/aethons-tools/cove/internal/awake"` (present).

- [ ] **Step 5: Update the help text**

In `main.go`, in the `usage` const, add the `loop` usage line (after `connect`):

```
  at-cove loop     [<name>] [kit-dir] [--once] [--keep] [--interval <dur>]
```

and add a `loop flags:` stanza:

```
loop flags:
  --once             run one drain (all currently-available work) and exit
  --keep             reuse/leave the loop instance instead of auto-destroying it
  --interval <dur>   override the loop's poll interval (e.g. 30s, 5m)
```

- [ ] **Step 6: Run the tests, build, vet, gofmt**

Run: `/usr/local/go/bin/go test ./... && /usr/local/go/bin/go build ./... && /usr/local/go/bin/go vet ./... && /usr/local/go/bin/gofmt -l main.go`
Expected: PASS across all packages (the four new `loop` tests plus everything else); no vet output; clean gofmt.

- [ ] **Step 7: Commit**

```bash
git add main.go main_test.go
git commit -m "feat(cli): the loop command — scheduled unattended agent runs"
```

---

## Self-Review

**Spec coverage (Phase C-7 slice / loop-mode completion):**
- `loop [<name>] [--once] [--keep] [--interval]` → Task 2 (parsing + `doLoop` + dispatch + help).
- Auto-create / reuse-with-`--keep` / auto-destroy → `doLoop` lifecycle.
- Per-instance lock blocks destroy while running → `AcquireSharedFor`.
- Drain-then-poll with the C-5 scheduler → `loop.Run(once, interval, tick, sleepReset)`.
- Tick = `fresh?`→seed→check→agent → the `tick` closure over the landed mechanics.
- Stop-aware drain + signal-aware sleep (C-5 carry-in) → `done` channel observed by `stopped()` and `sleep`; poison-task cap `maxDrain`.
- Unattended auth: `ANTHROPIC_API_KEY` must resolve → the env check.
- Keep-awake for the loop's duration → `awake.New().Inhibit()`.
- Last-instance shared-image reclaim (C-4 carry-in) → Task 1 (`OtherLoopInstancesExist` + `doDestroyInstance`).
- Sentinel/volume lifecycle (C-5 carry-in) → satisfied by construction: an auto-created instance gets both volumes fresh; `--keep` reuse keeps both together.

**Placeholder scan:** none — every code and command step is concrete.

**Type consistency:** `doLoop(kitDir string, r runner.Runner, loopName string, once, keep bool, intervalOverride time.Duration, dryRun bool, stdout, stderr io.Writer) error` matches its dispatch call; `loop.Run(once, interval, tick, sleep)`, `connect.{RunCheck,SeedLoopWorkspace,ResetLoopWorkspace,RunAgent}`, `createLoopInstance`, `state.{LoopInstance,ExistsFor,LoadFor,AcquireSharedFor,OtherLoopInstancesExist}` are all used with their real signatures. `OtherLoopInstancesExist` is defined in Task 1 and consumed in Task 1's `doDestroyInstance` change.

**Note on testing:** `doLoop`'s post-create path (dial, scheduler, signals) is integration glue that mirrors `doConnect` and is not hermetically unit-tested; the dry-run + parsing tests cover the command surface, and every mechanism it composes (`createLoopInstance`, `RunCheck`, `SeedLoopWorkspace`, `RunAgent`, `loop.Run`, `doDestroyInstance`) is unit-tested in its own phase.
