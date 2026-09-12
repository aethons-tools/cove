# harbor managed-cove supervisor spine — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build harbor's durable, leased managed-cove runtime registry + lifecycle supervisor (Raise → Record → status → Teardown, reconciler-backstopped), driven by `at-harbor cove` admin verbs, with a fake/placeholder launcher — no gRPC, no real backend.

**Architecture:** A new `Instance` entity (runtime) keyed by the roster `Actor`'s id (identity), both in harbor's JSON store (additively extended). A `Supervisor` struct in `internal/harbor` owns the lifecycle state machine and lease logic, driven by an injected `Launcher` seam (fake in tests, a placeholder in `serve`). The admin API + `at-harbor cove` verbs are the slice-1 driver. Pure, kit-free, grpc-free, hermetic.

**Tech Stack:** Go stdlib only (`net/http`, `log/slog`, `crypto/rand`, `encoding/json`); `gopkg.in/yaml.v3` for serve config (already a dep).

**Spec:** `docs/superpowers/specs/2026-09-12-harbor-cove-supervisor.md` (COV-149).

## Global Constraints

- **`internal/harbor` must stay kit-free and grpc-free.** The supervisor depends only on the `Launcher` interface; the real backend/kit launcher is a later slice wired from `cmd/at-harbor`. Gate: `go list -deps ./internal/harbor | grep -iE 'internal/kit|grpc'` stays empty.
- **Secrets/tokens never hit logs.** The identity token minted at raise is returned in the API response (once, like `enroll`) but never logged. Admin mutation logs carry only `operator`, `id`, `role`, `project`, `phase` — never tokens/hashes.
- **Identity vs runtime are separate entities.** The roster `Actor` (`{id, tokenHash, grants, expiry}`) is identity; the new `Instance` (`{location, phase, activity, lease, …}`) is runtime, keyed by `ActorID`. `raise` = `Enroll` + create `Instance`; `teardown` = `Launcher.Teardown` + remove `Instance` + `RemoveActor`.
- **Phase is supervisor-owned; Activity is cove-reported.** A status report only ever sets `Activity` (+ lease/last-seen); the sole phase effect of a report is `Activity==Done ⇒ Phase=Terminating`.
- **Store is additive.** Versioning is structural (no version int). A v4 file (no `instances` key) loads with an empty `Instances` map — never a migration error.
- **Tests are hermetic** — fake `Launcher`, temp-dir `FileStore`, injected `now` clock. No network, no backend, no Docker. `go test -race` is unavailable in the sandbox; argue concurrency by construction (mutex-guarded store) + a functional test.
- **Append, don't overwrite tests.** Every task adds tests to existing `_test.go` files without deleting pre-existing ones.

---

## File Structure

- **Create** `internal/harbor/instance.go` — `Phase`, `Activity`, `Lease`, `Instance` types.
- **Create** `internal/harbor/instance_test.go` — type + JSON round-trip tests.
- **Create** `internal/harbor/supervisor.go` — `Liveness`, `RaiseSpec`, `Launcher`, `Supervisor` + `NewSupervisor`/`NewHolderID`/`Raise`/`Report`/`Teardown`/`Reconcile`/`Run`.
- **Create** `internal/harbor/supervisor_test.go` — fake launcher + state-machine/lease/reconciler/restart tests.
- **Modify** `internal/harbor/filestore.go` — `storeFile.Instances`, `FileStore.instances`, load/save, 4 `Store` methods.
- **Modify** `internal/harbor/filestore_test.go` — store v5 CRUD + additive-load tests.
- **Modify** `internal/harbor/admin.go` — cove wire types + routes; `NewAdminHandler` gains a `*Supervisor` param.
- **Modify** `internal/harbor/admin_test.go` — update harness for the new param; cove-route tests.
- **Modify** `internal/harbor/oidc_test.go`, `operator_test.go` — update `NewAdminHandler` call sites (new param).
- **Modify** `internal/harbor/adminclient/adminclient.go` — `RaiseCove`/`ListCoves`/`ReportCoveStatus`/`TeardownCove`.
- **Modify** `internal/harbor/adminclient/adminclient_test.go` — client round-trip tests.
- **Modify** `cmd/at-harbor/main.go` — `cmdCove` + command-table row; placeholder launcher; serve wiring.
- **Modify** `cmd/at-harbor/config.go` — `serveConfig.Runtime` + `runtimeDurations()`.
- **Modify** `cmd/at-harbor/config_test.go` — runtime-config parse/default/validation tests.
- **Create** `docs/usage/harbor/coves.md`; **Modify** `docs/usage/harbor/INDEX.md`, `operators.md`, `serve.md`.

---

## Task 1: Instance model + store v5 registry

**Files:**
- Create: `internal/harbor/instance.go`
- Create: `internal/harbor/instance_test.go`
- Modify: `internal/harbor/filestore.go` (storeFile, FileStore, NewFileStore v3/v4 branch, save, Store interface, 4 methods)
- Test: `internal/harbor/filestore_test.go`

**Interfaces:**
- Produces: the `Instance`/`Phase`/`Activity`/`Lease` types and `Store` methods `PutInstance(Instance) error`, `GetInstance(string) (Instance, bool)`, `ListInstances() []Instance`, `RemoveInstance(string) error`. `Instance` has **no** slice/map fields, so value returns are already copies (unlike `Kit`).

- [ ] **Step 1: Write the failing test** (`internal/harbor/instance_test.go`)

```go
package harbor

import (
	"encoding/json"
	"testing"
	"time"
)

func TestInstanceJSONRoundTrip(t *testing.T) {
	in := Instance{
		ActorID: "spider-42", Project: "acme", Role: "guest", Unit: "AET-7",
		Backend: "", Location: "placeholder:spider-42",
		Phase: PhaseLive, Activity: ActivityWaiting,
		Lease:    Lease{Holder: "host/1/abcd", Expiry: time.Unix(1000, 0).UTC()},
		RaisedAt: time.Unix(10, 0).UTC(), LastSeen: time.Unix(20, 0).UTC(),
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Instance
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if out != in {
		t.Errorf("round-trip mismatch:\n got %+v\nwant %+v", out, in)
	}
}

func TestPhaseActivityConstants(t *testing.T) {
	// Guards the wire strings callers and the gRPC slice will depend on.
	cases := map[string]string{
		string(PhaseRaising): "raising", string(PhaseLive): "live",
		string(PhaseTerminating): "terminating", string(PhaseGone): "gone", string(PhaseLost): "lost",
		string(ActivityRunning): "running", string(ActivityWaiting): "waiting",
		string(ActivityBlocked): "blocked", string(ActivityDone): "done",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("constant = %q, want %q", got, want)
		}
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/harbor/ -run 'TestInstanceJSONRoundTrip|TestPhaseActivityConstants' -v`
Expected: FAIL (undefined: Instance / PhaseLive / …).

- [ ] **Step 3: Create `internal/harbor/instance.go`**

```go
package harbor

import "time"

// Phase is the supervisor-owned lifecycle of a managed cove. Only the supervisor
// writes it (via raise/teardown/reconcile); it is never set from a cove-reported
// value.
type Phase string

const (
	PhaseRaising     Phase = "raising"     // Launcher.Raise called, not yet confirmed Live
	PhaseLive        Phase = "live"        // running and leased; Activity is meaningful
	PhaseTerminating Phase = "terminating" // teardown decided (Done or Lost); Launcher.Teardown in flight
	PhaseGone        Phase = "gone"        // torn down + deregistered (terminal)
	PhaseLost        Phase = "lost"        // reconciler declared dead (lease expired + Probe dead) → Terminating
)

// Activity is cove-reported and only meaningful while Phase == Live. The
// supervisor records what the cove says it is doing; the only phase effect of a
// report is ActivityDone ⇒ PhaseTerminating.
type Activity string

const (
	ActivityRunning Activity = "running"
	ActivityWaiting Activity = "waiting"
	ActivityBlocked Activity = "blocked"
	ActivityDone    Activity = "done"
)

// Lease records which harbor process owns an Instance and until when. Past
// Expiry, any process may steal it (the holder is presumed dead). This is also
// the reconnect handoff for the later gRPC-stream slice.
type Lease struct {
	Holder string    `json:"holder"`
	Expiry time.Time `json:"expiry"`
}

// Instance is the runtime record of one managed cove, keyed by the roster
// Actor's id. Identity (token, grants, expiry) lives on the Actor; this is the
// operational half (location, status, lease). It has no slice/map fields, so a
// value copy is a full copy.
type Instance struct {
	ActorID  string    `json:"actor_id"`
	Project  string    `json:"project"`
	Role     string    `json:"role"`
	Unit     string    `json:"unit,omitempty"`
	Backend  string    `json:"backend,omitempty"`  // populated by the real launcher (later slice)
	Location string    `json:"location,omitempty"` // opaque handle from Launcher.Raise
	Phase    Phase     `json:"phase"`
	Activity Activity  `json:"activity,omitempty"`
	Lease    Lease     `json:"lease"`
	RaisedAt time.Time `json:"raised_at"`
	LastSeen time.Time `json:"last_seen"`
}
```

