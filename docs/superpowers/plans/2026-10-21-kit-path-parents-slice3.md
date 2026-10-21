# Same-repo path parents — slice 3 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A kit in a git repo can name a sibling kit in the same repo as its parent by path, as in `base: { kit: ../web-dev }`. `kit import`, `kit track` and `kit sync` then bring in the whole set from one commit: ancestors first, each child pinned to the exact parent version that commit produced, and either everything is written or nothing is.

**Architecture:** Path parents exist only at the source level. During a git import, the loader walks the child's path ancestors inside the same checkout. It builds an ordered **set plan** with one entry per kit (name, kit, source dir, and whether it is unchanged). It rewrites each path reference to the registry form `name@v<N>`, using the version that entry will have, and validates the whole set against an **overlay** of the store holding the pending versions. It then writes every changed kit doc in **one atomic store call**. Stored kits only ever contain registry references, so slices 1–2 (raise, build, staleness, guards) work unchanged.

**Tech Stack:** Go 1.26; the existing `studio`, `gitsource` and `jam` kit code; the Mem and Postgres stores.

**Spec:** [`docs/superpowers/specs/2026-10-20-kit-git-sources-and-inheritance-design.md`](../specs/2026-10-20-kit-git-sources-and-inheritance-design.md) §3 and slice 3.

## Global Constraints

- **Syntax:** a `base.kit` value that starts with `.` is a **path**.
  - It resolves relative to the child's directory in the checkout. It must stay inside the checkout after `path.Clean`, and its target directory must contain `kit.yml`.
  - `kit push` from a file refuses a path parent with `path parents resolve only in a git import (kit import/track --git)`.
- **Names:** a path parent's registry name is its `kit.yml` `name:`, else its directory's base name. It must pass `CheckName`. Two kits in one set must not share a name.
- **Order and pinning:**
  - The set is imported in topological order, root first.
  - A path reference is stored as `<name>@v<N>`, where `N` is the version the parent ends up at in this import: its new version, or its current version if unchanged.
  - A registry-name parent (no leading `.`) keeps follow/pin semantics exactly as in slice 2.
- **Refusals** (fixed text; none echo file contents):
  1. **Wrong source:** a path parent whose name already belongs to a kit that has a tracked source with a different `{repo, dir}`, or that has never been imported from this `{repo, dir}`, gives `kit <name> exists and is not from <repo>//<dir>; rename the kit or untrack it first`.
  2. **Newer local push:** a kit whose latest version from this source is followed by a `local` (file-pushed) version gives `kit <name> has a local push newer than its last import from <repo>//<dir>; re-push or untrack it first`.
  3. **Escaping path:** `parent path <p> (of <child>) leaves the checkout`.
  4. **Missing kit.yml:** `parent path <p> (of <child>) has no kit.yml`.
  5. **Cycle:** a path cycle gives `cycle: a → b → a`.
  6. **Too long:** a chain of more than 4 kits gives the slice-2 depth error.
- **Atomicity:** if validation of any kit in the set fails, nothing is written. Writes go through one `Store.PutKitDocs` call, which is atomic in Mem (one lock) and in Postgres (one transaction).
- **Provenance and tracking:**
  - Each version written records its own `{repo, dir, ref, commit}`.
  - `track` sets the tracked source on every kit in the set, each with its own dir.
  - `sync` of a kit re-resolves its ref. If the commit moved, it re-imports the whole set at the new commit.
  - Every kit reports `unchanged` when its own stored config is identical.
- **Results:** `KitSourceResult` gains `Also []KitSourceResult` (`also,omitempty`), listing the ancestors imported or checked with it, root first.
- **Tests and docs:** hermetic tests; `GOPROXY=https://proxy.golang.org,direct`; docs change in the same task as the behavior.

## Review Focus

1. **Re-sync with an unchanged parent and a changed child:** the parent reports `unchanged` and gets no new version. The child is re-pinned to the parent's current version. *(Task 3: `TestSetSyncParentUnchanged`.)*
2. **Re-sync with a changed parent and unchanged child text:** the child still gets a new version, because its stored `@vN` changed. *(Task 3: `TestSetSyncParentChangedChildRepinned`.)*
3. **The set's last ancestor fails validation** (merged prompt over budget at the child): nothing is written for any kit, including ancestors that were valid on their own. *(Task 3: `TestSetAtomicOnFailure`.)*
4. **A path parent's name collides with an unrelated local kit:** the import is refused and the local kit is untouched. *(Task 3: `TestSetRefusesForeignKit`.)*
5. **A Postgres failure mid-write:** no partial set is visible after reload. *(Task 2: conformance plus a Postgres failing-tx test.)*

