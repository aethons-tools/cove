# Loop Mode — Phase C-2: Named-Instance Lifecycle Management

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let loop instances share the kit's image,
and make `destroy`/`status` name-aware (`--loop <name>`)
so a loop instance can be inspected and torn down —
with teardown preserving the shared image.

**Architecture:** Add an optional `CreateContext.Image` to the backend
so a loop instance is built/run against the kit's shared image tag (`at-cove-for-<kit>`)
while keeping its own loop-suffixed container and volumes.
Generalize `doDestroy`/`doStatus` over a `state.Instance` (via the Phase-A `*For` functions),
add a `--loop <name>` flag that selects the instance,
and clear the image on a loop teardown so the shared kit image is never removed.
The `loop` command that auto-creates these instances comes in a later sub-phase;
this one delivers the backend support and the management commands.

**Tech Stack:** Go 1.22, standard library only.

## Global Constraints

- Go version floor `go 1.22`; standard library only — no new dependencies.
- Loop instances **share the kit image** (`at-cove-for-<kit>`); the image is never removed on a loop teardown (other instances depend on it).
- `--loop <name>` selects a named instance; the name must pass `state.ValidLoopName` (Phase A) and is only valid for `destroy` and `status`.
- Interactive-instance behavior is **unchanged**: `destroy`/`status` with no `--loop` operate on `state.Interactive` exactly as before, and `doRecreate` still tears down the interactive instance.
- Backward compatible: `CreateContext.Image == ""` derives the tag from `Name` as today.
- Hermetic tests (`runner.Fake`); follow existing `internal/backend/colima` and `main_test.go` style.

---

### Task 1: Backend `CreateContext.Image` for a shared image

