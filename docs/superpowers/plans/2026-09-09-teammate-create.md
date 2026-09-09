# Teammate Create/Lifecycle (COV-136) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let `at-cove create`/`recreate`/`destroy`/`status` provision and manage a **teammate-only** class, so `at-cove create <t> && at-cove teammate <t>` works end-to-end from a stock teammates config — no dual `collaborators.<t>` declaration.

**Architecture:** Class resolution is a single choke point (`instanceFor` → `SelectCollaborator`, collaborators-only). Replace it with a kind-aware `SelectClass` over both the `Collaborators` and `Teammates` maps (validation keeps the maps name-disjoint), and thread a `ClassKind` through `instanceFor` so the three collaborator-specific downstream calls (`sharedWorkspaceMount`, `doRecreate`'s shared branch, `doChat`) don't misfire `ResolvedCollaborator` on a teammate. A teammate provisions exactly like an isolated collaborator — `createInstance`/`buildState`/`state`/`naming` are already class-agnostic and unchanged.

**Tech Stack:** Go stdlib; `internal/kit`, `cmd/at-cove`, `internal/state`/`naming`/`backend` (consumed, not changed).

## Global Constraints

- Module `github.com/aethons-tools/cove`. Builds on the wiring branch (`Teammate`/`DiscordConfig`/`ResolvedTeammate`/`ResolvedTeammateDomains`/`doTeammate` already present).
- A teammate provisions as an **isolated** sandbox (teammates have no `share-repo-dir`/`shadow-dirs`); state records only what a collaborator create records (root `cfg.Secrets`) — the bot token + Discord egress are resolved/applied only at `teammate` launch, NOT at create. Do not add teammate-specific state.
- **Class names must be unique across `collaborators` and `teammates`** (a name can't be both) — add the first cross-map uniqueness check.
- Don't regress collaborator/worker resolution. `chat` stays collaborator-only (reject a teammate class with a helpful message). `work`/`dispatch` are workers, untouched.
- Tests hermetic (`runner.Fake`, no Docker/VM). TDD, DRY, YAGNI. Docs updated in the same change.

---

### Task 1: Kind-aware class resolution + cross-map uniqueness (`internal/kit`)

**Files:**
- Modify: `internal/kit/config.go` (add `ClassKind`, `SelectClass`, `selectableClassNames`; cross-map uniqueness in `ParseConfig`)
- Test: `internal/kit/config_test.go`

**Interfaces:**
- Produces: `type ClassKind int` with `ClassNone`/`ClassCollaborator`/`ClassTeammate`; `func (c Config) SelectClass(explicit string) (class string, kind ClassKind, err error)`.

**Design notes:** `SelectClass` mirrors `SelectCollaborator` (config.go:408-439) but resolves across both maps. Explicit lookup: collaborators first, then teammates (uniqueness makes this unambiguous), else a "no collaborator or teammate" error listing the union. Empty explicit: sole class across the union wins; else the `Default:true` one; else ambiguity error. Cross-map uniqueness is validated in `ParseConfig` right after the teammates loop (config.go:~738), reusing `collaboratorKeys`/`teammateKeys` (config.go:916-929).

- [ ] **Step 1: Write the failing test**

```go
func TestSelectClass(t *testing.T) {
	cfg := Config{Name: "k",
		Collaborators: map[string]Collaborator{"planner": {}},
		Teammates:     map[string]Teammate{"helper": {Discord: &DiscordConfig{Channels: []string{"1"}, BotTokenSecret: "T"}, Secrets: map[string]SecretConfig{"T": {}}}},
	}
	if c, k, err := cfg.SelectClass("planner"); err != nil || c != "planner" || k != ClassCollaborator {
		t.Fatalf("planner => %q/%v/%v", c, k, err)
	}
	if c, k, err := cfg.SelectClass("helper"); err != nil || c != "helper" || k != ClassTeammate {
		t.Fatalf("helper => %q/%v/%v", c, k, err)
	}
	if _, _, err := cfg.SelectClass("nope"); err == nil {
		t.Fatal("unknown class should error")
	}
	if _, _, err := cfg.SelectClass(""); err == nil {
		t.Fatal("ambiguous (2 classes, no default) should error")
	}
	// sole teammate, no explicit => selected
	solo := Config{Name: "k", Teammates: map[string]Teammate{"helper": {Discord: &DiscordConfig{Channels: []string{"1"}, BotTokenSecret: "T"}, Secrets: map[string]SecretConfig{"T": {}}}}}
	if c, k, err := solo.SelectClass(""); err != nil || c != "helper" || k != ClassTeammate {
		t.Fatalf("sole teammate => %q/%v/%v", c, k, err)
	}
}

func TestParseConfig_RejectsCollaboratorTeammateNameCollision(t *testing.T) {
	y := `
name: k
image: {}
workers: {}
collaborators:
  dup: {}
teammates:
  dup:
    discord: {channels: ["1"], bot-token-secret: T}
    secrets: {T: {description: x}}
`
	if _, err := ParseConfig([]byte(y)); err == nil {
		t.Fatal("a name declared as both collaborator and teammate must be rejected")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/kit/ -run 'TestSelectClass|TestParseConfig_RejectsCollaboratorTeammate'`
Expected: FAIL — `SelectClass`/`ClassKind` undefined; the collision config parses without error.

- [ ] **Step 3: Implement**

Add the kind type + selector (near `SelectCollaborator`, config.go:~440):
```go
// ClassKind identifies which class map a selected instance class came from.
type ClassKind int

const (
	ClassNone ClassKind = iota
	ClassCollaborator
	ClassTeammate
)

// selectableClassNames returns the sorted union of collaborator + teammate class
// names (excluding <common>), for error messages.
func (c Config) selectableClassNames() []string {
	var names []string
	for n := range c.Collaborators {
		if n != commonKey {
			names = append(names, n)
		}
	}
	for n := range c.Teammates {
		if n != commonKey {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}

// SelectClass resolves an optional class positional against both the collaborator
// and teammate maps (kept name-disjoint by validation), returning the class name
// and its kind. Empty explicit selects a sole/default class across the union;
// ambiguity is an error.
func (c Config) SelectClass(explicit string) (string, ClassKind, error) {
	if explicit == commonKey {
		return "", ClassNone, fmt.Errorf("%q is not a selectable class", commonKey)
	}
	if explicit != "" {
		if _, ok := c.Collaborators[explicit]; ok {
			return explicit, ClassCollaborator, nil
		}
		if _, ok := c.Teammates[explicit]; ok {
			return explicit, ClassTeammate, nil
		}
		return "", ClassNone, fmt.Errorf("kit %q declares no collaborator or teammate %q (have: %s)", c.Name, explicit, strings.Join(c.selectableClassNames(), ", "))
	}
	type cand struct {
		name string
		kind ClassKind
		def  bool
	}
	var cands []cand
	for n, col := range c.Collaborators {
		if n != commonKey {
			cands = append(cands, cand{n, ClassCollaborator, col.Default})
		}
	}
	for n, tm := range c.Teammates {
		if n != commonKey {
			cands = append(cands, cand{n, ClassTeammate, tm.Default})
		}
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].name < cands[j].name })
	switch len(cands) {
	case 0:
		return "", ClassNone, nil
	case 1:
		return cands[0].name, cands[0].kind, nil
	default:
		for _, cd := range cands {
			if cd.def {
				return cd.name, cd.kind, nil
			}
		}
		return "", ClassNone, fmt.Errorf("kit %q has multiple classes; specify one of: %s", c.Name, strings.Join(c.selectableClassNames(), ", "))
	}
}
```
In `ParseConfig`, after the teammates validation loop (config.go:~738, before `validateModelProvider`):
```go
	for name := range cfg.Collaborators {
		if name == commonKey {
			continue
		}
		if _, dup := cfg.Teammates[name]; dup {
			return Config{}, fmt.Errorf("config.yml: %q is declared as both a collaborator and a teammate; class names must be unique", name)
		}
	}
```

> Note: `SelectCollaborator` may now be unused (only `instanceFor` called it, and Task 2 switches `instanceFor` to `SelectClass`). Check its callers/tests: if truly unused, remove it and its tests in Task 2; if a test still exercises it directly, leave it.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/kit/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/kit/config.go internal/kit/config_test.go
git commit -m "feat(kit): kind-aware SelectClass over collaborators+teammates; reject name collision"
```

---

### Task 2: Make the instance lifecycle teammate-aware (`cmd/at-cove`)

**Files:**
- Modify: `cmd/at-cove/main.go` (`instanceFor`, `sharedWorkspaceMount`, `doRecreate`, `doChat`; callers of `instanceFor`)
- Test: `cmd/at-cove/*_test.go` (a teammate-create/status/chat-reject test)

**Interfaces:**
- Consumes: `kit.SelectClass`/`kit.ClassKind` (Task 1).
- Produces: `instanceFor(cfg, class) (className string, kind kit.ClassKind, instKey state.Instance, container string, err error)`.

**Design notes:** `instanceFor` (main.go:665-675) currently returns `(class, hasCollab bool, instKey, name, err)` via `SelectCollaborator`. Switch it to `SelectClass` and return the `kind`. Update the three collaborator-specific sites so a teammate is treated as isolated:
- `sharedWorkspaceMount` (main.go:736-752): take the `kind`; for anything other than `ClassCollaborator`, return `WorkspaceMount{Mode: backend.Isolated}` and never call `ResolvedCollaborator`.
- `doRecreate` (main.go:1329-1335): guard the `ResolvedCollaborator` call with `kind == kit.ClassCollaborator` (teammates are always isolated, so the shared branch won't trigger, but don't call the collaborator resolver on a teammate).
- `doChat` (main.go:837 area): after `instanceFor`, if `kind == kit.ClassTeammate`, return a usage error: "`<class>` is a teammate; launch it with `at-cove teammate <class>` (use `at-cove view <class>` or ssh to inspect)". `hasCollab` semantics become `kind == kit.ClassCollaborator`.
- `doCreate`/`resolveInstanceLenient` (`doDestroy`/`doStatus`): just pass through the new `instanceFor` signature; they are otherwise state-driven and work for both kinds unchanged. `createInstance`/`buildState`/`saveState` are untouched.

- [ ] **Step 1: Write the failing test**

```go
// A teammates-only kit: create provisions an isolated instance; chat rejects it.
func TestTeammate_CreateProvisionsIsolatedInstance(t *testing.T) {
	env := newTeammateKitEnv(t) // install manifest + a teammates-only config (helper); Fake backend recording Create
	var out, errb strings.Builder
	code := run([]string{"create", "--project-dir", env.projectDir, "helper"},
		env.fakeRunner, env.lookup, env.lookPath, &out, &errb)
	if code != 0 {
		t.Fatalf("create teammate: exit %d: %s", code, errb.String())
	}
	env.assertInstanceCreated(t, "helper")            // state file <helper>.json written
	env.assertWorkspaceMode(t, "helper", "isolated")  // teammate is isolated

	// chat must reject a teammate class with a helpful message
	var o2, e2 strings.Builder
	code = run([]string{"chat", "--project-dir", env.projectDir, "helper"}, env.fakeRunner, env.lookup, env.lookPath, &o2, &e2)
	if code == 0 || !strings.Contains(e2.String(), "teammate") {
		t.Fatalf("chat should reject a teammate class; exit=%d err=%q", code, e2.String())
	}
}
```

> Reuse the existing `create`/`status` test fixtures and the `Fake` backend used by the wiring/create tests (grep `writeInstall`/`seedConfigDir`/`writeStateFor` and the fake backend recording `Create`/state). If the full `run()` fixture is disproportionate, it's acceptable to test `doCreate` + `doChat` directly with a hand-built teammates-only `kit.Config` + `runner.Fake`, asserting: an isolated instance is created for the teammate, and `doChat` returns a teammate-rejection error. Say which approach you took.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/at-cove/ -run TestTeammate_CreateProvisions`
Expected: FAIL — `at-cove create helper` errors "declares no collaborator" (SelectCollaborator), and chat doesn't yet reject teammates.

- [ ] **Step 3: Implement**

Rewrite `instanceFor`:
```go
func instanceFor(cfg kit.Config, class string) (name string, kind kit.ClassKind, instKey state.Instance, container string, err error) {
	name, kind, err = cfg.SelectClass(class)
	if err != nil {
		return "", kit.ClassNone, state.Interactive, "", usageErr{err}
	}
	if kind == kit.ClassNone {
		return "", kit.ClassNone, state.Interactive, naming.Container(cfg.Name, ""), nil
	}
	return name, kind, state.Instance(name), naming.Container(cfg.Name, name), nil
}
```
Update `sharedWorkspaceMount` to take the kind and only resolve a collaborator:
```go
func sharedWorkspaceMount(cfg kit.Config, kitDir, class string, kind kit.ClassKind) (backend.WorkspaceMount, error) {
	if kind != kit.ClassCollaborator {
		return backend.WorkspaceMount{Mode: backend.Isolated}, nil
	}
	role, err := cfg.ResolvedCollaborator(class)
	if err != nil {
		return backend.WorkspaceMount{}, err
	}
	if !role.ShareRepoDir {
		return backend.WorkspaceMount{Mode: backend.Isolated}, nil
	}
	abs, err := filepath.Abs(filepath.Dir(kitDir))
	if err != nil {
		return backend.WorkspaceMount{}, err
	}
	return backend.WorkspaceMount{Mode: backend.Shared, HostPath: abs, ShadowDirs: role.ShadowDirs}, nil
}
```
Update the callers: `doCreate` (`class, kind, instKey, name, err := instanceFor(...)` then `sharedWorkspaceMount(cfg, kitDir, class, kind)`); `doRecreate` (same destructure; guard the `ResolvedCollaborator` at ~1330 with `if kind == kit.ClassCollaborator`); `resolveInstanceLenient` (destructure to the new signature, still returns `instKey`); `doChat` (destructure; add `if kind == kit.ClassTeammate { return usageErr{fmt.Errorf("%q is a teammate; launch it with `at-cove teammate %s` (inspect with `at-cove view %s` or ssh)", class, class, class)} }`; set `hasCollab := kind == kit.ClassCollaborator`). Remove `SelectCollaborator` if now unused (and its tests), else leave it.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/at-cove/` then `just test`/`just lint`
Expected: PASS; whole suite green (existing create/chat/recreate/status/destroy tests still pass under the new signature).

- [ ] **Step 5: Commit**

```bash
git add cmd/at-cove/main.go cmd/at-cove/*_test.go
git commit -m "feat(at-cove): create/recreate/destroy/status provision teammate classes (isolated)"
```

---

### Task 3: Docs — drop the dual-declaration workaround

**Files:**
- Modify: `docs/usage/discord-teammate.md` (remove the "declare a matching collaborators.<class>" workaround; document the direct `create`→`teammate` flow)
- Modify: any config-reference mention of the workaround; the COV-136/COV-137 references

**Design notes:** the wiring doc documented a workaround (declare a minimal `collaborators.<class>` so `create` provisions the sandbox) and referenced COV-136 as the fix. This task removes it: `at-cove create <teammate-class>` now provisions directly, then `at-cove teammate <teammate-class>` launches. Update the COV-137 egress-clobber note: with a teammate now on its **own** container (no shared collaborator) and `chat` rejecting teammate classes, the co-located-`chat` clobber can't occur via `chat` — soften/close that warning accordingly (keep any residual caveat honest; reference COV-137). Keep progressive disclosure; verify links resolve with the docs-audit checker.

- [ ] **Step 1: Update `docs/usage/discord-teammate.md`** — replace the workaround/prerequisite section with the direct flow: `at-cove create <class>` (provisions the isolated sandbox), then a one-time `at-cove chat <a-collaborator>`-style login note is no longer needed for provisioning — but the saved-login auth prerequisite still stands (a one-time interactive Claude login must exist in `/agent-data`); keep that. Remove the COV-136 "pending" caveat.

- [ ] **Step 2: Reconcile the COV-137 warning** — since `chat` now rejects teammate classes and a teammate has its own container, note that a `chat`/`work` session can no longer target/clobber the teammate's container; keep only a truthful residual note if any (reference COV-137).

- [ ] **Step 3: Update any config-reference doc** mention of the dual-declaration; run the docs-audit checker (no orphans/dangling links).

- [ ] **Step 4: Commit**

```bash
git add docs/usage/discord-teammate.md docs/usage/*config*.md
git commit -m "docs(usage): teammate classes are created directly (COV-136 removes the workaround)"
```

---

## Self-Review

**DoD coverage (COV-136):**
- `create`/`recreate`/`destroy`/`status` resolve teammate classes → Task 1 (`SelectClass`) + Task 2 (`instanceFor` + call-site guards). `--all` paths already cover teammates (state.List). ✅
- Teammate provisions as isolated; no teammate-specific state → Task 2 (`sharedWorkspaceMount` isolated for non-collaborator; `createInstance`/`buildState` unchanged). ✅
- Cross-map name uniqueness → Task 1. ✅
- No regression to collaborator/worker resolution; `chat` stays collaborator-only (rejects teammate) → Task 2. ✅
- Docs drop the workaround → Task 3. ✅
- Hermetic tests, `just test`/`just lint` green. ✅

**Placeholder scan:** concrete code for Task 1/2; Task 2's fixture cites the existing create/status fixtures with a sanctioned direct-`doCreate`/`doChat` fallback; Task 3 is doc edits with a docs-audit gate.

**Type consistency:** `ClassKind`/`SelectClass` (Task 1) consumed by `instanceFor`/`sharedWorkspaceMount`/`doRecreate`/`doChat` (Task 2). `instanceFor`'s new signature `(name, kind, instKey, container, err)` is applied at every call site (doCreate, doRecreate, resolveInstanceLenient, doChat).

**Risk note:** the `instanceFor` signature change ripples to ~4 call sites — the review must confirm each was updated and no caller still reads the old `hasCollab bool` positionally. The `chat`-rejects-teammate behavior is a small intentional addition (not a regression) — a teammate has no interactive-chat semantics.
