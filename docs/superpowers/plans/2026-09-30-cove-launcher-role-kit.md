# Role→Kit Resolution (Launcher Phase 2a) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** A Jam role that names a kit (`role.Kit`) raises a managed cove from a managed (Anthropic-stripped) variant of that kit, versioned in the kit registry; `role.Kit==""` keeps the default `managed` kit.

**Architecture:** The supervisor (backend-free) resolves `role.Kit` at raise into a managed-derivative `KitRef` (`ManagedKit()` strip + idempotent registry push under `managed-<name>`), falling back to the wiring-set default ref; the launcher, which owns the substrate, resolves+gates a role-named kit's own `image.base` when building (the default kit keeps the manifest `BaseRef`). Strictly additive over shipped Phase 1.

**Tech Stack:** Go (stdlib + `internal/{jam,jam/launcher,backend,backend/colima,kit,runner}`), hermetic tests via `runner.Fake`/fakes, `just test`.

**Spec:** `docs/superpowers/specs/2026-09-30-cove-launcher-role-kit-resolution-design.md`

## Global Constraints

- **Module** `github.com/aethons-tools/cove`; build/test via `just` (`just test` hermetic, `just build`, `just lint`).
- **TDD**, failing test first; hermetic (no Docker/VM/network) driving `runner.Fake` + fakes.
- **Strictly additive.** A role with no `Kit` is byte-for-byte the Phase-1 behavior. `at-cove` untouched.
- **COV-208 holds for every managed kit** — the Anthropic strip (`ManagedKit`) is applied at resolution to whatever kit a role names.
- **Brokered kits get the base gate ON** — a role-named kit's base is resolved with `AllowUnverified=false` (no escape hatch); the default `managed` kit keeps reusing the manifest's already-gated `BaseRef`.
- **Derivative id is tag-safe:** `managed` (default) / `managed-<kitName>` (reserved prefix). A `KitRef.ID` becomes the docker tag `cove-kit:<id>-v<n>`, whose tag component admits only `[A-Za-z0-9_.-]`.
- **Commit footer** on every commit:
  ```
  Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_018DRQWUqrvQMzYD8o9ZeLHv
  ```

## File structure

- `internal/jam/managedkit.go` (modify) — factor `ensureManagedVariant(store, id, base)`; `EnsureManagedKit` delegates with id `managed`; add `EnsureManagedKitFor(store, srcKit)` (reads `srcKit` from the registry, strips, registers `managed-<srcKit>`) + `tagSafeKitName`.
- `internal/jam/supervisor.go` (modify) — `Raise` resolves the role's kit ref via a new `kitRefFor(role)` (reusing the role it already fetches for egress); fail closed on a bad `role.Kit`.
- `internal/backend/backend.go` (modify) — `KitImageBuilder` gains `ResolveKitBase(declaredBase string) (string, error)`.
- `internal/backend/colima/colima.go` (modify) — implement `ResolveKitBase` (wraps `resolveBase`, gate on).
- `internal/jam/launcher/prepare.go` (modify) — pick the FROM-base per kit: default id → `cfg.BaseImage`; else `Ops.ResolveKitBase(def.Config.Image.Base)`.
- Tests alongside each; docs: `kits.md` (role→kit resolution), `coves.md` (per-kit base note).

---

## Task 1: Managed-variant registration for an arbitrary source kit

**Files:**
- Modify: `internal/jam/managedkit.go`
- Test: `internal/jam/managedkit_test.go`

**Interfaces:**
- Consumes: `Store` (`KitConfig`/`GetKit`/`PushKit`), `ManagedKit`, `marshalKitConfig`, `KitRef` (Phase 1).
- Produces:
  - `func ensureManagedVariant(store Store, id string, base kit.Config) (KitRef, error)` — the idempotent-push body (was inline in `EnsureManagedKit`).
  - `func EnsureManagedKit(store Store, base kit.Config) (KitRef, error)` — now `ensureManagedVariant(store, ManagedKitID, base)`.
  - `func EnsureManagedKitFor(store Store, srcKit string) (KitRef, error)` — resolves `srcKit`'s current config from the registry, strips + registers under `managed-<srcKit>`. Errors (fail closed) if `srcKit` is not tag-safe or absent.
  - `func ManagedVariantID(srcKit string) string` → `"managed-" + srcKit`.

- [ ] **Step 1: Write the failing test.**

