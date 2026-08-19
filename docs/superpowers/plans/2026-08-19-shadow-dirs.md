# shadow-dirs Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a `share-repo-dir` collaborator class declare `shadow-dirs` (e.g. `.venv`, `node_modules`) that are overmounted with persistent per-sandbox volumes, so transient/platform-specific dirs no longer corrupt each other across the host↔sandbox boundary.

**Architecture:** The list is composed into the `docker run` argv as one `-v` overmount per dir (no capability grant; strictly more isolation). Volume names are derived by `internal/naming`; teardown removes them via the existing `VolumeSet`; the list is persisted in per-instance state so `recreate` re-emits the mounts (COV-72: recreate reads state, never config). The hardening entrypoint chowns the fresh (root-owned) volumes to `agent`.

**Tech Stack:** Go, `just` (task runner), `go test` (hermetic tests driving `internal/runner.Fake`), YAML kit config (`gopkg.in/yaml.v3`, strict/KnownFields).

## Global Constraints

- **Module:** `github.com/aethons-tools/cove`. Spec: `docs/superpowers/specs/2026-08-18-shadow-dirs-design.md`.
- **Tests are hermetic** — drive `internal/runner.Fake`; no Docker/network/VM. Run the full suite with `just test`; run one package with `go test ./internal/<pkg>/ -run <TestName> -v`.
- **Toolchain:** this repo builds in an egress-locked sandbox; if `go` fails on module fetches, apply the `GOPROXY`/`GOPATH` settings from `docs/DEVELOPMENT.md` before running tests.
- **`shadow-dirs` is per-class only** — rejected on `<common>`, and rejected unless that class has `share-repo-dir: true`.
- **Each entry is a clean relative path under the workspace** — non-empty, not `.`, not absolute, no `..` escape; duplicates and sanitize-collisions rejected.
- **Persistence:** shadow volumes are named, survive `recreate` (which keeps volumes), and are purged only by a real `destroy`.
- **The hardening entrypoint is a security boundary** (`internal/assemble/hardening/`): the change adds no capability, touches no nftables/squid/sshd, and only chowns validated workspace subpaths.
- **Docs in the same change** — route to the doc that owns the subject; keep `docs/INDEX` in sync (per AGENTS.md).
- **Commit after each task.** Conventional-commit style; reference COV-130.

---

### Task 1: Volume naming for shadow dirs

**Files:**
- Modify: `internal/naming/naming.go` (add after `DockerVolume`, ~line 50; add `strings` import)
- Test: `internal/naming/naming_test.go`

**Interfaces:**
- Produces: `SanitizeShadowDir(dir string) string` — `/`→`-`, drop a single leading `.`. `ShadowVolume(container, dir string) string` → `<container>-shadow-<sanitized>`. Both consumed by Task 2 (collision check) and Task 3 (argv).

- [ ] **Step 1: Write the failing test**

Add to `internal/naming/naming_test.go`:

```go
// ShadowVolume hangs a per-dir volume off the instance's container base, with a
// -shadow- token whose sanitized suffix drops path separators and a leading dot,
// so it sorts with the instance's other volumes and never collides (COV-130).
func TestShadowVolume(t *testing.T) {
	collab := Container("box", "human") // atcove-box-human
	cases := map[string]string{
		".venv":         "atcove-box-human-shadow-venv",
		"node_modules":  "atcove-box-human-shadow-node_modules",
		"foo/bar":       "atcove-box-human-shadow-foo-bar",
		".pytest_cache": "atcove-box-human-shadow-pytest_cache",
	}
	for dir, want := range cases {
		if got := ShadowVolume(collab, dir); got != want {
			t.Errorf("ShadowVolume(%q) = %q, want %q", dir, got, want)
		}
	}
}

// SanitizeShadowDir is the single source of the sanitize rule shared by
// ShadowVolume and config validation, so a collision check matches the real name.
func TestSanitizeShadowDir(t *testing.T) {
	if got := SanitizeShadowDir(".venv"); got != "venv" {
		t.Fatalf("SanitizeShadowDir(.venv) = %q, want venv", got)
	}
	if got := SanitizeShadowDir("a/b/c"); got != "a-b-c" {
		t.Fatalf("SanitizeShadowDir(a/b/c) = %q, want a-b-c", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/naming/ -run 'TestShadowVolume|TestSanitizeShadowDir' -v`