- [ ] **Step 4: Run to verify type tests pass**

Run: `go test ./internal/harbor/ -run 'TestInstanceJSONRoundTrip|TestPhaseActivityConstants' -v`
Expected: PASS.

- [ ] **Step 5: Write the failing store test** (append to `internal/harbor/filestore_test.go`)

```go
func TestInstanceRegistryRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.json")
	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	inst := Instance{ActorID: "w1", Project: "default", Role: "guest", Phase: PhaseLive,
		Lease: Lease{Holder: "h1", Expiry: time.Unix(500, 0).UTC()}}
	if err := fs.PutInstance(inst); err != nil {
		t.Fatal(err)
	}
	// Reload from disk — durability.
	fs2, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := fs2.GetInstance("w1")
	if !ok || got.Role != "guest" || got.Phase != PhaseLive || got.Lease.Holder != "h1" {
		t.Fatalf("reloaded instance wrong: %+v ok=%v", got, ok)
	}
	if n := len(fs2.ListInstances()); n != 1 {
		t.Fatalf("ListInstances = %d, want 1", n)
	}
	if err := fs2.RemoveInstance("w1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := fs2.GetInstance("w1"); ok {
		t.Fatal("instance still present after remove")
	}
	if err := fs2.RemoveInstance("w1"); err == nil {
		t.Fatal("expected error removing absent instance")
	}
}

func TestStoreLoadsV4FileWithoutInstances(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.json")
	// A v4 file: roles + actors + kits, NO "instances" key.
	v4 := `{"roles":{"default":{"guest":{"name":"guest","scope":{"destinations":["a"],"repos":[],"ttl":0}}}},` +
		`"actors":{},"destinations":{},"kits":{}}`
	if err := os.WriteFile(path, []byte(v4), 0o600); err != nil {
		t.Fatal(err)
	}
	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("v4 file must load additively, got %v", err)
	}
	if n := len(fs.ListInstances()); n != 0 {
		t.Fatalf("expected empty instance registry, got %d", n)
	}
	if _, ok := fs.GetRole("default", "guest"); !ok {
		t.Fatal("v4 role lost on load")
	}
}
```

(`os` is already imported in `filestore_test.go`; if not, add it.)

- [ ] **Step 6: Run to verify it fails**

Run: `go test ./internal/harbor/ -run 'TestInstanceRegistry|TestStoreLoadsV4' -v`
Expected: FAIL (fs.PutInstance undefined).

- [ ] **Step 7: Extend `filestore.go`**

In `storeFile` add the field:
```go
	Kits         map[string]Kit             `json:"kits"`
	Instances    map[string]Instance        `json:"instances"` // keyed by Instance.ActorID
```
In `FileStore` add `instances map[string]Instance`. In `NewFileStore`'s initializer add `instances: map[string]Instance{}`. In the **v3/v4 branch** (the `if v3.Actors != nil || v3.Roles != nil {` block), after the `Kits` copy add:
```go
			if v3.Instances != nil {
				fs.instances = v3.Instances
			}
```
In `save()` include it:
```go
	data, err := json.MarshalIndent(storeFile{Roles: fs.roles, Actors: fs.actors, Destinations: fs.dests, Kits: fs.kits, Instances: fs.instances}, "", "  ")
```
Add to the `Store` interface (after the kit block):
```go
	PutInstance(i Instance) error
	GetInstance(actorID string) (Instance, bool)
	ListInstances() []Instance
	RemoveInstance(actorID string) error
```
Add the methods:
```go
func (fs *FileStore) PutInstance(i Instance) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if i.ActorID == "" {
		return fmt.Errorf("instance actor id is required")
	}
	fs.instances[i.ActorID] = i
	return fs.save()
}

func (fs *FileStore) GetInstance(actorID string) (Instance, bool) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	i, ok := fs.instances[actorID]
	return i, ok // Instance has no reference fields; value copy is a full copy
}

func (fs *FileStore) ListInstances() []Instance {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	out := make([]Instance, 0, len(fs.instances))
	for _, i := range fs.instances {
		out = append(out, i)
	}
	return out
}

func (fs *FileStore) RemoveInstance(actorID string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if _, ok := fs.instances[actorID]; !ok {
		return fmt.Errorf("instance %q not found", actorID)
	}
	delete(fs.instances, actorID)
	return fs.save()
}
```

- [ ] **Step 8: Run to verify all Task-1 tests pass**

Run: `go test ./internal/harbor/ -run 'Instance|StoreLoadsV4' -v`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/harbor/instance.go internal/harbor/instance_test.go internal/harbor/filestore.go internal/harbor/filestore_test.go
git commit -m "harbor: Instance runtime model + store v5 registry (COV-149)"
```

---

## Task 2: Supervisor — Launcher seam + lifecycle (Raise / Report / Teardown)

**Files:**
- Create: `internal/harbor/supervisor.go`
- Create: `internal/harbor/supervisor_test.go`

**Interfaces:**
- Consumes: `Store` (Task 1 methods), `Enroll`, `RemoveActor`, `orDefaultProject` (exists in `admin.go`), `Instance`/`Phase`/`Activity`/`Lease` (Task 1).
- Produces:
  - `type Liveness int` with `LivenessUnknown=0, LivenessAlive, LivenessDead`.
  - `type RaiseSpec struct { ActorID, Project, Role, Unit string }`.
  - `type Launcher interface { Raise(ctx, RaiseSpec) (string, error); Teardown(ctx, Instance) error; Probe(ctx, Instance) (Liveness, error) }`.
  - `type Supervisor struct{…}`; `func NewSupervisor(store Store, launcher Launcher, holder string, ttl, reconcile time.Duration, now func() time.Time, log *slog.Logger) *Supervisor`.
  - `func NewHolderID() string`.
  - `func (s *Supervisor) Raise(ctx, RaiseSpec) (Instance, string, error)` — returns the Instance and the minted identity token (once).
  - `func (s *Supervisor) Report(ctx, actorID string, a Activity) error`.
  - `func (s *Supervisor) Teardown(ctx, actorID string) error`.

- [ ] **Step 1: Write the failing test** (`internal/harbor/supervisor_test.go`)

```go
package harbor

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"
)

// fakeLauncher is a scripted Launcher for hermetic supervisor tests.
type fakeLauncher struct {
	loc         string
	raiseErr    error
	teardownErr error
	liveness    Liveness
	probeErr    error
	raised      []string
	tornDown    []string
}

func (f *fakeLauncher) Raise(_ context.Context, spec RaiseSpec) (string, error) {
	if f.raiseErr != nil {
		return "", f.raiseErr
	}
	f.raised = append(f.raised, spec.ActorID)
	if f.loc != "" {
		return f.loc, nil
	}
	return "fake:" + spec.ActorID, nil
}
func (f *fakeLauncher) Teardown(_ context.Context, inst Instance) error {
	f.tornDown = append(f.tornDown, inst.ActorID)
	return f.teardownErr
}
func (f *fakeLauncher) Probe(_ context.Context, _ Instance) (Liveness, error) {
	return f.liveness, f.probeErr
}

