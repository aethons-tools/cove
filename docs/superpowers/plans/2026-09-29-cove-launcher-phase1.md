# Cove Launcher Abstraction — Phase 1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Introduce a `Launcher` abstraction that owns a managed cove's `kit → build → raise → init` from a **kit reference**, with a Colima implementation that consolidates today's build (`assemble`/`install`) and init (`connect.LaunchCoveMaster`), a **managed kit** whose egress omits Anthropic (ships COV-208), and supervisor wiring for the lazy `KIT_NOT_READY → PrepareKit → retry` protocol.

**Architecture:** Extend the existing `jam.Launcher` seam (driven by the `at-jam` supervisor) so `Raise` carries a light `KitRef{ID,Version}`; a launcher that lacks that kit returns `ErrKitNotReady`, the supervisor sends the full definition via `PrepareKit` (build local now, remote later), and retries. The launcher tracks its own prepared-kit inventory (source of truth for "do I have this"); the registry/config owns immutable definitions. Colima is the only implementation; the interface is shaped for remote/multi-substrate but only local build is built (YAGNI). Strictly additive — `at-cove` and its `internal/*` callers are untouched.

**Tech Stack:** Go (stdlib + existing `internal/{backend,jam/launcher,assemble,install,connect,kit,runner}`), hermetic tests via `runner.Fake` and fake launchers/backends, `just test`.

**Spec:** `docs/superpowers/specs/2026-09-29-cove-launcher-abstraction-design.md`

## Global Constraints