Expected: FAIL — `undefined: ShadowVolume` / `undefined: SanitizeShadowDir`.

- [ ] **Step 3: Write minimal implementation**

In `internal/naming/naming.go`, change the import line `import "fmt"` to:

```go
import (
	"fmt"
	"strings"
)
```

Add after `DockerVolume`:

```go
// SanitizeShadowDir turns a validated relative shadow-dir path into the suffix
// token used in its volume name: path separators become '-' and a single leading
// '.' is dropped (so ".venv" and "node_modules" yield clean, sortable tokens). It
// is the single source of the sanitize rule, shared with config validation's
// collision check (COV-130).
func SanitizeShadowDir(dir string) string {
	return strings.TrimPrefix(strings.ReplaceAll(dir, "/", "-"), ".")
}

// ShadowVolume names the per-sandbox volume that overmounts a shared workspace's
// transient dir (e.g. .venv) so it stays VM-local instead of colliding with the
// host's copy (COV-130). It hangs off the instance's container (base) name with a
// -shadow-<dir> suffix, so it sorts with the instance's other volumes and is
// removed on destroy alongside them.
func ShadowVolume(container, dir string) string {
	return container + "-shadow-" + SanitizeShadowDir(dir)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/naming/ -run 'TestShadowVolume|TestSanitizeShadowDir' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/naming/naming.go internal/naming/naming_test.go
git commit -m "feat(naming): ShadowVolume + SanitizeShadowDir for shadow-dirs (COV-130)"
```

---

### Task 2: Config field and validation

**Files:**
- Modify: `internal/kit/config.go` — add `ShadowDirs` to `Collaborator` (~line 274); add `path` import; extend the `<common>` reject (~line 629); add a per-class validation call and a `validateShadowDirs` helper
- Test: `internal/kit/config_test.go`

**Interfaces:**
- Consumes: `naming.SanitizeShadowDir` (Task 1).
- Produces: `Collaborator.ShadowDirs []string` (own-only, surfaced by the existing `ResolvedCollaborator`, which returns `own`). Consumed by Task 4 (cmd wiring).

- [ ] **Step 1: Write the failing tests**

Add to `internal/kit/config_test.go`:

```go
func TestParseConfigRejectsShadowDirsOnCommon(t *testing.T) {
	src := "name: k\ncollaborators:\n  <common>:\n    shadow-dirs: [.venv]\n  human:\n    share-repo-dir: true\n"
	if _, err := ParseConfig([]byte(src)); err == nil {
		t.Fatal("shadow-dirs on <common> must be rejected")
	}
}

func TestParseConfigRejectsShadowDirsWithoutShareRepoDir(t *testing.T) {
	src := "name: k\ncollaborators:\n  human:\n    shadow-dirs: [.venv]\n"
	if _, err := ParseConfig([]byte(src)); err == nil {
		t.Fatal("shadow-dirs without share-repo-dir:true must be rejected")
	}
}

func TestParseConfigRejectsBadShadowDirEntries(t *testing.T) {
	for _, entry := range []string{"/abs", "..", "../escape", ".", ""} {
		src := "name: k\ncollaborators:\n  human:\n    share-repo-dir: true\n    shadow-dirs: [\"" + entry + "\"]\n"
		if _, err := ParseConfig([]byte(src)); err == nil {
			t.Errorf("shadow-dir entry %q must be rejected", entry)
		}
	}
}

func TestParseConfigRejectsDuplicateAndCollidingShadowDirs(t *testing.T) {
	dup := "name: k\ncollaborators:\n  human:\n    share-repo-dir: true\n    shadow-dirs: [.venv, .venv]\n"
	if _, err := ParseConfig([]byte(dup)); err == nil {
		t.Error("duplicate shadow-dirs must be rejected")
	}
	// ".venv" and "venv" both sanitize to the same volume token.
	collide := "name: k\ncollaborators:\n  human:\n    share-repo-dir: true\n    shadow-dirs: [.venv, venv]\n"
	if _, err := ParseConfig([]byte(collide)); err == nil {
		t.Error("shadow-dirs colliding on sanitized name must be rejected")
	}
}

func TestResolvedCollaboratorKeepsShadowDirs(t *testing.T) {
	src := "name: k\ncollaborators:\n  human:\n    share-repo-dir: true\n    shadow-dirs: [.venv, node_modules]\n"
	cfg, err := ParseConfig([]byte(src))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	col, err := cfg.ResolvedCollaborator("human")
	if err != nil {
		t.Fatalf("ResolvedCollaborator: %v", err)
	}
	if len(col.ShadowDirs) != 2 || col.ShadowDirs[0] != ".venv" || col.ShadowDirs[1] != "node_modules" {
		t.Fatalf("shadow-dirs not preserved: %+v", col.ShadowDirs)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/kit/ -run 'ShadowDir' -v`