---

### Task 1: Path-parent syntax

**Files:**
- Modify: `internal/studio/base.go`, `internal/studio/base_test.go`, `internal/jam/studiokit.go` (`ensureKit` refuses path parents), `internal/jam/kitchain.go` (a path value reaching `ResolveKitChain` is an error)

**Interfaces:**
- `func (b Base) ParentPath() (string, bool)` returns the cleaned slash path when `Kit` starts with `.`.
- `ParentRef()` on a path returns `errPathParent`, which carries the new message from Global Constraints (it replaces the slice-2 "not supported yet" text).
- `Validate` accepts a path form if it is non-empty after the dot, has no `\`, is not absolute, and is at most 200 bytes.
- `ensureKit` returns 400 with the `kit push` message when `sk.Base.ParentPath()` is set.

- [ ] **Step 1: Failing tests:**
  - `ParentPath` for `../web`, `./sub/web` and `../../a/b` (cleaned);
  - refusals for `.`, `./`, `.\\web` and 201 bytes;
  - `ParentRef` on a path returns the new error;
  - `PushStudioKit` with `base: {kit: ../web}` returns 400 with the exact message.
- [ ] **Step 2–4:** implement; `go test ./internal/studio/ ./internal/jam/ -count=1` → PASS.
- [ ] **Step 5: Commit** `git commit -m "feat(studio): path parent references in kit.yml"`

---

### Task 2: Atomic multi-kit store write

**Files:**
- Modify: `internal/jam/store.go` (interface), `memstate.go`/`memstore.go`, `pgstore.go`
- Create: `internal/jam/storetest/kits_set.go` (wired into the conformance runner)
- Test: a Postgres integration test for a failing transaction

**Interfaces:**
- `PutKitDocs(kits []Kit) error` replaces each named kit's whole doc (versions, meta, source, current) in one atomic step.
  - **Mem:** under `fs.mu`, apply all or none.
  - **Postgres:** one `BEGIN` … one upsert per kit … `COMMIT`, using the same doc encoding as `PushKitMeta`; the cache is updated only after commit.
  - Callers build the new docs from `copyKit` of the current doc plus new versions. The store does not enforce version immutability: jam owns it, and the doc says so.
- [ ] **Step 1: Failing conformance tests:**
  - two kits written together are both readable, with deep copies;
  - an empty slice is a no-op;
  - a doc with an empty name is refused, and nothing is written for the other kits in the same call.
  - Postgres (`integration` tag): a forced error on the second upsert leaves both kits as they were after reopen. Inject the failure with a test hook on the store, or by violating a constraint, e.g. a name longer than the column allows. Use whichever the schema supports, and say which in the report.
- [ ] **Step 2–4:** implement; `go test ./internal/jam/... -count=1` → PASS; `go vet -tags integration ./internal/jam/`.
- [ ] **Step 5: Commit** `git commit -m "feat(jam): atomic multi-kit doc write"`

---

### Task 3: Importing a kit set

**Files:**
- Create: `internal/jam/kitset.go`, `internal/jam/kitset_test.go`
- Modify: `internal/jam/kitsource.go` (`importKit` builds and applies a set plan; `ImportKit`, `TrackKit` and `SyncKit` return `Also`; locking covers every kit in the set, taken in sorted name order to avoid deadlock), `internal/jam/gitsource/load.go` (export a helper to load `<root>/<dir>/kit.yml` for a computed dir; the existing `Load` stays)

**Interfaces:**
```go
type kitSetEntry struct {
	Name      string
	Dir       string           // source dir in the checkout
	Kit       studio.StudioKit // with path refs rewritten to name@vN
	Version   int              // the version this import leaves it at
	Unchanged bool
}
// planKitSet loads src.Dir's kit and its path ancestors from root (root first),
// assigns names, applies the refusals, rewrites path refs, and validates every
// entry against an overlay of store holding the set's pending versions.
func planKitSet(store Store, root string, src gitsource.Source, name string) ([]kitSetEntry, error)
```
- **Overlay:** an unexported `overlayStore` that wraps `Store` and serves `GetKit` and `KitConfig` from the plan's pending docs. Validation runs `ResolveKitChain`, the merged `CheckPrompt`/`CheckNotes`, and `CheckName` on each entry through the overlay.
- **Apply:** under `kitMu`, re-plan the set (so nothing moved since the fetch), build the new docs, then call one `PutKitDocs`. The fast path for "everything unchanged plus a track update" also goes through `PutKitDocs`.
- **Names:** the top kit's name follows today's rules (the explicit name, the hint, then the repo base name). Each ancestor's name comes from its own `kit.yml` or directory.
- [ ] **Step 1: Failing tests** (fake fetcher serving an in-memory tree):
  - **Two-kit import:** a repo with `web/kit.yml` (`name: web-dev`) and `app/kit.yml` (`base: {kit: ../web}`). Import `//app@main` and expect `web-dev` v1 and `app` v1. `app`'s stored config says `base.kit: web-dev@v1`, and both versions record their own dirs and the commit.
  - **Three-level chain:** import the leaf; all three kits are written in order.
  - **Re-sync cases:** `TestSetSyncParentUnchanged` and `TestSetSyncParentChangedChildRepinned`.
  - **Atomicity:** `TestSetAtomicOnFailure`.
  - **Refusals:**
    - `TestSetRefusesForeignKit` (local kit of the same name);
    - another tracked source with the same name;
    - a newer local push;
    - an escaping path (`../../..`);
    - a missing `kit.yml`;
    - a path cycle;
    - depth 5;
    - duplicate names within the set.
  - **Track:** sets the source on every kit in the set, each with its own dir.
  - **Locking:** `TestSetLocksSorted`, where concurrent imports of two overlapping sets don't deadlock (run with a timeout).
  - **Mixed parents:** the top kit names a path parent whose own parent is a registry name (`default`). It works, and the registry parent is followed.
