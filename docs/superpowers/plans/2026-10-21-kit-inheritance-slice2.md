# Kits building on kits — slice 2 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A studio kit can declare `base: { kit: <name>[@vN] }`. Its image then builds `FROM` the parent kit's base stage, and its definition (egress, MCP servers, build args, secrets, prompt, notes) is merged on top of the parent's, so a role bound to `my-app` gets `default → web-dev → my-app`. A parent change marks following children stale.

**Architecture:**
- **Chain resolution:** a pure `studio.Merge` combines a resolved chain of kits. `jam.ResolveKitChain` resolves the chain from the registry, following `current` or a pinned `@vN`, with a depth cap and cycle detection.
- **One substitution point:** `kitDefinitionFromConfig` becomes chain-aware, so the effective kit flows to the launcher, the session context and staleness without other changes. `KitDefinition` gains the resolved chain (parent refs plus each kit's own base), and the build digest covers the chain.
- **Building:** the launcher builds base stages bottom-up. The root resolves as today (blessed default, a gated image, or a context `FROM` blessed). Each descendant with a context builds `FROM` the previous stage through a new backend op and is gated. A descendant with no context reuses its parent's stage. Harness and hardening are applied once, on the last stage.
- **Guards:** push validates the chain. `rm` and `pin` refuse while children reference the kit. The UI and CLI show *Built on*, *Used by* and the merged view.

**Tech Stack:** Go 1.26; the existing studio/jam/launcher/backend layers; docker (colima backend); html/template.

**Spec:** [`docs/superpowers/specs/2026-10-20-kit-git-sources-and-inheritance-design.md`](../specs/2026-10-20-kit-git-sources-and-inheritance-design.md) §2, §4 (Built on / Used by), §5, §6, slice 2. Slice 3 (same-repo path parents) is out of scope.

**Amendment (build args).** The spec says "each kit's args apply to its own base-stage build only". Today build args never reach the base-stage build: they go to the final (harness/hardening) build and to the session context's `tools.md`. This plan therefore merges `build-args` by key (child wins), passes them to the final build as today, and lists them in `tools.md`. Task 8 records this in the spec.

## Global Constraints

- **Syntax:** `base: { kit: "<name>" }` follows the parent's `current`; `base: { kit: "<name>@v<N>" }` pins.
  - `kit` combines with at most one of `context-files`, `context` or `context-dir` (the child's own stage). It is mutually exclusive with `image`.
  - A value starting with `.` is a slice-3 path parent and is refused for now: `path parents are not supported yet; use the parent kit's registry name`.
- **Chain:** at most **4** kits, counting the kit itself. A cycle is refused. The root is a kit whose base is not `kit:`.
- **Merge** (root first, child last):
  - `egress`: union, sorted, deduplicated.
  - `mcp-servers`: by name; the child replaces.
  - `secrets`: union by name; the child's description wins.
  - `build-args`: by key; the child wins.
  - `prompt`: non-empty prompts joined with `"\n\n"`, root first.
  - `notes`: by name, in order of first appearance; a child's note of the same name replaces the earlier one in place.
- **Push-time checks on the merged kit:** `CheckPrompt` (≤ 800 B) and `CheckNotes` (≤ 20). The kit's own fields are also validated.
- **Digest:** `BuildDigest` of a chained kit covers every link's own digest-relevant fields (base, egress, build args, MCP servers) in order, plus the harness. A change anywhere in a following chain changes the child's digest.
- **Stages:** each kit's base stage is cached under `cove-kit-base:<stage-digest[:32]>`, where the stage digest is sha256 of the parent stage ref and the kit's own `Base`. The provenance gate runs on every stage that is built; the chain must descend from the blessed base.
- **Following vs pinned:** a following parent resolves to its `current` at raise, build and staleness time. A pinned parent is fixed. Each pushed child version records the parent version it was resolved against: `KitVersionMeta.Parent`, which is `{name, version}`.
- **Guards:**
  - `kit rm` refuses while any kit's current version references the kit.
  - `kit pin` refuses while any kit's current version *follows* it.
  - Both errors list the referencing kits (409).
  - A role binding is unchanged.
- **Fail closed:** a missing or invalid parent at raise is a clear error (`kit web-dev (parent of my-app) is missing`), never a silent fallback.
- **Tests:** hermetic (a fake backend for builds). The real docker two-level build runs behind `integration`. `GOPROXY=https://proxy.golang.org,direct`. Docs change in the same task as the behaviour they describe.

## Review Focus

1. **A parent's prompt grows after children were pushed**, so the merged prompt passes 800 B. The raise still works; the session context truncates with its existing warning and nothing panics. *(Task 3: `TestMergedPromptOverBudgetAtRaise`.)*
2. **A pinned child whose parent version was deleted.** In fact a version can't be deleted, only the whole kit, which `rm` refuses. Pinning the child to an old version whose parent kit has since been removed via a forced path fails closed at raise with the missing-parent message. *(Task 3: `TestMissingParentFailsClosed`.)*
3. **A parent edit while a child studio runs.** The child shows `image=stale`, a pinned child does not, and the `currentKitRef` cache doesn't hide the change. *(Task 4: `TestParentChangeMarksFollowingChildStale`.)*
4. **Two children of one parent.** The parent's stage is built once and reused, so the second child does no rebuild of the parent context. *(Task 5: `TestSiblingsShareParentStage`.)*
5. **A child with no context.** Its image is the parent's stage, with harness and hardening applied once; the hardening Dockerfile is not applied twice. *(Task 5: `TestChildWithoutContextReusesParentStage`.)*

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/studio/base.go` (modify) | `Base.Kit`, `BaseKind` additions, validation, `ParentRef` parse |
| `internal/studio/merge.go` | pure `Merge(chain []StudioKit) StudioKit`, `ChainDigest` |
| `internal/jam/kitchain.go` | `ResolveKitChain`, `KitsReferencing`, push-time checks |
| `internal/jam/kit.go`, `studiokit.go`, `supervisor.go` (modify) | `KitDefinition.Chain`, chain-aware `kitDefinitionFromConfig`, cache key |
| `internal/jam/identity.go` (modify) | `KitVersionMeta.Parent` |
| `internal/backend/backend.go`, `colima/colima.go`, `colima/baseimage.go` (modify) | `ResolveKitBaseTarOn(ctx, parentRef)`, `HasKitBase(tag)` / stage tagging |
| `internal/jam/launcher/prepare.go` (modify) | bottom-up stage build |
| `internal/jam/admin.go`, `adminclient`, `cmd/at-jam/main.go` (modify) | rm/pin guards, `kit show` merged/`--own`, chain in API |
| `internal/jam/adminui/kit_detail.go`, `templates/kit.html`, `kits.html` (modify) | Built on, Used by (roles + kits), effective view |
| `docs/usage/jam/kits.md`, `kits-git.md`, spec | docs |

---

### Task 1: `base.kit` syntax and the pure merge

**Files:**
- Modify: `internal/studio/base.go`
- Create: `internal/studio/merge.go`, `internal/studio/merge_test.go`
- Test: `internal/studio/base_test.go`

**Interfaces:**
- Produces:
  - `Base.Kit string \`yaml:"kit,omitempty" json:"kit,omitempty"\``.
  - `BaseKind` gains `BaseKit`, meaning `kit:` with no own context. With a context, `Kind()` keeps returning the context kind; add `func (b Base) HasParent() bool { return b.Kit != "" }`.
  - `func (b Base) ParentRef() (name string, version int, err error)`: version 0 means follow. It refuses `.`-prefixed values with the slice-3 message, and anything that isn't `name` or `name@v<N>` with N ≥ 1 and a `TagSafeName` name.
  - `Kind()` rules: `image` with `kit` is refused (`kit and image are mutually exclusive`), and `kit` plus one context form is allowed.
  - `func Merge(chain []StudioKit) StudioKit`, where `chain[0]` is the root and the last element is the kit itself. The result's `Base` is the **last** kit's own `Base` with `Kit` cleared. It is used only for display; builds use the chain.
  - `func ChainDigest(chain []StudioKit, h harnessinstall.Install) string`, per the Global Constraints. `BuildDigest(sk, h)` stays as is for unchained kits, and a chain of length 1 must yield **exactly** `BuildDigest`, so existing image tags don't churn.
- [ ] **Step 1: Failing tests**

```go
func TestBaseKitParse(t *testing.T) {
	for in, want := range map[string]struct {
		name string
		ver  int
	}{"web-dev": {"web-dev", 0}, "web-dev@v3": {"web-dev", 3}} {
		n, v, err := Base{Kit: in}.ParentRef()
		if err != nil || n != want.name || v != want.ver {
			t.Errorf("%q: %q %d %v", in, n, v, err)
		}
	}
	for _, bad := range []string{"../web", "./web", "web@3", "web@v0", "web@vx", "we b", "web@v1@v2"} {
		if _, _, err := (Base{Kit: bad}).ParentRef(); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, err := (Base{Kit: "web", Image: "x"}).Kind(); err == nil {
		t.Error("kit+image accepted")
	}
	if k, err := (Base{Kit: "web"}).Kind(); err != nil || k != BaseKit {
		t.Errorf("kit alone: %v %v", k, err)
	}
	if k, err := (Base{Kit: "web", Context: validCtx(t)}).Kind(); err != nil || k != BaseContextZip {
		t.Errorf("kit+context: %v %v", k, err)
	}
}

func TestMerge(t *testing.T) {
	root := StudioKit{Kind: "studio", Egress: []string{"github.com"}, BuildArgs: map[string]string{"GO_VERSION": "1.26", "A": "root"},
		Secrets: map[string]kit.SecretConfig{"S": {Description: "root"}}, Prompt: "root prompt",
		Notes:      []KitNote{{Name: "a.md", Body: "ra"}, {Name: "b.md", Body: "rb"}},
		MCPServers: map[string]kit.MCPServer{"docs": {Type: "http", URL: "https://root"}}}
	mid := StudioKit{Kind: "studio", Base: Base{Kit: "root"}, Egress: []string{"registry.npmjs.org", "github.com"}, BuildArgs: map[string]string{"NODE_VERSION": "22"}}
	leaf := StudioKit{Kind: "studio", Base: Base{Kit: "mid"}, BuildArgs: map[string]string{"A": "leaf"},
		Secrets: map[string]kit.SecretConfig{"S": {Description: "leaf"}, "T": {}}, Prompt: "leaf prompt",
		Notes:      []KitNote{{Name: "b.md", Body: "lb"}, {Name: "c.md", Body: "lc"}},
		MCPServers: map[string]kit.MCPServer{"docs": {Type: "http", URL: "https://leaf"}}}
	m := Merge([]StudioKit{root, mid, leaf})
	if !slices.Equal(m.Egress, []string{"github.com", "registry.npmjs.org"}) {
		t.Errorf("egress %v", m.Egress)
	}
	if m.BuildArgs["A"] != "leaf" || m.BuildArgs["GO_VERSION"] != "1.26" || m.BuildArgs["NODE_VERSION"] != "22" {
		t.Errorf("build-args %v", m.BuildArgs)
	}
	if m.Secrets["S"].Description != "leaf" || len(m.Secrets) != 2 {
		t.Errorf("secrets %v", m.Secrets)
	}
	if m.Prompt != "root prompt\n\nleaf prompt" {
		t.Errorf("prompt %q", m.Prompt)
	}
	if got := noteNames(m.Notes); !slices.Equal(got, []string{"a.md", "b.md", "c.md"}) || m.Notes[1].Body != "lb" {
		t.Errorf("notes %v %q", got, m.Notes[1].Body)
	}
	if m.MCPServers["docs"].URL != "https://leaf" {
		t.Errorf("mcp %v", m.MCPServers)
	}
	if m.Base.Kit != "" {
		t.Error("merged base keeps the parent ref")
	}
}

func TestChainDigest(t *testing.T) {
	h := harnessinstall.Default()
	solo := StudioKit{Kind: "studio", Egress: []string{"github.com"}}
	if ChainDigest([]StudioKit{solo}, h) != BuildDigest(solo, h) {
		t.Fatal("a chain of one must equal BuildDigest")
	}
	child := StudioKit{Kind: "studio", Base: Base{Kit: "p"}}
	p1 := StudioKit{Kind: "studio", Egress: []string{"a.example"}}
	p2 := StudioKit{Kind: "studio", Egress: []string{"b.example"}}
	if ChainDigest([]StudioKit{p1, child}, h) == ChainDigest([]StudioKit{p2, child}, h) {
		t.Fatal("parent change must change the child digest")
	}
	withPrompt := p1
	withPrompt.Prompt = "x"
	if ChainDigest([]StudioKit{p1, child}, h) != ChainDigest([]StudioKit{withPrompt, child}, h) {
		t.Fatal("prompt is not build-relevant")
	}
}
```
- [ ] **Step 2: Run** `go test ./internal/studio/ -count=1` → FAIL.
- [ ] **Step 3: Implement.** `Validate` calls `ParentRef` when `Base.Kit != ""`, so a bad ref fails at parse. `ChainDigest` for `len > 1`: sha256 over a JSON array of each link's `{base, egress, buildArgs, mcpServers}`, normalized the same way as `BuildDigest`, plus the harness.
- [ ] **Step 4: Run** → PASS. `go test ./... -count=1` must still pass, because existing digests are unchanged.
- [ ] **Step 5: Commit** `git commit -m "feat(studio): base.kit parent references and the chain merge"`

---

### Task 2: Chain resolution and push-time checks

**Files:**
- Create: `internal/jam/kitchain.go`, `internal/jam/kitchain_test.go`
- Modify: `internal/jam/studiokit.go` (`ensureKit` runs `checkChain` and records `meta.Parent`), `internal/jam/identity.go` (`KitVersionMeta.Parent *KitParentRef \`json:"parent,omitempty"\``, `type KitParentRef struct{ Name string; Version int }`), `internal/jam/memstate.go` (`copyKitMeta` copies `Parent`)

**Interfaces:**
- Produces:
```go
// KitLink is one resolved kit in a chain: root first, the kit itself last.
type KitLink struct {
	Ref    KitRef // ID, Version (Digest = the link's own BuildDigest)
	Kit    studio.StudioKit
	Follow bool // reached by a following (unpinned) reference
}
const MaxKitChain = 4
var ErrKitChain = errors.New("kit chain")
// ResolveKitChain resolves sk's parents from the registry (current, or @vN) up
// to its root. self names sk (for cycle detection; "" when unnamed).
func ResolveKitChain(store Store, self string, sk studio.StudioKit) ([]KitLink, error)
// KitsReferencing lists kits whose CURRENT version names parent (following or pinned).
func KitsReferencing(store Store, parent string) (follow, pinned []string)
```
  - Errors wrap `ErrKitChain` with fixed, specific text:
    - `kit web-dev (parent of my-app) is missing`
    - `kit web-dev has no version v9 (parent of my-app)`
    - `kit chain too deep (max 4): a → b → c → d → e`
    - `cycle: a → b → a`
    - `kit web-dev@v2 (parent of my-app) is invalid: <parse error>`
  - `ensureKit`, in this order:
    1. `ResolveKitChain(store, name, sk)`.
    2. Merge the chain.
    3. `CheckPrompt` and `CheckNotes` on the merged kit.
    4. Push with `meta.Parent = {resolved parent name, resolved version}`.
  - The unchanged check stays on the kit's own canonical JSON. A following child is never re-pushed because its parent moved; staleness covers that.
- [ ] **Step 1: Failing tests** (MemStore):
  - a 3-link resolve (follow and pinned, versions recorded);
  - a missing parent;
  - a missing version;
  - a cycle, built by pushing `a` (root) and `b` (base `a`), then a new `a` with base `b`;
  - depth 5 refused;
  - an invalid parent version;
  - push of a child whose merged prompt exceeds 800 B refused with the prompt error;
  - merged notes over 20 refused;
  - `meta.Parent` recorded;
  - `KitsReferencing` splits follow and pinned;
  - an existing unchained push is untouched (`meta.Parent` nil).
- [ ] **Step 2: Run** → FAIL. **Step 3: Implement.** **Step 4: Run** `go test ./internal/jam/... -count=1` → PASS.
- [ ] **Step 5: Commit** `git commit -m "feat(jam): resolve kit parent chains; validate them at push"`

---

### Task 3: The effective kit everywhere a kit is used

**Files:**
- Modify: `internal/jam/kit.go` (`KitDefinition.Chain []KitLink`), `internal/jam/studiokit.go` (`kitDefinitionFromConfig(store, ref, text, h)`, which resolves the chain, sets `Kit = studio.Merge(kits)` and `Ref.Digest = studio.ChainDigest(kits, h)`; `ResolveKitDefinition` and `StudioKitDefinition` pass the store), `internal/jam/supervisor.go` (`kitSource.definition(store, h)`; `compileContext`'s prompt/notes re-resolve uses the chain-aware path, so the merged prompt and notes follow `current` like today)
- Test: `internal/jam/kitchain_raise_test.go`

**Interfaces:**
- **What a `KitDefinition` now carries:** `Kit` is the merged effective kit (egress, MCP servers, build args, prompt and notes as merged) and `Chain` is the resolved links. The launcher (Task 5) builds from `Chain`. Every other consumer (`studioEgress`, `AssembleContext`, build args, `KitLayer`, `studioFacts`) reads `Kit` unchanged.
- **Unchained kits:** `Chain` has length 1 with the kit itself, and the digest is unchanged.
- **`prepareKitAndRetry`'s re-resolve path:** the chain is re-resolved at the current parents. If the resulting digest differs from `spec.Kit.Digest`, it uses the fresh definition and updates `spec.Kit`, because the image that will run is the one now built. Add a comment and a test.
- [ ] **Step 1: Failing tests** (fake launcher recording the `KitDefinition` it receives):
  - a role on `my-app → web-dev → default` raises with merged egress/MCP/build-args;
  - the compiled session context's kit layer contains `root prompt\n\nleaf prompt` and the union of notes;
  - `tools.md` lists the parent's tool versions;
  - `TestMergedPromptOverBudgetAtRaise`: the parent prompt is grown after the child push, the raise succeeds, and the context compiler reports its truncation warning;
  - `TestMissingParentFailsClosed`: the parent is removed via the store directly, and the raise errors with `kit web-dev (parent of my-app) is missing`, with no launcher call.
- [ ] **Step 2–4:** implement; `go test ./internal/jam/... -count=1` → PASS.
- [ ] **Step 5: Commit** `git commit -m "feat(jam): raise studios from the merged kit chain"`

---

### Task 4: Staleness and the current-kit cache

**Files:**
- Modify: `internal/jam/supervisor.go` (`currentKitRef` cache key gains the chain: each link's `ID@Version` plus a config hash; `kitRefKey` grows a `chain string`), `internal/jam/admin.go` (`imageStatus` unchanged; verify it goes through `CurrentImage`)
- Test: `internal/jam/kitchain_stale_test.go`

- [ ] **Step 1: Failing test** `TestParentChangeMarksFollowingChildStale`:
  1. Raise studios on a following child and on a pinned child; both are `ok`.
  2. Push a new parent version that changes egress.
  3. The following child's status becomes `stale` and the pinned child's stays `ok`.
  4. Push a parent version that changes only the prompt; status stays `ok`, because the prompt isn't in the digest.
  5. The cache never serves the pre-push tag after the push.
- [ ] **Step 2–4:** implement → PASS.
- [ ] **Step 5: Commit** `git commit -m "fix(jam): parent kit changes invalidate the current-image cache"`

---

### Task 5: Building the chain (launcher and backend)

**Files:**
- Modify: `internal/backend/backend.go` (`KitImageBuilder` gains `ResolveKitBaseTarOn(ctx io.Reader, parentRef, stageTag string) (string, error)` and `HasKitBase(tag string) (bool, error)`), `internal/backend/colima/colima.go`, `internal/backend/colima/baseimage.go` (a `streamDockerImg` with `baseArg = parentRef` tags the result `stageTag` and gates against the blessed refs), `internal/jam/launcher/prepare.go` (`resolveStudioBase` builds the chain)
- Test: `internal/jam/launcher/prepare_chain_test.go`, `internal/backend/colima/baseimage_test.go`

**Interfaces:**
- **`resolveStudioBase(def)` with `len(def.Chain) == 1`:** exactly today's behaviour.
- **With a longer chain:**
  1. `ref := resolve(root)`, using today's rules for the root's own base.
  2. For each later link with an own context: compute `stage := "cove-kit-base:" + stageDigest(ref, link.Kit.Base)[:32]`. If `HasKitBase(stage)`, set `ref = stage`. Otherwise set `ref = ResolveKitBaseTarOn(ctxTar(link), ref, stage)`, after `ScanContextTar`.
  3. A link with no own context leaves `ref` unchanged.
  4. Return `ref`. `BuildKitImage(buildDir, tag, ref, def.Kit.BuildArgs, …)` then applies harness and hardening once.
- **Colima:**
  - `--build-arg COVE_BASE_IMAGE=<parentRef>` and `AT_JAM_STUDIO_BASE_IMAGE=<parentRef>`;
  - `docker tag <id> <stageTag>`;
  - the gate runs `baseimage.Resolve` with the blessed refs (the parent descends from blessed, so the child does too);
  - `HasKitBase` uses `docker image inspect`.
- [ ] **Step 1: Failing tests** (`runner.Fake` / fake `Ops`):
  - the build order root → mid → leaf, each `ResolveKitBaseTarOn` called with the previous ref;
  - `TestSiblingsShareParentStage`: the second child's prepare calls `HasKitBase` for the parent stage, which returns true, and does not rebuild it;
  - `TestChildWithoutContextReusesParentStage`: `BuildKitImage` receives the parent stage ref and is called exactly once, so hardening is applied once;
  - a pinned parent's stage digest is stable across parent pushes;
  - a gate failure on any stage surfaces as an error naming the kit link;
  - the colima args include `COVE_BASE_IMAGE=<parentRef>` and `docker tag … <stageTag>`.
- [ ] **Step 2–4:** implement; `go test ./internal/jam/launcher/ ./internal/backend/... -count=1 && just lint` → PASS.
- [ ] **Step 5: Commit** `git commit -m "feat(jam): build kit chains stage by stage on the parent's base"`

---

### Task 6: Guards, API and CLI

**Files:**
- Modify: `internal/jam/admin.go`, `internal/jam/adminclient/adminclient.go`, `cmd/at-jam/main.go`
- Test: `internal/jam/admin_kitchain_test.go`, `cmd/at-jam/kit_chain_test.go`

**Interfaces:**
- **Guards:**
  - `DELETE /admin/kits/{name}` returns 409 `kit web-dev is the parent of my-app, other-kit` when `KitsReferencing` finds any. The existing role check is unchanged.
  - `POST /admin/kits/{name}/pin` returns 409 `kit web-dev is followed by my-app (pin those to a web-dev version, or point them elsewhere)` when following children exist. Pinned children don't block.
- **`GET /admin/kits/{name}`** gains:
  - `chain []{name, version, follow}` (root first, excluding self);
  - `effective string`, the merged kit as YAML (`yaml.Marshal(studio.Merge(…))`, base omitted);
  - `used_by_kits []string`.
  - An older client ignores these fields.
- **`at-jam kit show NAME`:**
  - prints `# built on: default@v1 → web-dev (follows, now v5; pushed against v3)` when chained;
  - then the **effective** YAML by default;
  - `--own` prints the kit's own stored YAML, which is today's output;
  - the egress ceiling lines use the merged egress.
- **`at-jam kit list`:** adds a `parent=web-dev[@vN]` column when set.
- **`kit push`:** a server 400 from chain validation is printed as-is.
- [ ] **Step 1: Failing tests:**
  - rm and pin 409 paths, and the allowed paths;
  - JSON fields;
  - CLI output for chained, `--own` and unchained kits (the existing show test must keep passing unchanged for unchained kits, except that the effective YAML equals the own YAML there);
  - `list` column.
- [ ] **Step 2–4:** implement; `go test ./internal/jam/... ./cmd/at-jam/ -count=1` → PASS.
- [ ] **Step 5: Commit** `git commit -m "feat(jam): kit chain guards, API fields and kit show/list"`

---

### Task 7: Kit page — Built on, Used by, Effective

**Files:**
- Modify: `internal/jam/adminui/kit_detail.go` (`baseLabel` handles `kit`; `kitDetail.Chain`, `UsedByKits`, `Effective kitView`; `kitUses` includes kits), `templates/kit.html` (a **Built on** row linking each parent kit's page, showing version and follows/pinned; a **Used by** list of roles plus child kits; an **Effective** card with merged egress, ceiling, MCP servers, build args, prompt and notes, shown only when chained), `templates/kits.html` (the Base column shows `kit: web-dev`)
- Test: `internal/jam/adminui/kit_chain_test.go`

- [ ] **Step 1: Failing tests:**
  - a chained kit page renders Built on with links, and an Effective card whose egress is the union;
  - the parent page's Used by lists the child kit;
  - an unchained page has no Effective card;
  - names are escaped;
  - the list Base column.
- [ ] **Step 2–4:** implement; `go test ./internal/jam/adminui/ -count=1` → PASS.
- [ ] **Step 5: Docs** — the kit pages section of `docs/usage/jam/ui-pages.md`.
- [ ] **Step 6: Commit** `git commit -m "feat(jam): kit page shows the chain, its users and the effective kit"`

---

### Task 8: Docs, spec amendment and integration

**Files:**
- Modify:
  - `docs/usage/jam/kits.md`: a `### Building on another kit (base.kit)` subsection after `base`, covering syntax, the follow/pin rules, merge rules, the 4-kit cap, staleness, the guards, and the build-args amendment. Update the verbs table for `show --own` and `list`.
  - `docs/usage/jam/kits-git.md`: a git-sourced kit may name a registry parent; path parents come later.
  - `docs/usage/jam/INDEX.md`: the kits row's read_when gains "build on another kit".
  - The spec: §2 build-args amendment.
- Create: `internal/jam/launcher/chain_integration_test.go` (`//go:build integration`, skipped without docker): a real two-level chain build of `default` → a parent with `RUN echo parent > /opt/parent` → a child with `RUN test -f /opt/parent && echo child > /opt/child`. Assert the final image has both files and the hardening marker once.

- [ ] **Step 1: Write the docs**; docs audit (`python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs`), with no new errors.
- [ ] **Step 2: Integration test.** Write it; it runs only where docker exists (CI or the Jam host).
- [ ] **Step 3:** `just test && just lint`.
- [ ] **Step 4: Commit** `git commit -m "docs(jam): kits building on kits; chain build integration test"`

---

### Task 9: Manual acceptance (operator)

- [ ] Push `web-dev` (base: default context, `RUN` installs Node; egress `registry.npmjs.org`; a note), then `my-app` with `base: { kit: web-dev }` and its own small context. Bind a role to `my-app` and raise it. Check that `node --version` works in the studio, `npm` reaches the registry, and the session context shows both kits' notes.
- [ ] Push a new `web-dev` → the `my-app` studio shows `image=stale` → `standing upgrade` picks it up.
- [ ] `kit rm web-dev` → 409 naming `my-app`; the kit page shows Built on / Used by.