Expected: FAIL — the `shadow-dirs` YAML key is unknown (strict decoding), so parsing errors before validation exists; some cases fail because the field/validation is absent.

- [ ] **Step 3: Add the struct field**

In `internal/kit/config.go`, in the `Collaborator` struct, add below `ShareRepoDir` (line 274):

```go
	ShadowDirs     []string                `yaml:"shadow-dirs,omitempty"` // subpaths of the shared workspace to overmount with a VM-local volume (.venv, node_modules, …); requires share-repo-dir: true; per-class only (COV-130)
```

- [ ] **Step 4: Add the `path` import**

In the import block, add `"path"` (keep alphabetical, after `"net"`):

```go
	"net"
	"path"
	"sort"
```

- [ ] **Step 5: Extend the `<common>` reject and add the per-class call**

Change the `<common>` block (currently lines ~628-633):

```go
		if name == commonKey {
			if col.Prompt != "" || col.Default || col.ShareRepoDir {
				return Config{}, fmt.Errorf("config.yml: collaborators[%q]: the base must not set a prompt, default, or share-repo-dir", commonKey)
			}
			continue
		}
```

to:

```go
		if name == commonKey {
			if col.Prompt != "" || col.Default || col.ShareRepoDir || len(col.ShadowDirs) > 0 {
				return Config{}, fmt.Errorf("config.yml: collaborators[%q]: the base must not set a prompt, default, share-repo-dir, or shadow-dirs", commonKey)
			}
			continue
		}
		if err := validateShadowDirs(name, col.ShareRepoDir, col.ShadowDirs); err != nil {
			return Config{}, err
		}
```

- [ ] **Step 6: Add the `validateShadowDirs` helper**

Add near the other validation helpers in `internal/kit/config.go` (and add `"github.com/aethons-tools/cove/internal/naming"` to the import block — `naming` is pure, so no import cycle):

```go
// validateShadowDirs enforces the shadow-dirs contract (COV-130): the list is
// meaningful only with a shared bind-mount, and each entry must be a clean
// relative path inside the workspace that maps to a unique volume name.
func validateShadowDirs(class string, shareRepoDir bool, dirs []string) error {
	if len(dirs) == 0 {
		return nil
	}
	if !shareRepoDir {
		return fmt.Errorf("config.yml: collaborators[%q].shadow-dirs: only valid when share-repo-dir: true", class)
	}
	seenPath := map[string]bool{}
	seenVol := map[string]bool{}
	for i, d := range dirs {
		if strings.TrimSpace(d) == "" {
			return fmt.Errorf("config.yml: collaborators[%q].shadow-dirs[%d]: must not be empty", class, i)
		}
		if path.IsAbs(d) {
			return fmt.Errorf("config.yml: collaborators[%q].shadow-dirs[%d]: must be a relative path, got %q", class, i, d)
		}
		clean := path.Clean(d)
		if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
			return fmt.Errorf("config.yml: collaborators[%q].shadow-dirs[%d]: must stay within the workspace, got %q", class, i, d)
		}
		if seenPath[clean] {
			return fmt.Errorf("config.yml: collaborators[%q].shadow-dirs: duplicate entry %q", class, clean)
		}
		seenPath[clean] = true
		vol := naming.SanitizeShadowDir(clean)
		if seenVol[vol] {
			return fmt.Errorf("config.yml: collaborators[%q].shadow-dirs: %q collides with another entry after sanitizing to a volume name", class, d)
		}
		seenVol[vol] = true
	}
	return nil
}
```