// supTestKit builds a supervisor over a temp store with a guest role, a fixed
// clock, and the given launcher. Returns the supervisor, store, and a pointer to
// the mutable clock.
func supTestKit(t *testing.T, l Launcher) (*Supervisor, Store, *time.Time) {
	t.Helper()
	store, err := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutRole("default", Role{Name: "guest", Scope: Scope{Destinations: []string{"anthropic"}, TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	clk := time.Unix(1000, 0).UTC()
	clkp := &clk
	sup := NewSupervisor(store, l, "holder-A", 60*time.Second, 30*time.Second,
		func() time.Time { return *clkp }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return sup, store, clkp
}

func TestRaiseEnrollsAndRecordsLiveInstance(t *testing.T) {
	f := &fakeLauncher{liveness: LivenessAlive}
	sup, store, _ := supTestKit(t, f)
	inst, tok, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest", Unit: "AET-1"})
	if err != nil {
		t.Fatal(err)
	}
	if tok == "" {
		t.Fatal("expected a minted identity token")
	}
	if inst.Phase != PhaseLive || inst.Activity != ActivityRunning {
		t.Fatalf("phase/activity = %s/%s", inst.Phase, inst.Activity)
	}
	if inst.Lease.Holder != "holder-A" || !inst.Lease.Expiry.Equal(time.Unix(1060, 0).UTC()) {
		t.Fatalf("lease = %+v", inst.Lease)
	}
	if got, ok := store.GetInstance("w1"); !ok || got.Location != "fake:w1" {
		t.Fatalf("instance not recorded: %+v ok=%v", got, ok)
	}
	// Identity was enrolled (an actor exists).
	if len(store.ListActors()) != 1 {
		t.Fatalf("expected 1 actor, got %d", len(store.ListActors()))
	}
	if f.raised[0] != "w1" {
		t.Fatalf("launcher not called: %+v", f.raised)
	}
}

func TestRaiseRollsBackIdentityWhenLauncherFails(t *testing.T) {
	f := &fakeLauncher{raiseErr: errors.New("backend down")}
	sup, store, _ := supTestKit(t, f)
	if _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest"}); err == nil {
		t.Fatal("expected raise to fail")
	}
	if len(store.ListActors()) != 0 {
		t.Fatal("failed raise must leave no dangling identity")
	}
	if _, ok := store.GetInstance("w1"); ok {
		t.Fatal("failed raise must record no instance")
	}
}

func TestRaiseRequiresExistingRole(t *testing.T) {
	sup, _, _ := supTestKit(t, &fakeLauncher{})
	if _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "ghost"}); err == nil {
		t.Fatal("expected fail-closed on unknown role")
	}
}

func TestReportSetsActivityAndRenewsLease(t *testing.T) {
	sup, store, clk := supTestKit(t, &fakeLauncher{liveness: LivenessAlive})
	sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest"})
	*clk = clk.Add(10 * time.Second) // now 1010
	if err := sup.Report(context.Background(), "w1", ActivityWaiting); err != nil {
		t.Fatal(err)
	}
	got, _ := store.GetInstance("w1")
	if got.Activity != ActivityWaiting {
		t.Fatalf("activity = %s", got.Activity)
	}
	if !got.Lease.Expiry.Equal(time.Unix(1070, 0).UTC()) { // 1010 + 60
		t.Fatalf("lease not renewed: %+v", got.Lease)
	}
	if got.Phase != PhaseLive {
		t.Fatalf("report must not change Phase off Done: %s", got.Phase)
	}
}

func TestReportDoneTearsDown(t *testing.T) {
	f := &fakeLauncher{liveness: LivenessAlive}
	sup, store, _ := supTestKit(t, f)
	sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest"})
	if err := sup.Report(context.Background(), "w1", ActivityDone); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.GetInstance("w1"); ok {
		t.Fatal("Done should tear the instance down")
	}
	if len(store.ListActors()) != 0 {
		t.Fatal("teardown should revoke the identity")
	}
	if len(f.tornDown) != 1 || f.tornDown[0] != "w1" {
		t.Fatalf("launcher teardown not called: %+v", f.tornDown)
	}
}

func TestTeardownIsIdempotent(t *testing.T) {
	sup, _, _ := supTestKit(t, &fakeLauncher{})
	sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest"})
	if err := sup.Teardown(context.Background(), "w1"); err != nil {
		t.Fatal(err)
	}
	if err := sup.Teardown(context.Background(), "w1"); err != nil {
		t.Fatalf("second teardown must be a no-op, got %v", err)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/harbor/ -run 'TestRaise|TestReport|TestTeardown' -v`
Expected: FAIL (NewSupervisor undefined).

- [ ] **Step 3: Create `internal/harbor/supervisor.go`**

```go
package harbor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"time"
)

// Liveness is a Launcher.Probe result.
type Liveness int

const (
	LivenessUnknown Liveness = iota
	LivenessAlive
	LivenessDead
)

// RaiseSpec is the request to raise a managed cove. Scope/kit resolution lives in
// the role (and, in a later slice, the Launcher); this carries only identity.
type RaiseSpec struct {
	ActorID string
	Project string
	Role    string
	Unit    string
}

// Launcher is the seam over "actually start/stop/probe a cove on a backend". The
// supervisor depends only on this, so it stays kit-free and grpc-free and fully
// hermetic. The real backend+kit implementation is a later slice, wired from
// cmd/at-harbor.
type Launcher interface {
	Raise(ctx context.Context, spec RaiseSpec) (location string, err error)
	Teardown(ctx context.Context, inst Instance) error
	Probe(ctx context.Context, inst Instance) (Liveness, error)
}

// Supervisor owns the managed-cove lifecycle: the durable registry (via Store),
// the lease model, and the state machine. One supervisor per harbor process.
type Supervisor struct {
	store     Store
	launcher  Launcher
	holder    string // this process's lease-holder id
	ttl       time.Duration
	reconcile time.Duration
	now       func() time.Time
	log       *slog.Logger
}

func NewSupervisor(store Store, launcher Launcher, holder string, ttl, reconcile time.Duration, now func() time.Time, log *slog.Logger) *Supervisor {
	if now == nil {
		now = time.Now
	}
	return &Supervisor{store: store, launcher: launcher, holder: holder, ttl: ttl, reconcile: reconcile, now: now, log: log}
}

// NewHolderID mints a per-process lease-holder id: <hostname>/<pid>/<4 hex>.
func NewHolderID() string {
	host, _ := os.Hostname()
	var b [4]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%s/%d/%s", host, os.Getpid(), hex.EncodeToString(b[:]))
}

// Raise enrolls the identity, launches the cove, and records a Live Instance
// leased to this process. Returns the Instance and the minted identity token
// (once — the launcher consumes it to connect the cove in a later slice). A
// failed launch rolls back the enrollment so no dangling identity is left.
func (s *Supervisor) Raise(ctx context.Context, spec RaiseSpec) (Instance, string, error) {
	if spec.ActorID == "" {
		return Instance{}, "", fmt.Errorf("actor id is required")
	}
	tok, err := Enroll(s.store, spec.ActorID, spec.Project, spec.Role, nil, s.now())
	if err != nil {
		return Instance{}, "", err
	}
	loc, err := s.launcher.Raise(ctx, spec)
	if err != nil {
		_ = s.store.RemoveActor(spec.ActorID) // rollback identity on failed launch
		return Instance{}, "", fmt.Errorf("raise: %w", err)
	}
	now := s.now()
	inst := Instance{
		ActorID: spec.ActorID, Project: orDefaultProject(spec.Project), Role: spec.Role, Unit: spec.Unit,
		Location: loc, Phase: PhaseLive, Activity: ActivityRunning,
		Lease:    Lease{Holder: s.holder, Expiry: now.Add(s.ttl)},
		RaisedAt: now, LastSeen: now,
	}
	if err := s.store.PutInstance(inst); err != nil {
		_ = s.launcher.Teardown(ctx, inst)
		_ = s.store.RemoveActor(spec.ActorID)
		return Instance{}, "", err
	}
	if s.log != nil {
		s.log.Info("cove raised", "id", spec.ActorID, "project", inst.Project, "role", spec.Role, "phase", string(inst.Phase))
	}
	return inst, tok, nil
}

// Report records a cove-reported Activity, renewing (and stealing if necessary)
// the lease — a report means the cove is talking to THIS process now. The only
// phase effect is ActivityDone ⇒ Terminating (then teardown).
func (s *Supervisor) Report(ctx context.Context, actorID string, a Activity) error {
	inst, ok := s.store.GetInstance(actorID)
	if !ok {
		return fmt.Errorf("no instance for actor %q", actorID)
	}
	if inst.Phase == PhaseGone {
		return fmt.Errorf("instance %q is gone", actorID)
	}
	now := s.now()
	inst.Activity = a
	inst.LastSeen = now
	inst.Lease = Lease{Holder: s.holder, Expiry: now.Add(s.ttl)}
	if a == ActivityDone {
		inst.Phase = PhaseTerminating
	}
	if err := s.store.PutInstance(inst); err != nil {
		return err
	}
	if inst.Phase == PhaseTerminating {
		return s.Teardown(ctx, actorID)
	}
	return nil
}

// Teardown tears the cove down and deregisters it: Launcher.Teardown, then remove
// the Instance and revoke the identity. Idempotent — an absent instance is a
// no-op.
func (s *Supervisor) Teardown(ctx context.Context, actorID string) error {
	inst, ok := s.store.GetInstance(actorID)
	if !ok {
		return nil
	}
	if inst.Phase != PhaseTerminating && inst.Phase != PhaseLost {
		inst.Phase = PhaseTerminating
		_ = s.store.PutInstance(inst)
	}
	if err := s.launcher.Teardown(ctx, inst); err != nil {
		return fmt.Errorf("teardown launcher: %w", err)
	}
	if err := s.store.RemoveInstance(actorID); err != nil {
		return err
	}
	_ = s.store.RemoveActor(actorID) // revoke identity; ignore "not found"
	if s.log != nil {
		s.log.Info("cove torn down", "id", actorID)
	}
	return nil
}
```

- [ ] **Step 4: Run to verify Task-2 tests pass**

Run: `go test ./internal/harbor/ -run 'TestRaise|TestReport|TestTeardown' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/harbor/supervisor.go internal/harbor/supervisor_test.go
git commit -m "harbor: supervisor lifecycle — Launcher seam + Raise/Report/Teardown (COV-149)"
```

---

## Task 3: Reconciler + Run loop + restart re-adoption

**Files:**
- Modify: `internal/harbor/supervisor.go` (add `Reconcile`, `Run`)
- Test: `internal/harbor/supervisor_test.go`

**Interfaces:**
- Produces: `func (s *Supervisor) Reconcile(ctx) error`, `func (s *Supervisor) Run(ctx)`.
- Reconcile rule: for each non-Gone Instance — if lease is unexpired and ours, renew it; if expired, `Probe`: dead (or probe error) → `PhaseLost` → teardown; alive → steal + renew; unknown → leave. Run does a startup reconcile (restart re-adoption) then ticks every `reconcile` interval. `reconcile` must be `< ttl` (enforced by config, Task 6).

- [ ] **Step 1: Write the failing test** (append to `internal/harbor/supervisor_test.go`)

```go
func TestReconcileReapsExpiredDead(t *testing.T) {
	f := &fakeLauncher{liveness: LivenessDead}
	sup, store, clk := supTestKit(t, f)
	sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest"})
	*clk = clk.Add(2 * time.Minute) // lease (60s) now expired
	if err := sup.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.GetInstance("w1"); ok {
		t.Fatal("expired+dead instance must be reaped")
	}
	if len(store.ListActors()) != 0 {
		t.Fatal("reaped instance must be revoked")
	}
	if len(f.tornDown) != 1 {
		t.Fatalf("launcher teardown expected once, got %d", len(f.tornDown))
	}
}

func TestReconcileAdoptsExpiredAlive(t *testing.T) {
	f := &fakeLauncher{liveness: LivenessAlive}
	sup, store, clk := supTestKit(t, f)
	sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest"})
	// Simulate another holder owning it, lease expired.
	inst, _ := store.GetInstance("w1")
	inst.Lease = Lease{Holder: "holder-B", Expiry: time.Unix(900, 0).UTC()}
	store.PutInstance(inst)
	*clk = clk.Add(1 * time.Minute) // now 1060 > 900
	if err := sup.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, ok := store.GetInstance("w1")
	if !ok {
		t.Fatal("alive instance must be adopted, not reaped")
	}
	if got.Lease.Holder != "holder-A" || !got.Lease.Expiry.Equal(time.Unix(1120, 0).UTC()) {
		t.Fatalf("lease not stolen+renewed: %+v", got.Lease)
	}
	if len(f.tornDown) != 0 {
		t.Fatal("alive instance must not be torn down")
	}
}

func TestReconcileLeavesHealthyInstance(t *testing.T) {
	f := &fakeLauncher{liveness: LivenessAlive}
	sup, store, clk := supTestKit(t, f)
	sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest"})
	*clk = clk.Add(10 * time.Second) // well within the 60s lease
	if err := sup.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := store.GetInstance("w1")
	// Ours + unexpired ⇒ renewed to now+ttl; never probed/torn down.
	if !got.Lease.Expiry.Equal(time.Unix(1070, 0).UTC()) {
		t.Fatalf("own lease should be renewed: %+v", got.Lease)
	}
	if len(f.tornDown) != 0 {
		t.Fatal("healthy instance must not be torn down")
	}
}

func TestRestartReadoptsLiveInstances(t *testing.T) {
	// Seed a store with a Live instance as if a prior process had raised it, then
	// build a FRESH supervisor over the same store (a restart) and reconcile.
	store, err := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err != nil {
		t.Fatal(err)
	}
	store.PutInstance(Instance{ActorID: "survivor", Project: "default", Role: "guest",
		Phase: PhaseLive, Lease: Lease{Holder: "old-holder", Expiry: time.Unix(100, 0).UTC()}})
	f := &fakeLauncher{liveness: LivenessAlive}
	clk := time.Unix(1000, 0).UTC()
	sup := NewSupervisor(store, f, "holder-NEW", 60*time.Second, 30*time.Second,
		func() time.Time { return clk }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := sup.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, ok := store.GetInstance("survivor")
	if !ok {
		t.Fatal("a live instance must survive a restart (re-adopted), not be dropped")
	}
	if got.Lease.Holder != "holder-NEW" {
		t.Fatalf("re-adopt should steal the lease, got holder %q", got.Lease.Holder)
	}
}

func TestRunStartsAndStops(t *testing.T) {
	sup, _, _ := supTestKit(t, &fakeLauncher{liveness: LivenessAlive})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { sup.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/harbor/ -run 'TestReconcile|TestRestart|TestRunStarts' -v`
Expected: FAIL (Reconcile undefined).

- [ ] **Step 3: Add `Reconcile` and `Run` to `supervisor.go`**

```go
// Reconcile is the self-healing + restart-re-adoption pass. For each non-Gone
// Instance: renew our own unexpired lease; for an expired lease, Probe the cove —
// dead (or probe error) ⇒ declare Lost and tear down; alive ⇒ steal + renew
// (adopt); unknown ⇒ leave for a later tick. Run once at startup (re-adopting
// instances a crashed/old process left behind) and on every tick.
func (s *Supervisor) Reconcile(ctx context.Context) error {
	now := s.now()
	for _, inst := range s.store.ListInstances() {
		if inst.Phase == PhaseGone {
			continue
		}
		if inst.Lease.Expiry.After(now) {
			if inst.Lease.Holder == s.holder {
				inst.Lease.Expiry = now.Add(s.ttl) // renew our own lease
				_ = s.store.PutInstance(inst)
			}
			continue // someone else's live lease: not ours to touch
		}
		live, err := s.launcher.Probe(ctx, inst)
		if err != nil || live == LivenessDead {
			inst.Phase = PhaseLost
			_ = s.store.PutInstance(inst)
			if derr := s.Teardown(ctx, inst.ActorID); derr != nil && s.log != nil {
				s.log.Warn("reconcile teardown failed", "id", inst.ActorID, "err", derr.Error())
			}
			continue
		}
		if live == LivenessAlive {
			inst.Lease = Lease{Holder: s.holder, Expiry: now.Add(s.ttl)} // steal + renew
			_ = s.store.PutInstance(inst)
		}
		// LivenessUnknown: leave for the next tick.
	}
	return nil
}

// Run drives the reconciler: one startup pass (restart re-adoption) then a tick
// every reconcile interval until ctx is cancelled. reconcile MUST be < ttl so a
// live owner renews before its own lease expires (enforced by serve config).
func (s *Supervisor) Run(ctx context.Context) {
	_ = s.Reconcile(ctx)
	t := time.NewTicker(s.reconcile)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = s.Reconcile(ctx)
		}
	}
}
```

- [ ] **Step 4: Run to verify Task-3 tests pass**

Run: `go test ./internal/harbor/ -run 'TestReconcile|TestRestart|TestRunStarts' -v`
Expected: PASS.

- [ ] **Step 5: Run the whole harbor package**

Run: `go test ./internal/harbor/`
Expected: PASS (no regressions).

- [ ] **Step 6: Commit**

```bash
git add internal/harbor/supervisor.go internal/harbor/supervisor_test.go
git commit -m "harbor: supervisor reconciler + Run loop + restart re-adoption (COV-149)"
```

---

## Task 4: Admin API — cove routes + wire types

**Files:**
- Modify: `internal/harbor/admin.go` (wire types, routes, `NewAdminHandler` signature)
- Test: `internal/harbor/admin_test.go` (harness update + cove tests)
- Modify: `internal/harbor/oidc_test.go`, `internal/harbor/operator_test.go` (call-site updates)

**Interfaces:**
- Consumes: `*Supervisor` (Tasks 2-3), `store.ListInstances()` (Task 1).
- Produces: `NewAdminHandler(store Store, sup *Supervisor, auth OperatorAuthenticator, credExists func(string) bool, login *OperatorLoginConfig, log *slog.Logger) http.Handler` (adds `sup` as the 2nd param), wire types `CoveRaiseBody`, `CoveRaiseResult`, `CoveSummary`, `CoveStatusBody`, and routes `POST/GET /admin/coves`, `POST /admin/coves/{id}/status`, `DELETE /admin/coves/{id}`.

- [ ] **Step 1: Write the failing test** (append to `internal/harbor/admin_test.go`)

First, add a supervisor-backed harness helper:
```go
func newTestAdminWithSupervisor(t *testing.T) (http.Handler, Store, *Supervisor) {
	t.Helper()
	store, err := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutRole("default", Role{Name: "guest", Scope: Scope{Destinations: []string{"anthropic"}, TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	sup := NewSupervisor(store, &fakeLauncher{liveness: LivenessAlive}, "holder-admin",
		time.Minute, 30*time.Second, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	credExists := func(n string) bool { return true }
	h := NewAdminHandler(store, sup, LoopbackAuthenticator{}, credExists, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return h, store, sup
}

func TestCoveRaiseListStatusTeardown(t *testing.T) {
	h, store, _ := newTestAdminWithSupervisor(t)

	// Raise.
	rec := doJSON(t, h, "POST", "/admin/coves", CoveRaiseBody{ID: "w1", Role: "guest", Unit: "AET-9"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("raise code = %d body=%s", rec.Code, rec.Body.String())
	}
	var res CoveRaiseResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Token == "" || res.Phase != string(PhaseLive) {
		t.Fatalf("raise result = %+v", res)
	}

	// List — never leaks a token/hash.
	var coves []CoveSummary
	getJSON(t, h, "/admin/coves", &coves)
	if len(coves) != 1 || coves[0].ID != "w1" || coves[0].Role != "guest" || coves[0].Unit != "AET-9" {
		t.Fatalf("list = %+v", coves)
	}
	// The runtime summary must never carry identity secrets.
	if body := string(mustJSON(t, coves)); strings.Contains(body, "token") || strings.Contains(body, "hash") {
		t.Fatalf("cove summary leaks a secret field: %s", body)
	}

	// Status report.
	if rc := doJSON(t, h, "POST", "/admin/coves/w1/status", CoveStatusBody{Activity: "waiting"}); rc.Code != http.StatusNoContent {
		t.Fatalf("status code = %d body=%s", rc.Code, rc.Body.String())
	}
	got, _ := store.GetInstance("w1")
	if got.Activity != ActivityWaiting {
		t.Fatalf("activity = %s", got.Activity)
	}

	// Bad activity → 400.
	if rc := doJSON(t, h, "POST", "/admin/coves/w1/status", CoveStatusBody{Activity: "bogus"}); rc.Code != http.StatusBadRequest {
		t.Fatalf("bad activity code = %d", rc.Code)
	}

	// Teardown.
	if rc := doReq(t, h, "DELETE", "/admin/coves/w1", nil); rc.Code != http.StatusNoContent {
		t.Fatalf("teardown code = %d", rc.Code)
	}
	if _, ok := store.GetInstance("w1"); ok {
		t.Fatal("instance still present after teardown")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
```
(Ensure `time` and `log/slog` are imported in `admin_test.go`; add if missing.)

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/harbor/ -run 'TestCoveRaiseListStatusTeardown' -v`
Expected: FAIL (CoveRaiseBody undefined; NewAdminHandler arity).

- [ ] **Step 3: Add wire types + routes to `admin.go`, change `NewAdminHandler`**

Add the wire types near the other `…Body`/`…Summary` types:
```go
// CoveRaiseBody is the POST /admin/coves request.
type CoveRaiseBody struct {
	ID      string `json:"id"`
	Project string `json:"project"`
	Role    string `json:"role"`
	Unit    string `json:"unit,omitempty"`
}

// CoveRaiseResult is the POST /admin/coves response — the identity token is
// returned once (the launcher will consume it to connect the cove).
type CoveRaiseResult struct {
	ID       string `json:"id"`
	Token    string `json:"token"`
	Phase    string `json:"phase"`
	Location string `json:"location,omitempty"`
}

// CoveSummary is a GET /admin/coves item: runtime only, never a token or hash.
type CoveSummary struct {
	ID          string    `json:"id"`
	Project     string    `json:"project"`
	Role        string    `json:"role"`
	Unit        string    `json:"unit,omitempty"`
	Phase       string    `json:"phase"`
	Activity    string    `json:"activity,omitempty"`
	LeaseHolder string    `json:"lease_holder"`
	RaisedAt    time.Time `json:"raised_at"`
	LastSeen    time.Time `json:"last_seen"`
}

// CoveStatusBody is the POST /admin/coves/{id}/status request.
type CoveStatusBody struct {
	Activity string `json:"activity"`
}

// parseActivity validates a cove-reported activity string.
func parseActivity(s string) (Activity, bool) {
	switch Activity(s) {
	case ActivityRunning, ActivityWaiting, ActivityBlocked, ActivityDone:
		return Activity(s), true
	}
	return "", false
}
```
Change the signature:
```go
func NewAdminHandler(store Store, sup *Supervisor, auth OperatorAuthenticator, credExists func(string) bool, login *OperatorLoginConfig, log *slog.Logger) http.Handler {
```
Add the routes inside `NewAdminHandler` (before `return authMiddleware(...)`). The GET reads the store directly; mutations go through `sup` (503 if unconfigured):
```go
	mux.HandleFunc("GET /admin/coves", func(w http.ResponseWriter, r *http.Request) {
		var out []CoveSummary
		for _, i := range store.ListInstances() {
			out = append(out, CoveSummary{
				ID: i.ActorID, Project: i.Project, Role: i.Role, Unit: i.Unit,
				Phase: string(i.Phase), Activity: string(i.Activity),
				LeaseHolder: i.Lease.Holder, RaisedAt: i.RaisedAt, LastSeen: i.LastSeen,
			})
		}
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("POST /admin/coves", func(w http.ResponseWriter, r *http.Request) {
		if sup == nil {
			http.Error(w, "runtime supervisor not configured", http.StatusServiceUnavailable)
			return
		}
		var b CoveRaiseBody
		if !decode(w, r, &b) {
			return
		}
		if b.ID == "" || b.Role == "" {
			http.Error(w, "id and role are required", http.StatusBadRequest)
			return
		}
		inst, tok, err := sup.Raise(r.Context(), RaiseSpec{ActorID: b.ID, Project: b.Project, Role: b.Role, Unit: b.Unit})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		log.Info("admin cove raised", "operator", operatorID(r), "id", b.ID, "project", inst.Project, "role", b.Role)
		writeJSON(w, http.StatusCreated, CoveRaiseResult{ID: b.ID, Token: tok, Phase: string(inst.Phase), Location: inst.Location})
	})
	mux.HandleFunc("POST /admin/coves/{id}/status", func(w http.ResponseWriter, r *http.Request) {
		if sup == nil {
			http.Error(w, "runtime supervisor not configured", http.StatusServiceUnavailable)
			return
		}
		var b CoveStatusBody
		if !decode(w, r, &b) {
			return
		}
		act, ok := parseActivity(b.Activity)
		if !ok {
			http.Error(w, "activity must be one of running|waiting|blocked|done", http.StatusBadRequest)
			return
		}
		if err := sup.Report(r.Context(), r.PathValue("id"), act); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		log.Info("admin cove status", "operator", operatorID(r), "id", r.PathValue("id"), "activity", b.Activity)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("DELETE /admin/coves/{id}", func(w http.ResponseWriter, r *http.Request) {
		if sup == nil {
			http.Error(w, "runtime supervisor not configured", http.StatusServiceUnavailable)
			return
		}
		if err := sup.Teardown(r.Context(), r.PathValue("id")); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		log.Info("admin cove torn down", "operator", operatorID(r), "id", r.PathValue("id"))
		w.WriteHeader(http.StatusNoContent)
	})
```

- [ ] **Step 4: Update the other `NewAdminHandler` call sites**

In `internal/harbor/admin_test.go` `newTestAdmin` (the non-supervisor helper), pass `nil`:
```go
	h := NewAdminHandler(store, nil, LoopbackAuthenticator{}, credExists, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
```
In `internal/harbor/oidc_test.go` and `internal/harbor/operator_test.go`, add `nil` as the 2nd arg to each `NewAdminHandler(...)` call (3 call sites total per the earlier grep: lines ~162, ~213, ~236 — find each `NewAdminHandler(store,` and insert `nil,` after `store,`).

- [ ] **Step 5: Run to verify Task-4 tests + the package pass**

Run: `go test ./internal/harbor/`
Expected: PASS (new cove tests + all pre-existing admin/oidc/operator tests).

- [ ] **Step 6: Commit**

```bash
git add internal/harbor/admin.go internal/harbor/admin_test.go internal/harbor/oidc_test.go internal/harbor/operator_test.go
git commit -m "harbor: admin API cove routes (raise/list/status/teardown) (COV-149)"
```

---

## Task 5: adminclient + `at-harbor cove` CLI verbs

**Files:**
- Modify: `internal/harbor/adminclient/adminclient.go`
- Test: `internal/harbor/adminclient/adminclient_test.go`
- Modify: `cmd/at-harbor/main.go` (`cmdCove` + command-table row)

**Interfaces:**
- Consumes: harbor wire types (Task 4).
- Produces: `CoveRaiseParams{ID,Project,Role,Unit}`; `(*Client) RaiseCove(CoveRaiseParams) (harbor.CoveRaiseResult, error)`, `ListCoves() ([]harbor.CoveSummary, error)`, `ReportCoveStatus(id, activity string) error`, `TeardownCove(id string) error`; `cmdCove` registered as `cove`.

- [ ] **Step 1: Write the failing client test** (append to `internal/harbor/adminclient/adminclient_test.go`)

```go
func TestCoveClientRoundTrip(t *testing.T) {
	srv, store := newServer(t) // existing helper: httptest server over a real admin handler
	defer srv.Close()
	// newServer must build the handler WITH a supervisor for cove routes — see note below.
	c := New(srv.URL, "")

	res, err := c.RaiseCove(CoveRaiseParams{ID: "w1", Role: "guest", Unit: "AET-3"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Token == "" || res.Phase != "live" {
		t.Fatalf("raise result = %+v", res)
	}
	coves, err := c.ListCoves()
	if err != nil {
		t.Fatal(err)
	}
	if len(coves) != 1 || coves[0].ID != "w1" {
		t.Fatalf("list = %+v", coves)
	}
	if err := c.ReportCoveStatus("w1", "blocked"); err != nil {
		t.Fatal(err)
	}
	if inst, _ := store.GetInstance("w1"); inst.Activity != harbor.ActivityBlocked {
		t.Fatalf("activity = %s", inst.Activity)
	}
	if err := c.TeardownCove("w1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.GetInstance("w1"); ok {
		t.Fatal("instance present after teardown")
	}
}
```

**Note for the implementer:** the existing `newServer(t)` helper in `adminclient_test.go` builds the admin handler. Update it to (a) create a `guest` role in the store and (b) pass a supervisor with a fake launcher to `harbor.NewAdminHandler`, so the cove routes work. Because `adminclient_test` is an **external** test package, the fake launcher must be expressible through exported harbor API — use `harbor.NewSupervisor(store, <launcher>, …)`. Define a tiny local launcher in the test file that returns `harbor.LivenessAlive`:
```go
type aliveLauncher struct{}
func (aliveLauncher) Raise(context.Context, harbor.RaiseSpec) (string, error) { return "fake", nil }
func (aliveLauncher) Teardown(context.Context, harbor.Instance) error          { return nil }
func (aliveLauncher) Probe(context.Context, harbor.Instance) (harbor.Liveness, error) { return harbor.LivenessAlive, nil }
```
Preserve every assertion already in `newServer`'s existing callers — only extend the helper, do not remove its current behavior.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/harbor/adminclient/ -run TestCoveClientRoundTrip -v`
Expected: FAIL (RaiseCove undefined).

- [ ] **Step 3: Add client methods to `adminclient.go`**

```go
// CoveRaiseParams are the inputs to raising a managed cove. Scope/kit come from
// the named role.
type CoveRaiseParams struct {
	ID      string
	Project string
	Role    string
	Unit    string
}

// RaiseCove raises a managed cove for a role and returns its runtime result
// (including the identity token, once).
func (c *Client) RaiseCove(p CoveRaiseParams) (harbor.CoveRaiseResult, error) {
	var res harbor.CoveRaiseResult
	err := c.do("POST", "/admin/coves", harbor.CoveRaiseBody{ID: p.ID, Project: p.Project, Role: p.Role, Unit: p.Unit}, &res)
	return res, err
}

// ListCoves lists the managed-cove runtime registry (never a token or hash).
func (c *Client) ListCoves() ([]harbor.CoveSummary, error) {
	var out []harbor.CoveSummary
	err := c.do("GET", "/admin/coves", nil, &out)
	return out, err
}

// ReportCoveStatus reports a cove's activity (running|waiting|blocked|done).
func (c *Client) ReportCoveStatus(id, activity string) error {
	return c.do("POST", "/admin/coves/"+url.PathEscape(id)+"/status", harbor.CoveStatusBody{Activity: activity}, nil)
}

// TeardownCove tears a managed cove down and deregisters it.
func (c *Client) TeardownCove(id string) error {
	return c.do("DELETE", "/admin/coves/"+url.PathEscape(id), nil, nil)
}
```
(`net/url` is already imported.)

- [ ] **Step 4: Run to verify the client test passes**

Run: `go test ./internal/harbor/adminclient/`
Expected: PASS.

- [ ] **Step 5: Add `cmdCove` to `cmd/at-harbor/main.go`**

Register in the command table (after `roster`):
```go
				{Name: "cove", Brief: "manage managed coves (raise|list|status|teardown) via the admin API", Run: cmdCove},
```
Add the function (model on `cmdKit`):
```go
func cmdCove(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "at-harbor cove: expected raise|list|status|teardown")
		return 2
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("cove "+sub, flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	adminURLFlag := fs.String("admin-url", "", "harbor admin API URL (overrides the app's settings)")
	token := fs.String("token", os.Getenv("AT_HARBOR_ADMIN_TOKEN"), "operator token (env: AT_HARBOR_ADMIN_TOKEN)")
	id := fs.String("id", "", "cove/actor id")
	project := fs.String("project", "", "project name (default: "+harbor.DefaultProject+")")
	role := fs.String("role", "", "role to raise the cove for")
	unit := fs.String("unit", "", "unit of work (e.g. issue identifier)")
	activity := fs.String("activity", "", "reported activity: running|waiting|blocked|done (status only)")
	pos, code, ok := cli.ParseFlags(fs, rest, stdout, stderr)
	if !ok {
		return code
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "at-harbor cove:", err)
		return 2
	}
	adminURL := firstNonEmpty(*adminURLFlag, loadSettings(*app).AdminURL, defaultAdminURL)
	c := adminclient.New(adminURL, resolveToken(*app, *token, stderr))
	switch sub {
	case "raise":
		if *id == "" || *role == "" {
			fmt.Fprintln(stderr, "at-harbor cove raise: --id and --role are required")
			return 2
		}
		res, err := c.RaiseCove(adminclient.CoveRaiseParams{ID: *id, Project: *project, Role: *role, Unit: *unit})
		if err != nil {
			fmt.Fprintln(stderr, "at-harbor:", err)
			return 1
		}
		fmt.Fprintf(stdout, "raised %s (phase=%s)\n", res.ID, res.Phase)
	case "list":
		coves, err := c.ListCoves()
		if err != nil {
			fmt.Fprintln(stderr, "at-harbor:", err)
			return 1
		}
		for _, cv := range coves {
			fmt.Fprintf(stdout, "%s\trole=%s\tunit=%s\tphase=%s\tactivity=%s\tholder=%s\n",
				cv.ID, cv.Role, cv.Unit, cv.Phase, cv.Activity, cv.LeaseHolder)
		}
	case "status":
		if *id == "" || *activity == "" {
			fmt.Fprintln(stderr, "at-harbor cove status: --id and --activity are required")
			return 2
		}
		if err := c.ReportCoveStatus(*id, *activity); err != nil {
			fmt.Fprintln(stderr, "at-harbor:", err)
			return 1
		}
		fmt.Fprintf(stdout, "reported %s activity=%s\n", *id, *activity)
	case "teardown":
		name := *id
		if name == "" && len(pos) == 1 {
			name = pos[0]
		}
		if name == "" {
			fmt.Fprintln(stderr, "at-harbor cove teardown: --id (or a positional id) is required")
			return 2
		}
		if err := c.TeardownCove(name); err != nil {
			fmt.Fprintln(stderr, "at-harbor:", err)
			return 1
		}
		fmt.Fprintln(stdout, "tore down", name)
	default:
		fmt.Fprintln(stderr, "at-harbor cove: unknown subcommand", sub)
		return 2
	}
	return 0
}
```

- [ ] **Step 6: Build and run tests**

Run: `go build ./... && go test ./internal/harbor/... ./cmd/at-harbor/`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/harbor/adminclient/adminclient.go internal/harbor/adminclient/adminclient_test.go cmd/at-harbor/main.go
git commit -m "harbor: adminclient + at-harbor cove verbs (raise/list/status/teardown) (COV-149)"
```

---

## Task 6: Wire the supervisor into `serve` + `runtime:` config

**Files:**
- Modify: `cmd/at-harbor/config.go` (`serveConfig.Runtime` + `runtimeDurations()`)
- Test: `cmd/at-harbor/config_test.go`
- Modify: `cmd/at-harbor/main.go` (placeholder launcher + supervisor wiring in `cmdServe`)

**Interfaces:**
- Consumes: `serveConfig`, `harbor.NewSupervisor`, `harbor.NewHolderID`, `harbor.NewAdminHandler` (now takes `sup`).
- Produces: `serveConfig.Runtime struct{ LeaseTTL, ReconcileInterval string }`; `func (c serveConfig) runtimeDurations() (ttl, reconcile time.Duration, err error)` (defaults 60s/30s; errors if `reconcile >= ttl`); `placeholderLauncher` in `cmd/at-harbor`.

- [ ] **Step 1: Write the failing config test** (append to `cmd/at-harbor/config_test.go`)

```go
func TestRuntimeDurationsDefaults(t *testing.T) {
	c, err := parseServeConfig([]byte("listen: \":443\"\nstore: /tmp/s.json\n"))
	if err != nil {
		t.Fatal(err)
	}
	ttl, rec, err := c.runtimeDurations()
	if err != nil {
		t.Fatal(err)
	}
	if ttl != 60*time.Second || rec != 30*time.Second {
		t.Fatalf("defaults = %s / %s", ttl, rec)
	}
}

func TestRuntimeDurationsParsedAndValidated(t *testing.T) {
	c, err := parseServeConfig([]byte("runtime:\n  lease-ttl: 2m\n  reconcile-interval: 40s\n"))
	if err != nil {
		t.Fatal(err)
	}
	ttl, rec, err := c.runtimeDurations()
	if err != nil || ttl != 2*time.Minute || rec != 40*time.Second {
		t.Fatalf("parsed = %s / %s err=%v", ttl, rec, err)
	}

	bad, _ := parseServeConfig([]byte("runtime:\n  lease-ttl: 30s\n  reconcile-interval: 60s\n"))
	if _, _, err := bad.runtimeDurations(); err == nil {
		t.Fatal("expected error when reconcile-interval >= lease-ttl")
	}
}

func TestRuntimeIsAKnownServeKey(t *testing.T) {
	// runtime: must not be reported as an unknown key.
	if got := unknownServeKeys([]byte("runtime:\n  lease-ttl: 1m\n")); len(got) != 0 {
		t.Fatalf("unknown keys = %v, want none", got)
	}
}
```
(Ensure `time` is imported in `config_test.go`.)

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./cmd/at-harbor/ -run TestRuntime -v`
Expected: FAIL (c.runtimeDurations undefined).

- [ ] **Step 3: Add the `Runtime` field + method to `config.go`**

In `serveConfig` (after `OperatorAuth`):
```go
	Runtime struct {
		LeaseTTL          string `yaml:"lease-ttl"`
		ReconcileInterval string `yaml:"reconcile-interval"`
	} `yaml:"runtime"`
```
Add (import `time` if not present):
```go
// runtimeDurations resolves the supervisor's lease-ttl and reconcile-interval,
// defaulting to 60s and 30s. reconcile-interval must be strictly less than
// lease-ttl, so a live owner always renews before its own lease expires.
func (c serveConfig) runtimeDurations() (ttl, reconcile time.Duration, err error) {
	ttl, reconcile = 60*time.Second, 30*time.Second
	if c.Runtime.LeaseTTL != "" {
		if ttl, err = time.ParseDuration(c.Runtime.LeaseTTL); err != nil {
			return 0, 0, fmt.Errorf("runtime.lease-ttl: %w", err)
		}
	}
	if c.Runtime.ReconcileInterval != "" {
		if reconcile, err = time.ParseDuration(c.Runtime.ReconcileInterval); err != nil {
			return 0, 0, fmt.Errorf("runtime.reconcile-interval: %w", err)
		}
	}
	if reconcile >= ttl {
		return 0, 0, fmt.Errorf("runtime.reconcile-interval (%s) must be less than lease-ttl (%s)", reconcile, ttl)
	}
	return ttl, reconcile, nil
}
```
(The `serveConfigKeys()` reflection already makes `runtime` a known key automatically — no other change needed for `TestRuntimeIsAKnownServeKey`.)

- [ ] **Step 4: Wire the supervisor into `cmdServe` (main.go)**

Add the placeholder launcher (near the top-level helpers):
```go
// placeholderLauncher satisfies harbor.Launcher without a real backend: it
// records a synthetic location and always probes Alive, so the supervisor spine
// (registry, leases, reconciler, restart re-adoption) runs end-to-end against a
// live `at-harbor serve`. `cove raise` against it creates a Live Instance with no
// actual cove. The real backend+kit launcher lands in a later slice.
type placeholderLauncher struct{}

func (placeholderLauncher) Raise(_ context.Context, spec harbor.RaiseSpec) (string, error) {
	return "placeholder:" + spec.ActorID, nil
}
func (placeholderLauncher) Teardown(context.Context, harbor.Instance) error { return nil }
func (placeholderLauncher) Probe(context.Context, harbor.Instance) (harbor.Liveness, error) {
	return harbor.LivenessAlive, nil
}
```
In `cmdServe`, after `broker := harbor.NewBroker(...)` and before the `if cfg.AdminListen != "" {` block, build the supervisor:
```go
	ttl, reconcile, err := cfg.runtimeDurations()
	if err != nil {
		fmt.Fprintln(stderr, "at-harbor:", err)
		return 1
	}
	sup := harbor.NewSupervisor(st, placeholderLauncher{}, harbor.NewHolderID(), ttl, reconcile, time.Now, log)
	go sup.Run(context.Background())
```
Change the admin handler construction to pass `sup`:
```go
		admin := harbor.NewAdminHandler(st, sup, auth, credExists, cfg.operatorLoginConfig(), log)
```
(`context` and `time` are already imported in main.go.)

- [ ] **Step 5: Build + run tests**

Run: `go build ./... && go test ./cmd/at-harbor/ ./internal/harbor/...`
Expected: PASS.

- [ ] **Step 6: Verify the dependency boundary holds**

Run: `go list -deps ./internal/harbor | grep -iE 'internal/kit|grpc' || echo CLEAN`
Expected: `CLEAN` (internal/harbor pulls neither kit nor grpc).

- [ ] **Step 7: Commit**

```bash
git add cmd/at-harbor/config.go cmd/at-harbor/config_test.go cmd/at-harbor/main.go
git commit -m "harbor: wire supervisor into serve + runtime config (COV-149)"
```

---

## Task 7: Docs — operator manual for the supervisor

**Files:**
- Create: `docs/usage/harbor/coves.md`
- Modify: `docs/usage/harbor/INDEX.md` (add the row)
- Modify: `docs/usage/harbor/operators.md` (add `cove` to the verb list)
- Modify: `docs/usage/harbor/serve.md` (document the `runtime:` block + config-table row)

**Interfaces:** none (documentation). Follows the progressive-disclosure doctrine: INDEX is a map, each fact lives in one doc, links resolve.

- [ ] **Step 1: Create `docs/usage/harbor/coves.md`**

Frontmatter + body (keep under the 200-line leaf budget; this is ~70 lines):
```markdown
---
summary: The managed-cove supervisor operator guide — harbor's runtime registry of raised coves (Phase/Activity, leases) and the `at-harbor cove raise|list|status|teardown` verbs, plus the `runtime:` serve-config block.
read_when: You are raising or tearing down a managed cove through harbor, inspecting the runtime registry, or tuning the supervisor's lease/reconcile timing.
owns: the operator-facing managed-cove runtime story — the Instance registry (Phase vs Activity, leases), the `cove` verbs, and the `runtime:` serve-config block
prereqs: INDEX.md for the service overview; operators.md for the admin-client flags; roster.md for the role a cove is raised for
tier: leaf
updated: 2026-09-12
---

# Managed coves (the supervisor)

Harbor keeps a **runtime registry** of the coves it manages — each a raised
Worker or standing Manager — and drives their lifecycle: raise → record → track
status → tear down, self-healing across harbor restarts. This is the spine the
resident dispatcher and standing teammates build on.

> **This slice is the spine.** `at-harbor serve` wires a **placeholder launcher**:
> `cove raise` records a live registry entry but does **not** start a real cove
> yet. Raising coves on a real backend is a later slice.

## The model

A managed cove has a durable **identity** (a roster [Actor](roster.md) — token,
grants) and a runtime **Instance** keyed by that actor's id. The Instance carries
two status fields with different owners:

- **Phase** (harbor owns it): `raising → live → terminating → gone`, or
  `→ lost → terminating` when the reconciler finds it dead.
- **Activity** (the cove reports it, only while `live`): `running | waiting |
  blocked | done`. Reporting `done` tells harbor to tear the cove down.

Each Instance is **leased** to the harbor process supervising it. A lease has a
TTL; the owner renews it, and if it expires another process may take over
(reconnect/failover). On restart, harbor re-adopts live Instances from the store
instead of abandoning them — so in-progress work survives a restart.

## The `cove` verbs

```
at-harbor cove raise    --id spider-42 --role guest [--project acme] [--unit AET-9]
at-harbor cove list     # id  role  unit  phase  activity  lease-holder
at-harbor cove status   --id spider-42 --activity waiting
at-harbor cove teardown --id spider-42
```

- `cove raise` enrolls the identity (the role must exist — fail-closed) and
  records a `live` Instance. The role supplies scope, exactly as with
  [enroll](roster.md).
- `cove status` reports the cove's activity; `--activity done` triggers teardown.
- `cove teardown` tears the cove down and revokes its identity (idempotent).

All verbs take the admin-client flags (`--app`/`--admin-url`/`--token`); see
[operators.md](operators.md).

## Tuning the supervisor (`runtime:`)

Optional serve-config block (see [serve.md](serve.md) for the whole config):

```yaml
runtime:
  lease-ttl: 60s            # how long a lease is valid without renewal
  reconcile-interval: 30s   # reconcile + renew cadence (must be < lease-ttl)
```

Defaults are `60s` / `30s`. `reconcile-interval` must be strictly less than
`lease-ttl` so a live owner always renews before its own lease expires.

Design rationale (the identity/runtime split, the lease/steal model, the
reconciler) lives in
[`../../superpowers/specs/2026-09-12-harbor-cove-supervisor.md`](../../superpowers/specs/2026-09-12-harbor-cove-supervisor.md).
```

- [ ] **Step 2: Add the INDEX row** (`docs/usage/harbor/INDEX.md`)

Add a row to the doc table (matching the existing format), e.g. after the `kits.md` row:
```markdown
| [coves.md](coves.md) | You are raising/tearing down a managed cove, inspecting the runtime registry, or tuning the supervisor's lease/reconcile timing. |
```
(Match the exact column layout already in that INDEX.)

- [ ] **Step 3: Update `operators.md`** — add `cove` to the admin-verb enumeration

In the opening sentence that lists every admin verb (`destination`, `role`,
`grant`, `ungrant`, `roster`, `enroll`, `revoke`, `kit`), insert `cove`:
```markdown
Every `at-harbor` admin verb (`destination`, `role`, `grant`, `ungrant`, `roster`,
`enroll`, `revoke`, `kit`, `cove`) is a client of a running harbor's admin API.
```
And add a closing cross-reference near where it points to `roster.md`/`kits.md`:
```markdown
the managed-cove verbs are in [coves.md](coves.md).
```

- [ ] **Step 4: Update `serve.md`** — document the `runtime:` block

Add `runtime:` to the serve-config YAML example (after `operator-auth:`):
```yaml
runtime:                            # optional — supervisor lease/reconcile timing
  lease-ttl: 60s
  reconcile-interval: 30s           # must be < lease-ttl
```
Add a config-table row:
```markdown
| `runtime.lease-ttl` / `runtime.reconcile-interval` | no | Managed-cove supervisor timing (defaults 60s / 30s; reconcile must be < ttl). See [coves.md](coves.md). |
```

- [ ] **Step 5: Run the docs audit (delta check)**

Run: `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs --index OVERVIEW.md`
Expected: no **new** orphans/dangling-links/oversize/duplication attributable to the new `coves.md` (the repo carries a pre-existing baseline — compare the delta, e.g. via `git stash`, per the docs-audit note). `coves.md` must be reachable from INDEX and all its links resolve.

- [ ] **Step 6: Commit**

```bash
git add docs/usage/harbor/coves.md docs/usage/harbor/INDEX.md docs/usage/harbor/operators.md docs/usage/harbor/serve.md
git commit -m "docs(harbor): managed-cove supervisor operator manual (COV-149)"
```

---

## Final steps (after all tasks)

- [ ] Run the full suite: `just test` (or `go test ./...`). Expected: PASS.
- [ ] Re-verify the boundary gate: `go list -deps ./internal/harbor | grep -iE 'internal/kit|grpc'` is empty; `go list -deps ./cmd/at-cove | grep -i oidc` is empty (unchanged).
- [ ] Dispatch the final whole-branch code review (subagent-driven-development's final review), then `superpowers:finishing-a-development-branch`.
