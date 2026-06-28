# Loop Mode — Phase C-6: `createLoopInstance` (provisioning)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `createLoopInstance` —
build the shared kit image,
run a dedicated loop-suffixed isolated container,
and record a loop state file with the resolved per-loop setup —
failing fast when the loop is undefined or `ANTHROPIC_API_KEY` is not declared.

**Architecture:** Refactor the existing `saveState` into a reusable `buildState(cfg, inst, setup)`
so both the interactive instance and loop instances build their state the same way.
`createLoopInstance` resolves the loop from `cfg.Loops`,
requires `ANTHROPIC_API_KEY` to be declared (an unattended run can't log in),
builds via the existing `doBuild`,
creates the instance with `CreateContext.Kit = cfg.Name` (shared image)
and `Name = <kit>-loop-<name>` (distinct container/volumes),
and saves to the `loop-<name>` state file.
The `doLoop` command that drives a created instance is C-7.

**Tech Stack:** Go 1.22, standard library only.

## Global Constraints

- Go version floor `go 1.22`; standard library only — no new dependencies.
- Loop instances are always isolated, share the kit image via `CreateContext.Kit = cfg.Name`, and use a `<kit>-loop-<name>` container/volume name.
- Resolved setup is the loop's `setup` if set, else the kit-level `setup`.
- Fail fast (before any build): undefined loop; `ANTHROPIC_API_KEY` not declared in `config.yml` secrets; the loop instance already exists.
- The interactive `create` path and its `saveState` output are unchanged by the `buildState` refactor.
- Hermetic tests using the existing `seedConfigDir`/`dockerArg0Index` helpers (the build/create path runs with `runner.Fake` once the keypair is seeded).

---

### Task 1: `buildState` refactor + `createLoopInstance`

**Files:**
- Modify: `main.go` (refactor `saveState`; add `buildState`, `loopContainer`, `declaresSecret`, `createLoopInstance`)
- Test: `main_test.go`

**Interfaces:**
- Consumes: `doBuild`, `getBackend`, `kit.Config`/`kit.Loop`, `backend.CreateContext`/`Instance`, `state.SaveFor`/`LoopInstance`/`ExistsFor` (all existing/landed).
- Produces (relied on by C-7): `func createLoopInstance(kitDir string, r runner.Runner, cfg kit.Config, loopName string, stdout io.Writer) (state.State, error)`; helpers `buildState`, `loopContainer`, `declaresSecret`.

- [ ] **Step 1: Write the failing tests**

Append to `main_test.go` (add `"io"` and `"slices"` to its imports):

```go
func writeLoopKit(t *testing.T, dir string) string {
	t.Helper()
	cove := filepath.Join(dir, ".at-cove")
	if err := os.MkdirAll(cove, 0o755); err != nil {
		t.Fatal(err)
	}
	yml := "name: box\nbackend: colima\n" +
		"secrets:\n  - name: ANTHROPIC_API_KEY\n  - name: GITHUB_TOKEN\n" +
		"loops:\n  default:\n    interval: 5m\n    check: \"test -e q\"\n    prompt: \"do it\"\n    setup: \"git clone https://x .\"\n"
	if err := os.WriteFile(filepath.Join(cove, "config.yml"), []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	return cove
}

func TestCreateLoopInstance(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeLoopKit(t, dir)
	seedConfigDir(t)
	f := &runner.Fake{}
	cfg, err := kit.Load(kitDir)
	if err != nil {
		t.Fatal(err)
	}
	st, err := createLoopInstance(kitDir, f, cfg, "default", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if st.Container != "box-loop-default" {
		t.Fatalf("container = %q, want box-loop-default", st.Container)
	}
	if st.Image != "at-cove-for-box" {
		t.Fatalf("image = %q, want the shared kit image at-cove-for-box", st.Image)
	}
	if st.Setup != "git clone https://x ." {
		t.Fatalf("setup = %q (per-loop setup should win)", st.Setup)
	}
	if !state.ExistsFor(kitDir, state.LoopInstance("default")) {
		t.Fatal("loop state file not written")
	}
	bi := dockerArg0Index(f.Calls, "build")
	ri := dockerArg0Index(f.Calls, "run")
	if bi == -1 || ri == -1 {
		t.Fatalf("must build + run; calls=%+v", f.Calls)
	}
	if !slices.Contains(f.Calls[bi].Args, "at-cove-for-box") {
		t.Fatalf("build must tag the shared image: %+v", f.Calls[bi])
	}
	if !slices.Contains(f.Calls[ri].Args, "box-loop-default") {
		t.Fatalf("run must name the loop container: %+v", f.Calls[ri])
	}
}

func TestCreateLoopInstanceRequiresAPIKey(t *testing.T) {
	dir := t.TempDir()
	cove := filepath.Join(dir, ".at-cove")
	if err := os.MkdirAll(cove, 0o755); err != nil {
		t.Fatal(err)
	}
	yml := "name: box\nbackend: colima\nloops:\n  default:\n    interval: 1m\n    check: c\n    prompt: p\n"
	if err := os.WriteFile(filepath.Join(cove, "config.yml"), []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &runner.Fake{}
	cfg, _ := kit.Load(cove)
	_, err := createLoopInstance(cove, f, cfg, "default", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "ANTHROPIC_API_KEY") {
		t.Fatalf("must require ANTHROPIC_API_KEY; err=%v", err)
	}
	if len(f.Calls) != 0 {
		t.Fatalf("must fail before building/creating; calls=%+v", f.Calls)
	}
}

func TestCreateLoopInstanceUnknownLoop(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeLoopKit(t, dir)
	f := &runner.Fake{}
	cfg, _ := kit.Load(kitDir)
	if _, err := createLoopInstance(kitDir, f, cfg, "nope", io.Discard); err == nil {
		t.Fatal("unknown loop must error")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `/usr/local/go/bin/go test ./ -run TestCreateLoopInstance`
Expected: FAIL — build error, `undefined: createLoopInstance`.

- [ ] **Step 3: Refactor `saveState` to use a `buildState` helper**

In `main.go`, replace the existing `saveState` function with `buildState` plus a thin `saveState` wrapper:

```go
// buildState assembles the state snapshot for a created instance: the backend
// handles, the workspace mode, the setup command to seed an isolated workspace,
// and the kit's secret specs (names + resolver commands, never values).
func buildState(cfg kit.Config, inst backend.Instance, setup string) state.State {
	st := state.State{
		Name:          cfg.Name,
		Backend:       inst.Backend,
		Container:     inst.Container,
		Image:         inst.Image,
		WorkspaceMode: "isolated",
		Setup:         setup,
		CreatedAt:     time.Now().UTC().Format(time.RFC3339),
	}
	if inst.Workspace.Mode == backend.Shared {
		st.WorkspaceMode = "shared"
		st.WorkspaceHostPath = inst.Workspace.HostPath
	}
	for _, s := range cfg.Secrets {
		st.Secrets = append(st.Secrets, state.Secret{Name: s.Name, Command: s.Command})
	}
	return st
}

// saveState snapshots the created interactive instance into the kit state file.
func saveState(kitDir string, cfg kit.Config, inst backend.Instance) error {
	return state.Save(kitDir, buildState(cfg, inst, cfg.Setup))
}
```

- [ ] **Step 4: Add the loop helpers and `createLoopInstance`**

In `main.go`, add (near `saveState`):

```go
// loopContainer is the backend container/volume name for a named loop: it
// suffixes the kit name so loop instances never collide with the interactive
// instance or each other, while sharing the kit image (CreateContext.Kit).
func loopContainer(kitName, loopName string) string {
	return kitName + "-loop-" + loopName
}

// declaresSecret reports whether the kit declares a secret with the given name.
func declaresSecret(cfg kit.Config, name string) bool {
	for _, s := range cfg.Secrets {
		if s.Name == name {
			return true
		}
	}
	return false
}

// createLoopInstance provisions a dedicated, isolated sandbox for one named loop:
// it builds the shared kit image, runs a loop-suffixed container with its own
// volumes, and records a loop state file with the resolved setup command. It
// fails fast (before any build) when the loop is undefined, ANTHROPIC_API_KEY is
// not declared (an unattended run cannot log in interactively), or the loop
// instance already exists.
func createLoopInstance(kitDir string, r runner.Runner, cfg kit.Config, loopName string, stdout io.Writer) (state.State, error) {
	lp, ok := cfg.Loops[loopName]
	if !ok {
		return state.State{}, fmt.Errorf("no loop %q defined in config.yml", loopName)
	}
	if !declaresSecret(cfg, "ANTHROPIC_API_KEY") {
		return state.State{}, fmt.Errorf("loop %q requires ANTHROPIC_API_KEY to be declared in config.yml secrets (unattended runs cannot log in interactively)", loopName)
	}
	if state.ExistsFor(kitDir, state.LoopInstance(loopName)) {
		return state.State{}, fmt.Errorf("loop %q already has an instance; run `at-cove destroy --loop %s` first", loopName, loopName)
	}
	if err := doBuild(kitDir, r, false, stdout); err != nil {
		return state.State{}, err
	}
	b, err := getBackend(cfg.Backend, r)
	if err != nil {
		return state.State{}, err
	}
	inst, err := b.Create(backend.CreateContext{
		Name:      loopContainer(cfg.Name, loopName),
		Kit:       cfg.Name,
		BuildDir:  filepath.Join(kitDir, ".build"),
		Workspace: backend.WorkspaceMount{Mode: backend.Isolated},
	})
	if err != nil {
		return state.State{}, err
	}
	setup := lp.Setup
	if setup == "" {
		setup = cfg.Setup
	}
	st := buildState(cfg, inst, setup)
	if err := state.SaveFor(kitDir, state.LoopInstance(loopName), st); err != nil {
		return state.State{}, err
	}
	return st, nil
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `/usr/local/go/bin/go test ./`
Expected: PASS — the three new tests plus the existing `TestCreateWritesStateAndRejectsSecond`/`TestSaveStateSnapshotsSetup` (the `buildState` refactor preserves `saveState`'s output: `buildState(cfg, inst, cfg.Setup)` equals the previous inline construction).

- [ ] **Step 6: Run the full suite, build, vet, gofmt, and commit**

Run: `/usr/local/go/bin/go test ./... && /usr/local/go/bin/go build ./... && /usr/local/go/bin/go vet ./... && /usr/local/go/bin/gofmt -l main.go`
Expected: PASS across all packages; no vet output; `gofmt -l` prints nothing.

```bash
git add main.go main_test.go
git commit -m "feat(cli): createLoopInstance provisions a dedicated loop sandbox"
```

---

## Self-Review

**Spec coverage (Phase C-6 slice):**
- Build shared image + loop-suffixed isolated container + loop state → `createLoopInstance` (Task 1), verified hermetically (build tag `at-cove-for-box`, run name `box-loop-default`, loop state written).
- Resolved per-loop setup (loop `setup` overrides kit `setup`) → `setup := lp.Setup; if "" { cfg.Setup }` + `TestCreateLoopInstance` setup assertion.
- `ANTHROPIC_API_KEY` declared-secret fail-fast → `declaresSecret` + `TestCreateLoopInstanceRequiresAPIKey` (no calls made).
- Undefined loop / already-exists fail-fast → the two guards.
- Interactive `create`/`saveState` unchanged → `buildState` refactor preserves output; existing create tests pass.

Deferred to C-7 (the `loop` command): `doLoop` (parse `loop [<name>] [--once] [--keep] [--interval]`, resolve secrets against `secrets.yml`, dial, keep-awake, auto-create via this function / auto-destroy via `doDestroyInstance`), the **stop-aware tick** wiring (reset?→seed→check→agent, honoring the stop signal so a poison task / signal breaks the drain — C-5 carry-in), the **signal-aware sleep**, and the C-4 carry-in (remove the shared image when the **last** instance is torn down). The sentinel/workspace-volume lifecycle (other C-5 carry-in) is satisfied by construction here: an auto-created loop instance gets both `/agent-data` and workspace volumes fresh together, so no stale sentinel can outlive its workspace.

**Placeholder scan:** none — every code and command step is concrete.

**Type consistency:** `createLoopInstance(kitDir string, r runner.Runner, cfg kit.Config, loopName string, stdout io.Writer) (state.State, error)`, `buildState(cfg kit.Config, inst backend.Instance, setup string) state.State`, `loopContainer(kitName, loopName string) string`, and `declaresSecret(cfg kit.Config, name string) bool` are defined once and consumed consistently. `CreateContext.Kit` (Phase C-4) and `state.SaveFor`/`LoopInstance`/`ExistsFor` (Phase A) are used with their real signatures.