- [ ] **Step 2–4:** implement; `go test ./internal/jam/... -count=1 && just lint` → PASS.
- [ ] **Step 5: Commit** `git commit -m "feat(jam): import and sync a repo's kit set as one unit"`

---

### Task 4: API, CLI and UI surfaces

**Files:**
- Modify: `internal/jam/kitsource_admin.go` (`KitSourceResult.Also` passes through), `cmd/at-jam/main.go` (`kit import`, `track` and `sync` print one line per ancestor before the kit's own line, e.g. `  web-dev v2 (with app)` or `  web-dev unchanged (v1)`), `internal/jam/adminui/kits.go` + `templates/kit.html` (the import/sync success fragment lists ancestors; the Source card says "imported with <kits>" when the version's source dir differs from a parent's in the same repo)
- Test: the CLI and UI tests for the multi-line output and fragment

- [ ] **Step 1: Failing tests** for each surface; **Step 2–4** implement → PASS.
- [ ] **Step 5: Commit** `git commit -m "feat(jam): show kit sets in import/sync results"`

---

### Task 5: End-to-end test and docs

**Files:**
- Create: `cmd/at-jam/kit_set_integration_test.go` (`//go:build integration`). It uses the slice-1 `gittest.Serve` harness with a two-kit repo and covers:
  - track `//app@main`;
  - both kits exist, with `app` pinned to `web-dev@v1`;
  - change `web` and sync `app`, so `web-dev` goes to v2 and `app` to v2 pinned `@v2`;
  - sync again, and both are unchanged.
- Modify: `docs/usage/jam/kits-inheritance.md` (a "Parents in the same repo" section covering syntax, order, pinning, refusals, atomicity and tracking), `docs/usage/jam/kits-git.md` (the "path parents later" line becomes a link), and the spec (§3 notes the stored `name@vN` rewrite)
- [ ] **Step 1:** write the test and docs.
- [ ] **Step 2:** run `just test && just lint && go test -tags integration ./cmd/at-jam/ -run KitSet -count=1`, plus the docs audit (no new errors).
- [ ] **Step 3: Commit** `git commit -m "docs(jam): same-repo kit parents; kit set end-to-end test"`

---

### Task 6: Manual acceptance (operator)

- [ ] In the test-assets repo, add `web-dev/kit.yml` (installs Node) and `app/kit.yml` with `base: { kit: ../web-dev }`. Run `at-jam kit track app --git https://github.com/aethons-tools/jam-test-assets//app@main` and check that both kits appear and `app` is pinned.
- [ ] Change `web-dev`, push, and Sync `app`. Both should get new versions, and a studio on `app` should show `image=stale`.
