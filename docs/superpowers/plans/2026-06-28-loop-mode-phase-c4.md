# Loop Mode — Phase C-4: Shared-Image Identity & Destroy Guard

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make loop instances share the kit image cleanly —
the caller names a kit *identity*, not a backend-specific tag —
and stop the interactive `destroy` from removing that shared image
while any loop instance still depends on it.

**Architecture:** Replace C-2's `CreateContext.Image`
(a full image tag only the backend knows how to form)
with `CreateContext.Kit` (an identity; the backend forms `at-cove-for-<Kit>`, defaulting to `Name`).
This lets `main` request the shared kit image without hardcoding the tag format.
Then add `state.HasLoopInstances` and use it
so the interactive `destroy` skips `docker rmi` whenever a `loop-<name>.json` state file exists.
This finishes the "shared image, built once" story and sets up C-5's `createLoopInstance`.

**Tech Stack:** Go 1.22, standard library only.

## Global Constraints

- Go version floor `go 1.22`; standard library only — no new dependencies.
- The backend owns its image-tag format; callers pass a kit identity, never a tag.
- `CreateContext.Kit == ""` derives the image from `Name` — so the interactive `create` path is byte-for-byte unchanged.
- Loop instances share the kit image and it is never removed while any loop instance exists (or when destroying a loop instance itself, per C-2).
- Container and volumes always derive from `Name`.
- Hermetic tests; follow existing `internal/backend/colima` and `main_test.go` style.

---

### Task 1: `CreateContext.Kit` — kit-identity-based shared image

**Files:**
- Modify: `internal/backend/backend.go` (rename `Image` → `Kit` on `CreateContext`)
- Modify: `internal/backend/colima/colima.go` (form the tag from `Kit` or `Name`)
- Test: `internal/backend/colima/colima_test.go` (update the C-2 test)