```go
// managedkit_test.go
func TestEnsureManagedKitForStripsAndRegistersDerivative(t *testing.T) {
	st := newKitTestStore(t)
	// operator pushes an ordinary kit that lists Anthropic egress
	webCfg := kit.Config{Name: "web", Image: kit.ImageConfig{AllowedDomains: []string{"github.com", ".anthropic.com"}}}
	text, err := yaml.Marshal(webCfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PushKit("web", string(text)); err != nil {
		t.Fatal(err)
	}

	ref, err := EnsureManagedKitFor(st, "web")
	if err != nil {
		t.Fatalf("EnsureManagedKitFor: %v", err)
	}
	if ref.ID != "managed-web" || ref.Version == 0 || ref.Digest == "" {
		t.Fatalf("bad ref: %+v", ref)
	}
	// the registered derivative is Anthropic-stripped
	def, ok, err := ResolveKitDefinition(st, ref)
	if err != nil || !ok {
		t.Fatalf("resolve: %v %v", ok, err)
	}
	if slices.Contains(def.Config.Image.AllowedDomains, ".anthropic.com") {
		t.Fatalf("derivative must strip Anthropic egress: %v", def.Config.Image.AllowedDomains)
	}
	// idempotent
	ref2, _ := EnsureManagedKitFor(st, "web")
	if ref2.Version != ref.Version {
		t.Fatalf("unchanged source must reuse the version: %d vs %d", ref.Version, ref2.Version)
	}
}

func TestEnsureManagedKitForFailsClosed(t *testing.T) {
	st := newKitTestStore(t)
	if _, err := EnsureManagedKitFor(st, "missing"); err == nil {
		t.Fatal("a kit absent from the registry must fail closed")
	}
	if _, err := EnsureManagedKitFor(st, "bad/name"); err == nil {
		t.Fatal("a non-tag-safe kit name must fail closed")
	}
}
```

- [ ] **Step 2: Run — expect FAIL** (`undefined: EnsureManagedKitFor`): `go test ./internal/jam/ -run 'TestEnsureManagedKitFor' -v`

- [ ] **Step 3: Implement in `managedkit.go`.**

```go
// ManagedVariantID is the registry id (and docker-tag stem) of the managed
// derivative of the registered kit srcKit. The "managed-" prefix is reserved.
func ManagedVariantID(srcKit string) string { return "managed-" + srcKit }

// EnsureManagedKitFor records the managed (Anthropic-stripped) variant of the
// registered kit srcKit under ManagedVariantID(srcKit), returning its reference.
// Idempotent like EnsureManagedKit. Fails closed if srcKit is not tag-safe or is
// absent from the registry (never a silent fallback — that would hide a
// misconfigured role).
func EnsureManagedKitFor(store Store, srcKit string) (KitRef, error) {
	if !tagSafeKitName(srcKit) {
		return KitRef{}, fmt.Errorf("kit name %q is not usable in an image tag", srcKit)
	}
	text, ok := store.KitConfig(srcKit, 0) // 0 = current
	if !ok {
		return KitRef{}, fmt.Errorf("kit %q not in registry", srcKit)
	}
	var cfg kit.Config
	if err := yaml.Unmarshal([]byte(text), &cfg); err != nil {
		return KitRef{}, fmt.Errorf("managed kit for %q: unmarshal config: %w", srcKit, err)
	}
	return ensureManagedVariant(store, ManagedVariantID(srcKit), cfg)
}

// ensureManagedVariant strips base and idempotently records it under id (a new
// monotonic version only when the stripped content changes), returning its ref.
func ensureManagedVariant(store Store, id string, base kit.Config) (KitRef, error) {
	text, digest, err := marshalKitConfig(ManagedKit(base))
	if err != nil {
		return KitRef{}, fmt.Errorf("managed kit %q: marshal config: %w", id, err)
	}
	if k, ok := store.GetKit(id); ok && k.Current != 0 {
		if cur, ok := store.KitConfig(id, k.Current); ok && cur == text {
			return KitRef{ID: id, Version: k.Current, Digest: digest}, nil
		}
	}
	version, err := store.PushKit(id, text)
	if err != nil {
		return KitRef{}, fmt.Errorf("managed kit %q: push to registry: %w", id, err)
	}
	return KitRef{ID: id, Version: version, Digest: digest}, nil
}

// tagSafeKitName reports whether name is usable as the stem of a docker tag
// (cove-kit:managed-<name>-v<n>): docker tag components allow only
// [A-Za-z0-9_.-]. Empty is not tag-safe.
func tagSafeKitName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '.', r == '-':
		default:
			return false
		}
	}
	return true
}
```