- [ ] **Step 7: Run tests to verify they pass**

Run: `go test ./internal/kit/ -run 'ShadowDir|ResolvedCollaborator' -v`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/kit/config.go internal/kit/config_test.go
git commit -m "feat(kit): shadow-dirs config field + validation (COV-130)"
```

---

### Task 3: Backend types, argv emit, and teardown

**Files:**
- Modify: `internal/backend/backend.go` — add `WorkspaceMount.ShadowDirs` (~line 31) and `VolumeSet.Shadow` (~line 100)
- Modify: `internal/backend/colima/colima.go` — add a `shadowArgs` helper, wire it into `Create`, and add `Shadow` to `Destroy`'s `volume rm`
- Test: `internal/backend/colima/colima_test.go`

**Interfaces:**
- Consumes: `naming.ShadowVolume` (Task 1); `WorkspaceMount.ShadowDirs`.
- Produces: `Create` emits `-v <ShadowVolume>:/home/agent/workspace/<dir>` per dir + one `-e COVE_SHADOW_DIRS="<space-joined>"`, and records the names in `Instance.Volumes.Shadow`. `Destroy` removes them.

- [ ] **Step 1: Add the struct fields**

In `internal/backend/backend.go`, `WorkspaceMount` becomes:

```go
type WorkspaceMount struct {
	Mode     WorkspaceMode
	HostPath string
	// ShadowDirs are subpaths of a Shared workspace overmounted with a per-sandbox
	// volume so their VM-local content never collides with the host's (COV-130).
	// Empty unless Mode == Shared.
	ShadowDirs []string
}
```

In `VolumeSet` (after `Docker`):

```go
	Shadow    []string // per-shadow-dir overmount volume names (COV-130); empty unless a shared workspace declared shadow-dirs