**Interfaces:**
- Consumes: nothing new.
- Produces (relied on by C-5's `createLoopInstance`): `backend.CreateContext.Kit string` — when non-empty, the backend builds/runs `at-cove-for-<Kit>`; when empty, derives from `Name`. Container/volumes always from `Name`.

- [ ] **Step 1: Update the existing C-2 test to use `Kit`**

In `internal/backend/colima/colima_test.go`, replace `TestCreateUsesProvidedImage` with:

```go
func TestCreateSharesImageViaKit(t *testing.T) {
	f := &runner.Fake{}
	c := New(f)
	_, err := c.Create(backend.CreateContext{
		Name:      "box-loop-foo",
		BuildDir:  "/b",
		Kit:       "box",
		Workspace: backend.WorkspaceMount{Mode: backend.Isolated},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Build tags the SHARED kit image (derived from Kit), not from the instance Name.
	if f.Calls[0].Args[0] != "build" || !contains(f.Calls[0].Args, "at-cove-for-box") {
		t.Fatalf("build call = %+v", f.Calls[0])
	}
	if contains(f.Calls[0].Args, "at-cove-for-box-loop-foo") {
		t.Fatalf("must not derive a per-loop image tag: %+v", f.Calls[0])
	}
	// Container and volumes still derive from Name.
	run := f.Calls[1].Args
	if !contains(run, "box-loop-foo") || !contains(run, "box-loop-foo-state:/agent-data") {
		t.Fatalf("container/volumes must derive from Name: %v", run)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `/usr/local/go/bin/go test ./internal/backend/colima/ -run TestCreateSharesImageViaKit`
Expected: FAIL — build error, `CreateContext` has no field `Kit`.

- [ ] **Step 3: Rename the field to `Kit`**

In `internal/backend/backend.go`, in `CreateContext`, replace the `Image` field with `Kit`:

```go
// CreateContext is everything a backend needs to provision a VM.
type CreateContext struct {
	Name      string
	BuildDir  string
	Kit       string // identity for the shared image tag; "" => derive from Name. Lets loop instances share the kit image.
	Workspace WorkspaceMount
}
```

- [ ] **Step 4: Form the image tag from `Kit` (or `Name`) in colima**

In `internal/backend/colima/colima.go`, change the start of `Create`:

```go
func (c *Colima) Create(ctx backend.CreateContext) (backend.Instance, error) {
	kit := ctx.Kit
	if kit == "" {
		kit = ctx.Name
	}
	img := image(kit)
	if err := c.r.Run("docker", "build", "-t", img, ctx.BuildDir); err != nil {
		return backend.Instance{}, err
	}
```

(The rest of `Create` is unchanged — container name and volumes still use `ctx.Name`, and the returned `Instance.Image` is `img`.)

- [ ] **Step 5: Run the tests to verify they pass**

Run: `/usr/local/go/bin/go test ./internal/backend/colima/`
Expected: PASS — the updated test plus the existing colima tests (`TestCreateIsolated` passes `Kit: ""`, deriving `at-cove-for-box` from `Name`).

- [ ] **Step 6: Build, vet, gofmt, and commit**

Run: `/usr/local/go/bin/go build ./... && /usr/local/go/bin/go vet ./internal/backend/... && /usr/local/go/bin/gofmt -l internal/backend/`
Expected: success, no vet output, `gofmt -l` prints nothing. (No production caller set `CreateContext.Image`, so the rename does not break `doCreate`.)

```bash
git add internal/backend/backend.go internal/backend/colima/colima.go internal/backend/colima/colima_test.go
git commit -m "feat(backend): CreateContext.Kit identity for the shared kit image"
```

---

### Task 2: Interactive `destroy` preserves the image while loops exist

**Files:**
- Modify: `internal/state/state.go` (add `HasLoopInstances`)
- Modify: `main.go` (`doDestroyInstance` guards the interactive `rmi`)
- Test: `internal/state/state_test.go`, `main_test.go`

**Interfaces:**
- Consumes: `state.Dir` (existing).
- Produces: `func state.HasLoopInstances(kitDir string) bool`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/state/state_test.go`:

```go
func TestHasLoopInstances(t *testing.T) {
	dir := t.TempDir()
	if HasLoopInstances(dir) {
		t.Fatal("no .state dir yet: should be false")
	}
	if err := SaveFor(dir, Interactive, State{Name: "x", Backend: "colima", Container: "x"}); err != nil {
		t.Fatal(err)
	}
	if HasLoopInstances(dir) {
		t.Fatal("only the interactive instance exists: should be false")
	}
	if err := SaveFor(dir, LoopInstance("foo"), State{Name: "x", Backend: "colima", Container: "x-loop-foo"}); err != nil {
		t.Fatal(err)
	}
	if !HasLoopInstances(dir) {
		t.Fatal("a loop instance exists: should be true")
	}
}
```

Append to `main_test.go`:

```go
func TestDestroyInteractivePreservesImageWhenLoopsExist(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	writeState(t, kitDir, "colima", "box") // interactive instance
	if err := state.SaveFor(kitDir, state.LoopInstance("foo"), state.State{
		Name: "box", Backend: "colima", Container: "box-loop-foo", Image: "at-cove-for-box",
	}); err != nil {
		t.Fatal(err)
	}
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"destroy", kitDir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut.String())
	}
	var rmi bool
	for _, c := range f.Calls {
		if c.Name == "docker" && len(c.Args) > 0 && c.Args[0] == "rmi" {
			rmi = true
		}
	}
	if rmi {
		t.Fatal("interactive destroy must NOT remove the shared image while a loop instance exists")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `/usr/local/go/bin/go test ./internal/state/ ./ -run 'TestHasLoopInstances|TestDestroyInteractivePreservesImageWhenLoopsExist'`
Expected: FAIL — `HasLoopInstances` undefined; and the interactive destroy still issues `docker rmi`.

- [ ] **Step 3: Add `HasLoopInstances`**

In `internal/state/state.go`, add `"strings"` to the import block, then add:

```go
// HasLoopInstances reports whether any loop instance state file (loop-*.json)
// exists in the kit. Used so the interactive destroy can keep the shared kit
// image while loop instances still depend on it.
func HasLoopInstances(kitDir string) bool {
	entries, err := os.ReadDir(Dir(kitDir))
	if err != nil {
		return false
	}
	for _, e := range entries {
		n := e.Name()
		if strings.HasPrefix(n, "loop-") && strings.HasSuffix(n, ".json") {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Guard the interactive `rmi` in `doDestroyInstance`**

In `main.go`, in `doDestroyInstance`, replace the image-clearing block:

```go
	bi := instanceFromState(st)
	if inst != state.Interactive {
		bi.Image = "" // loop instances share the kit image; never remove it on teardown
	}
```

with:

```go
	bi := instanceFromState(st)
	if inst != state.Interactive {
		bi.Image = "" // loop instances share the kit image; never remove it on teardown
	} else if state.HasLoopInstances(kitDir) {
		bi.Image = "" // loop instances still depend on the shared kit image; keep it
	}
```

- [ ] **Step 5: Run the tests, build, vet, gofmt**

Run: `/usr/local/go/bin/go test ./... && /usr/local/go/bin/go build ./... && /usr/local/go/bin/go vet ./... && /usr/local/go/bin/gofmt -l internal/state/ main.go`
Expected: PASS across all packages (new tests plus the existing `TestDestroyRemovesContainerImageAndState`, which has no loop instances, so it still removes the image); no vet output; `gofmt -l` prints nothing.

- [ ] **Step 6: Commit**

```bash
git add internal/state/state.go main.go internal/state/state_test.go main_test.go
git commit -m "feat(cli): interactive destroy keeps the shared image while loops exist"
```

---

## Self-Review

**Spec coverage (Phase C-4 slice):**
- Loop instances share the kit image via a clean identity (no leaked tag) → Task 1 (`CreateContext.Kit`).
- The shared image survives interactive destroy while loops exist → Task 2 (`HasLoopInstances` + the guard); the loop-instance-teardown case stays from C-2.
- Interactive `create` unchanged → `Kit == ""` derives from `Name`.

Deferred to C-5: `createLoopInstance` (using `Kit` + `<kit>-loop-<name>` naming + resolved per-loop setup + the `ANTHROPIC_API_KEY` declared-secret check), `RunCheck`, the loop workspace seeding sentinel/`fresh-workspace` reset, the drain-then-poll scheduler, and the `loop` command (`--once`/`--keep`/`--interval`, keep-awake, auto-create/destroy) — all tested at the command level like the existing commands. Loop volume reclamation (the other C-2 note) is also a C-5 decision.

**Placeholder scan:** none — every code and command step is concrete.

**Type consistency:** `CreateContext.Kit` is defined in Task 1 and consumed by its test (C-5 will consume it from `createLoopInstance`). `state.HasLoopInstances(kitDir string) bool` is defined in Task 2 and used in `doDestroyInstance` with the matching signature. The `instanceFromState`/`bi.Image` guard extends the C-2 logic without changing the loop-instance branch.