Then replace `EnsureManagedKit`'s body with `return ensureManagedVariant(store, ManagedKitID, base)` (drop the now-duplicated inline push).

- [ ] **Step 4: Run — expect PASS** + `go test ./internal/jam/ -run 'TestEnsureManagedKit|TestResolveKitDefinition|TestManagedKit'`.
- [ ] **Step 5: Commit** (`feat(jam): register a managed variant of any registered kit (role→kit)`).

---

## Task 2: Supervisor resolves the role's kit at raise

**Files:**
- Modify: `internal/jam/supervisor.go`
- Test: `internal/jam/supervisor_test.go`

**Interfaces:**
- Consumes: `EnsureManagedKitFor` (Task 1), `s.store.GetRole`, `s.managedKit` (Phase 1 default), `Role.Kit`.
- Produces: `func (s *Supervisor) kitRefFor(role Role) (KitRef, bool, error)` — the ref to stamp on the raise (`ok=false` → leave `spec.Kit` zero = legacy path). `Raise` calls it once, failing closed on error.

- [ ] **Step 1: Write the failing test.** A role bound to a registered kit raises from `managed-<name>`; a role bound to a missing kit fails closed (no instance, identity rolled back); an unbound role still uses the default managed ref.

```go
// supervisor_test.go
func TestRaiseUsesRoleKit(t *testing.T) {
	fl := &fakeLauncher{liveness: LivenessAlive}
	sup, store, _ := supTestKit(t, fl)
	// register the role's source kit + bind the guest role to it
	webText, _ := yaml.Marshal(kit.Config{Name: "web", Image: kit.ImageConfig{AllowedDomains: []string{"github.com", ".anthropic.com"}}})
	if _, err := store.PushKit("web", string(webText)); err != nil {
		t.Fatal(err)
	}
	r, _ := store.GetRole("default", "guest")
	r.Kit = "web"
	if err := store.PutRole("default", r); err != nil {
		t.Fatal(err)
	}
	// a default managed kit is also wired (unbound roles use it)
	def, _ := EnsureManagedKit(store, kit.Config{Name: "base"})
	sup.SetManagedKit(def)

	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "spider", Project: "default", Role: "guest"}); err != nil {
		t.Fatalf("Raise: %v", err)
	}
	if fl.gotSpec.Kit.ID != "managed-web" {
		t.Fatalf("raise used kit %q, want managed-web", fl.gotSpec.Kit.ID)
	}
}

func TestRaiseFailsClosedOnMissingRoleKit(t *testing.T) {
	fl := &fakeLauncher{liveness: LivenessAlive}
	sup, store, _ := supTestKit(t, fl)
	r, _ := store.GetRole("default", "guest")
	r.Kit = "nope"
	_ = store.PutRole("default", r)

	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "spider", Project: "default", Role: "guest"}); err == nil {
		t.Fatal("a role naming an absent kit must fail the raise")
	}
	if len(store.ListActors()) != 0 {
		t.Fatal("failed raise must roll back the identity")
	}
	if len(fl.raised) != 0 {
		t.Fatal("no cove may be raised")
	}
}
```

- [ ] **Step 2: Run — expect FAIL.**

- [ ] **Step 3: Implement.** Add `kitRefFor` and call it in `Raise`, reusing the role fetched for egress. The role is guaranteed to exist (Enroll proved it), but re-fetch defensively.

```go
// kitRefFor returns the kit reference a raise for role should carry. A role that
// names a kit uses a managed (Anthropic-stripped) variant of it, resolved +
// registered on demand (fail closed if the kit is absent/unusable); an unnamed
// kit falls back to the wiring-set default managed ref (ok=false when none is
// set — the legacy static-image path).
func (s *Supervisor) kitRefFor(role Role) (KitRef, bool, error) {
	if role.Kit != "" {
		ref, err := EnsureManagedKitFor(s.store, role.Kit)
		if err != nil {
			return KitRef{}, false, err
		}
		return ref, true, nil
	}
	if s.managedKit != nil {
		return *s.managedKit, true, nil
	}
	return KitRef{}, false, nil
}
```