**Files:**
- Modify: `internal/backend/backend.go` (add `Image` to `CreateContext`)
- Modify: `internal/backend/colima/colima.go` (use `ctx.Image` when set)
- Test: `internal/backend/colima/colima_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces (relied on by C-4's loop create): `backend.CreateContext.Image string` — when non-empty, the backend builds/runs that image tag; when empty, it derives `at-cove-for-<Name>` as before. Container and volumes always derive from `Name`.

- [ ] **Step 1: Write the failing test**

Append to `internal/backend/colima/colima_test.go`:

```go
func TestCreateUsesProvidedImage(t *testing.T) {
	f := &runner.Fake{}
	c := New(f)
	_, err := c.Create(backend.CreateContext{
		Name:      "box-loop-foo",
		BuildDir:  "/b",
		Image:     "at-cove-for-box",
		Workspace: backend.WorkspaceMount{Mode: backend.Isolated},
	})
	if err != nil {
		t.Fatal(err)
	}
	// build tags the SHARED image, not one derived from the instance name.
	if f.Calls[0].Args[0] != "build" || !contains(f.Calls[0].Args, "at-cove-for-box") {
		t.Fatalf("build call = %+v", f.Calls[0])
	}
	if contains(f.Calls[0].Args, "at-cove-for-box-loop-foo") {
		t.Fatalf("must not derive a per-loop image tag: %+v", f.Calls[0])
	}
	// container and volumes still derive from Name.
	run := f.Calls[1].Args
	if !contains(run, "box-loop-foo") || !contains(run, "box-loop-foo-state:/agent-data") {
		t.Fatalf("container/volumes must derive from Name: %v", run)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `/usr/local/go/bin/go test ./internal/backend/colima/ -run TestCreateUsesProvidedImage`
Expected: FAIL — build error, `CreateContext` has no field `Image`.

- [ ] **Step 3: Add `Image` to `CreateContext`**

In `internal/backend/backend.go`, add the field to `CreateContext`:

```go
// CreateContext is everything a backend needs to provision a VM.
type CreateContext struct {
	Name      string
	BuildDir  string
	Image     string // image tag to build/run; "" => derive from Name. Lets loop instances share the kit image.
	Workspace WorkspaceMount
}
```

- [ ] **Step 4: Use `ctx.Image` in colima `Create`**

In `internal/backend/colima/colima.go`, change the first line of `Create`:

```go
func (c *Colima) Create(ctx backend.CreateContext) (backend.Instance, error) {
	img := ctx.Image
	if img == "" {
		img = image(ctx.Name)
	}
	if err := c.r.Run("docker", "build", "-t", img, ctx.BuildDir); err != nil {
		return backend.Instance{}, err
	}
```

(The rest of `Create` is unchanged — container name and volumes still use `ctx.Name`, and the returned `Instance.Image` is `img`.)

- [ ] **Step 5: Run the tests to verify they pass**

Run: `/usr/local/go/bin/go test ./internal/backend/colima/`
Expected: PASS — the new test plus the existing colima tests (`TestCreateIsolated` passes `Image: ""`, so it still derives `at-cove-for-box`).

- [ ] **Step 6: Build, vet, gofmt, and commit**

Run: `/usr/local/go/bin/go build ./... && /usr/local/go/bin/go vet ./internal/backend/... && /usr/local/go/bin/gofmt -l internal/backend/`
Expected: success, no vet output, `gofmt -l` prints nothing.

```bash
git add internal/backend/backend.go internal/backend/colima/colima.go internal/backend/colima/colima_test.go
git commit -m "feat(backend): optional CreateContext.Image so instances can share an image"
```

---

### Task 2: Name-aware `destroy`/`status` with `--loop`

**Files:**
- Modify: `main.go` (`--loop` flag; generalize `doDestroy`/`doStatus` over `state.Instance`)
- Test: `main_test.go`

**Interfaces:**
- Consumes: `state.LoadFor`/`PathFor`/`DeleteFor`/`AcquireExclusiveFor`/`Instance`/`Interactive`/`LoopInstance`/`ValidLoopName` (Phase A).
- Produces: `doDestroyInstance(kitDir string, r runner.Runner, inst state.Instance, dryRun bool, stdout io.Writer) error` and `doStatusInstance(...)` with the same shape; `--loop <name>` CLI flag.

- [ ] **Step 1: Write the failing tests**

Append to `main_test.go` (ensure imports include `state`, `runner`, `bytes`, `strings`, `os` — most are already present):

```go
func TestDestroyLoopInstancePreservesImage(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
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
	var rm, rmi bool
	for _, c := range f.Calls {
		if c.Name == "docker" && len(c.Args) > 0 && c.Args[0] == "rm" {
			rm = true
		}
		if c.Name == "docker" && len(c.Args) > 0 && c.Args[0] == "rmi" {
			rmi = true
		}
	}
	if !rm {
		t.Fatal("loop container should be removed")
	}
	if rmi {
		t.Fatal("shared image must NOT be removed on loop teardown")
	}
	if state.ExistsFor(kitDir, state.LoopInstance("foo")) {
		t.Fatal("loop state should be deleted")
	}
}

func TestStatusLoopInstance(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	if err := state.SaveFor(kitDir, state.LoopInstance("foo"), state.State{
		Name: "box", Backend: "colima", Container: "box-loop-foo", Image: "at-cove-for-box",
	}); err != nil {
		t.Fatal(err)
	}
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "true\n"}}}
	var out, errOut bytes.Buffer
	code := run([]string{"status", "--loop", "foo", kitDir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "running") {
		t.Fatalf("status = %q", out.String())
	}
}

func TestLoopFlagRejectsBadName(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"destroy", "--loop", "../etc", kitDir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code == 0 {
		t.Fatal("invalid loop name must error")
	}
}

func TestLoopFlagRejectedForOtherCommands(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"--loop", "foo", "build", kitDir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code == 0 {
		t.Fatal("--loop on a non-destroy/status command must error")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `/usr/local/go/bin/go test ./ -run 'TestDestroyLoopInstance|TestStatusLoopInstance|TestLoopFlag'`
Expected: FAIL — `--loop` is parsed as a positional (kit-dir), so the commands act on the interactive instance / error differently than asserted.

- [ ] **Step 3: Parse the `--loop` flag**

In `main.go`, in `run(...)`, add the variable next to the others (after `wsPath := ""`):

```go
	loopName := ""
```

and add a case in the argument-parsing `switch` (after the `--workspace`/`--ws` case):

```go
		case a == "--loop":
			if i+1 >= len(argv) {
				fmt.Fprintln(stderr, "at-cove: --loop requires a name")
				return 2
			}
			i++
			loopName = argv[i]
```

- [ ] **Step 4: Resolve the target instance and route destroy/status**

In `main.go`, immediately before the `switch cmd {` dispatch, add:

```go
	inst := state.Interactive
	if loopName != "" {
		if err := state.ValidLoopName(loopName); err != nil {
			fmt.Fprintln(stderr, "at-cove:", err)
			return 2
		}
		if cmd != "destroy" && cmd != "status" {
			fmt.Fprintln(stderr, "at-cove: --loop is only valid for destroy and status")
			return 2
		}
		inst = state.LoopInstance(loopName)
	}
```

Then change the `destroy` and `status` dispatch cases:

```go
	case "destroy":
		err = doDestroyInstance(kitDir, r, inst, dryRun, stdout)
	case "status":
		err = doStatusInstance(kitDir, r, inst, dryRun, stdout)
```

- [ ] **Step 5: Generalize `doDestroy` over an instance**

In `main.go`, replace the existing `doDestroy` function with an instance-aware version plus an interactive wrapper (so `doRecreate`'s `doDestroy(kitDir, r, false, stdout)` call keeps working):

```go
// doDestroyInstance tears an instance down under an EXCLUSIVE lock: it refuses
// if any connection (or a running loop) holds the shared lock. It removes the
// container (keeping volumes), removes the image for the interactive instance
// but NOT for a loop instance (which shares the kit image), then deletes the
// state file.
func doDestroyInstance(kitDir string, r runner.Runner, inst state.Instance, dryRun bool, stdout io.Writer) error {
	st, err := state.LoadFor(kitDir, inst)
	if err != nil {
		return err
	}
	if dryRun {
		if inst == state.Interactive {
			fmt.Fprintf(stdout, "would destroy %s (keeping volumes), remove image %s, and delete %s\n",
				st.Container, st.Image, state.PathFor(kitDir, inst))
		} else {
			fmt.Fprintf(stdout, "would destroy %s (keeping volumes and the shared image) and delete %s\n",
				st.Container, state.PathFor(kitDir, inst))
		}
		return nil
	}
	b, err := getBackend(st.Backend, r)
	if err != nil {
		return err
	}

	lock, err := state.AcquireExclusiveFor(kitDir, inst)
	if err != nil {
		if errors.Is(err, state.ErrLocked) {
			return fmt.Errorf("refusing to destroy %s: it has active connection(s)", st.Container)
		}
		return err
	}
	defer lock.Release()

	bi := instanceFromState(st)
	if inst != state.Interactive {
		bi.Image = "" // loop instances share the kit image; never remove it on teardown
	}
	if err := b.Destroy(bi); err != nil {
		return err
	}
	return state.DeleteFor(kitDir, inst)
}

func doDestroy(kitDir string, r runner.Runner, dryRun bool, stdout io.Writer) error {
	return doDestroyInstance(kitDir, r, state.Interactive, dryRun, stdout)
}
```

- [ ] **Step 6: Generalize `doStatus` over an instance**

In `main.go`, replace the existing `doStatus` function with:

```go
func doStatusInstance(kitDir string, r runner.Runner, inst state.Instance, dryRun bool, stdout io.Writer) error {
	st, err := state.LoadFor(kitDir, inst)
	if errors.Is(err, state.ErrNotCreated) {
		fmt.Fprintln(stdout, "absent (not created)")
		return nil
	}
	if err != nil {
		return err
	}
	if dryRun {
		fmt.Fprintf(stdout, "would query status of %s\n", st.Container)
		return nil
	}
	b, err := getBackend(st.Backend, r)
	if err != nil {
		return err
	}
	vmState, err := b.GetStatus(st.Container)
	if err != nil {
		return err
	}
	labels := map[backend.State]string{
		backend.StateAbsent:  "absent",
		backend.StateStopped: "stopped",
		backend.StateRunning: "running",
	}
	fmt.Fprintf(stdout, "%s  (image %s)\n", labels[vmState], st.Image)
	return nil
}
```

(The old `doStatus` had no other callers; the dispatch now calls `doStatusInstance` directly.)

- [ ] **Step 7: Update the help text**

In `main.go`, in the `usage` const, update the `destroy` and `status` lines and document the flag:

```
  at-cove destroy  [kit-dir] [--loop <name>]
  at-cove status   [kit-dir] [--loop <name>]
```

and add under the connect flags block (or a new line):

```
  --loop <name>  act on the named loop instance (destroy, status)
```

- [ ] **Step 8: Run the tests, build, vet, gofmt**

Run: `/usr/local/go/bin/go test ./... && /usr/local/go/bin/go build ./... && /usr/local/go/bin/go vet ./... && /usr/local/go/bin/gofmt -l main.go`
Expected: PASS across all packages (new tests plus existing `TestDestroyRemovesContainerImageAndState`, `TestDestroyBlockedByActiveConnection`, `TestStatusDispatchesToBackend`, which exercise the interactive path through the wrappers); no vet output; `gofmt -l` prints nothing.

- [ ] **Step 9: Commit**

```bash
git add main.go main_test.go
git commit -m "feat(cli): name-aware destroy/status via --loop; preserve shared image"
```

---

## Self-Review

**Spec coverage (Phase C-2 slice):**
- Loop instances share the kit image, built once/reused → Task 1 (`CreateContext.Image`); loop teardown preserves it → Task 2 (`bi.Image = ""` for non-interactive).
- Loop-suffixed container/volumes distinct from interactive → Task 1 (container/volumes from `Name`); the `<kit>-loop-<name>` naming is applied by C-4's create, exercised here via test state with `Container: "box-loop-foo"`.
- Name-aware `destroy`/`status` so a `--keep` loop instance can be inspected/torn down → Task 2.
- Per-instance exclusive lock blocks destroy of a running instance → Task 2 (`AcquireExclusiveFor`).
- Loop-name charset validation at use → Task 2 (`state.ValidLoopName`) + `TestLoopFlagRejectsBadName`.

Deferred to C-3/C-4: the headless agent run and setup carry-ins (C-3); loop config resolution, `createLoopInstance` (using `CreateContext.Image` + `<kit>-loop-<name>` naming), the drain-then-poll scheduler, and the `loop` command with `--once`/`--keep`/`--interval`/keep-awake/auto-create/destroy (C-4).

**Placeholder scan:** none — every code and command step is concrete.

**Type consistency:** `doDestroyInstance`/`doStatusInstance` signatures match between their definitions and the dispatch call sites; `state.Instance`, `state.Interactive`, `state.LoopInstance`, `state.SaveFor`/`LoadFor`/`PathFor`/`DeleteFor`/`ExistsFor`/`AcquireExclusiveFor`, and `state.ValidLoopName` are all Phase-A symbols used with their real signatures. `CreateContext.Image` is defined in Task 1 and consumed by the test there.