- **Module** `github.com/aethons-tools/cove`; build/test via `just` (`just test` hermetic, `just build`, `just lint`).
- **TDD**, failing test first; hermetic (no Docker/VM/network) driving `runner.Fake` and fakes; real-substrate paths behind the `integration` tag.
- **Strictly additive.** Do NOT change `cmd/at-cove`, `.at-cove/`, or the sealed `internal/assemble/hardening` base allow-list. `at-cove` must not regress. The Launcher is a new *composition* of existing packages for the managed flavor.
- **Phase-1 placement:** the interface + types + Colima impl live in the existing `internal/jam/launcher` package (least churn; it already holds the Colima managed launcher and is wired to the supervisor). Promotion to a substrate-neutral `internal/launcher` is deferred to when a 2nd substrate lands (YAGNI) — do NOT relocate now.
- **One substrate only (Colima).** Implement local build only. `PrepareKit` may be synchronous in Colima but the interface must permit async (`KitStatus` Preparing/Ready) so a future remote launcher fits.
- **Kit key** is `(KitID, KitVersion)` — an immutable content key (registry versions are monotonic/immutable). Optionally carry a content `Digest` for integrity.
- **Secrets** never hit disk/argv/logs (existing rule); the managed init path already uses `ANTHROPIC_AUTH_TOKEN` (#256) — unchanged here.
- **Docs in the same change** (AGENTS.md): route to `docs/usage/jam/coves.md` (launcher/lifecycle) + `pool.md` (managed-kit egress note) + `kits.md` (kit reference/prepare).
- **Commit footer** on every commit:
  ```
  Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_018DRQWUqrvQMzYD8o9ZeLHv
  ```

## File structure

- `internal/jam/launcher/kit.go` (new) — `KitRef`, `KitDefinition`, `KitStatus`, `ErrKitNotReady`, the extended `Launcher` interface additions (`PrepareKit`), and the prepared-kit **inventory** interface.
- `internal/jam/launcher/launcher.go` (modify) — Colima impl: `PrepareKit` (build via `assemble`+`install`, keyed by `(id,version)`), inventory check, `Raise` from a `KitRef` (miss → `ErrKitNotReady`).
- `internal/jam/launcher/inventory.go` (new) — Colima prepared-kit inventory (does a docker image tagged `cove-kit:<id>-<version>` exist).
- `internal/jam/managedkit.go` (new) — the managed kit definition (a `kit.Config` derived value whose egress omits Anthropic) + its `(id,version)` derivation. **This is the COV-208 deliverable.**
- `internal/jam/supervisor.go` (modify) — `RaiseSpec` gains `Kit KitRef`; `Supervisor.Raise`/reconcile handle `ErrKitNotReady` → resolve definition → `PrepareKit` → retry.
- `cmd/at-jam/main.go` (modify) — construct the launcher with the managed kit; no new command.
- Tests alongside each; docs as above.

---

## Task 1: Launcher kit-reference types + interface

**Files:**
- Create: `internal/jam/launcher/kit.go`
- Test: `internal/jam/launcher/kit_test.go`

**Interfaces:**
- Consumes: nothing (leaf).
- Produces:
  - `type KitRef struct { ID string; Version int; Digest string }`
  - `type KitDefinition struct { Ref KitRef; Config kit.Config }` (the full, chunky payload)
  - `type KitState int` with `KitPreparing`, `KitReady`
  - `type KitStatus struct { State KitState; Err string }`
  - `var ErrKitNotReady = errors.New("launcher: kit not prepared")`
  - `func (r KitRef) String() string` → `"<id>@v<version>"` (stable key for logs/inventory)

- [ ] **Step 1: Write the failing test.**

```go
// internal/jam/launcher/kit_test.go
package launcher

import (
	"errors"
	"testing"
)

func TestKitRefStringIsStableKey(t *testing.T) {
	r := KitRef{ID: "managed", Version: 3}
	if got := r.String(); got != "managed@v3" {
		t.Fatalf("KitRef.String() = %q, want managed@v3", got)
	}
}

func TestErrKitNotReadyIsSentinel(t *testing.T) {
	wrapped := errors.Join(errors.New("raise"), ErrKitNotReady)
	if !errors.Is(wrapped, ErrKitNotReady) {
		t.Fatal("ErrKitNotReady must be matchable with errors.Is")
	}
}

func TestKitStateReadyDistinctFromPreparing(t *testing.T) {
	if KitReady == KitPreparing {
		t.Fatal("KitReady and KitPreparing must differ")
	}
}
```

- [ ] **Step 2: Run — expect FAIL** (`undefined: KitRef` …): `go test ./internal/jam/launcher/ -run 'TestKitRef|TestErrKitNotReady|TestKitState' -v`

- [ ] **Step 3: Implement `kit.go`.**

```go
// Package launcher: kit reference + lazy-prepare types (spec Revision: cove
// Launcher abstraction). A KitRef is the light hot-path reference; KitDefinition
// is the chunky payload sent only on a miss.
package launcher

import (
	"errors"
	"fmt"

	"github.com/aethons-tools/cove/internal/kit"
)

type KitRef struct {
	ID      string // stable kit identity (e.g. "managed")
	Version int    // monotonic, immutable
	Digest  string // optional content digest for integrity; "" = unset
}

func (r KitRef) String() string { return fmt.Sprintf("%s@v%d", r.ID, r.Version) }

type KitDefinition struct {
	Ref    KitRef
	Config kit.Config
}

type KitState int

const (
	KitPreparing KitState = iota
	KitReady
)

type KitStatus struct {
	State KitState
	Err   string // populated when a prepare failed; never a secret
}

// ErrKitNotReady is returned by a Launcher's Raise when it has not prepared the
// requested kit; the supervisor responds by PrepareKit(fullDefinition) + retry.
var ErrKitNotReady = errors.New("launcher: kit not prepared")
```

- [ ] **Step 4: Run — expect PASS.**
- [ ] **Step 5: Commit** (`feat(launcher): kit reference + lazy-prepare types`).

---

## Task 2: Prepared-kit inventory (Colima)

**Files:**
- Create: `internal/jam/launcher/inventory.go`, `internal/jam/launcher/inventory_test.go`

**Interfaces:**
- Consumes: `KitRef` (Task 1); the backend's image query.
- Produces:
  - `type Inventory interface { Has(KitRef) (bool, error) }`
  - `func imageTag(r KitRef) string` → `"cove-kit:<id>-v<version>"` (the docker tag a prepared Colima kit carries)
  - a Colima inventory backed by `docker image inspect` via `runner.Runner`.

- [ ] **Step 1: Failing test** — `Has` returns true when the tagged image exists, false on a non-zero inspect:

```go
// inventory_test.go
package launcher

import (
	"testing"
	"github.com/aethons-tools/cove/internal/runner"
)

func TestInventoryHasByImageTag(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "sha256:abc\n"}}}
	inv := newColimaInventory(f)
	ok, err := inv.Has(KitRef{ID: "managed", Version: 2})
	if err != nil || !ok {
		t.Fatalf("Has = %v,%v want true,nil", ok, err)
	}
	// the inspect targeted the (id,version) tag
	if last := f.Calls[len(f.Calls)-1]; !containsArg(last.Args, "cove-kit:managed-v2") {
		t.Fatalf("inspect did not target the kit tag: %v", last.Args)
	}
}

func TestInventoryMissOnInspectError(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{{Err: &runner.ExitError{Code: 1}}}}
	if ok, _ := newColimaInventory(f).Has(KitRef{ID: "managed", Version: 9}); ok {
		t.Fatal("Has must be false when the image is absent")
	}
}

func containsArg(a []string, s string) bool {
	for _, x := range a { if x == s { return true } }
	return false
}
```

- [ ] **Step 2: Run — expect FAIL** (`undefined: newColimaInventory`).

- [ ] **Step 3: Implement `inventory.go`.**

```go
package launcher

import (
	"fmt"
	"github.com/aethons-tools/cove/internal/runner"
)

// Inventory answers whether this launcher has already prepared a kit — the
// launcher's own source of truth. Eviction is invisible: a miss re-prepares.
type Inventory interface {
	Has(KitRef) (bool, error)
}

func imageTag(r KitRef) string { return fmt.Sprintf("cove-kit:%s-v%d", r.ID, r.Version) }

type colimaInventory struct{ r runner.Runner }

func newColimaInventory(r runner.Runner) *colimaInventory { return &colimaInventory{r: r} }

// Has reports whether the (id,version) image exists locally. `docker image
// inspect` exits non-zero when absent → not-ready (never an error to the caller).
func (c *colimaInventory) Has(ref KitRef) (bool, error) {
	if _, err := c.r.Output("docker", "image", "inspect", imageTag(ref)); err != nil {
		return false, nil
	}
	return true, nil
}
```

- [ ] **Step 4: Run — expect PASS.**
- [ ] **Step 5: Commit** (`feat(launcher): Colima prepared-kit inventory keyed by (id,version)`).

---

## Task 3: Colima `PrepareKit` (build) — from a full definition

**Files:**
- Modify: `internal/jam/launcher/launcher.go` (add `PrepareKit` + extend `Config` with a build dir + assembler seam)
- Test: `internal/jam/launcher/prepare_test.go`

**Interfaces:**
- Consumes: `KitDefinition` (Task 1); `internal/assemble.Assemble`, `internal/install.Compile`; a `buildImage` seam so tests inject `runner.Fake`.
- Produces: `func (l *Launcher) PrepareKit(ctx, def KitDefinition) (KitStatus, error)` — assembles the kit into a build dir and `docker build -t cove-kit:<id>-v<version>`, returning `{KitReady}` on success. Idempotent: a `PrepareKit` for an already-present tag is a no-op success. De-duped per ref via an in-flight map + mutex.

- [ ] **Step 1: Failing test** — `PrepareKit` assembles + builds the (id,version) tag, and is idempotent/de-duped. Drive `runner.Fake`; assert a `docker build -t cove-kit:managed-v1` call happened, and a second `PrepareKit` for the same ref while "present" issues no second build.

```go
// prepare_test.go (sketch — assert the build command + idempotency via runner.Fake)
func TestPrepareKitBuildsTaggedImage(t *testing.T) {
	f := &runner.Fake{}
	l := newTestLauncher(t, f)                    // helper: Launcher over the fake
	st, err := l.PrepareKit(context.Background(), KitDefinition{Ref: KitRef{ID: "managed", Version: 1}, Config: minimalKitConfig(t)})
	if err != nil || st.State != KitReady {
		t.Fatalf("PrepareKit = %+v, %v", st, err)
	}
	if !anyCallHasArgs(f, "build", "-t", "cove-kit:managed-v1") {
		t.Fatalf("no docker build for the kit tag; calls=%+v", f.Calls)
	}
}
```

- [ ] **Step 2: Run — expect FAIL.**
- [ ] **Step 3: Implement `PrepareKit`.** Wire `assemble.Assemble(kitDir, buildDir, …)` (from `def.Config`) then `docker build -t <imageTag(ref)> <buildDir>` via `l.cfg.Runner`; guard concurrent builds of the same ref with `sync.Map`/mutex; short-circuit when `Inventory.Has(ref)` is already true. Colima build is synchronous → return `{State: KitReady}`. (The `KitStatus`/async return exists for a future remote launcher.)
- [ ] **Step 4: Run — expect PASS** + `go test ./internal/jam/launcher/`.
- [ ] **Step 5: Commit** (`feat(launcher): Colima PrepareKit builds a kit image, idempotent/de-duped`).

---

## Task 4: `Raise` from a `KitRef` → `ErrKitNotReady` or raise+init

**Files:**
- Modify: `internal/jam/launcher/launcher.go` (Raise consults inventory; uses `imageTag(ref)` as the run image)
- Modify: `internal/jam/supervisor.go:RaiseSpec` — add `Kit KitRef`
- Test: `internal/jam/launcher/launcher_test.go`

**Interfaces:**
- Consumes: `KitRef`, `Inventory.Has` (Tasks 1-2); existing `RunEphemeral`/`Dial`/`LaunchCoveMaster`.
- Produces: `Raise` returns `ErrKitNotReady` (wrapped) when `Inventory.Has(spec.Kit)` is false; otherwise raises `RunEphemeral(imageTag(spec.Kit), …)` and inits as today.

- [ ] **Step 1: Failing test** — with an empty inventory, `Raise` returns `ErrKitNotReady` and creates NO container; with the kit present, it `RunEphemeral`s the `cove-kit:<id>-v<n>` image and launches cove-master.

```go
func TestRaiseKitNotReady(t *testing.T) {
	ops := &fakeOps{}                    // records RunEphemeral
	l := newTestLauncher(t, &runner.Fake{}, withInventory(missingInventory{}))
	_, err := l.Raise(context.Background(), jam.RaiseSpec{ActorID: "a", Kit: KitRef{ID: "managed", Version: 1}}, jam.LaunchCreds{})
	if !errors.Is(err, ErrKitNotReady) {
		t.Fatalf("want ErrKitNotReady, got %v", err)
	}
	if ops.ran {
		t.Fatal("no container may be created on a not-ready kit")
	}
}
```

- [ ] **Step 2: Run — expect FAIL.**
- [ ] **Step 3: Implement.** In `Raise`: `ok, _ := l.inv.Has(spec.Kit); if !ok { return "", fmt.Errorf("raise %s: %w", spec.Kit, ErrKitNotReady) }`; then `RunEphemeral(imageTag(spec.Kit), …)` in place of the fixed `l.cfg.Image`. Add `Kit KitRef` to `RaiseSpec` (nil-safe: a zero KitRef with empty ID keeps the legacy `cfg.Image` path so nothing else breaks — Phase-1 additive).
- [ ] **Step 4: Run — expect PASS** + full `internal/jam/launcher/` + `internal/jam/` suites.
- [ ] **Step 5: Commit** (`feat(launcher): Raise from a KitRef; ErrKitNotReady on a miss`).

---

## Task 5: The managed kit (locked egress) — **COV-208**

**Files:**
- Create: `internal/jam/managedkit.go`, `internal/jam/managedkit_test.go`
- Modify: `docs/usage/jam/pool.md` (drop the "planned hardening" caveat → shipped)

**Interfaces:**
- Consumes: `kit.Config`, the interactive kit as a base; `launcher.KitDefinition`/`KitRef`.
- Produces: `func ManagedKit(base kit.Config) launcher.KitDefinition` — the managed kit: `base` with `image.allowed-domains` **stripped of `.anthropic.com`/`.claude.com`/`claude.ai`** (managed coves reach Anthropic only via the broker), and a `KitRef{ID:"managed", Version: N}` whose `Version` bumps when the config content changes (a content hash → version, or a committed constant bumped on change — pick the hash to make drift automatic).

- [ ] **Step 1: Failing test** — the managed kit's egress excludes the Anthropic hosts and keeps the rest; the version is stable for identical content and changes when content changes.

```go
func TestManagedKitStripsAnthropicEgress(t *testing.T) {
	base := kit.Config{Image: kit.Image{AllowedDomains: []string{".anthropic.com", "claude.ai", "proxy.golang.org", ".local.aethons.tools"}}}
	mk := ManagedKit(base)
	for _, banned := range []string{".anthropic.com", ".claude.com", "claude.ai"} {
		if slices.Contains(mk.Config.Image.AllowedDomains, banned) {
			t.Fatalf("managed kit egress must not include %q: %v", banned, mk.Config.Image.AllowedDomains)
		}
	}
	if !slices.Contains(mk.Config.Image.AllowedDomains, "proxy.golang.org") {
		t.Fatal("non-Anthropic domains must be preserved")
	}
	if mk.Ref.ID != "managed" {
		t.Fatalf("ref id = %q", mk.Ref.ID)
	}
}
```

- [ ] **Step 2: Run — expect FAIL.**
- [ ] **Step 3: Implement `ManagedKit`.** Clone `base`, filter `AllowedDomains` removing any entry whose host is under `anthropic.com`/`claude.com` or equals `claude.ai`; derive `Version` from a stable hash of the resulting config (so any change bumps it). Keep the jam host + providers (they're infra, unaffected).
- [ ] **Step 4: Run — expect PASS.**
- [ ] **Step 5: Docs + commit.** Update `pool.md`: the "planned hardening (not yet shipped)" note becomes "brokered coves run the managed kit, which omits Anthropic egress." Commit (`feat(jam): managed kit omits Anthropic egress (COV-208)`).

---

## Task 6: Supervisor wiring — `KIT_NOT_READY → PrepareKit → retry`

**Files:**
- Modify: `internal/jam/supervisor.go` (Raise path), `cmd/at-jam/main.go` (construct the launcher with the managed kit)
- Test: `internal/jam/supervisor_test.go`

**Interfaces:**
- Consumes: `ErrKitNotReady`, `PrepareKit`, `ManagedKit` (Tasks 1,3,5); the `Launcher` interface gains `PrepareKit(ctx, KitDefinition) (KitStatus, error)`.
- Produces: `Supervisor.Raise` sets `spec.Kit = ManagedKit(base).Ref`; on `errors.Is(err, ErrKitNotReady)` it calls `launcher.PrepareKit(ctx, ManagedKit(base))` then retries `Raise` once (a `KitPreparing` status defers to the next reconcile rather than blocking).

- [ ] **Step 1: Failing test** — a fake launcher returns `ErrKitNotReady` on the first `Raise`, records a `PrepareKit`, then succeeds on retry; assert the cove ends up raised and `PrepareKit` was called once with the managed ref.

```go
func TestSupervisorPreparesKitOnNotReadyThenRaises(t *testing.T) {
	fl := &fakeLauncher{notReadyOnce: true}
	sup := newTestSupervisor(t, fl)
	_, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "spider", Project: "p", Role: "r"})
	if err != nil {
		t.Fatalf("Raise after prepare: %v", err)
	}
	if fl.prepareCalls != 1 {
		t.Fatalf("PrepareKit called %d times, want 1", fl.prepareCalls)
	}
	if !fl.raised {
		t.Fatal("cove not raised after prepare+retry")
	}
}
```

- [ ] **Step 2: Run — expect FAIL** (`PrepareKit` not on the interface / no retry).
- [ ] **Step 3: Implement.** Add `PrepareKit` to the `jam.Launcher` interface; in `Supervisor.Raise`, catch `ErrKitNotReady`, call `PrepareKit(ManagedKit(base))`, and retry once (on `KitPreparing`, return a typed "preparing" outcome the reconcile loop retries — do NOT block the raise). Construct the real launcher in `cmd/at-jam` with the base kit so it can build the managed image on demand (replacing the static install-manifest image for managed coves; keep the manifest path as the build base).
- [ ] **Step 4: Run — expect PASS** + `just test`.
- [ ] **Step 5: Docs + commit.** Update `coves.md` (managed coves are raised via the Launcher's kit-prepare protocol) and `kits.md` (the KitRef/PrepareKit reference). Commit (`feat(jam): supervisor prepares a not-ready kit then retries the raise`).

---

## Self-Review

**Spec coverage:**
- Launcher abstraction owning kit→build→raise→init → Tasks 1–4 ✅
- Kit reference + lazy PrepareKit (light Raise, chunky-on-miss, ErrKitNotReady) → Tasks 1,3,4,6 ✅
- Launcher owns its install inventory → Task 2 ✅
- Async/idempotent PrepareKit → Task 3 (idempotent/de-duped; sync now, `KitStatus` allows async) ✅
- Managed kit / COV-208 egress lock → Task 5 ✅
- Supervisor not-ready→prepare→retry → Task 6 ✅
- Additive (no `at-cove`/sealed-base changes) → Global Constraints; the zero-`KitRef` legacy path (Task 4) keeps other callers working ✅
- YAGNI (Colima-only, local build, no relocation) → Global Constraints ✅
- Registry-owns-definitions / kit-by-reference resolution → **Phase 2** (per spec); Phase 1 uses the single `ManagedKit` derived from the base config, not registry resolution — noted, not a gap.

**Placeholder scan:** the `PrepareKit`/supervisor tests are sketched with helper names (`newTestLauncher`, `fakeLauncher`, `anyCallHasArgs`) the implementer defines to match existing `launcher_test.go`/`supervisor_test.go` fakes — flagged inline, not silent TODOs. No "TBD"/"handle edge cases".

**Type consistency:** `KitRef{ID,Version,Digest}`, `KitDefinition{Ref,Config}`, `KitStatus{State,Err}`, `KitPreparing/KitReady`, `ErrKitNotReady`, `imageTag`, `Inventory.Has`, `PrepareKit(ctx,KitDefinition)(KitStatus,error)`, `RaiseSpec.Kit KitRef` — used identically across tasks.

---

## Board decomposition

Under a new **Launcher epic**, each task is one PR's worth:
- Tasks 1–5 → `class:implementor` (self-contained, hermetic). Task 2/3/4 depend on 1; Task 5 independent; **COV-208 = Task 5**.
- Task 6 → `class:attended` (supervisor wiring + `cmd/at-jam` construction + the live retry loop; wants a human in the loop).
- Phase 2 (registry kit-resolution; remote-build second substrate) tracked separately on the epic, not sliced here.