In `Raise`, replace the egress+managedKit block. The current code fetches the role only when it has an egress policy; fetch it unconditionally now (it exists — Enroll checked) and use it for both egress and kit:

```go
	spec.Egress = nil
	role, roleOK := s.store.GetRole(spec.Project, spec.Role)
	if roleOK && role.Scope.Egress != nil {
		spec.Egress = &EgressPolicy{Domains: slices.Clone(role.Scope.Egress.Domains)}
	}
	// ... MintToken (unchanged) ...
	if roleOK {
		ref, have, err := s.kitRefFor(role)
		if err != nil {
			if rmErr := s.store.RemoveActor(spec.ActorID); rmErr != nil && s.log != nil {
				s.log.Warn("raise rollback: failed to revoke identity after kit resolve failure", "id", spec.ActorID, "error", rmErr)
			}
			return Instance{}, "", "", fmt.Errorf("raise: resolve kit for role %s/%s: %w", spec.Project, spec.Role, err)
		}
		if have {
			spec.Kit = ref
		}
	}
```

(Delete the old `if s.managedKit != nil { spec.Kit = *s.managedKit }` line.)

- [ ] **Step 4: Run — expect PASS** + `go test ./internal/jam/`.
- [ ] **Step 5: Commit** (`feat(jam): supervisor raises from the role's kit`).

---

## Task 3: Launcher resolves+gates a role-named kit's own base

**Files:**
- Modify: `internal/backend/backend.go`, `internal/backend/colima/colima.go`, `internal/jam/launcher/prepare.go`
- Test: `internal/backend/colima/colima_test.go`, `internal/jam/launcher/prepare_test.go`

**Interfaces:**
- Consumes: `resolveBase` (colima), `backend.BaseSpec`, `def.Config.Image.Base`, `jam.ManagedKitID`.
- Produces:
  - `backend.KitImageBuilder` gains `ResolveKitBase(declaredBase string) (resolvedBase string, err error)`.
  - `*Colima.ResolveKitBase` wraps `resolveBase(BaseSpec{Base: declaredBase})` (gate on; `""` → default blessed base).
  - `PrepareKit` picks the FROM-base: default `managed` id → `cfg.BaseImage`; any other id → `Ops.ResolveKitBase(def.Config.Image.Base)`.

- [ ] **Step 1: Failing tests.**

```go
// colima_test.go — ResolveKitBase gates the declared base (context-pinned inspect
// on a kit-chosen base). With the default blessed base ("" declared), no gate
// inspect is needed and it returns the blessed default.
func TestResolveKitBaseDefaultsToBlessed(t *testing.T) {
	f := &runner.Fake{}
	got, err := New(f).(backend.KitImageBuilder).ResolveKitBase("")
	if err != nil || got == "" {
		t.Fatalf("ResolveKitBase(\"\") = %q, %v; want the blessed default", got, err)
	}
}
```

```go
// prepare_test.go — a role-named kit (id != managed) resolves its own declared
// base via the backend; the default managed kit keeps cfg.BaseImage.
func TestPrepareKitNamedKitResolvesOwnBase(t *testing.T) {
	ops := &fakeOps{resolvedBase: "blessed@sha256:web"}
	l := newPrepareLauncher(ops, &fakeInv{}, func(KitDefinition, string) error { return nil })
	_, err := l.PrepareKit(context.Background(), KitDefinition{
		Ref:    KitRef{ID: "managed-web", Version: 1},
		Config: kit.Config{Name: "web", Image: kit.ImageConfig{Base: "some/base:tag"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ops.resolvedFrom != "some/base:tag" {
		t.Fatalf("named kit must resolve its declared base; resolved from %q", ops.resolvedFrom)
	}
	if ops.builtBase != "blessed@sha256:web" {
		t.Fatalf("named kit must build FROM the resolved base; got %q", ops.builtBase)
	}
}

func TestPrepareKitDefaultKitKeepsConfigBase(t *testing.T) {
	ops := &fakeOps{}
	l := newPrepareLauncher(ops, &fakeInv{}, func(KitDefinition, string) error { return nil })
	// newPrepareLauncher sets BaseImage: "cove-base@sha256:base"
	_, err := l.PrepareKit(context.Background(), KitDefinition{
		Ref:    KitRef{ID: "managed", Version: 1},
		Config: kit.Config{Name: "managed"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ops.resolvedFrom != "" {
		t.Fatal("default managed kit must NOT re-resolve a base")
	}
	if ops.builtBase != "cove-base@sha256:base" {
		t.Fatalf("default kit must build FROM cfg.BaseImage; got %q", ops.builtBase)
	}
}
```