```

- [ ] **Step 2: Write the failing tests**

Add to `internal/backend/colima/colima_test.go`:

```go
// A shared workspace with shadow-dirs overmounts each dir with its own volume and
// signals the list to the entrypoint via COVE_SHADOW_DIRS (COV-130).
func TestCreateSharedShadowDirs(t *testing.T) {
	f := &runner.Fake{}
	inst, err := New(f).Create(backend.CreateContext{
		Name: "box", Image: "atcove-box",
		Workspace: backend.WorkspaceMount{
			Mode: backend.Shared, HostPath: "/host/repo",
			ShadowDirs: []string{".venv", "node_modules"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	run := dockerCall(f.Calls, "run")
	for _, want := range []string{
		"box-shadow-venv:/home/agent/workspace/.venv",
		"box-shadow-node_modules:/home/agent/workspace/node_modules",
		"COVE_SHADOW_DIRS=.venv node_modules",
	} {
		if !contains(run, want) {
			t.Errorf("run argv missing %q: %+v", want, run)
		}
	}
	if len(inst.Volumes.Shadow) != 2 || inst.Volumes.Shadow[0] != "box-shadow-venv" {
		t.Fatalf("shadow volume names not recorded: %+v", inst.Volumes.Shadow)
	}
}

// A shared workspace with no shadow-dirs emits no shadow flags (unchanged path).
func TestCreateSharedNoShadowDirs(t *testing.T) {
	f := &runner.Fake{}
	if _, err := New(f).Create(backend.CreateContext{
		Name: "box", Image: "atcove-box",
		Workspace: backend.WorkspaceMount{Mode: backend.Shared, HostPath: "/host/repo"},
	}); err != nil {
		t.Fatal(err)
	}
	if contains(dockerCall(f.Calls, "run"), "COVE_SHADOW_DIRS=") {
		t.Fatal("no shadow-dirs must emit no COVE_SHADOW_DIRS")
	}
}

// Destroy removes the recorded shadow volumes alongside the others (COV-130).
func TestDestroyRemovesShadowVolumes(t *testing.T) {
	f := &runner.Fake{}
	err := New(f).Destroy(backend.Instance{
		Backend: "colima", Container: "box",
		Volumes: backend.VolumeSet{State: "box-agent-data", Shadow: []string{"box-shadow-venv"}},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	rm := dockerCall(f.Calls, "volume")
	if !contains(rm, "box-shadow-venv") {
		t.Fatalf("destroy must rm shadow volume: %+v", rm)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/backend/colima/ -run 'ShadowDir|ShadowVolume' -v`
Expected: FAIL — `shadowArgs` undefined / no shadow flags emitted / `Shadow` not in `volume rm`.

- [ ] **Step 4: Add the `shadowArgs` helper**

In `internal/backend/colima/colima.go` (ensure `"strings"` is imported), add near `dockerArgs`:

```go
// shadowArgs emits, for a shared workspace's declared shadow-dirs, one -v
// overmount per dir (a per-sandbox volume named via naming.ShadowVolume) plus a
// single -e COVE_SHADOW_DIRS the entrypoint reads to chown the fresh mountpoints.
// It returns the volume names used so Create can record them for teardown. Empty
// for a non-shared mount or when no shadow-dirs are declared (COV-130).
func shadowArgs(container string, ws backend.WorkspaceMount) (args, names []string) {
	if ws.Mode != backend.Shared || len(ws.ShadowDirs) == 0 {
		return nil, nil
	}
	for _, d := range ws.ShadowDirs {
		name := naming.ShadowVolume(container, d)
		args = append(args, "-v", name+":/home/agent/workspace/"+d)
		names = append(names, name)
	}
	args = append(args, "-e", "COVE_SHADOW_DIRS="+strings.Join(ws.ShadowDirs, " "))
	return args, names
}
```

- [ ] **Step 5: Wire it into `Create`**

In `Create`, after the `if ctx.Docker { vols.Docker = ... }` block and before building `runArgs`, add:

```go
	shadowRun, shadowVols := shadowArgs(ctx.Name, ctx.Workspace)
	vols.Shadow = shadowVols
```

Then change the final `runArgs` append so the shadow mounts land after the workspace bind and before the image. Replace:

```go
	runArgs = append(runArgs,
		"-p", "127.0.0.1::2222",
		"-v", vols.State+":/agent-data",
		"-v", ws,
		img,
	)
```

with:

```go
	runArgs = append(runArgs,
		"-p", "127.0.0.1::2222",
		"-v", vols.State+":/agent-data",
		"-v", ws,
	)
	runArgs = append(runArgs, shadowRun...)
	runArgs = append(runArgs, img)
```

- [ ] **Step 6: Add `Shadow` to `Destroy`**

In `Destroy`, change:

```go
		vols := []string{inst.Volumes.State, inst.Volumes.Workspace, inst.Volumes.Docker}
```

to:

```go
		vols := append([]string{inst.Volumes.State, inst.Volumes.Workspace, inst.Volumes.Docker}, inst.Volumes.Shadow...)
```

(`nonEmpty` already drops any empties; the legacy-fallback branch below is unaffected.)

- [ ] **Step 7: Run tests to verify they pass**

Run: `go test ./internal/backend/... -run 'ShadowDir|ShadowVolume|Create|Destroy' -v`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/backend/backend.go internal/backend/colima/colima.go internal/backend/colima/colima_test.go
git commit -m "feat(backend): overmount shadow-dirs on shared workspace; remove on destroy (COV-130)"
```

---

### Task 4: State persistence and cmd wiring

**Files:**
- Modify: `internal/state/state.go` — add `State.ShadowDirs` (~line 59) and `Volumes.Shadow` (~line 45)
- Modify: `cmd/at-cove/main.go` — `sharedWorkspaceMount` (line 623), `buildState` (~lines 676-682), `instanceFromState` (~lines 696-705), and the recreate ws-rebuild (~line 1069)
- Test: `internal/state/state_test.go`

**Interfaces:**
- Consumes: `Collaborator.ShadowDirs` (Task 2), `WorkspaceMount.ShadowDirs` / `VolumeSet.Shadow` (Task 3).
- Produces: shadow-dirs persisted so `recreate` re-emits the `-v` mounts and `destroy` (from a fresh process reading only state) removes the volumes.

- [ ] **Step 1: Write the failing test**

Add to `internal/state/state_test.go`:

```go
// The shared-workspace shadow-dirs and their volume names round-trip through the
// state file, so recreate re-emits the mounts and destroy removes them (COV-130).
func TestStateRoundTripsShadowDirs(t *testing.T) {
	in := State{
		SchemaVersion: 2, Name: "k", Container: "atcove-k-human",
		WorkspaceMode: "shared", WorkspaceHostPath: "/host/repo",
		ShadowDirs: []string{".venv", "node_modules"},
		Volumes: &Volumes{State: "atcove-k-human-agent-data",
			Shadow: []string{"atcove-k-human-shadow-venv", "atcove-k-human-shadow-node_modules"}},
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out State
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.ShadowDirs) != 2 || out.ShadowDirs[1] != "node_modules" {
		t.Fatalf("ShadowDirs lost: %+v", out.ShadowDirs)
	}
	if out.Volumes == nil || len(out.Volumes.Shadow) != 2 || out.Volumes.Shadow[0] != "atcove-k-human-shadow-venv" {
		t.Fatalf("Volumes.Shadow lost: %+v", out.Volumes)
	}
}
```

Ensure `state_test.go` imports `"encoding/json"` (add if absent).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/state/ -run TestStateRoundTripsShadowDirs -v`
Expected: FAIL — `unknown field ShadowDirs` / `Volumes has no field Shadow`.

- [ ] **Step 3: Add the state fields**

In `internal/state/state.go`, `Volumes` becomes:

```go
type Volumes struct {
	State     string   `json:"state"`
	Workspace string   `json:"workspace,omitempty"`
	Shadow    []string `json:"shadow,omitempty"` // per-shadow-dir overmount volume names (COV-130)
}
```

In `State`, add after `WorkspaceHostPath` (line 59):

```go
	ShadowDirs        []string `json:"shadowDirs,omitempty"`        // shared-workspace subpaths overmounted VM-local (COV-130); empty otherwise
```

(Both are additive and `omitempty`, so legacy files decode unchanged — no schema bump needed.)

- [ ] **Step 4: Run the state test to verify it passes**

Run: `go test ./internal/state/ -run TestStateRoundTripsShadowDirs -v`
Expected: PASS.

- [ ] **Step 5: Wire the cmd paths**

In `cmd/at-cove/main.go`:

`sharedWorkspaceMount` — change the shared return (line 623):

```go
	return backend.WorkspaceMount{Mode: backend.Shared, HostPath: abs, ShadowDirs: role.ShadowDirs}, nil
```

`buildState` — in the `Shared` block add the dirs, and record the volume names:

```go
	if inst.Workspace.Mode == backend.Shared {
		st.WorkspaceMode = "shared"
		st.WorkspaceHostPath = inst.Workspace.HostPath
		st.ShadowDirs = inst.Workspace.ShadowDirs
	}
```

and change the `st.Volumes` line to:

```go
	st.Volumes = &state.Volumes{State: inst.Volumes.State, Workspace: inst.Volumes.Workspace, Shadow: inst.Volumes.Shadow}
```

`instanceFromState` — restore both the mount dirs and the volume names:

```go
	if st.WorkspaceMode == "shared" {
		ws = backend.WorkspaceMount{Mode: backend.Shared, HostPath: st.WorkspaceHostPath, ShadowDirs: st.ShadowDirs}
	}
```

and inside the `if st.Volumes != nil {` branch:

```go
		inst.Volumes = backend.VolumeSet{State: st.Volumes.State, Workspace: st.Volumes.Workspace, Shadow: st.Volumes.Shadow}
```

Recreate ws-rebuild (~line 1069) — carry the dirs so recreate re-emits the mounts:

```go
	if st, err := state.LoadFor(kitDir, instKey); err == nil && st.WorkspaceMode == "shared" {
		ws = backend.WorkspaceMount{Mode: backend.Shared, HostPath: st.WorkspaceHostPath, ShadowDirs: st.ShadowDirs}
	}
```

- [ ] **Step 6: Verify the whole module builds and tests pass**

Run: `just test`
Expected: PASS (build succeeds; new state/naming/kit/colima tests green).

- [ ] **Step 7: Commit**

```bash
git add internal/state/state.go internal/state/state_test.go cmd/at-cove/main.go
git commit -m "feat(cmd,state): persist + restore shadow-dirs across create/recreate/destroy (COV-130)"
```

---

### Task 5: Entrypoint chowns fresh shadow volumes

**Files:**
- Modify: `internal/assemble/hardening/image-files/usr/local/bin/entrypoint.sh` (common prologue, right after `chown -R agent:agent /agent-data`, before the `COVE_DOCKER` branch)
- Test: `internal/assemble/embed_test.go`

**Interfaces:**
- Consumes: `COVE_SHADOW_DIRS` env (space-joined dirs) set by Task 3's `shadowArgs`.
- Produces: each shadow mountpoint owned by `agent` so `uv`/`npm` can write it on first boot.

**Why:** a fresh named volume mounts empty and `root:root`; the non-root `agent` cannot write it. The chown must run in the common prologue (both the systemd/`COVE_DOCKER` and sshd paths need it).

- [ ] **Step 1: Write the failing test**

Add to `internal/assemble/embed_test.go`:

```go
// A shared workspace's shadow-dir volumes mount empty and root-owned; the
// entrypoint chowns each declared mountpoint to agent so uv/npm can write it on
// first boot, in the common prologue so both the systemd and sshd paths get it
// (COV-130).
func TestEntrypointChownsShadowDirs(t *testing.T) {
	b, err := fs.ReadFile(hardeningFS, "hardening/image-files/usr/local/bin/entrypoint.sh")
	if err != nil {
		t.Fatalf("entrypoint.sh not embedded: %v", err)
	}
	s := string(b)
	for _, want := range []string{"${COVE_SHADOW_DIRS:-}", `chown agent:agent "/home/agent/workspace/$d"`} {
		if !strings.Contains(s, want) {
			t.Errorf("entrypoint must chown shadow-dirs; missing %q:\n%s", want, s)
		}
	}
	// The chown must precede the docker/systemd handoff so both paths run it.
	if strings.Index(s, "COVE_SHADOW_DIRS") > strings.Index(s, `[ "${COVE_DOCKER:-}" = "1" ]`) {
		t.Error("shadow-dir chown must run before the COVE_DOCKER branch")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/assemble/ -run TestEntrypointChownsShadowDirs -v`
Expected: FAIL — `COVE_SHADOW_DIRS` not present in the entrypoint.

- [ ] **Step 3: Edit the entrypoint**

In `entrypoint.sh`, immediately after the `chown -R agent:agent /agent-data` line and before the `# docker:true sandboxes boot systemd…` comment, insert:

```bash
# A share-repo-dir class may overmount transient dirs (.venv, node_modules) with
# fresh per-sandbox volumes (COV-130). Each mounts empty and root:root, so chown
# the mountpoint to agent (non-recursive: first boot is empty; later boots' content
# is already agent-owned). COVE_SHADOW_DIRS is the space-joined list from the run.
# Defense-in-depth for this sealed file: skip anything that escapes the workspace
# (config already rejects '..'/absolute at parse time).
for d in ${COVE_SHADOW_DIRS:-}; do
  case "$d" in /*|*/../*|../*|*/..|..) continue ;; esac
  chown agent:agent "/home/agent/workspace/$d"
done
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/assemble/ -run TestEntrypointChownsShadowDirs -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/assemble/hardening/image-files/usr/local/bin/entrypoint.sh internal/assemble/embed_test.go
git commit -m "feat(hardening): chown fresh shadow-dir volumes to agent at boot (COV-130)"
```

---

### Task 6: Documentation

**Files:**
- Modify: `docs/usage/at-cove-config.md` (owns the `config.yml` schema incl. `collaborators`)
- Modify: `docs/OVERVIEW.md` (one-line pointer where the shared-workspace tradeoff is described)
- Verify: `docs/usage/INDEX.md` row already covers "every field" — no new row expected

**Note:** Use the **docs-author** skill to route the edit; do NOT duplicate the field's rules — the config doc is the single owner, and OVERVIEW only links to it.

- [ ] **Step 1: Document the field in the config doc**

In `docs/usage/at-cove-config.md`, in the `collaborators` section (beside `share-repo-dir`), add a `shadow-dirs` entry stating: it's a list of workspace-relative dirs overmounted with a persistent per-sandbox volume so transient/platform-specific content (`.venv`, `node_modules`, `target`) doesn't collide across the shared bind; **per-class only** (rejected on `<common>`); **requires `share-repo-dir: true`**; entries must be clean relative paths (no absolute, no `..`); volumes survive `recreate` and are purged only by `destroy`. Include the common example:

```yaml
collaborators:
  human:
    share-repo-dir: true
    shadow-dirs: [.venv, node_modules]
```

Mention that `at-cove doctor` (forthcoming, COV-131) will recommend this list.

- [ ] **Step 2: Add the OVERVIEW pointer**

In `docs/OVERVIEW.md`, where the shared-workspace / `share-repo-dir` tradeoff is discussed, add one sentence: transient dirs that would collide across the shared bind can be made VM-local with `shadow-dirs` — link to the `at-cove-config.md` field. No duplicated rules.

- [ ] **Step 3: Run the docs audit**

Invoke the **docs-audit** skill (deterministic checker) to confirm no orphans, dangling links, oversize docs, or duplication introduced.
Expected: clean.

- [ ] **Step 4: Commit**

```bash
git add docs/usage/at-cove-config.md docs/OVERVIEW.md
git commit -m "docs(usage): document collaborators shadow-dirs (COV-130)"
```

---

### Task 7: Full verification

- [ ] **Step 1: Run the whole suite**

Run: `just test`
Expected: PASS.

- [ ] **Step 2: Lint**

Run: `just lint`
Expected: clean.

- [ ] **Step 3: Open the PR against `main`**

One PR bundling Tasks 1-6, referencing COV-130 and the spec. Confirm the branch is off `main` and `just test`/`just lint` are green in the PR description.

---

## Self-Review

**Spec coverage:**
- Config surface + validation (rejected on `<common>`, requires `share-repo-dir`, path rules, dup/collision) → Task 2. ✅
- Persistent per-sandbox named volumes → Tasks 3 (naming/emit) + 4 (state). ✅
- Docker-layer `-v` overmount + `COVE_SHADOW_DIRS` signal → Task 3. ✅
- Teardown removes shadow volumes; `recreate` keeps them (persists via state) → Tasks 3 + 4. ✅
- Ownership gotcha / entrypoint chown in common prologue with escape backstop → Task 5. ✅
- Naming/sanitization + collision rejection → Tasks 1 + 2. ✅
- Docs (config doc owner + OVERVIEW pointer, INDEX in sync) → Task 6. ✅
- Follow-up `doctor` ticket → filed as COV-131 (out of plan scope). ✅

**Placeholder scan:** none — every code/test step carries concrete content.

**Type consistency:** `ShadowVolume`/`SanitizeShadowDir` (Task 1) used identically in Tasks 2-3; `WorkspaceMount.ShadowDirs` and `VolumeSet.Shadow` (Task 3) consumed with matching names in Tasks 4-5; `COVE_SHADOW_DIRS` emitted in Task 3 and read in Task 5; state fields `ShadowDirs`/`Volumes.Shadow` (Task 4) match their producers/consumers. ✅