Extend `fakeOps` (in `launcher_test.go`): add `resolvedBase string`, `resolvedFrom string` and
```go
func (f *fakeOps) ResolveKitBase(declaredBase string) (string, error) {
	f.resolvedFrom = declaredBase
	if f.resolvedBase != "" {
		return f.resolvedBase, nil
	}
	return "blessed-default", nil
}
```
(Add the same method to `countingOps`'s embedded `fakeOps` — it's promoted, so no extra code.)

- [ ] **Step 2: Run — expect FAIL** (`undefined: ResolveKitBase`).

- [ ] **Step 3: Implement.**

`backend.go` — add to `KitImageBuilder`:
```go
	// ResolveKitBase resolves + gates a role-named kit's declared base (the value
	// to pass as BuildKitImage's base). "" resolves to the substrate's blessed
	// default. The provenance gate is ON — brokered kits get no --allow-unverified
	// escape hatch, so an unblessed base errors here rather than building.
	ResolveKitBase(declaredBase string) (resolvedBase string, err error)
```

`colima.go`:
```go
// ResolveKitBase resolves + gates declaredBase for a role-named managed kit
// (gate on; "" → the blessed default). See backend.KitImageBuilder.
func (c *Colima) ResolveKitBase(declaredBase string) (string, error) {
	return c.resolveBase(backend.BaseSpec{Base: declaredBase})
}
```

`prepare.go` — pick the base before building:
```go
	base := l.cfg.BaseImage
	if ref.ID != jam.ManagedKitID {
		// A role-named kit builds FROM its OWN declared base, resolved + gated on
		// the substrate (no --allow-unverified for brokered coves). The default
		// managed kit keeps the interactive install's already-gated base.
		resolved, err := l.cfg.Ops.ResolveKitBase(def.Config.Image.Base)
		if err != nil {
			return KitStatus{State: KitPreparing, Err: err.Error()}, fmt.Errorf("prepare kit %s: resolve base: %w", ref, err)
		}
		base = resolved
	}
	if _, err := l.cfg.Ops.BuildKitImage(buildDir, imageTag(ref), base, false); err != nil {
		return KitStatus{State: KitPreparing, Err: err.Error()}, fmt.Errorf("prepare kit %s: build: %w", ref, err)
	}
```
(`prepare.go` imports `internal/jam` for `jam.ManagedKitID`; the launcher already imports it.)

- [ ] **Step 4: Run — expect PASS** + `just test`.
- [ ] **Step 5: Docs + commit.** `kits.md`: role→kit resolution (a role names a kit; managed variant `managed-<name>`; base gated). `coves.md`: the per-kit base note (default reuses the manifest base; a named kit's own base is gated). Commit (`feat(launcher): role-named kits build FROM their own gated base`).

---

## Self-Review

**Spec coverage:**
- Role names its kit → managed variant → Task 1 (register) + Task 2 (resolve at raise) ✅
- COV-208 strip-at-resolution for any kit → `ensureManagedVariant` applies `ManagedKit()` (Task 1) ✅
- Supervisor backend-free → `kitRefFor` only touches the store (Task 2); base resolution is on the launcher (Task 3) ✅
- Fail closed on missing/untag-safe kit → Task 1 errors + Task 2 rollback ✅
- Base gate ON for named kits, default keeps manifest BaseRef → Task 3 ✅
- Tag-safe derivative id `managed-<name>` → `ManagedVariantID` + `tagSafeKitName` (Task 1) ✅
- Additive (unbound role unchanged) → Task 2 fallback to default/legacy ✅
- Remote substrate deferred → not in scope ✅

**Placeholder scan:** none — every step has the code.

**Type consistency:** `EnsureManagedKitFor`, `ensureManagedVariant`, `ManagedVariantID`, `tagSafeKitName`, `kitRefFor(Role)(KitRef,bool,error)`, `ResolveKitBase(string)(string,error)`, `jam.ManagedKitID` — used identically across tasks.

## Board decomposition

Under COV-211, Phase 2a. Tasks 1–3 are tightly coupled (a role kit can't raise without all three) — one PR, `class:attended` (continues the T6/COV-217 live-verified thread; ends with a live raise of a role-bound kit).
