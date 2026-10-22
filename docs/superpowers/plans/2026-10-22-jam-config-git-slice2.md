# A Jam's configuration in git — slice 2 (git) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A Jam follows one config repo in git. `at-jam config track --git <src>` records the source and diffs it, `config sync` applies from it, and `config untrack` forgets it; marks and objects stay. `config diff|apply --git <src>` plan or apply any source, and with no flag they use the tracked source. A config repo can also reference a kit by its own git source, `kits/<name>.source.yml`. Such a kit is fetched before the transaction, planned like any managed kit, and tracked on the kit. Marks and kit provenance carry the real `{repo, dir, ref, commit}`.

**Architecture:**
- **One more store record.** The tracked source is a `ConfigSource` (`{repo, dir, ref, commit, applied_at}`). It is kept in the existing `jam_settings` key/value table (Postgres) and as a field (Mem), so no migration is needed. `ConfigWrite.ConfigSource` writes it in the same transaction as an apply.
- **The same tree.** A git apply fetches the config repo with the one kit `gitsource.Fetcher`. It loads the tree with slice 1's `LoadConfigTree` from inside the checkout, at `//dir`, refusing a symlinked dir. The tree is in memory before the checkout is removed.
- **Kit sources are fetched before the plan.** `FetchConfigKits` makes one checkout per distinct repo and ref named by the tree's `.source.yml` files, one at a time, after the config checkout is closed. It loads each kit's set with slice 3's `loadKitSet` and merges the sets with the inline kits into `ConfigTree.Kits`, parents first. Entries carry a `Group` (their checkout), so `resolveKitEntries` resolves path parents only within their own checkout.
- **Planning is unchanged in shape.** `planKits` resolves every tree kit in one `resolveKitEntries` call against the planned state. A kit's canonical bytes are now its config plus, for a tracked kit, its source line. `configKitDoc` writes a new version with real provenance only when the config changed, and sets the kit's tracked source (a source kit) or clears it (an inline kit).
- **Locks.** Every config run holds `configMu` across all of its fetches, the plan and the write. It takes `kitMu` only around the plan and write, and never takes a per-kit lock. So the `kitLocks` invariant (at most one kit lock, taken before `kitMu`) holds trivially.
- **Surfaces.** The admin routes `PUT|DELETE /admin/config/source` and `POST /admin/config/sync` are added. `diff` and `apply` take `{source}` JSON as well as the tar. There are matching `adminclient` methods and the CLI verbs. `config write` renders a tracked kit as `kits/<name>.source.yml`.

**Tech Stack:** Go 1.26; `gopkg.in/yaml.v3`; the existing `jam` store (MemStore + Postgres via pgx), `gitsource` (Parse/Fetcher/Load, `gittest` smart-HTTP fixture), `studio`, `configtar`.

**Spec:** [`docs/superpowers/specs/2026-10-21-jam-config-in-git-design.md`](../specs/2026-10-21-jam-config-in-git-design.md) §2 (track/sync/untrack, `--git`, routes), §3 (kits: `.source.yml`, provenance, atomicity, locks, audit), §7, §8 (Git, Concurrency) and slice 2. It builds on slice 1's plan, [`2026-10-21-jam-config-git-slice1.md`](2026-10-21-jam-config-git-slice1.md). Slice 3 (Config page, badges, drift-on-save warning, `config release`, `config.drift`) is out of scope.

## Spec deviations and interpretations

1. **Where the tracked source lives.**
   - It is the `jam_settings` row `config_source` (a `ConfigSource` JSON doc). There is no new table and no migration: `jam_settings` is key/value, and `model_spec_schema` set the precedent.
   - Like the marks (slice 1, deviation 11), it is **not** part of `at-jam export`. A restored Jam tracks nothing until `config track`.
2. **What the record holds and when it changes.**
   - `config track` records `{repo, dir, ref}` with no commit, after a successful fetch and load. It records even when the plan has refusals, and a fetch failure records nothing. Re-tracking the same source keeps its last commit and time; tracking another source replaces it.
   - An apply *from the tracked source* records `commit` and `applied_at` in its own write. That means a sync, a no-flag apply, or a `--git` apply naming the same repo, dir and ref. Other `--git` applies leave the record alone.
   - `config untrack` is idempotent (204).
3. **Locks** (the spec's "configMu taken first", made concrete).
   - Every config run (diff, apply, track, sync, `--dir` upload) holds `configMu` from its first fetch to its write. `kitMu` is held only for the plan and the write.
   - Config runs take no per-kit lock. `.source.yml` kits are written under `kitMu`, the way a set's path ancestors already are.
   - Kit operations (push, import, track, sync, untrack) never take `configMu`, so there is no cycle.
   - Holding `configMu` across the fetch keeps two syncs from applying out of order (the older fetch last).
   - **Accepted residual:** a `kit sync`, `track` or push of a config-managed kit can land between a config run's fetch and its plan. The plan, run under `kitMu`, then sees it as drift: refused without `--force`.
4. **Fetch order and bounds.**
   - The config repo is fetched first. Its tree is loaded inside the checkout, and the checkout is closed.
   - Then each distinct `(repo, ref)` named by a `.source.yml` is fetched once, one at a time. Two `.source.yml` files naming the same repo and ref share one checkout and so one commit.
   - Kit sources are not fetched inside the config fetch's callback, because the `Fetcher` serializes fetches per repo URL. A `.source.yml` naming the config repo itself would deadlock there.
   - At most 16 distinct kit sources per tree (`ConfigKitSourceLimit`).
   - One run's fetches share a 4-minute deadline (`ConfigFetchTimeout`). The CLI's config calls wait 5 minutes.
5. **Ancestors of a `.source.yml` kit** are path parents in its own repo. Each becomes a managed kit too, tracking that repo at its own dir. `config write` gives each its own `kits/<ancestor>.source.yml`, which re-applies to the same set.
6. **A kit's canonical form** is its stored config JSON, plus `\n# source: <repo>[//<dir>]@<ref>\n` when the kit tracks a source. The commit is left out.
   - Inline kits that config applies are **untracked**: config owns them, not `kit sync`. So an inline kit's canonical form, and every slice-1 mark on one, is unchanged.
   - A slice-1-managed kit that someone also tracked in Jam shows up once as an `update (drift)`.
7. **A moved commit with identical content is `unchanged`.** The apply then makes no new version, no mark and no audit entry. It still writes the kits' tracked commits and the config source's commit, a metadata-only write. This keeps a later `kit sync` and the Config page in agreement.
8. **`config write` renders every tracked kit as `kits/<name>.source.yml`.**
   - The brief said "a kit that tracks a source outside the config repo". Config-applied inline kits are never tracked, so the only kits tracking the config repo itself are ones an operator ran `kit track` on.
   - Rendering those inline would make the next apply silently untrack them, so they are written as references too.
9. **An inline kit pinned to a source kit** (`base.kit: web@v3`, where `web` is written as `.source.yml`) cannot be expressed in the repo, because path parents only resolve within `kits/<n>/`. `config write` refuses it with 409, the same as slice 1's pin-behind case: `kit <child> pins <parent>@v<N>, a kit from its own source (kits/<parent>.source.yml); versions are per-Jam — base <child> on <parent> (unpinned) before config write`.
10. **A `.source.yml` kit's root pinning a registry parent** (`@vN`) is refused, as slice 1 refuses inline kits: `kits/<n>.source.yml: kit <n>'s base.kit <ref> pins a version; versions are per-Jam — name the kit or use a path parent`.
11. **Failure surfaces.**
    - A `.source.yml` fetch or load failure is a **plan refusal located at the file**. The diff shows it, apply refuses, and the kit is never planned as a delete.
    - The config repo's own fetch failure is the request's error: 502 with the fetcher's redacted text, as for kit sources.
    - A bad `//dir` (missing, a file or a symlink) is 400.
12. **`POST /admin/config/sync` requires a JSON body** (`{}`), as slice 1's tar routes require `application/gzip`. A browser's no-cors POST cannot send one, so it gets 415. `config sync` does not skip the fetch when the config repo's ref has not moved, unlike `kit sync`, because a `.source.yml` kit may have moved.
13. **No `GET /admin/config/source`** in this slice; slice 3's page adds it. The diff, apply, track and sync answers carry `source` and `commit`, and the plan carries `kit_sources` (path, source, commit) for review. `kit_sources` is not part of the plan digest; content changes are.
14. **Inline-kit provenance from `--git`** is `{repo: <config repo>, dir: <config dir>/kits/<name>, ref, commit}`, so the dir is relative to the repo, not the tree. `--dir` keeps slice 1's `{repo: local, dir: kits/<name>}`.
15. **The "source logged raw" leftover.** Logs and results name a source by `configSourceLabel`:
    - `dir` for an upload;
    - otherwise the source exactly as `gitsource.Parse` renders it;
    - `invalid` for a source that does not re-parse, which the API never produces (for example a hand-built URL with userinfo).

    A URL with userinfo is therefore never logged.
16. **`--dir` uploads with `.source.yml` files fetch them too**, with the serve's kit fetcher and its `kits.git` credentials. The upload itself still needs no credentials.

## Global Constraints

- **Source syntax, fetcher and credentials:**
  - The syntax is exactly the kit sources': `<repo>[//<dir>]@<ref>`, parsed by `gitsource.Parse`.
  - Fetching uses the one `gitsource.Fetcher` serve builds (`resolveKitFetcher`, with `kits.git` prefixes).
  - Never write a second fetcher or a credential path.
- **`kits/<name>.source.yml`** is `{kind: kit-source, version: 1, git: <source>}`. Unknown fields are refused, and a `name:`, if present, equals the file name. `.source.yaml` gives `use .yml`. A `kits/<n>/` and a `kits/<n>.source.yml` together are refused.
- **The plan table (§3)** applies to source kits unchanged: create, update, update+drift, adopt, delete, unchanged, unmanaged, and the slice-1 refusal texts.
- **Atomicity:**
  - All fetching, loading and validation happen first.
  - Then the plan, the marks and the tracked-source record are written in one `Store.ApplyConfig`. Any failure writes nothing.
- **Locks:**
  - `configMu` is held across the whole config run. `kitMu` is held only for the plan and the write, then the store's `mu`.
  - Config runs never take a per-kit lock (`lockKit`).
  - Kit operations never take `configMu`.
- **Errors:**
  - Errors are specific and file-located. They never echo a token, a credential value or unvalidated repo text.
  - A repo URL with userinfo is refused by `gitsource.Parse` with its fixed text.
  - Fetch errors are the fetcher's redacted text.
  - No log line ever carries userinfo (`configSourceLabel`).
- **Audit:**
  - `config applied` carries source, commit, counts, operator and forced, and each object keeps its usual `admin …` entry plus `via=config`.
  - New entries: `config tracked` (operator, source, commit) and `config untracked` (operator, source).
- **Routes:**
  - `POST /admin/config/diff` and `/apply` take a tar (`application/gzip`) or `{source}` (`application/json`, `""` = the tracked source).
  - `PUT /admin/config/source` takes `{source}` and answers with the diff.
  - `DELETE /admin/config/source` answers 204.
  - `POST /admin/config/sync?force=1&plan=<digest>` takes a JSON body.
  - Statuses: refused, stale or untracked 409; bad input 400; fetch, auth or limit failure 502; other content types 415.
- **Tests and docs:**
  - Tests are hermetic by default. `gittest` tests skip without git. Postgres tests use the `integration` tag and `JAM_TEST_POSTGRES_DSN`. Set `GOPROXY=https://proxy.golang.org,direct`.
  - Docs change in the same change as the behavior.
  - `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs` must not report more errors than before (710 at the start of this slice).

## Review Focus

1. **A sync of a repo that moved without changing anything.** Examples: a README commit, or a `.source.yml` kit's repo moving only in a non-kit file. Every op is `unchanged`: no new version, no mark rewrite, no `config applied` entry. The kits' tracked commits and the config source's commit still advance. *(Task 5: `TestPlanSourceKitReapplyAndCommitOnlyMove`; Task 6: `TestSyncCommitOnlyMoveRecordsCommit`.)*
2. **One `.source.yml` repo is unreachable.** The diff shows a refusal located at that file and no `delete kit <n>` for the kit, and apply writes nothing. *(Task 5: `TestPlanSourceKitFetchFailureIsNotADelete`.)*
3. **A clone URL with a token pasted into `--git`, `config track` or a `.source.yml`'s `git:`.** It is refused and never echoed: not in CLI output, HTTP bodies, plan refusals or logs. *(Task 2: `TestParseKitSourceFileNeverEchoesSource`; Task 7: `TestConfigSourceRoutesRefuseCredentialURL`; Task 8: `TestConfigGitEndToEnd`.)*
4. **A config source whose `//dir` is a symlink out of the checkout, a file, or missing.** It is refused with 400, and nothing outside the checkout is read. *(Task 6: `TestFetchConfigTreeRefusesBadDir`.)*
5. **Two `config sync`s at once, racing a kit push.** There is no deadlock and no duplicate kit version, and the later sync is a no-op. *(Task 6: `TestConcurrentSyncsAndKitPush`.)*

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/jam/config_marks.go` (modify) | `ConfigSource`, `checkConfigSource`, memState read/apply, `ConfigWrite.ConfigSource` |
| `internal/jam/memstate.go`, `memstore.go`, `store.go` (modify) | the `configSource` field, `MemStore.SetConfigSource`, the `Store` methods |
| `internal/jam/pgstore.go`, `pgstore_config.go`, `pgstore_testhelpers.go` (modify) | load, `SetConfigSource`, the in-tx write, the truncate reset |
| `internal/jam/storetest/config.go` (modify) | conformance for the tracked source |
| `internal/jam/config_files.go` (modify) | `ConfigKindKitSource`, `KitSourceFile`, `ParseKitSourceFile`, `sourceKitPath`, `renderKitSourceFile` |
| `internal/jam/kitset.go` (modify) | `kitSetEntry.Group`; group-aware `resolveKitEntries` |
| `internal/jam/config_tree.go` (modify) | `ConfigTree.KitSources/KitGroups/KitsFetched`, `KitSourceRef`, `.source.yml` loading, `orderKitEntries` |
| `internal/jam/config_fetch.go` (create) | `FetchConfigKits`, `ConfigKitGroup`, `kitGroupKey`, `checkSourceKitSet`, `ConfigKitSourceLimit` |
| `internal/jam/config_export.go` (modify) | `kitCanon`, `.source.yml` rendering, removing a managed kit's other form, `kitPinSourceError` |
| `internal/jam/config_plan.go` (modify) | `ConfigKitSource`, `ConfigPlan.KitSources`, `planner.paths`, `kitProvenance`, `configKitDoc`, `planKits` |
| `internal/jam/config_apply.go` (modify) | `ApplyConfigTree(ctx, store, f, …)`, `applyConfigTreeLocked`, metadata-only write, `ConfigApplyResult.Source/Commit`, audit label |
| `internal/jam/config_source.go` (create) | `ApplyConfigSource`, `TrackConfigSource`, `UntrackConfigSource`, `fetchConfigTree`, `configCheckoutDir`, `configSourceLabel`, `trackedRecord`, `ErrConfigNotTracked`, `ConfigFetchTimeout` |
| `internal/jam/config_admin.go` (modify) | `WithConfig(store, f, env, log)`, `ConfigSourceBody`, `ConfigStatus`, the new routes |
| `internal/jam/adminclient/adminclient.go` (modify) | `ConfigDiffSource`, `ConfigApplySource`, `ConfigTrack`, `ConfigUntrack`, `ConfigSync`, `configGitTimeout` |
| `cmd/at-jam/configgit.go`, `cmd/at-jam/main.go` (modify) | `track|sync|untrack`, `--git`, the tracked default; wiring the fetcher into `WithConfig` |
| `cmd/at-jam/config_git_integration_test.go` (create) | end-to-end over a real fetcher with a token |
| `docs/usage/jam/config-git-sources.md` (create); `config-git.md`, `kits-git.md`, `INDEX.md` (modify) | docs |

---

### Task 1: The tracked config source in the store

**Files:**
- Modify: `internal/jam/config_marks.go` (`ConfigWrite`, `Empty`, `checkConfigWrite`, `applyConfigWrite`; new type and helpers)
- Modify: `internal/jam/memstate.go` (field), `internal/jam/memstore.go` (`SetConfigSource`), `internal/jam/store.go` (interface)
- Modify: `internal/jam/pgstore.go` (load; `SetConfigSource`), `internal/jam/pgstore_config.go` (`writeConfigTx`), `internal/jam/pgstore_testhelpers.go`
- Modify: `internal/jam/storetest/config.go`; Test: `internal/jam/pgstore_integration_test.go`, `internal/jam/config_snapshot_test.go`

**Interfaces:**
- Consumes: `gitsource.Source`; slice 1's `ConfigWrite`, `checkConfigWrite`, `applyConfigWrite`, `writeConfigTx`.
- Produces:
```go
type ConfigSource struct {
	Repo      string    `json:"repo"`
	Dir       string    `json:"dir,omitempty"`
	Ref       string    `json:"ref"`
	Commit    string    `json:"commit,omitempty"`     // last commit applied from it; "" = none since tracked
	AppliedAt time.Time `json:"applied_at,omitzero"`
}
func (c ConfigSource) Source() gitsource.Source
func checkConfigSource(src *ConfigSource) error // nil ok; repo and ref required
// ConfigWrite gains: ConfigSource *ConfigSource // nil = untouched
// Store gains:
GetConfigSource() (ConfigSource, bool)
SetConfigSource(src *ConfigSource) error // nil clears
func (m *memState) applySetConfigSource(src *ConfigSource)
```

- [ ] **Step 1: Write the failing conformance test**

Append to `runConfigConformance` in `internal/jam/storetest/config.go`:

```go
	t.Run("the tracked config source is set, written with an apply, and cleared", func(t *testing.T) {
		st := mk(t)
		if _, ok := st.GetConfigSource(); ok {
			t.Fatal("a new store tracks a config source")
		}
		src := jam.ConfigSource{Repo: "https://github.com/acme/jam", Dir: "cfg", Ref: "main"}
		if err := st.SetConfigSource(&src); err != nil {
			t.Fatal(err)
		}
		if got, ok := st.GetConfigSource(); !ok || got != src {
			t.Fatalf("source %+v %v", got, ok)
		}
		applied := src
		applied.Commit, applied.AppliedAt = "c1", time.Unix(200, 0).UTC()
		if err := st.ApplyConfig(jam.ConfigWrite{ConfigSource: &applied}); err != nil {
			t.Fatal(err)
		}
		if got, _ := st.GetConfigSource(); got != applied {
			t.Fatalf("after apply %+v", got)
		}
		bad, _, _, _ := full()
		bad.ConfigSource = &jam.ConfigSource{Commit: "c2"} // no repo or ref
		if err := st.ApplyConfig(bad); err == nil {
			t.Fatal("a config source without repo and ref must be refused")
		}
		if got, _ := st.GetConfigSource(); got != applied || len(st.ListUsers()) != 0 {
			t.Fatalf("a refused write changed the store: %+v users=%d", got, len(st.ListUsers()))
		}
		if err := st.SetConfigSource(&jam.ConfigSource{}); err == nil {
			t.Fatal("SetConfigSource must refuse a source without repo and ref")
		}
		if err := st.SetConfigSource(nil); err != nil {
			t.Fatal(err)
		}
		if _, ok := st.GetConfigSource(); ok {
			t.Fatal("untracked source still set")
		}
	})
```

Append to `internal/jam/config_snapshot_test.go`:

```go
// The tracked config source is per-Jam state, like the marks: not exported.
func TestConfigSourceNotInExport(t *testing.T) {
	st := NewMemStore()
	if err := st.SetConfigSource(&ConfigSource{Repo: "https://github.com/acme/jam", Ref: "main", Commit: "c1"}); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(st.ExportConfig())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "acme/jam") {
		t.Fatalf("export carries the tracked source: %s", b)
	}
}
```

(Add `"encoding/json"` and `"strings"` to that file's imports if absent.)

Append to `internal/jam/pgstore_integration_test.go`:

```go
// TestPostgresConfigSourcePersists: the tracked source survives a reopen,
// whether set directly or by an apply, and a clear is durable.
func TestPostgresConfigSourcePersists(t *testing.T) {
	dsn := os.Getenv("JAM_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set JAM_TEST_POSTGRES_DSN to run the Postgres store integration tests")
	}
	ctx := context.Background()
	s, err := jam.NewPostgresStore(ctx, dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.TruncateAllForTest(ctx); err != nil {
		t.Fatal(err)
	}
	src := jam.ConfigSource{Repo: "https://github.com/acme/jam", Dir: "cfg", Ref: "main", Commit: "c1", AppliedAt: time.Unix(300, 0).UTC()}
	if err := s.ApplyConfig(jam.ConfigWrite{ConfigSource: &src}); err != nil {
		t.Fatal(err)
	}
	r, err := jam.NewPostgresStore(ctx, dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := r.GetConfigSource()
	r.Close()
	if !ok || got.Repo != src.Repo || got.Dir != "cfg" || got.Commit != "c1" || !got.AppliedAt.Equal(src.AppliedAt) {
		t.Fatalf("after reopen: %+v %v", got, ok)
	}
	if err := s.SetConfigSource(nil); err != nil {
		t.Fatal(err)
	}
	r, err = jam.NewPostgresStore(ctx, dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, ok := r.GetConfigSource(); ok {
		t.Fatal("a cleared source came back on reopen")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/ -run 'TestMemStoreConformance|TestConfigSourceNotInExport'`
Expected: FAIL to compile with `st.GetConfigSource undefined` and `unknown field ConfigSource in struct literal`.

- [ ] **Step 3: Implement**

In `internal/jam/config_marks.go`, add `"github.com/aethons-tools/cove/internal/jam/gitsource"` to the imports. Then add:

```go
// ConfigSource is the Jam's one tracked config source and its last apply
// from it. It lives in jam_settings ('config_source'), not in a table of
// its own, and is not part of at-jam export (like the marks).
type ConfigSource struct {
	Repo      string    `json:"repo"`
	Dir       string    `json:"dir,omitempty"`
	Ref       string    `json:"ref"`
	Commit    string    `json:"commit,omitempty"` // last commit applied from it; "" = none since tracked
	AppliedAt time.Time `json:"applied_at,omitzero"`
}

// Source is c as a fetchable source.
func (c ConfigSource) Source() gitsource.Source {
	return gitsource.Source{Repo: c.Repo, Dir: c.Dir, Ref: c.Ref}
}

// checkConfigSource refuses a tracked source without a repo or a ref; nil
// (clear / untouched) is fine.
func checkConfigSource(src *ConfigSource) error {
	if src != nil && (src.Repo == "" || src.Ref == "") {
		return errors.New("config source: repo and ref are required")
	}
	return nil
}

// GetConfigSource returns the tracked config source; ok=false when none.
func (m *memState) GetConfigSource() (ConfigSource, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.configSource == nil {
		return ConfigSource{}, false
	}
	return *m.configSource, true
}

// applySetConfigSource replaces the tracked source (nil clears). Caller holds mu.
func (m *memState) applySetConfigSource(src *ConfigSource) {
	if src == nil {
		m.configSource = nil
		return
	}
	c := *src
	m.configSource = &c
}
```

(add `"errors"` to the imports.) In `ConfigWrite`, after `RemoveMarks`:

```go
	ConfigSource       *ConfigSource // the tracked source's new record; nil = untouched
```

In `Empty()` change the tail to `… len(w.Marks)+len(w.RemoveMarks) == 0 && w.JamContext == nil && w.ConfigSource == nil`. At the top of `checkConfigWrite`, after the `checkKitDocs` check:

```go
	if err := checkConfigSource(w.ConfigSource); err != nil {
		return fmt.Errorf("config write: %w", err)
	}
```

At the end of `applyConfigWrite`:

```go
	if w.ConfigSource != nil {
		m.applySetConfigSource(w.ConfigSource)
	}
```

In `internal/jam/memstate.go`, after the `marks` field:

```go
	// configSource is the tracked config source (config_source.go); nil = none.
	configSource *ConfigSource
```

In `internal/jam/memstore.go`, after `ApplyConfig`:

```go
func (fs *MemStore) SetConfigSource(src *ConfigSource) error {
	if err := checkConfigSource(src); err != nil {
		return err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.applySetConfigSource(src)
	return nil
}
```

In `internal/jam/store.go`, after `ApplyConfig(w ConfigWrite) error`:

```go
	// GetConfigSource / SetConfigSource read and replace the Jam's tracked
	// config source (nil clears). ApplyConfig also writes it, with the apply
	// (ConfigWrite.ConfigSource).
	GetConfigSource() (ConfigSource, bool)
	SetConfigSource(src *ConfigSource) error
```

In `internal/jam/pgstore.go`, right after the `loadDocs(ctx, "config_marks", …)` block in `load`:

```go
	var cs []byte
	switch err := s.pool.QueryRow(ctx, `SELECT doc FROM jam_settings WHERE key = 'config_source'`).Scan(&cs); {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return fmt.Errorf("pgstore: load jam_settings config_source: %w", err)
	default:
		var src ConfigSource
		if err := json.Unmarshal(cs, &src); err != nil {
			return fmt.Errorf("pgstore: decode jam_settings config_source: %w", err)
		}
		s.configSource = &src
	}
```

and next to `SetJamContext`:

```go
func (s *PostgresStore) SetConfigSource(src *ConfigSource) error {
	if err := checkConfigSource(src); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if src == nil {
		if err := s.exec("clearConfigSource", `DELETE FROM jam_settings WHERE key = 'config_source'`); err != nil {
			return err
		}
	} else {
		doc, err := json.Marshal(src)
		if err != nil {
			return err
		}
		if err := s.exec("setConfigSource",
			`INSERT INTO jam_settings (key, doc) VALUES ('config_source', $1)
			 ON CONFLICT (key) DO UPDATE SET doc = EXCLUDED.doc, updated_at = now()`, doc); err != nil {
			return err
		}
	}
	s.applySetConfigSource(src)
	return nil
}
```

In `internal/jam/pgstore_config.go`, at the end of `writeConfigTx` (before `return nil`):

```go
	if w.ConfigSource != nil {
		doc, err := json.Marshal(*w.ConfigSource)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO jam_settings (key, doc) VALUES ('config_source', $1)
			 ON CONFLICT (key) DO UPDATE SET doc = EXCLUDED.doc, updated_at = now()`, doc); err != nil {
			return err
		}
	}
```

In `internal/jam/pgstore_testhelpers.go`, change the context-row delete to clear both rows, and reset the cache:

```go
	if _, err := s.pool.Exec(ctx, `DELETE FROM jam_settings WHERE key IN ('context', 'config_source')`); err != nil {
		return err
	}
```

and after `s.jamContext = sessionctx.Layer{}` add `s.configSource = nil`.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/... && go vet -tags integration ./internal/jam/`
Expected: PASS (the Postgres test skips without `JAM_TEST_POSTGRES_DSN`; with it, `go test -tags integration -run 'TestPostgres' ./internal/jam/` passes).

- [ ] **Step 5: Commit**

```bash
git add internal/jam/config_marks.go internal/jam/memstate.go internal/jam/memstore.go internal/jam/store.go internal/jam/pgstore.go internal/jam/pgstore_config.go internal/jam/pgstore_testhelpers.go internal/jam/storetest/config.go internal/jam/pgstore_integration_test.go internal/jam/config_snapshot_test.go
git commit -m "feat(jam): store a tracked config source (jam_settings config_source)"
```

---

### Task 2: `kits/<name>.source.yml` files and group-aware kit entries

**Files:**
- Modify: `internal/jam/config_files.go`, `internal/jam/kitset.go`, `internal/jam/config_tree.go`
- Test: `internal/jam/config_files_test.go`, `internal/jam/config_tree_test.go`

**Interfaces:**
- Consumes: `decodeStrict`, `checkHeader`, `renderYAML`, `gitsource.Parse`, `studio.TagSafeName`, slice 1's `treeLoader`.
- Produces:
```go
const ConfigKindKitSource = "kit-source" // a file kind; its object is a ConfigKindKit
type KitSourceFile struct{ Kind string; Version int; Name, Git string } // yaml kind, version, name,omitempty, git
func ParseKitSourceFile(path, name string, data []byte) (KitSourceFile, gitsource.Source, error)
func sourceKitPath(name string) string     // "kits/<name>.source.yml"
func renderKitSourceFile(src KitSource) []byte

// kitSetEntry gains: Group string // "" = the config tree / a kit import's checkout; else kitGroupKey of its .source.yml checkout
// resolveKitEntries: a path ref resolves to an earlier entry with the same Group and dir.

type KitSourceRef struct {
	Path   string           // kits/<name>.source.yml
	Source gitsource.Source
	Commit string           // set by FetchConfigKits; "" = not fetched
}
// ConfigTree gains:
//   KitSources  map[string]KitSourceRef   // by kit name
//   KitGroups   map[string]ConfigKitGroup // Task 3
//   KitsFetched bool                      // Task 3
func orderKitEntries(entries []kitSetEntry) (ordered []kitSetEntry, cycle []string)
```

- [ ] **Step 1: Write the failing tests**

Append to `internal/jam/config_files_test.go`:

```go
func TestParseKitSourceFile(t *testing.T) {
	f, src, err := ParseKitSourceFile("kits/app.source.yml", "app", []byte("kind: kit-source\nversion: 1\ngit: https://github.com/acme/kits//app@main\n"))
	if err != nil || f.Name != "app" || src != (gitsource.Source{Repo: "https://github.com/acme/kits", Dir: "app", Ref: "main"}) {
		t.Fatalf("%+v %+v %v", f, src, err)
	}
	if got := string(renderKitSourceFile(KitSource{Repo: src.Repo, Dir: src.Dir, Ref: src.Ref, Commit: "c1"})); got != "kind: kit-source\nversion: 1\ngit: https://github.com/acme/kits//app@main\n" {
		t.Fatalf("render %q", got)
	}
}

// A pasted clone URL holding a token is refused with gitsource.Parse's fixed
// text: the file's value never reaches the error.
func TestParseKitSourceFileNeverEchoesSource(t *testing.T) {
	const token = "ghp_s3cretTOKEN"
	for _, git := range []string{
		"https://x-access-token:" + token + "@github.com/acme/kits@main",
		"https://github.com/acme/kits@main@" + token,
		token,
	} {
		_, _, err := ParseKitSourceFile("kits/app.source.yml", "app", []byte("kind: kit-source\nversion: 1\ngit: "+git+"\n"))
		if err == nil || strings.Contains(err.Error(), token) || !strings.HasPrefix(err.Error(), "kits/app.source.yml: git: source: ") {
			t.Errorf("%q: %v", git, err)
		}
	}
}
```

(Add the `gitsource` import to that file if absent.) Append to `internal/jam/config_tree_test.go`:

```go
func TestLoadConfigTreeKitSources(t *testing.T) {
	tr := treeOf(t, map[string]string{
		"kits/app.source.yml": "kind: kit-source\nversion: 1\ngit: https://github.com/acme/kits//app@main\n",
	})
	if len(tr.Refusals) > 0 {
		t.Fatal(tr.Refusals)
	}
	ref := tr.KitSources["app"]
	if ref.Path != "kits/app.source.yml" || ref.Source != (gitsource.Source{Repo: "https://github.com/acme/kits", Dir: "app", Ref: "main"}) ||
		ref.Commit != "" || len(tr.Kits) != 0 || tr.KitsFetched {
		t.Fatalf("%+v kits=%v fetched=%v", ref, tr.Kits, tr.KitsFetched)
	}
}

func TestLoadConfigTreeKitSourceRefusals(t *testing.T) {
	tr := treeOf(t, map[string]string{
		"kits/web/kit.yml":      "kind: studio\n",
		"kits/web.source.yml":   "kind: kit-source\nversion: 1\ngit: https://github.com/acme/kits//web@main\n",
		"kits/nogit.source.yml": "kind: kit-source\nversion: 1\n",
		"kits/other.source.yml": "kind: kit-source\nversion: 1\nname: else\ngit: https://github.com/acme/kits@main\n",
		"kits/extra.source.yml": "kind: kit-source\nversion: 1\ngit: https://github.com/acme/kits@main\nref: main\n",
		"kits/x.source.yaml":    "kind: kit-source\n",
	})
	want := []string{
		`kits/extra.source.yml: unknown field "ref"`,
		"kits/nogit.source.yml: git is required (<repo>[//<dir>]@<ref>)",
		`kits/other.source.yml: name "else" does not match the file name "other"`,
		"kits/web.source.yml: kit web is also kits/web/kit.yml",
		"kits/x.source.yaml: use .yml",
	}
	if !slices.Equal(tr.Refusals, want) {
		t.Fatalf("refusals:\n%s", strings.Join(tr.Refusals, "\n"))
	}
	if _, ok := tr.KitSources["web"]; ok {
		t.Fatal("a refused duplicate is kept as a kit source")
	}
}

func TestResolveKitEntriesPathParentsStayInTheirGroup(t *testing.T) {
	set := []kitSetEntry{
		{Name: "web", Dir: "kits/web", Kit: studio.StudioKit{Kind: "studio"}},
		{Name: "web2", Group: "g", Dir: "kits/web", Kit: studio.StudioKit{Kind: "studio"}}, // the same dir in another checkout
		{Name: "app", Group: "g", Dir: "kits/app", Kit: studio.StudioKit{Kind: "studio", Base: studio.Base{Kit: "../web"}}},
	}
	out, err := resolveKitEntries(NewMemStore(), set, nil)
	if err != nil || out[2].Kit.Base.Kit != "web2@v1" {
		t.Fatalf("%v %+v", err, out)
	}
}

func TestOrderKitEntriesAcrossGroups(t *testing.T) {
	in := []kitSetEntry{
		{Name: "app", Group: "g", Dir: "app", Kit: studio.StudioKit{Kind: "studio", Base: studio.Base{Kit: "../web"}}},
		{Name: "web", Group: "g", Dir: "web", Kit: studio.StudioKit{Kind: "studio", Base: studio.Base{Kit: "base"}}},
		{Name: "base", Dir: "kits/base", Kit: studio.StudioKit{Kind: "studio"}},
		{Name: "x", Dir: "kits/x", Kit: studio.StudioKit{Kind: "studio", Base: studio.Base{Kit: "y"}}},
		{Name: "y", Dir: "kits/y", Kit: studio.StudioKit{Kind: "studio", Base: studio.Base{Kit: "x"}}},
	}
	out, cycle := orderKitEntries(in)
	var names []string
	for _, e := range out {
		names = append(names, e.Name)
	}
	if !slices.Equal(names, []string{"base", "web", "app"}) || !slices.Equal(cycle, []string{"x", "y"}) {
		t.Fatalf("order %v cycle %v", names, cycle)
	}
}
```

(Add `"github.com/aethons-tools/cove/internal/jam/gitsource"` to that file's imports.) Also update slice 1's expectations, which change wording:
- In `TestLoadConfigTreeRefusals`, replace `"kits/stray.yml: unexpected file (kits are directories: kits/<name>/kit.yml)",` with `"kits/stray.yml: unexpected file (a kit is kits/<name>/kit.yml or kits/<name>.source.yml)",`. Replace `"kits/web.source.yml: kit source references are not supported yet",` with `"kits/web.source.yml: version must be 1",`.
- In `TestLoadKitsRefusesSymlinkedAncestor`, replace `"kits/web: unexpected file (kits are directories: kits/<name>/kit.yml)",` with `"kits/web: unexpected file (a kit is kits/<name>/kit.yml or kits/<name>.source.yml)",`.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/ -run 'TestParseKitSource|TestLoadConfigTree|TestLoadKits|TestResolveKitEntries|TestOrderKitEntries'`
Expected: FAIL to compile (`undefined: ParseKitSourceFile`, `unknown field Group`, `undefined: orderKitEntries`).

- [ ] **Step 3: Implement**

In `internal/jam/config_files.go`, add the `gitsource` import and:

```go
// ConfigKindKitSource is the file kind of kits/<name>.source.yml; the object
// it names is a kit (ConfigKindKit).
const ConfigKindKitSource = "kit-source"

// KitSourceFile is kits/<name>.source.yml: a kit kept in its own git repo,
// fetched and tracked by the config apply.
type KitSourceFile struct {
	Kind    string `yaml:"kind"`
	Version int    `yaml:"version"`
	Name    string `yaml:"name,omitempty"`
	Git     string `yaml:"git"`
}

// ParseKitSourceFile parses kits/<name>.source.yml and its git: source. A
// bad source is refused with gitsource.Parse's fixed text, never the value.
func ParseKitSourceFile(path, name string, data []byte) (KitSourceFile, gitsource.Source, error) {
	var f KitSourceFile
	if err := decodeStrict(path, data, &f); err != nil {
		return f, gitsource.Source{}, err
	}
	if err := checkHeader(path, ConfigKindKitSource, f.Kind, f.Version, name, &f.Name); err != nil {
		return f, gitsource.Source{}, err
	}
	if f.Git == "" {
		return f, gitsource.Source{}, fmt.Errorf("%s: git is required (<repo>[//<dir>]@<ref>)", path)
	}
	src, err := gitsource.Parse(f.Git)
	if err != nil {
		return f, gitsource.Source{}, fmt.Errorf("%s: git: %s", path, err)
	}
	return f, src, nil
}

// sourceKitPath is the repo file of a kit kept by reference.
func sourceKitPath(name string) string { return "kits/" + name + ".source.yml" }

// renderKitSourceFile renders a tracked kit's reference (its commit is Jam's,
// not the repo's).
func renderKitSourceFile(src KitSource) []byte {
	return renderYAML(KitSourceFile{Kind: ConfigKindKitSource, Version: ConfigFileVersion,
		Git: gitsource.Source{Repo: src.Repo, Dir: src.Dir, Ref: src.Ref}.String()})
}
```

In `internal/jam/kitset.go`, add to `kitSetEntry` after `Dir`:

```go
	Group     string           // the checkout path refs resolve in: "" = the config tree or a kit import's; else a .source.yml's (kitGroupKey)
```

and in `resolveKitEntries` change the parent lookup to:

```go
			j := slices.IndexFunc(out[:i], func(o kitSetEntry) bool { return o.Group == e.Group && checkoutDir(o.Dir) == want })
```

In `internal/jam/config_tree.go`, add to `ConfigTree` after `Kits`:

```go
	KitSources  map[string]KitSourceRef   // kits/<n>.source.yml by kit name
	KitGroups   map[string]ConfigKitGroup // the checkouts FetchConfigKits loaded them from (kitGroupKey)
	KitsFetched bool                      // FetchConfigKits ran: Kits holds the source kits' sets too
```

and change the `Kits` comment to `// every kit, parents first: kits/<n>/ (and, once fetched, the .source.yml kits' sets); path refs as written`. Add:

```go
// KitSourceRef is one kits/<name>.source.yml.
type KitSourceRef struct {
	Path   string           // kits/<name>.source.yml
	Source gitsource.Source // the kit's own git source
	Commit string           // the fetched commit; "" = not fetched
}
```

In `LoadConfigTree`, initialize `KitSources: map[string]KitSourceRef{}` with the other maps. Replace `loadKits` and `orderKits` with:

```go
// loadKits loads each kits/<n>/ dir as a kit set (the kit and its path
// parents), refusing a parent outside kits/ and a registry pin, and each
// kits/<n>.source.yml as a reference (fetched later, by FetchConfigKits);
// then merges the clean sets into one parents-first list.
func (l *treeLoader) loadKits() {
	byDir := map[string]kitSetEntry{}
	for _, e := range l.readDir("kits") {
		n := e.Name()
		rel := "kits/" + n
		if !e.IsDir() {
			switch kn, isSrc := strings.CutSuffix(n, ".source.yml"); {
			case isSrc:
				l.loadKitSource(rel, kn)
			case strings.HasSuffix(n, ".yaml"):
				l.refuse("%s: use .yml", rel)
			default:
				l.refuse("%s: unexpected file (a kit is kits/<name>/kit.yml or kits/<name>.source.yml)", rel)
			}
			continue
		}
		if !l.exists(rel + "/" + gitsource.KitFile) {
			l.refuse("%s: no kit.yml", rel)
			continue
		}
		set, err := loadKitSet(l.root, gitsource.Source{Repo: ConfigDirRepo, Dir: rel}, n)
		if err != nil {
			l.refuse("%s/kit.yml: %s", rel, err)
			continue
		}
		if msg := l.checkTreeKitSet(set); msg != "" {
			l.refuse("%s", msg)
			continue
		}
		for _, se := range set {
			byDir[se.Dir] = se
		}
	}
	for _, n := range slices.Sorted(maps.Keys(l.tr.KitSources)) {
		if l.exists("kits/" + n) {
			l.refuse("%s: kit %s is also kits/%s/kit.yml", l.tr.KitSources[n].Path, n, n)
			delete(l.tr.KitSources, n)
		}
	}
	ordered, cycle := orderKitEntries(slices.Collect(maps.Values(byDir)))
	if len(cycle) > 0 {
		l.refuse("kits: cycle among %s", strings.Join(cycle, ", "))
	}
	l.tr.Kits = ordered
}

// loadKitSource loads kits/<name>.source.yml into the tree's KitSources.
func (l *treeLoader) loadKitSource(rel, name string) {
	if !studio.TagSafeName(name) {
		l.refuse("%s: not a valid kit name", rel)
		return
	}
	data, ok := l.read(rel)
	if !ok {
		return
	}
	if _, src, err := ParseKitSourceFile(rel, name, data); err != nil {
		l.refuse("%s", err)
	} else {
		l.tr.KitSources[name] = KitSourceRef{Path: rel, Source: src}
	}
}

// orderKitEntries orders kits parents first (Kahn's algorithm, ties by name)
// over path parents — resolved within an entry's own checkout (Group) — and
// registry-name parents that are among entries. It returns the kits left in
// a cycle, sorted, apart.
func orderKitEntries(entries []kitSetEntry) ([]kitSetEntry, []string) {
	type at struct{ group, dir string }
	byName := map[string]kitSetEntry{}
	byDir := map[at]kitSetEntry{}
	for _, e := range entries {
		byName[e.Name] = e
		byDir[at{e.Group, checkoutDir(e.Dir)}] = e
	}
	parentOf := map[string]string{} // child → parent among entries
	for name, e := range byName {
		if p, isPath := e.Kit.Base.ParentPath(); isPath {
			if pe, ok := byDir[at{e.Group, path.Join(checkoutDir(e.Dir), p)}]; ok {
				parentOf[name] = pe.Name
			}
		} else if e.Kit.Base.HasParent() {
			if pn, _, err := e.Kit.Base.ParentRef(); err == nil {
				if _, ok := byName[pn]; ok {
					parentOf[name] = pn
				}
			}
		}
	}
	var out []kitSetEntry
	done := map[string]bool{}
	for len(done) < len(byName) {
		var ready []string
		for name := range byName {
			if p, has := parentOf[name]; !done[name] && (!has || done[p]) {
				ready = append(ready, name)
			}
		}
		if len(ready) == 0 {
			break
		}
		next := slices.Min(ready)
		done[next] = true
		out = append(out, byName[next])
	}
	var left []string
	for name := range byName {
		if !done[name] {
			left = append(left, name)
		}
	}
	slices.Sort(left)
	return out, left
}
```

(Add `"maps"` to `config_tree.go`'s imports.) `ConfigKitGroup` is declared in Task 3. Until then, add this temporary declaration at the bottom of `config_tree.go` so the package compiles:

```go
// ConfigKitGroup is one checkout FetchConfigKits loaded .source.yml kits from.
type ConfigKitGroup struct {
	Source gitsource.Source // repo and ref (no dir)
	Commit string
}
```

Task 3 moves this declaration into `config_fetch.go`.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/jam/config_files.go internal/jam/kitset.go internal/jam/config_tree.go internal/jam/config_files_test.go internal/jam/config_tree_test.go
git commit -m "feat(jam): load kits/<name>.source.yml references; group-aware kit entries"
```

---

### Task 3: Fetch the `.source.yml` kits before planning

**Files:**
- Create: `internal/jam/config_fetch.go`, `internal/jam/config_fetch_test.go`
- Modify: `internal/jam/config_tree.go` (move `ConfigKitGroup` out)

**Interfaces:**
- Consumes: `KitFetcher` (`kitsource.go`), `loadKitSet`, `checkoutDir`, `orderKitEntries`, `KitSourceRef`, test helpers `newFakeFetcher`/`fakeFetcher` (`kitsource_test.go`), `treeOf` (`config_plan_test.go`).
- Produces:
```go
const ConfigKitSourceLimit = 16
type ConfigKitGroup struct{ Source gitsource.Source; Commit string } // moved here
func kitGroupKey(src gitsource.Source) string // "<repo>@<ref>"
func FetchConfigKits(ctx context.Context, f KitFetcher, tree *ConfigTree) // idempotent; failures are tree refusals
func checkSourceKitSet(file string, set []kitSetEntry) string
// test helpers (config_fetch_test.go), used by Tasks 5–7:
type repoFetcher map[string]*fakeFetcher // by repo URL; an unknown repo fails with gitsource.ErrFetch
const kitsRepo = "https://github.com/acme/kits"
const appSourceYML = "kind: kit-source\nversion: 1\ngit: https://github.com/acme/kits//app@main\n"
func fetchedTree(t *testing.T, files map[string]string, f KitFetcher) *ConfigTree
```

- [ ] **Step 1: Write the failing tests**

Create `internal/jam/config_fetch_test.go`:

```go
package jam

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam/gitsource"
)

const kitsRepo = "https://github.com/acme/kits"

const appSourceYML = "kind: kit-source\nversion: 1\ngit: https://github.com/acme/kits//app@main\n"

// repoFetcher serves several fake repos by URL; an unknown repo is a fetch
// failure.
type repoFetcher map[string]*fakeFetcher

func (r repoFetcher) Fetch(ctx context.Context, src gitsource.Source, fn func(root, commit string) error) error {
	f, ok := r[src.Repo]
	if !ok {
		return fmt.Errorf("%w: repository not found", gitsource.ErrFetch)
	}
	return f.Fetch(ctx, src, fn)
}

func (r repoFetcher) Resolve(ctx context.Context, src gitsource.Source) (string, error) {
	f, ok := r[src.Repo]
	if !ok {
		return "", fmt.Errorf("%w: repository not found", gitsource.ErrFetch)
	}
	return f.Resolve(ctx, src)
}

// fetchedTree loads files as a config tree and fetches its kit sources from f.
func fetchedTree(t *testing.T, files map[string]string, f KitFetcher) *ConfigTree {
	t.Helper()
	tr := treeOf(t, files)
	FetchConfigKits(context.Background(), f, tr)
	return tr
}

func TestFetchConfigKitsOneCheckoutPerSource(t *testing.T) {
	kits := newFakeFetcher(t, map[string]string{"main": "k1"}, map[string]map[string]string{"k1": {
		"app/kit.yml": "kind: studio\nbase: {kit: ../web}\n",
		"api/kit.yml": "kind: studio\nbase: {kit: ../web}\n",
		"web/kit.yml": "kind: studio\nbase: {kit: base}\n", // a registry parent kept inline in the config repo
	}})
	tr := fetchedTree(t, map[string]string{
		"kits/base/kit.yml":   "kind: studio\n",
		"kits/app.source.yml": appSourceYML,
		"kits/api.source.yml": "kind: kit-source\nversion: 1\ngit: https://github.com/acme/kits//api@main\n",
	}, repoFetcher{kitsRepo: kits})
	if len(tr.Refusals) > 0 {
		t.Fatal(tr.Refusals)
	}
	if kits.fetches != 1 {
		t.Fatalf("fetches = %d, want 1 (one checkout per repo and ref)", kits.fetches)
	}
	var names []string
	for _, e := range tr.Kits {
		names = append(names, e.Name)
	}
	if !slices.Equal(names, []string{"base", "web", "api", "app"}) {
		t.Fatalf("order %v", names)
	}
	g := kitGroupKey(gitsource.Source{Repo: kitsRepo, Ref: "main"})
	if tr.KitGroups[g].Commit != "k1" || tr.KitSources["app"].Commit != "k1" || !tr.KitsFetched {
		t.Fatalf("groups %+v sources %+v", tr.KitGroups, tr.KitSources)
	}
	if tr.Kits[0].Group != "" || tr.Kits[1].Group != g || tr.Kits[3].Group != g {
		t.Fatalf("groups %+v", tr.Kits)
	}
	FetchConfigKits(context.Background(), repoFetcher{kitsRepo: kits}, tr)
	if kits.fetches != 1 {
		t.Fatal("a second FetchConfigKits fetched again")
	}
}

func TestFetchConfigKitsRefusals(t *testing.T) {
	kits := newFakeFetcher(t, map[string]string{"main": "k1"}, map[string]map[string]string{"k1": {
		"app/kit.yml":    "kind: studio\nbase: {kit: ../base}\n", // its ancestor "base" collides with the inline kit
		"base/kit.yml":   "kind: studio\n",
		"pinned/kit.yml": "kind: studio\nbase: {kit: web@v2}\n",
	}})
	tr := fetchedTree(t, map[string]string{
		"kits/base/kit.yml":      "kind: studio\n",
		"kits/app.source.yml":    appSourceYML,
		"kits/pinned.source.yml": "kind: kit-source\nversion: 1\ngit: https://github.com/acme/kits//pinned@main\n",
		"kits/gone.source.yml":   "kind: kit-source\nversion: 1\ngit: https://github.com/acme/missing//x@main\n",
	}, repoFetcher{kitsRepo: kits})
	want := []string{
		"kits/app.source.yml: kit base is also kits/base/kit.yml",
		"kits/gone.source.yml: git fetch failed: repository not found",
		"kits/pinned.source.yml: kit pinned's base.kit web@v2 pins a version; versions are per-Jam — name the kit or use a path parent",
	}
	if !slices.Equal(tr.Refusals, want) {
		t.Fatalf("refusals:\n%s", strings.Join(tr.Refusals, "\n"))
	}
}

func TestFetchConfigKitsWithoutFetcherOrOverLimit(t *testing.T) {
	tr := fetchedTree(t, map[string]string{"kits/app.source.yml": appSourceYML}, nil)
	if !slices.Equal(tr.Refusals, []string{"kits/app.source.yml: kit sources cannot be fetched here"}) {
		t.Fatal(tr.Refusals)
	}
	files := map[string]string{}
	for i := range ConfigKitSourceLimit + 1 {
		files[fmt.Sprintf("kits/k%d.source.yml", i)] = fmt.Sprintf("kind: kit-source\nversion: 1\ngit: https://github.com/acme/kits//k@r%d\n", i)
	}
	tr = fetchedTree(t, files, repoFetcher{})
	if !slices.Equal(tr.Refusals, []string{"kits: 17 kit sources (distinct repo and ref); at most 16 per config repo"}) {
		t.Fatal(tr.Refusals)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/ -run TestFetchConfigKits`
Expected: FAIL to compile (`undefined: FetchConfigKits`, `undefined: kitGroupKey`, `undefined: ConfigKitSourceLimit`).

- [ ] **Step 3: Implement**

Delete the temporary `ConfigKitGroup` from `config_tree.go`. Create `internal/jam/config_fetch.go`:

```go
package jam

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/aethons-tools/cove/internal/jam/gitsource"
)

// ConfigKitSourceLimit caps the distinct kit sources (repo and ref) one
// config tree's kits/<name>.source.yml files may name: each is a fetch.
const ConfigKitSourceLimit = 16

// ConfigKitGroup is one checkout FetchConfigKits loaded .source.yml kits from.
type ConfigKitGroup struct {
	Source gitsource.Source // repo and ref (no dir)
	Commit string
}

// kitGroupKey names the checkout a kit source loads from: its repo and ref.
func kitGroupKey(src gitsource.Source) string {
	return gitsource.Source{Repo: src.Repo, Ref: src.Ref}.String()
}

// FetchConfigKits fetches tree's kits/<name>.source.yml kits before any plan:
// one checkout per distinct repo and ref, one at a time, each kit's set
// (the kit and its path parents) loaded from it with loadKitSet, then merged
// with the inline kits into tree.Kits, parents first. The caller holds
// configMu and no other lock (a fetch can take a minute). Every failure is a
// refusal at the .source.yml (a fetch error is the fetcher's redacted text);
// a second call is a no-op.
func FetchConfigKits(ctx context.Context, f KitFetcher, tree *ConfigTree) {
	if tree.KitsFetched {
		return
	}
	tree.KitsFetched = true
	if len(tree.KitSources) == 0 {
		return
	}
	refuse := func(format string, a ...any) { tree.Refusals = append(tree.Refusals, fmt.Sprintf(format, a...)) }
	defer func() {
		slices.Sort(tree.Refusals)
		tree.Refusals = slices.Compact(tree.Refusals)
	}()
	groups := map[string][]string{} // group key → kit names, sorted
	for _, n := range slices.Sorted(maps.Keys(tree.KitSources)) {
		k := kitGroupKey(tree.KitSources[n].Source)
		groups[k] = append(groups[k], n)
	}
	if len(groups) > ConfigKitSourceLimit {
		refuse("kits: %d kit sources (distinct repo and ref); at most %d per config repo", len(groups), ConfigKitSourceLimit)
		return
	}
	if f == nil {
		for _, ref := range tree.KitSources {
			refuse("%s: kit sources cannot be fetched here", ref.Path)
		}
		return
	}
	type at struct{ group, dir string }
	byDir := map[at]kitSetEntry{}
	byName := map[string]at{}
	from := map[string]string{} // kit name → the file that brought it in
	add := func(e kitSetEntry, file string) {
		k := at{e.Group, checkoutDir(e.Dir)}
		if prev, ok := byName[e.Name]; ok {
			if prev != k {
				refuse("%s: kit %s is also %s", file, e.Name, from[e.Name])
			}
			return // the same kit from the same checkout: loaded once
		}
		byName[e.Name], from[e.Name], byDir[k] = k, file, e
	}
	for _, e := range tree.Kits {
		add(e, "kits/"+e.Name+"/kit.yml")
	}
	tree.KitGroups = map[string]ConfigKitGroup{}
	for _, g := range slices.Sorted(maps.Keys(groups)) {
		names := groups[g]
		first := tree.KitSources[names[0]].Source
		gsrc := gitsource.Source{Repo: first.Repo, Ref: first.Ref}
		sets := map[string][]kitSetEntry{}
		var commit string
		err := f.Fetch(ctx, gsrc, func(root, c string) error {
			commit = c
			for _, n := range names {
				ref := tree.KitSources[n]
				set, err := loadKitSet(root, ref.Source, n)
				if err != nil {
					refuse("%s: %s", ref.Path, err)
					continue
				}
				if msg := checkSourceKitSet(ref.Path, set); msg != "" {
					refuse("%s", msg)
					continue
				}
				sets[n] = set
			}
			return nil
		})
		if err != nil {
			for _, n := range names {
				refuse("%s: %s", tree.KitSources[n].Path, err)
			}
			continue
		}
		tree.KitGroups[g] = ConfigKitGroup{Source: gsrc, Commit: commit}
		for _, n := range names {
			ref := tree.KitSources[n]
			ref.Commit = commit
			tree.KitSources[n] = ref
			for _, e := range sets[n] {
				e.Group = g
				add(e, ref.Path)
			}
		}
	}
	ordered, cycle := orderKitEntries(slices.Collect(maps.Values(byDir)))
	if len(cycle) > 0 {
		refuse("kits: cycle among %s", strings.Join(cycle, ", "))
	}
	tree.Kits = ordered
}

// checkSourceKitSet is a fetched set's refusal, "" = none: a root pinning a
// registry parent's version (versions are per-Jam), as for inline kits.
func checkSourceKitSet(file string, set []kitSetEntry) string {
	root := set[0]
	if _, isPath := root.Kit.Base.ParentPath(); !isPath && root.Kit.Base.HasParent() {
		if _, v, err := root.Kit.Base.ParentRef(); err == nil && v > 0 {
			return fmt.Sprintf("%s: kit %s's base.kit %s pins a version; versions are per-Jam — name the kit or use a path parent", file, root.Name, root.Kit.Base.Kit)
		}
	}
	return ""
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/jam/config_fetch.go internal/jam/config_fetch_test.go internal/jam/config_tree.go
git commit -m "feat(jam): fetch a config tree's .source.yml kits, one checkout per source"
```

---

### Task 4: Kit canonical form with its source; `config write` renders `.source.yml`

**Files:**
- Modify: `internal/jam/config_export.go`
- Test: `internal/jam/config_export_test.go`

**Interfaces:**
- Consumes: `sourceKitPath`, `renderKitSourceFile` (Task 2), `trackedFixture` (`kitsource_test.go`, web-dev tracks `https://github.com/acme/kits//web@main` at `c1`).
- Produces:
```go
func kitCanon(config string, tracked *KitSource) []byte // config, plus "\n# source: <src>\n" when tracked
type kitPinSourceError struct{ kit, parent string; pin int } // Unwrap() == ErrConfigKitPinBehind
// exportObjects: a tracked kit is kits/<n>.source.yml with Canon kitCanon(cfg, k.Source)
// ExportConfigTree: a managed kit's other form (kit.yml vs .source.yml) is in Remove
```

- [ ] **Step 1: Write the failing tests**

Append to `internal/jam/config_export_test.go`:

```go
func TestExportRendersTrackedKitAsSourceFile(t *testing.T) {
	st, _ := trackedFixture(t)
	exp, err := ExportConfigTree(st.ExportConfig(), nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := exp.Files["kits/web-dev.source.yml"]; got != "kind: kit-source\nversion: 1\ngit: https://github.com/acme/kits//web@main\n" {
		t.Fatalf("source file %q", got)
	}
	if _, ok := exp.Files["kits/web-dev/kit.yml"]; ok {
		t.Fatal("a tracked kit is also written inline")
	}
}

func TestKitCanonCarriesTrackedSource(t *testing.T) {
	st, _ := trackedFixture(t)
	objs, err := exportObjects(stateFromSnapshot(st.ExportConfig(), nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	k, _ := st.GetKit("web-dev")
	cfg, _ := st.KitConfig("web-dev", k.Current)
	if got := string(objs[ConfigKey{ConfigKindKit, "web-dev"}].Canon); got != cfg+"\n# source: https://github.com/acme/kits//web@main\n" {
		t.Fatalf("canon %q", got)
	}
	if string(kitCanon(cfg, nil)) != cfg {
		t.Fatal("an untracked kit's canon is not its config")
	}
}

func TestExportRemovesAManagedKitsOtherForm(t *testing.T) {
	st, _ := trackedFixture(t)
	if _, _, err := PushStudioKit(st, "app", "kind: studio\n"); err != nil {
		t.Fatal(err)
	}
	marks := []ConfigMark{{Kind: ConfigKindKit, Name: "web-dev"}, {Kind: ConfigKindKit, Name: "app"}, {Kind: ConfigKindKit, Name: "gone"}}
	exp, err := ExportConfigTree(st.ExportConfig(), marks, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"kits/app.source.yml", "kits/gone.source.yml", "kits/gone/kit.yml", "kits/web-dev/kit.yml"}
	if !slices.Equal(exp.Remove, want) {
		t.Fatalf("remove %v", exp.Remove)
	}
}

func TestExportRefusesPinToSourceKit(t *testing.T) {
	st, _ := trackedFixture(t)
	if _, _, err := PushStudioKit(st, "app", "kind: studio\nbase:\n  kit: web-dev@v1\n"); err != nil {
		t.Fatal(err)
	}
	_, err := ExportConfigTree(st.ExportConfig(), nil, nil, true)
	want := "kit app pins web-dev@v1, a kit from its own source (kits/web-dev.source.yml); versions are per-Jam — base app on web-dev (unpinned) before config write"
	if !errors.Is(err, ErrConfigKitPinBehind) || err.Error() != want {
		t.Fatalf("%v", err)
	}
}
```

(Add `"errors"` and `"slices"` to the imports if absent.)

- [ ] **Step 2: Run the tests to verify they fail**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/ -run 'TestExport|TestKitCanon'`
Expected: FAIL to compile (`undefined: kitCanon`).

- [ ] **Step 3: Implement**

In `internal/jam/config_export.go` add the `gitsource` import and:

```go
// kitCanon is a kit's canonical bytes: its stored config, then, for a kit
// that tracks a source, that source (not its commit: a sync that only moves
// the commit is not a change).
func kitCanon(config string, tracked *KitSource) []byte {
	if tracked == nil {
		return []byte(config)
	}
	src := gitsource.Source{Repo: tracked.Repo, Dir: tracked.Dir, Ref: tracked.Ref}
	return []byte(config + "\n# source: " + src.String() + "\n")
}
```

In `exportObjects`, replace the kit loop body after `cfg, ok := …; if !ok { continue }` with:

```go
		key := ConfigKey{ConfigKindKit, n}
		if k.Source != nil { // a kit following its own source is written by reference
			out[key] = configObject{Key: key, Files: map[string][]byte{sourceKitPath(n): renderKitSourceFile(*k.Source)},
				Canon: kitCanon(cfg, k.Source)}
			continue
		}
		b, err := renderKitFile(m, n, cfg, inTree)
		if errors.Is(err, ErrConfigKitPinBehind) {
			return nil, err
		}
		if err != nil {
			return nil, fmt.Errorf("kit %s: %w", n, err)
		}
		out[key] = configObject{Key: key, Files: map[string][]byte{key.Path(): b}, Canon: kitCanon(cfg, nil)}
```

Change the `ErrConfigKitPinBehind` comment to: `// ErrConfigKitPinBehind is a config write of a kit whose parent pin the repo cannot express: behind its in-tree parent's current version (a path parent re-pins to current at apply), or to a parent written as kits/<parent>.source.yml (path parents cannot reach it). Versions are per-Jam.` Add:

```go
type kitPinSourceError struct {
	kit, parent string
	pin         int
}

func (e *kitPinSourceError) Error() string {
	return fmt.Sprintf("kit %s pins %s@v%d, a kit from its own source (%s); versions are per-Jam — base %s on %s (unpinned) before config write",
		e.kit, e.parent, e.pin, sourceKitPath(e.parent), e.kit, e.parent)
}

func (e *kitPinSourceError) Unwrap() error { return ErrConfigKitPinBehind }
```

In `renderKitFile`, inside the `if parent, v, err := …; … inTree(parent) {` block, first:

```go
		if m.kits[parent].Source != nil {
			return nil, &kitPinSourceError{kit: name, parent: parent, pin: v}
		}
```

In `ExportConfigTree`, replace the managed-removal loop with:

```go
	for k := range managed {
		if o, ok := objs[k]; ok {
			if k.Kind == ConfigKindKit { // the kit's other form, left from before it switched
				for _, p := range []string{k.Path(), sourceKitPath(k.Name)} {
					if _, ok := o.Files[p]; !ok {
						remove[p] = true
					}
				}
			}
			continue
		}
		remove[k.Path()] = true
		switch k.Kind {
		case ConfigKindProject:
			remove["projects/"+k.Name+"/context.md"] = true
		case ConfigKindKit:
			remove[sourceKitPath(k.Name)] = true
		}
	}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/...`
Expected: PASS (slice 1's export and plan tests are unchanged: untracked kits keep their canon).

- [ ] **Step 5: Commit**

```bash
git add internal/jam/config_export.go internal/jam/config_export_test.go
git commit -m "feat(jam): config write renders tracked kits as kits/<name>.source.yml"
```

---

### Task 5: Plan `.source.yml` kits and real kit provenance

**Files:**
- Modify: `internal/jam/config_plan.go`
- Test: `internal/jam/config_plan_test.go`

**Interfaces:**
- Consumes: `ConfigTree.KitSources/KitGroups/KitsFetched` (Tasks 2, 3), `kitCanon`, `sourceKitPath` (Tasks 2, 4), `resolveKitEntries`, `classify`, `record`, `copyKit`, `copyKitSource`; test helpers `repoFetcher`, `fetchedTree`, `kitsRepo`, `appSourceYML` (Task 3), `treeOf`, `planOn`, `applyPlan`, `opOf`, `planNow`, `allCreds`.
- Produces:
```go
type ConfigKitSource struct {
	Path   string `json:"path"`
	Source string `json:"source"`
	Commit string `json:"commit"`
}
// ConfigPlan gains: KitSources []ConfigKitSource `json:"kit_sources,omitempty"` (not in the digest)
// planner gains:    paths map[ConfigKey]string // op paths that differ from Key.Path() (kits by reference)
func (p *planner) kitProvenance(e kitSetEntry) (at KitSource, track bool)
func configKitDoc(store Store, e kitSetEntry, at KitSource, track bool, now time.Time) (Kit, bool)
func sameKitSource(a, b *KitSource) bool
// test helpers (config_plan_test.go), used by Tasks 6–7:
var cfgSrc = gitsource.Source{Repo: "https://github.com/acme/jam", Dir: "cfg", Ref: "main"}
func gitPlanOn(t *testing.T, st *MemStore, tr *ConfigTree, src gitsource.Source, commit string, force bool) ConfigPlan
```

- [ ] **Step 1: Write the failing tests**

Append to `internal/jam/config_plan_test.go`:

```go
var cfgSrc = gitsource.Source{Repo: "https://github.com/acme/jam", Dir: "cfg", Ref: "main"}

// gitPlanOn plans tr against st as an apply of src at commit.
func gitPlanOn(t *testing.T, st *MemStore, tr *ConfigTree, src gitsource.Source, commit string, force bool) ConfigPlan {
	t.Helper()
	return PlanConfig(ConfigPlanInput{
		Tree: tr, Snapshot: st.ExportConfig(), Instances: st.ListInstances(), Marks: st.ListConfigMarks(),
		Env: allCreds, Source: src, Commit: commit, Force: force, Now: planNow,
	})
}

func markOf(st *MemStore, kind, name string) ConfigMark {
	for _, mk := range st.ListConfigMarks() {
		if mk.Kind == kind && mk.Name == name {
			return mk
		}
	}
	return ConfigMark{}
}

// sourceKitJam applies a tree holding kits/app.source.yml (kitsRepo//app@main
// at k1) as config commit c1.
func sourceKitJam(t *testing.T) (*MemStore, *fakeFetcher) {
	t.Helper()
	kits := newFakeFetcher(t, map[string]string{"main": "k1"}, map[string]map[string]string{"k1": {"app/kit.yml": "kind: studio\nprompt: hi\n"}})
	st := NewMemStore()
	p := gitPlanOn(t, st, fetchedTree(t, map[string]string{"kits/app.source.yml": appSourceYML}, repoFetcher{kitsRepo: kits}), cfgSrc, "c1", false)
	if op := opOf(p, ConfigKindKit, "app"); op.Action != ConfigCreate || op.Path != "kits/app.source.yml" {
		t.Fatalf("op %+v", op)
	}
	if len(p.KitSources) != 1 || p.KitSources[0] != (ConfigKitSource{Path: "kits/app.source.yml", Source: "https://github.com/acme/kits//app@main", Commit: "k1"}) {
		t.Fatalf("kit sources %+v", p.KitSources)
	}
	applyPlan(t, st, p)
	return st, kits
}

func TestPlanSourceKitCreateTracksIt(t *testing.T) {
	st, _ := sourceKitJam(t)
	k, _ := st.GetKit("app")
	want := KitSource{Repo: kitsRepo, Dir: "app", Ref: "main", Commit: "k1"}
	if k.Source == nil || *k.Source != want || k.Meta[1].Source == nil || *k.Meta[1].Source != want {
		t.Fatalf("tracked %+v provenance %+v", k.Source, k.Meta[1].Source)
	}
	if mk := markOf(st, ConfigKindKit, "app"); mk.Commit != "c1" {
		t.Fatalf("mark %+v: want the config commit", mk)
	}
}

func TestPlanSourceKitReapplyAndCommitOnlyMove(t *testing.T) {
	st, kits := sourceKitJam(t)
	files := map[string]string{"kits/app.source.yml": appSourceYML}
	again := gitPlanOn(t, st, fetchedTree(t, files, repoFetcher{kitsRepo: kits}), cfgSrc, "c1", false)
	if op := opOf(again, ConfigKindKit, "app"); op.Action != ConfigUnchanged || len(again.write.Kits) != 0 {
		t.Fatalf("reapply: %+v kits=%d", op, len(again.write.Kits))
	}
	kits.commit("main", "k2", map[string]string{"app/kit.yml": "kind: studio\nprompt: hi\n", "README": "moved"})
	moved := gitPlanOn(t, st, fetchedTree(t, files, repoFetcher{kitsRepo: kits}), cfgSrc, "c1", false)
	if op := opOf(moved, ConfigKindKit, "app"); op.Action != ConfigUnchanged || ConfigChanges(moved) != 0 {
		t.Fatalf("commit-only move: %+v", moved.Ops)
	}
	if len(moved.write.Kits) != 1 || moved.write.Kits[0].Source.Commit != "k2" || len(moved.write.Kits[0].Versions) != 1 {
		t.Fatalf("the tracked commit does not advance alone: %+v", moved.write.Kits)
	}
}

func TestPlanSourceKitUpdateAndDrift(t *testing.T) {
	st, kits := sourceKitJam(t)
	files := map[string]string{"kits/app.source.yml": appSourceYML}
	kits.commit("main", "k2", map[string]string{"app/kit.yml": "kind: studio\nprompt: two\n"})
	p := gitPlanOn(t, st, fetchedTree(t, files, repoFetcher{kitsRepo: kits}), cfgSrc, "c2", false)
	if op := opOf(p, ConfigKindKit, "app"); op.Action != ConfigUpdate || op.Drift {
		t.Fatalf("op %+v", op)
	}
	if _, _, err := PushStudioKit(st, "app", "kind: studio\nprompt: jam\n"); err != nil {
		t.Fatal(err)
	}
	p = gitPlanOn(t, st, fetchedTree(t, files, repoFetcher{kitsRepo: kits}), cfgSrc, "c2", false)
	if !slices.Contains(p.Refusals, "kit app drifted (edited in Jam since config @c1); re-run with --force or config write it first") {
		t.Fatalf("refusals %v", p.Refusals)
	}
}

func TestPlanInlineKitFromGitRecordsProvenanceAndUntracks(t *testing.T) {
	st := NewMemStore()
	tr := treeOf(t, map[string]string{"kits/web/kit.yml": "kind: studio\n"})
	applyPlan(t, st, gitPlanOn(t, st, tr, cfgSrc, "c9", false))
	k, _ := st.GetKit("web")
	want := KitSource{Repo: cfgSrc.Repo, Dir: "cfg/kits/web", Ref: "main", Commit: "c9"}
	if k.Source != nil || k.Meta[1].Source == nil || *k.Meta[1].Source != want {
		t.Fatalf("tracked %+v provenance %+v", k.Source, k.Meta[1].Source)
	}
	if mk := markOf(st, ConfigKindKit, "web"); mk.Commit != "c9" {
		t.Fatalf("mark %+v", mk)
	}
	// Tracked in Jam since: the repo's inline copy is an update over drift
	// that untracks it, with no new version (same config).
	if err := st.SetKitSource("web", &KitSource{Repo: kitsRepo, Dir: "web", Ref: "main"}); err != nil {
		t.Fatal(err)
	}
	p := gitPlanOn(t, st, tr, cfgSrc, "c9", true)
	if op := opOf(p, ConfigKindKit, "web"); op.Action != ConfigUpdate || !op.Drift || op.Path != "kits/web/kit.yml" {
		t.Fatalf("op %+v", op)
	}
	applyPlan(t, st, p)
	if k, _ := st.GetKit("web"); k.Source != nil || len(k.Versions) != 1 {
		t.Fatalf("%+v", k)
	}
}

func TestPlanSourceKitFetchFailureIsNotADelete(t *testing.T) {
	st, _ := sourceKitJam(t)
	broken := fetchedTree(t, map[string]string{"kits/app.source.yml": appSourceYML}, repoFetcher{}) // the kits repo is unreachable
	p := gitPlanOn(t, st, broken, cfgSrc, "c2", false)
	if op := opOf(p, ConfigKindKit, "app"); op.Action == ConfigDelete {
		t.Fatalf("an unreachable source plans a delete: %+v", op)
	}
	if !slices.Contains(p.Refusals, "kits/app.source.yml: git fetch failed: repository not found") {
		t.Fatalf("refusals %v", p.Refusals)
	}
}

func TestPlanRefusesUnfetchedKitSources(t *testing.T) {
	p := planOn(t, NewMemStore(), treeOf(t, map[string]string{"kits/app.source.yml": appSourceYML}), false)
	if !slices.Contains(p.Refusals, "kits: the kit sources (kits/<name>.source.yml) were not fetched") {
		t.Fatalf("refusals %v", p.Refusals)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/ -run 'TestPlanSourceKit|TestPlanInlineKitFromGit|TestPlanRefusesUnfetched'`
Expected: FAIL to compile (`undefined: ConfigKitSource`, `p.KitSources undefined`).

- [ ] **Step 3: Implement**

In `internal/jam/config_plan.go` add `"path"` to the imports, and add:

```go
// ConfigKitSource is one kits/<name>.source.yml as the plan fetched it.
type ConfigKitSource struct {
	Path   string `json:"path"`
	Source string `json:"source"`
	Commit string `json:"commit"`
}
```

Add `KitSources []ConfigKitSource `json:"kit_sources,omitempty"`` to `ConfigPlan` after `ServeConfig` (`planDigest` is unchanged and does not include it). Add `paths map[ConfigKey]string // op paths other than Key.Path(): kits kept by reference` to `planner`, and initialize `paths: map[ConfigKey]string{}` in `PlanConfig`'s `&planner{…}`. In `op`:

```go
func (p *planner) op(key ConfigKey, action string, drift bool, before, after []byte) {
	file := key.Path()
	if pp, ok := p.paths[key]; ok {
		file = pp
	}
	p.plan.Ops = append(p.plan.Ops, ConfigOp{Kind: key.Kind, Name: key.Name, Action: action, Drift: drift,
		Path: file, Before: string(before), After: string(after)})
}
```

Replace `planKits` with:

```go
// planKits resolves the tree's kits — inline and, once fetched, the
// .source.yml kits' sets — as one parents-first list against the planned
// state (path parents pinned within their own checkout), classifies each by
// its canonical form (config plus tracked source), and builds the docs it
// changes: a new version with its provenance when the config changed, and
// the tracked source a .source.yml kit follows (an inline kit's is cleared).
func (p *planner) planKits() {
	tree := p.in.Tree
	for n, k := range p.m.kits {
		if k.Source != nil {
			p.paths[ConfigKey{ConfigKindKit, n}] = sourceKitPath(n)
		}
	}
	for _, n := range slices.Sorted(maps.Keys(tree.KitSources)) {
		ref := tree.KitSources[n]
		p.repo[ConfigKey{ConfigKindKit, n}] = true // a failed fetch is a refusal, never a delete
		if ref.Commit != "" {
			p.plan.KitSources = append(p.plan.KitSources, ConfigKitSource{Path: ref.Path, Source: ref.Source.String(), Commit: ref.Commit})
		}
	}
	if len(tree.KitSources) > 0 && !tree.KitsFetched {
		p.refuse("kits: the kit sources (kits/<name>.source.yml) were not fetched")
	}
	for _, e := range tree.Kits {
		key := ConfigKey{ConfigKindKit, e.Name}
		p.repo[key] = true
		if e.Group != "" {
			p.paths[key] = sourceKitPath(e.Name)
		} else {
			delete(p.paths, key)
		}
	}
	entries, err := resolveKitEntries(p.store, tree.Kits, nil)
	if err != nil {
		p.refuse("kits: %s", err)
		return
	}
	var docs []Kit
	for _, e := range entries {
		key := ConfigKey{ConfigKindKit, e.Name}
		at, track := p.kitProvenance(e)
		var tracked *KitSource
		if track {
			tracked = &at
		}
		repo := configObject{Key: key, Canon: kitCanon(e.text, tracked)}
		action, drift, ok := p.classify(key, repo)
		if !ok {
			continue
		}
		if doc, write := configKitDoc(p.store, e, at, track, p.in.Now); write {
			docs = append(docs, doc)
		}
		p.record(key, action, drift, repo)
	}
	for _, d := range docs {
		p.m.kits[d.Name] = copyKit(d)
	}
	p.w.Kits = docs
}

// kitProvenance is the KitSource entry e's new version records and whether
// e tracks it: a .source.yml kit's own repo, dir, ref and fetched commit
// (tracked); an inline kit's config source — its dir under the config dir,
// no ref or commit for --dir — untracked.
func (p *planner) kitProvenance(e kitSetEntry) (KitSource, bool) {
	if e.Group == "" {
		s := p.in.Source
		return KitSource{Repo: s.Repo, Dir: path.Join(s.Dir, e.Dir), Ref: s.Ref, Commit: provenanceCommit(p.in.Commit)}, false
	}
	g := p.in.Tree.KitGroups[e.Group]
	return KitSource{Repo: g.Source.Repo, Dir: e.Dir, Ref: g.Source.Ref, Commit: g.Commit}, true
}

// configKitDoc is entry e's new doc: a new version with provenance at when
// its config changed, and its tracked source set to at (track) or cleared
// (an inline kit is the config repo's, not tracked on its own). write=false
// when neither changes.
func configKitDoc(store Store, e kitSetEntry, at KitSource, track bool, now time.Time) (Kit, bool) {
	k, _ := store.GetKit(e.Name)
	doc := copyKit(k)
	doc.Name = e.Name
	write := false
	if !e.Unchanged {
		if doc.Meta == nil {
			doc.Meta = map[int]KitVersionMeta{}
		}
		doc.Versions[e.Version] = e.text
		doc.Meta[e.Version] = KitVersionMeta{PushedAt: now, Source: copyKitSource(&at), Parent: e.parent}
		doc.Current = e.Version
		write = true
	}
	var want *KitSource
	if track {
		want = copyKitSource(&at)
	}
	if !sameKitSource(doc.Source, want) {
		doc.Source = want
		write = true
	}
	return doc, write
}

// sameKitSource compares two optional sources by value.
func sameKitSource(a, b *KitSource) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/...`
Expected: PASS, including slice 1's `TestPlanCreatesInOrder` (`--dir` provenance `{local, kits/web-dev}`) and `TestPlanReapplyIsUnchanged`.

- [ ] **Step 5: Commit**

```bash
git add internal/jam/config_plan.go internal/jam/config_plan_test.go
git commit -m "feat(jam): plan .source.yml kits as managed, tracked kits; real kit provenance"
```

---

### Task 6: Config runs from git — apply, track, sync, untrack

**Files:**
- Create: `internal/jam/config_source.go`, `internal/jam/config_source_test.go`
- Modify: `internal/jam/config_apply.go`, `internal/jam/config_admin.go` (call sites only), `internal/jam/config_apply_test.go`, `internal/jam/config_roundtrip_test.go`

**Interfaces:**
- Consumes: `ConfigSource`, `GetConfigSource`/`SetConfigSource` (Task 1), `FetchConfigKits` (Task 3), `PlanConfig`, `KitFetcher`, `KitSourceStatus`, `writeErr`, `LoadConfigTree`; test helpers `repoFetcher` (Task 3), `cfgSrc` (Task 5), `newFakeFetcher`, `trackedFixture`, `mustWrite`, `dirOpts`, `planNow`, `allCreds`.
- Produces:
```go
const ConfigFetchTimeout = 4 * time.Minute
var ErrConfigNotTracked = errors.New("no config source is tracked (use config track --git <source>, or --dir)")
func ApplyConfigTree(ctx context.Context, store Store, f KitFetcher, tree *ConfigTree, env ConfigEnv, opt ConfigApplyOptions, log *slog.Logger, actor string) (ConfigApplyResult, error)
func ApplyConfigSource(ctx context.Context, store Store, f KitFetcher, src gitsource.Source /* zero = tracked */, env ConfigEnv, opt ConfigApplyOptions, log *slog.Logger, actor string) (ConfigApplyResult, error)
func TrackConfigSource(ctx context.Context, store Store, f KitFetcher, src gitsource.Source, env ConfigEnv, now time.Time, log *slog.Logger, actor string) (ConfigApplyResult, error)
func UntrackConfigSource(store Store, log *slog.Logger, actor string) error
func ConfigStatus(err error) (int, string) // 409 refused/stale/untracked; else KitSourceStatus with configStoreError for 500
func configSourceLabel(src gitsource.Source) string
// ConfigApplyResult gains: Source string `json:"source,omitempty"`; Commit string `json:"commit,omitempty"`
// test helpers (config_source_test.go), used by Task 7:
const cfgRepo = "https://github.com/acme/jam"
var cfgFiles map[string]string // cfg/users/bob.yml, cfg/kits/web/kit.yml, README.md
func cfgFetcher(t *testing.T, files map[string]string) (repoFetcher, *fakeFetcher)
```

- [ ] **Step 1: Write the failing tests**

Update slice 1's callers to the new signature first:

```bash
sed -i 's/ApplyConfigTree(\([a-z]*\), /ApplyConfigTree(context.Background(), \1, nil, /' internal/jam/config_apply_test.go internal/jam/config_roundtrip_test.go
```

Then add `"context"` to both files' imports. Create `internal/jam/config_source_test.go`:

```go
package jam

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/gitsource"
)

const cfgRepo = "https://github.com/acme/jam"

var cfgFiles = map[string]string{
	"cfg/users/bob.yml":    "kind: user\nversion: 1\n",
	"cfg/kits/web/kit.yml": "kind: studio\n",
	"README.md":            "not config",
}

// cfgFetcher serves the config repo cfgRepo at main = c1 holding files.
func cfgFetcher(t *testing.T, files map[string]string) (repoFetcher, *fakeFetcher) {
	t.Helper()
	cfg := newFakeFetcher(t, map[string]string{"main": "c1", "v1": "c1"}, map[string]map[string]string{"c1": files})
	return repoFetcher{cfgRepo: cfg}, cfg
}

func syncOpts() ConfigApplyOptions { return ConfigApplyOptions{Now: planNow} }

func TestTrackThenSyncThenUntrack(t *testing.T) {
	st := NewMemStore()
	f, _ := cfgFetcher(t, cfgFiles)
	ctx := context.Background()
	if _, err := ApplyConfigSource(ctx, st, f, gitsource.Source{}, allCreds, syncOpts(), nil, "alice"); !errors.Is(err, ErrConfigNotTracked) {
		t.Fatalf("sync without a tracked source: %v", err)
	}
	res, err := TrackConfigSource(ctx, st, f, cfgSrc, allCreds, planNow, nil, "alice")
	if err != nil || res.Applied || res.Commit != "c1" || res.Plan.Counts()[ConfigCreate] != 2 || len(st.ListUsers()) != 0 {
		t.Fatalf("track: %+v %v", res, err)
	}
	if got, ok := st.GetConfigSource(); !ok || got != (ConfigSource{Repo: cfgRepo, Dir: "cfg", Ref: "main"}) {
		t.Fatalf("tracked %+v", got)
	}
	res, err = ApplyConfigSource(ctx, st, f, gitsource.Source{}, allCreds, syncOpts(), nil, "alice")
	if err != nil || !res.Applied || res.Source != "https://github.com/acme/jam//cfg@main" || res.Commit != "c1" {
		t.Fatalf("sync: %+v %v", res, err)
	}
	if got, _ := st.GetConfigSource(); got.Commit != "c1" || !got.AppliedAt.Equal(planNow) {
		t.Fatalf("record %+v", got)
	}
	for _, mk := range st.ListConfigMarks() {
		if mk.Commit != "c1" {
			t.Fatalf("mark %+v: want the config commit", mk)
		}
	}
	k, _ := st.GetKit("web")
	if m := k.Meta[1].Source; m == nil || *m != (KitSource{Repo: cfgRepo, Dir: "cfg/kits/web", Ref: "main", Commit: "c1"}) {
		t.Fatalf("provenance %+v", m)
	}
	if _, err := TrackConfigSource(ctx, st, f, cfgSrc, allCreds, planNow, nil, "alice"); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetConfigSource(); got.Commit != "c1" {
		t.Fatalf("re-tracking the same source lost its last apply: %+v", got)
	}
	if err := UntrackConfigSource(st, nil, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.GetConfigSource(); ok || len(st.ListConfigMarks()) != 2 || len(st.ListUsers()) != 1 {
		t.Fatalf("untrack: marks=%d users=%d", len(st.ListConfigMarks()), len(st.ListUsers()))
	}
	if err := UntrackConfigSource(st, nil, "alice"); err != nil {
		t.Fatalf("a second untrack: %v", err)
	}
}

func TestSyncCommitOnlyMoveRecordsCommit(t *testing.T) {
	st := NewMemStore()
	f, cfg := cfgFetcher(t, cfgFiles)
	ctx := context.Background()
	if _, err := TrackConfigSource(ctx, st, f, cfgSrc, allCreds, planNow, nil, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyConfigSource(ctx, st, f, gitsource.Source{}, allCreds, syncOpts(), nil, "alice"); err != nil {
		t.Fatal(err)
	}
	moved := map[string]string{"README.md": "edited"}
	for p, c := range cfgFiles {
		if p != "README.md" {
			moved[p] = c
		}
	}
	cfg.commit("main", "c2", moved)
	var buf bytes.Buffer
	res, err := ApplyConfigSource(ctx, st, f, gitsource.Source{}, allCreds, syncOpts(), slog.New(slog.NewJSONHandler(&buf, nil)), "alice")
	if err != nil || res.Applied || ConfigChanges(res.Plan) != 0 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if got, _ := st.GetConfigSource(); got.Commit != "c2" {
		t.Fatalf("the tracked commit did not advance: %+v", got)
	}
	if buf.Len() != 0 {
		t.Fatalf("a no-change sync logged:\n%s", buf.String())
	}
	if mk := st.ListConfigMarks()[0]; mk.Commit != "c1" {
		t.Fatalf("an unchanged object's mark moved: %+v", mk)
	}
}

func TestApplyOtherGitSourceLeavesTrackedRecord(t *testing.T) {
	st := NewMemStore()
	f, _ := cfgFetcher(t, cfgFiles)
	ctx := context.Background()
	if _, err := TrackConfigSource(ctx, st, f, cfgSrc, allCreds, planNow, nil, "alice"); err != nil {
		t.Fatal(err)
	}
	other := gitsource.Source{Repo: cfgRepo, Dir: "cfg", Ref: "v1"}
	if res, err := ApplyConfigSource(ctx, st, f, other, allCreds, syncOpts(), nil, "alice"); err != nil || !res.Applied {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if got, _ := st.GetConfigSource(); got.Commit != "" || got.Ref != "main" {
		t.Fatalf("an apply of another source touched the tracked record: %+v", got)
	}
}

// badDirFetcher's checkout holds out → a directory outside it, and a file.
type badDirFetcher struct{ t *testing.T }

func (b badDirFetcher) Resolve(context.Context, gitsource.Source) (string, error) { return "c1", nil }

func (b badDirFetcher) Fetch(_ context.Context, _ gitsource.Source, fn func(root, commit string) error) error {
	root, outside := b.t.TempDir(), b.t.TempDir()
	mustWrite(b.t, filepath.Join(outside, "users/eve.yml"), "kind: user\nversion: 1\n", 0o644)
	if err := os.Symlink(outside, filepath.Join(root, "out")); err != nil {
		return err
	}
	mustWrite(b.t, filepath.Join(root, "file"), "x", 0o644)
	return fn(root, "c1")
}

func TestFetchConfigTreeRefusesBadDir(t *testing.T) {
	st := NewMemStore()
	for _, dir := range []string{"out", "file", "none"} {
		_, err := ApplyConfigSource(context.Background(), st, badDirFetcher{t}, gitsource.Source{Repo: cfgRepo, Dir: dir, Ref: "main"},
			allCreds, ConfigApplyOptions{DryRun: true, Now: planNow}, nil, "alice")
		if status, msg := ConfigStatus(err); status != http.StatusBadRequest || msg != "config source: "+dir+" is not a directory in the repo" {
			t.Errorf("%s: %d %q", dir, status, msg)
		}
	}
}

func TestConfigFetchFailureTracksNothing(t *testing.T) {
	st := NewMemStore()
	f, cfg := cfgFetcher(t, cfgFiles)
	cfg.failNext = fmt.Errorf("%w: auth failed for https://github.com/acme/", gitsource.ErrFetch)
	_, err := TrackConfigSource(context.Background(), st, f, cfgSrc, allCreds, planNow, nil, "alice")
	if status, msg := ConfigStatus(err); status != http.StatusBadGateway || !strings.Contains(msg, "auth failed") {
		t.Fatalf("%d %q", status, msg)
	}
	if _, ok := st.GetConfigSource(); ok {
		t.Fatal("a failed fetch tracked the source")
	}
}

func TestConfigAuditNeverLogsUserinfo(t *testing.T) {
	var buf bytes.Buffer
	logConfigApply(slog.New(slog.NewJSONHandler(&buf, nil)), "alice",
		ConfigApplyOptions{Source: gitsource.Source{Repo: "https://tok3n@github.com/acme/jam", Ref: "main"}, Commit: "c1"}, ConfigPlan{})
	if strings.Contains(buf.String(), "tok3n") || !strings.Contains(buf.String(), `"source":"invalid"`) {
		t.Fatalf("audit:\n%s", buf.String())
	}
	if got := configSourceLabel(cfgSrc); got != "https://github.com/acme/jam//cfg@main" {
		t.Fatal(got)
	}
	if got := configSourceLabel(gitsource.Source{Repo: ConfigDirRepo}); got != "dir" {
		t.Fatal(got)
	}
}

func TestConcurrentSyncsAndKitPush(t *testing.T) {
	st := NewMemStore()
	f, _ := cfgFetcher(t, cfgFiles)
	ctx := context.Background()
	if _, err := TrackConfigSource(ctx, st, f, cfgSrc, allCreds, planNow, nil, "alice"); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		var wg sync.WaitGroup
		wg.Go(func() {
			for i := range 30 {
				_, _, _ = PushStudioKit(st, "other", fmt.Sprintf("kind: studio\nprompt: p%d\n", i))
			}
		})
		for range 4 {
			wg.Go(func() { _, _ = ApplyConfigSource(ctx, st, f, gitsource.Source{}, allCreds, syncOpts(), nil, "alice") })
		}
		wg.Wait()
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("deadlock between config syncs and a kit push")
	}
	if k, _ := st.GetKit("web"); len(k.Versions) != 1 {
		t.Fatalf("racing syncs wrote %d versions of web", len(k.Versions))
	}
	if res, err := ApplyConfigSource(ctx, st, f, gitsource.Source{}, allCreds, syncOpts(), nil, "alice"); err != nil || res.Applied {
		t.Fatalf("a later sync is not a no-op: %+v %v", res, err)
	}
}

// TestConfigRoundTripWithSourceKit: a Jam whose kit tracks its own source
// writes it as kits/<n>.source.yml; applying that tree onto an empty Jam
// fetches and tracks the kit, and the replica writes the same tree.
func TestConfigRoundTripWithSourceKit(t *testing.T) {
	src, kits := trackedFixture(t)
	f := repoFetcher{kitsRepo: kits}
	exp, err := ExportConfigTree(src.ExportConfig(), nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for p, body := range exp.Files {
		mustWrite(t, filepath.Join(dir, filepath.FromSlash(p)), body, 0o644)
	}
	tr, err := LoadConfigTree(dir)
	if err != nil || len(tr.Refusals) > 0 {
		t.Fatalf("%v %v", err, tr.Refusals)
	}
	dst := NewMemStore()
	if res, err := ApplyConfigTree(context.Background(), dst, f, tr, allCreds, dirOpts(), nil, "local"); err != nil || !res.Applied {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	got, _ := ExportConfigTree(dst.ExportConfig(), nil, nil, true)
	if !reflect.DeepEqual(got.Files, exp.Files) {
		t.Fatalf("replica tree differs:\n%v\n---\n%v", got.Files, exp.Files)
	}
	if k, _ := dst.GetKit("web-dev"); k.Source == nil || *k.Source != (KitSource{Repo: kitsRepo, Dir: "web", Ref: "main", Commit: "c1"}) {
		t.Fatalf("tracked %+v", k.Source)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/ -run 'TestTrack|TestSync|TestApply|TestFetchConfigTree|TestConfig'`
Expected: FAIL to compile (`undefined: ApplyConfigSource`, `too many arguments in call to ApplyConfigTree`).

- [ ] **Step 3: Implement**

In `internal/jam/config_apply.go`:
- Add `"context"` to the imports.
- Add the two fields to `ConfigApplyResult`:

```go
	Source  string         `json:"source,omitempty"` // "dir", or the git source planned
	Commit  string         `json:"commit,omitempty"` // the config commit planned; "" for --dir
```

- Change the `Source` comment in `ConfigApplyOptions` to `// {Repo: ConfigDirRepo} for --dir; the git source for --git (set by ApplyConfigSource)`.
- Replace `ApplyConfigTree` with:

```go
// ApplyConfigTree plans tree against store — fetching its .source.yml kits
// with f first (f may be nil when it has none) — and, unless opt.DryRun,
// writes the whole plan in one Store.ApplyConfig call. A plan with refusals
// is ErrConfigRefused, a plan whose digest is not opt.WantDigest is
// ErrConfigPlanChanged; either writes nothing. configMu is held from the
// first fetch to the write, kitMu only for the plan and the write, and no
// kit lock at all: no kit push lands between plan and write, and two config
// runs never interleave. actor is the operator the audit log names; log may
// be nil.
func ApplyConfigTree(ctx context.Context, store Store, f KitFetcher, tree *ConfigTree, env ConfigEnv, opt ConfigApplyOptions, log *slog.Logger, actor string) (ConfigApplyResult, error) {
	ctx, cancel := context.WithTimeout(ctx, ConfigFetchTimeout)
	defer cancel()
	configMu.Lock()
	defer configMu.Unlock()
	return applyConfigTreeLocked(ctx, store, f, tree, env, opt, log, actor)
}

// applyConfigTreeLocked is ApplyConfigTree for a caller holding configMu.
func applyConfigTreeLocked(ctx context.Context, store Store, f KitFetcher, tree *ConfigTree, env ConfigEnv, opt ConfigApplyOptions, log *slog.Logger, actor string) (ConfigApplyResult, error) {
	if tree == nil {
		tree = &ConfigTree{}
	}
	FetchConfigKits(ctx, f, tree) // holding configMu only: kitMu is never held across a fetch
	kitMu.Lock()                  // configMu → kitMu → store mu, as every kit writer takes kitMu before the store
	defer kitMu.Unlock()
	plan := PlanConfig(ConfigPlanInput{Tree: tree, Snapshot: store.ExportConfig(), Instances: store.ListInstances(),
		Marks: store.ListConfigMarks(), Env: env, Source: opt.Source, Commit: opt.Commit, Force: opt.Force, Now: opt.Now})
	res := ConfigApplyResult{Plan: plan, Source: configSourceLabel(opt.Source), Commit: provenanceCommit(opt.Commit)}
	if opt.DryRun {
		return res, nil
	}
	if len(plan.Refusals) > 0 {
		return res, fmt.Errorf("%w:\n%s", ErrConfigRefused, strings.Join(plan.Refusals, "\n"))
	}
	if opt.WantDigest != "" && opt.WantDigest != plan.Digest {
		return res, ErrConfigPlanChanged
	}
	res.Counts = plan.Counts()
	changes := planChanges(plan)
	w := plan.write
	w.ConfigSource = trackedRecord(store, opt, changes)
	if !changes {
		// Nothing to apply; still record the tracked commits that moved (the
		// .source.yml kits' and the config source's): no version, mark or
		// audit entry.
		if meta := (ConfigWrite{Kits: w.Kits, ConfigSource: w.ConfigSource}); !meta.Empty() {
			if err := store.ApplyConfig(meta); err != nil {
				return res, fmt.Errorf("config apply: write: %w", err)
			}
		}
		return res, nil
	}
	if err := store.ApplyConfig(w); err != nil {
		return res, fmt.Errorf("config apply: write: %w", err)
	}
	res.Applied = true
	logConfigApply(log, actor, opt, plan)
	return res, nil
}
```

- In `logConfigApply`, replace the `source := …` lines with `source := configSourceLabel(opt.Source)`.

Create `internal/jam/config_source.go`:

```go
package jam

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/jam/gitsource"
)

// ConfigFetchTimeout bounds one config run's git fetches together: the
// config repo's and every kits/<name>.source.yml checkout.
const ConfigFetchTimeout = 4 * time.Minute

// ErrConfigNotTracked is a sync, or a diff/apply naming no source, on a Jam
// with no tracked config source.
var ErrConfigNotTracked = errors.New("no config source is tracked (use config track --git <source>, or --dir)")

// ApplyConfigSource fetches the config repo src (zero = the tracked source,
// else ErrConfigNotTracked), loads its tree at src.Dir, and plans or applies
// it as ApplyConfigTree does, with the fetched commit on the marks and the
// kit versions. An apply from the tracked source records its commit and
// time in the same write. configMu is held across every fetch.
func ApplyConfigSource(ctx context.Context, store Store, f KitFetcher, src gitsource.Source, env ConfigEnv, opt ConfigApplyOptions, log *slog.Logger, actor string) (ConfigApplyResult, error) {
	ctx, cancel := context.WithTimeout(ctx, ConfigFetchTimeout)
	defer cancel()
	configMu.Lock()
	defer configMu.Unlock()
	return applyConfigSourceLocked(ctx, store, f, src, env, opt, log, actor)
}

// TrackConfigSource fetches and plans src (a dry run), then records it as
// the Jam's one config source and returns the plan. Re-tracking the same
// source keeps its last commit and time; another source replaces it. A
// fetch or load failure records nothing; a plan with refusals is still
// recorded (the operator fixes the repo, then syncs).
func TrackConfigSource(ctx context.Context, store Store, f KitFetcher, src gitsource.Source, env ConfigEnv, now time.Time, log *slog.Logger, actor string) (ConfigApplyResult, error) {
	if src.Repo == "" {
		return ConfigApplyResult{}, writeErr(http.StatusBadRequest, "config track: a source is required (<repo>[//<dir>]@<ref>)")
	}
	ctx, cancel := context.WithTimeout(ctx, ConfigFetchTimeout)
	defer cancel()
	configMu.Lock()
	defer configMu.Unlock()
	res, err := applyConfigSourceLocked(ctx, store, f, src, env, ConfigApplyOptions{DryRun: true, Now: now}, log, actor)
	if err != nil {
		return res, err
	}
	rec := ConfigSource{Repo: src.Repo, Dir: src.Dir, Ref: src.Ref}
	if cur, ok := store.GetConfigSource(); ok && cur.Source() == src {
		rec = cur
	}
	if err := store.SetConfigSource(&rec); err != nil {
		return res, fmt.Errorf("config track: %w", err)
	}
	if log != nil {
		log.Info("config tracked", "operator", actor, "source", configSourceLabel(src), "commit", res.Commit)
	}
	return res, nil
}

// UntrackConfigSource forgets the tracked source; objects and their marks
// stay. Untracking with none tracked is a no-op.
func UntrackConfigSource(store Store, log *slog.Logger, actor string) error {
	configMu.Lock()
	defer configMu.Unlock()
	cur, ok := store.GetConfigSource()
	if !ok {
		return nil
	}
	if err := store.SetConfigSource(nil); err != nil {
		return fmt.Errorf("config untrack: %w", err)
	}
	if log != nil {
		log.Info("config untracked", "operator", actor, "source", configSourceLabel(cur.Source()))
	}
	return nil
}

// applyConfigSourceLocked is ApplyConfigSource for a caller holding configMu.
func applyConfigSourceLocked(ctx context.Context, store Store, f KitFetcher, src gitsource.Source, env ConfigEnv, opt ConfigApplyOptions, log *slog.Logger, actor string) (ConfigApplyResult, error) {
	if src.Repo == "" {
		cur, ok := store.GetConfigSource()
		if !ok {
			return ConfigApplyResult{}, ErrConfigNotTracked
		}
		src = cur.Source()
	}
	if f == nil {
		return ConfigApplyResult{}, errors.New("config: no git fetcher is configured")
	}
	tree, commit, err := fetchConfigTree(ctx, f, src)
	if err != nil {
		return ConfigApplyResult{}, err
	}
	opt.Source, opt.Commit = src, commit
	return applyConfigTreeLocked(ctx, store, f, tree, env, opt, log, actor)
}

// fetchConfigTree checks src out and loads the tree at src.Dir inside the
// checkout; the tree is in memory before the checkout is removed. Kit
// sources are fetched later, outside this fetch (the fetcher serializes per
// repo, and a .source.yml may name this repo).
func fetchConfigTree(ctx context.Context, f KitFetcher, src gitsource.Source) (*ConfigTree, string, error) {
	var tree *ConfigTree
	var commit string
	err := f.Fetch(ctx, src, func(root, c string) error {
		dir, err := configCheckoutDir(root, src.Dir)
		if err != nil {
			return err
		}
		t, err := LoadConfigTree(dir)
		if err != nil {
			return writeErr(http.StatusBadRequest, "config source: the checkout cannot be read")
		}
		tree, commit = t, c
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	return tree, commit, nil
}

// configCheckoutDir is root/dir when every component of dir is a real
// directory in the checkout: a symlink could lead out of it. dir passed
// gitsource.Parse (clean, relative), so the refusal may name it.
func configCheckoutDir(root, dir string) (string, error) {
	cur := root
	if dir == "" {
		return cur, nil
	}
	for part := range strings.SplitSeq(dir, "/") {
		cur = filepath.Join(cur, part)
		if fi, err := os.Lstat(cur); err != nil || !fi.IsDir() {
			return "", writeErr(http.StatusBadRequest, "config source: %s is not a directory in the repo", dir)
		}
	}
	return cur, nil
}

// configSourceLabel is how logs and results name a run's source: "dir" for
// an upload, else the source as gitsource.Parse renders it. A source that
// does not re-parse (never one the API built — say, a hand-made URL with
// userinfo) is "invalid", so no log line can carry a credential.
func configSourceLabel(src gitsource.Source) string {
	switch src.Repo {
	case ConfigDirRepo:
		return "dir"
	case "":
		return ""
	}
	if _, err := gitsource.Parse(src.String()); err != nil {
		return "invalid"
	}
	return src.String()
}

// trackedRecord is the tracked-source record an apply of opt writes, nil =
// none: an apply from the tracked source (same repo, dir and ref) records
// its commit and time when the commit moved or the apply changed something.
func trackedRecord(store Store, opt ConfigApplyOptions, changes bool) *ConfigSource {
	cur, ok := store.GetConfigSource()
	if !ok || cur.Source() != (gitsource.Source{Repo: opt.Source.Repo, Dir: opt.Source.Dir, Ref: opt.Source.Ref}) {
		return nil
	}
	if !changes && cur.Commit == opt.Commit {
		return nil
	}
	cur.Commit, cur.AppliedAt = opt.Commit, opt.Now
	return &cur
}
```

Add `ConfigStatus` to `internal/jam/config_admin.go`. Then change its two `ApplyConfigTree(store, tr, …)` calls to `ApplyConfigTree(r.Context(), store, nil, tr, …)`; Task 7 rewrites this file.

```go
// ConfigStatus maps a config diff, apply, track or sync error to an HTTP
// status and a message safe to show: a refused or stale plan and an
// untracked Jam 409; a bad source, kit or checkout 400; a fetch, auth or
// limit failure 502 with the fetcher's redacted message; anything else 500
// with a generic message (log the error itself).
func ConfigStatus(err error) (int, string) {
	switch {
	case errors.Is(err, ErrConfigRefused), errors.Is(err, ErrConfigPlanChanged), errors.Is(err, ErrConfigNotTracked):
		return http.StatusConflict, err.Error()
	}
	status, msg := KitSourceStatus(err)
	if status == http.StatusInternalServerError {
		msg = configStoreError
	}
	return status, msg
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `GOPROXY=https://proxy.golang.org,direct go test -race ./internal/jam/...`
Expected: PASS (slice 1's apply tests unchanged in behavior; `TestApplyConfigTreeAudit` still sees `"source":"dir"`).

- [ ] **Step 5: Commit**

```bash
git add internal/jam/config_source.go internal/jam/config_source_test.go internal/jam/config_apply.go internal/jam/config_admin.go internal/jam/config_apply_test.go internal/jam/config_roundtrip_test.go
git commit -m "feat(jam): apply, track, sync and untrack a config source from git"
```

---

### Task 7: Admin routes and client methods

**Files:**
- Modify: `internal/jam/config_admin.go`, `internal/jam/adminclient/adminclient.go`, `cmd/at-jam/main.go` (the `WithConfig` call)
- Modify (callers): `cmd/at-jam/configgit_test.go`, `internal/jam/adminclient/config_test.go`
- Test: `internal/jam/config_admin_test.go`, `internal/jam/adminclient/config_test.go`

**Interfaces:**
- Consumes: `ApplyConfigTree`, `ApplyConfigSource`, `TrackConfigSource`, `UntrackConfigSource`, `ConfigStatus` (Task 6), `decode`, `writeJSON`, `OperatorID`; test helpers `cfgFetcher`, `cfgFiles` (Task 6), `repoFetcher` (Task 3), `opOf`.
- Produces:
```go
type ConfigSourceBody struct{ Source string `json:"source,omitempty"` }
func WithConfig(store Store, f KitFetcher, env ConfigEnv, log *slog.Logger) AdminOption
// adminclient:
func (c *Client) ConfigDiffSource(source string, force bool) (jam.ConfigApplyResult, error)            // "" = tracked
func (c *Client) ConfigApplySource(source string, force bool, planDigest string) (jam.ConfigApplyResult, error)
func (c *Client) ConfigTrack(source string) (jam.ConfigApplyResult, error)
func (c *Client) ConfigUntrack() error
func (c *Client) ConfigSync(force bool, planDigest string) (jam.ConfigApplyResult, error)
```

- [ ] **Step 1: Write the failing tests**

In `internal/jam/config_admin_test.go`, change `configServer` and add helpers:

```go
func configServer(t *testing.T, st *MemStore) *httptest.Server {
	t.Helper()
	return configServerWith(t, st, nil)
}

func configServerWith(t *testing.T, st *MemStore, f KitFetcher) *httptest.Server {
	t.Helper()
	log := slog.New(slog.DiscardHandler)
	ts := httptest.NewServer(NewAdminHandler(st, nil, nil, LoopbackAuthenticator{}, allCreds.CredExists, nil, log, nil, nil,
		WithConfig(st, f, allCreds, log)))
	t.Cleanup(ts.Close)
	return ts
}

// sendJSON sends body (as application/json unless empty), checks the status,
// decodes the answer into out when non-nil, and returns the body text.
func sendJSON(t *testing.T, method, url, body string, want int, out any) string {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		t.Fatalf("%s %s = %d, want %d: %s", method, url, resp.StatusCode, want, b)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("%s %s JSON: %v (%s)", method, url, err, b)
		}
	}
	return string(b)
}
```

Replace the end of `TestConfigRoutes`, from `resp, err := http.Post(ts.URL+"/admin/config/diff", "application/json", …` through its `Fatalf`, with:

```go
	if msg := sendJSON(t, "POST", ts.URL+"/admin/config/diff", `{}`, http.StatusConflict, nil); !strings.Contains(msg, "no config source is tracked") {
		t.Fatalf("json body without a tracked source: %s", msg)
	}
```

Append:

```go
func TestConfigSourceRoutes(t *testing.T) {
	st := NewMemStore()
	f, cfg := cfgFetcher(t, cfgFiles)
	ts := configServerWith(t, st, f)
	const src = `{"source":"https://github.com/acme/jam//cfg@main"}`
	var tracked ConfigApplyResult
	sendJSON(t, "PUT", ts.URL+"/admin/config/source", src, http.StatusOK, &tracked)
	if tracked.Commit != "c1" || tracked.Applied || opOf(tracked.Plan, ConfigKindUser, "bob").Action != ConfigCreate || len(st.ListUsers()) != 0 {
		t.Fatalf("track: %+v", tracked)
	}
	var diff ConfigApplyResult
	sendJSON(t, "POST", ts.URL+"/admin/config/diff", `{}`, http.StatusOK, &diff)
	sendJSON(t, "POST", ts.URL+"/admin/config/sync?plan=stale", `{}`, http.StatusConflict, nil)
	var res ConfigApplyResult
	sendJSON(t, "POST", ts.URL+"/admin/config/sync?plan="+diff.Plan.Digest, `{}`, http.StatusOK, &res)
	if !res.Applied || len(st.ListUsers()) != 1 || res.Source != "https://github.com/acme/jam//cfg@main" {
		t.Fatalf("sync: %+v", res)
	}
	if got, _ := st.GetConfigSource(); got.Commit != "c1" {
		t.Fatalf("record %+v", got)
	}
	// sync needs a JSON body: a browser's no-cors POST cannot send one.
	resp, err := http.Post(ts.URL+"/admin/config/sync", "text/plain", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("text/plain sync = %d", resp.StatusCode)
	}
	sendJSON(t, "POST", ts.URL+"/admin/config/apply", src, http.StatusOK, &res)
	if res.Applied {
		t.Fatalf("re-apply wrote: %+v", res)
	}
	sendJSON(t, "DELETE", ts.URL+"/admin/config/source", "", http.StatusNoContent, nil)
	if _, ok := st.GetConfigSource(); ok || len(st.ListConfigMarks()) == 0 {
		t.Fatal("untrack kept the source or dropped the marks")
	}
	sendJSON(t, "POST", ts.URL+"/admin/config/sync", `{}`, http.StatusConflict, nil)
	sendJSON(t, "PUT", ts.URL+"/admin/config/source", `{}`, http.StatusBadRequest, nil)
	cfg.failNext = fmt.Errorf("%w: repository https://github.com/acme/jam not found", gitsource.ErrFetch)
	if msg := sendJSON(t, "POST", ts.URL+"/admin/config/diff", src, http.StatusBadGateway, nil); !strings.Contains(msg, "not found") {
		t.Fatalf("fetch failure: %s", msg)
	}
}

// A pasted clone URL with a token is refused on every route that takes a
// source, and the token reaches neither an answer nor a log line.
func TestConfigSourceRoutesRefuseCredentialURL(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	st := NewMemStore()
	ts := httptest.NewServer(NewAdminHandler(st, nil, nil, LoopbackAuthenticator{}, allCreds.CredExists, nil, log, nil, nil,
		WithConfig(st, repoFetcher{}, allCreds, log)))
	defer ts.Close()
	const token = "ghp_s3cretTOKEN"
	body := `{"source":"https://x-access-token:` + token + `@github.com/acme/jam@main"}`
	for _, route := range []struct{ method, path string }{{"PUT", "/admin/config/source"}, {"POST", "/admin/config/diff"}, {"POST", "/admin/config/apply"}} {
		if msg := sendJSON(t, route.method, ts.URL+route.path, body, http.StatusBadRequest, nil); strings.Contains(msg, token) {
			t.Errorf("%s %s echoes the token: %s", route.method, route.path, msg)
		}
	}
	if strings.Contains(logs.String(), token) {
		t.Fatalf("a log line carries the token:\n%s", logs.String())
	}
	if _, ok := st.GetConfigSource(); ok {
		t.Fatal("a refused source was tracked")
	}
}
```

(Add `"fmt"` and `"github.com/aethons-tools/cove/internal/jam/gitsource"` to the imports.)

In `internal/jam/adminclient/config_test.go`, change the existing `jam.WithConfig(store, jam.ConfigEnv{…}, log)` to `jam.WithConfig(store, nil, jam.ConfigEnv{CredExists: creds}, log)`, and append:

```go
func TestConfigSourceMethods(t *testing.T) {
	repo, _, _ := gittest.Serve(t, map[string]string{"users/bob.yml": "kind: user\nversion: 1\n"})
	store := jam.NewMemStore()
	log := slog.New(slog.DiscardHandler)
	creds := func(string) bool { return true }
	ts := httptest.NewServer(jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, creds, nil, log, nil, nil,
		jam.WithConfig(store, &gitsource.Fetcher{}, jam.ConfigEnv{CredExists: creds}, log)))
	defer ts.Close()
	c := New(ts.URL, "")
	src := repo + "@main"
	if _, err := c.ConfigDiffSource("", false); !errors.Is(err, ErrConflict) {
		t.Fatalf("diff with nothing tracked = %v, want ErrConflict", err)
	}
	tr, err := c.ConfigTrack(src)
	if err != nil || tr.Commit == "" || tr.Applied {
		t.Fatalf("ConfigTrack = %+v, %v", tr, err)
	}
	diff, err := c.ConfigDiffSource("", false)
	if err != nil || diff.Plan.Ops[0].Action != jam.ConfigCreate {
		t.Fatalf("ConfigDiffSource = %+v, %v", diff, err)
	}
	if res, err := c.ConfigSync(false, diff.Plan.Digest); err != nil || !res.Applied {
		t.Fatalf("ConfigSync = %+v, %v", res, err)
	}
	if res, err := c.ConfigApplySource(src, false, ""); err != nil || res.Applied {
		t.Fatalf("ConfigApplySource = %+v, %v", res, err)
	}
	if err := c.ConfigUntrack(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ConfigSync(false, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("sync after untrack = %v, want ErrConflict", err)
	}
}
```

(Add the imports `"github.com/aethons-tools/cove/internal/jam/gitsource"` and `".../gitsource/gittest"`.) In `cmd/at-jam/configgit_test.go`, change `jam.WithConfig(st, jam.ConfigEnv{CredExists: creds}, log)` to `jam.WithConfig(st, nil, jam.ConfigEnv{CredExists: creds}, log)`.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/ ./internal/jam/adminclient/ -run 'TestConfig'`
Expected: FAIL to compile (`too many arguments in call to WithConfig`, `c.ConfigTrack undefined`).

- [ ] **Step 3: Implement**

Replace `WithConfig` in `internal/jam/config_admin.go` (keep `configStoreError`, `ConfigStatus`) with:

```go
// ConfigSourceBody is the JSON body of POST /admin/config/diff and /apply
// (Source "" = the tracked source), PUT /admin/config/source and POST
// /admin/config/sync ({}).
type ConfigSourceBody struct {
	Source string `json:"source,omitempty"`
}

// maxConfigSourceBody caps a {source} JSON body.
const maxConfigSourceBody = 64 << 10

// WithConfig mounts the config-in-git admin API (under the /admin/* operator
// gate):
//
//	POST   /admin/config/diff?force=1                plan a tree or a source (writes nothing)
//	POST   /admin/config/apply?force=1&plan=<digest> apply it
//	PUT    /admin/config/source                      {source}: fetch and plan it, then track it
//	DELETE /admin/config/source                      untrack (objects and marks stay)
//	POST   /admin/config/sync?force=1&plan=<digest>  {}: apply the tracked source
//	GET    /admin/config/export-tree?all=1           render the managed (all: every) object
//
// diff and apply take a configtar body (application/gzip) or a {source}
// body (application/json; "" = the tracked source); sync takes {} — any
// other Content-Type is 415, so a browser's no-cors POST cannot trigger
// one. An oversize upload is 413, a malformed or unsafe one or a bad source
// 400, before anything is fetched or loaded. A refused or stale plan, or no
// tracked source, is 409; a fetch, auth or limit failure 502 with the
// fetcher's redacted message. f is the serve's one kit fetcher (kits.git
// credentials); it fetches config repos and kits/<name>.source.yml kits.
// env.Serve is the running serve's portable config.
func WithConfig(store Store, f KitFetcher, env ConfigEnv, log *slog.Logger) AdminOption {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	fail := func(w http.ResponseWriter, r *http.Request, what string, err error) {
		status, msg := ConfigStatus(err)
		if status == http.StatusInternalServerError {
			log.Error("admin config "+what+": store error", "operator", OperatorID(r), "err", err.Error())
		}
		http.Error(w, msg, status)
	}
	// upload reads and unpacks a configtar body into a temp dir and loads it,
	// answering the request itself on failure.
	upload := func(w http.ResponseWriter, r *http.Request) (*ConfigTree, bool) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, configtar.MaxUpload))
		if err != nil {
			if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
				http.Error(w, "config tree too large (limit 16 MiB)", http.StatusRequestEntityTooLarge)
				return nil, false
			}
			http.Error(w, "config tree: reading the upload failed", http.StatusBadRequest)
			return nil, false
		}
		dir, err := os.MkdirTemp("", "jam-config-")
		if err != nil {
			log.Error("admin config: temp dir", "operator", OperatorID(r), "err", err.Error())
			http.Error(w, configStoreError, http.StatusInternalServerError)
			return nil, false
		}
		defer os.RemoveAll(dir)
		if err := configtar.Unpack(bytes.NewReader(body), dir); err != nil {
			if errors.Is(err, configtar.ErrTooLarge) {
				http.Error(w, "config tree too large (limit 16 MiB)", http.StatusRequestEntityTooLarge)
			} else {
				http.Error(w, err.Error(), http.StatusBadRequest)
			}
			return nil, false
		}
		tr, err := LoadConfigTree(dir)
		if err != nil {
			log.Error("admin config: load", "operator", OperatorID(r), "err", err.Error())
			http.Error(w, configStoreError, http.StatusInternalServerError)
			return nil, false
		}
		return tr, true
	}
	// source decodes a {source} body ("" = the tracked one, unless required)
	// and parses it; Parse's errors never quote the input. It answers 400 itself.
	source := func(w http.ResponseWriter, r *http.Request, required bool) (gitsource.Source, bool) {
		var b ConfigSourceBody
		r.Body = http.MaxBytesReader(w, r.Body, maxConfigSourceBody)
		if !decode(w, r, &b) {
			return gitsource.Source{}, false
		}
		if b.Source == "" {
			if required {
				http.Error(w, "source is required (<repo>[//<dir>]@<ref>)", http.StatusBadRequest)
				return gitsource.Source{}, false
			}
			return gitsource.Source{}, true
		}
		src, err := gitsource.Parse(b.Source)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return gitsource.Source{}, false
		}
		return src, true
	}
	isJSON := func(r *http.Request) bool {
		ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		return ct == "application/json"
	}
	// run plans (opt.DryRun) or applies the request's tree or source and answers.
	run := func(w http.ResponseWriter, r *http.Request, what string, opt ConfigApplyOptions) {
		var res ConfigApplyResult
		var err error
		switch ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); ct {
		case "application/gzip":
			tr, ok := upload(w, r)
			if !ok {
				return
			}
			opt.Source, opt.Commit = gitsource.Source{Repo: ConfigDirRepo}, ConfigDirCommit
			res, err = ApplyConfigTree(r.Context(), store, f, tr, env, opt, log, OperatorID(r))
		case "application/json":
			src, ok := source(w, r, false)
			if !ok {
				return
			}
			res, err = ApplyConfigSource(r.Context(), store, f, src, env, opt, log, OperatorID(r))
		default: // e.g. a browser's no-cors text/plain POST to the loopback admin port
			http.Error(w, "config tree: Content-Type must be application/gzip (a tree) or application/json (a source)", http.StatusUnsupportedMediaType)
			return
		}
		if err != nil {
			fail(w, r, what, err)
			return
		}
		writeJSON(w, http.StatusOK, res)
	}
	return func(mux *http.ServeMux) {
		mux.HandleFunc("POST /admin/config/diff", func(w http.ResponseWriter, r *http.Request) {
			// force plans as a forced apply would, so the digests match.
			run(w, r, "diff", ConfigApplyOptions{DryRun: true, Force: r.URL.Query().Get("force") == "1", Now: time.Now()})
		})
		mux.HandleFunc("POST /admin/config/apply", func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			run(w, r, "apply", ConfigApplyOptions{Force: q.Get("force") == "1", WantDigest: q.Get("plan"), Now: time.Now()})
		})
		mux.HandleFunc("POST /admin/config/sync", func(w http.ResponseWriter, r *http.Request) {
			if !isJSON(r) {
				http.Error(w, "config sync: Content-Type must be application/json", http.StatusUnsupportedMediaType)
				return
			}
			q := r.URL.Query()
			res, err := ApplyConfigSource(r.Context(), store, f, gitsource.Source{}, env,
				ConfigApplyOptions{Force: q.Get("force") == "1", WantDigest: q.Get("plan"), Now: time.Now()}, log, OperatorID(r))
			if err != nil {
				fail(w, r, "sync", err)
				return
			}
			writeJSON(w, http.StatusOK, res)
		})
		mux.HandleFunc("PUT /admin/config/source", func(w http.ResponseWriter, r *http.Request) {
			src, ok := source(w, r, true)
			if !ok {
				return
			}
			res, err := TrackConfigSource(r.Context(), store, f, src, env, time.Now(), log, OperatorID(r))
			if err != nil {
				fail(w, r, "track", err)
				return
			}
			writeJSON(w, http.StatusOK, res)
		})
		mux.HandleFunc("DELETE /admin/config/source", func(w http.ResponseWriter, r *http.Request) {
			if err := UntrackConfigSource(store, log, OperatorID(r)); err != nil {
				fail(w, r, "untrack", err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		})
		mux.HandleFunc("GET /admin/config/export-tree", func(w http.ResponseWriter, r *http.Request) {
			exp, err := ExportConfigTree(store.ExportConfig(), store.ListConfigMarks(), env.Serve, r.URL.Query().Get("all") == "1")
			if errors.Is(err, ErrConfigKitPinBehind) {
				http.Error(w, err.Error(), http.StatusConflict) // kit names and versions only
				return
			}
			if err != nil {
				log.Error("admin config export-tree: store error", "operator", OperatorID(r), "err", err.Error())
				http.Error(w, configStoreError, http.StatusInternalServerError)
				return
			}
			writeJSON(w, http.StatusOK, exp)
		})
	}
}
```

In `internal/jam/adminclient/adminclient.go`, add after `gitTimeout`:

```go
// configGitTimeout bounds a config call that may fetch a config repo and its
// kit sources (jam.ConfigFetchTimeout plus the plan).
const configGitTimeout = 5 * time.Minute

// doConfig is do with the config-fetch timeout.
func (c *Client) doConfig(method, path string, body any, out any) error {
	hc := *c.httpc // keeps the Transport
	hc.Timeout = configGitTimeout
	return c.doWith(&hc, method, path, body, out)
}

// configPath is a config route with its force and plan query.
func configPath(path string, force bool, planDigest string) string {
	q := url.Values{}
	if force {
		q.Set("force", "1")
	}
	if planDigest != "" {
		q.Set("plan", planDigest)
	}
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	return path
}
```

Change `doRaw`'s `hc.Timeout = gitTimeout` to `hc.Timeout = configGitTimeout`, since a tree's `.source.yml` kits are fetched too, and update its comment. Rewrite `ConfigDiff` and `ConfigApply` to use `configPath`, with the same behavior: `c.doRaw("POST", configPath("/admin/config/diff", force, ""), "application/gzip", tree, &res)` and `configPath("/admin/config/apply", force, planDigest)`. Add:

```go
// ConfigDiffSource plans a git config source ("" = the tracked one) without
// writing; force plans as a forced apply would.
func (c *Client) ConfigDiffSource(source string, force bool) (jam.ConfigApplyResult, error) {
	var res jam.ConfigApplyResult
	err := c.doConfig("POST", configPath("/admin/config/diff", force, ""), jam.ConfigSourceBody{Source: source}, &res)
	return res, err
}

// ConfigApplySource applies a git config source ("" = the tracked one); a
// different plan than planDigest, or one with refusals, is ErrConflict.
func (c *Client) ConfigApplySource(source string, force bool, planDigest string) (jam.ConfigApplyResult, error) {
	var res jam.ConfigApplyResult
	err := c.doConfig("POST", configPath("/admin/config/apply", force, planDigest), jam.ConfigSourceBody{Source: source}, &res)
	return res, err
}

// ConfigTrack records source as the Jam's config source and returns its diff.
func (c *Client) ConfigTrack(source string) (jam.ConfigApplyResult, error) {
	var res jam.ConfigApplyResult
	err := c.doConfig("PUT", "/admin/config/source", jam.ConfigSourceBody{Source: source}, &res)
	return res, err
}

// ConfigUntrack forgets the tracked config source (objects and marks stay).
func (c *Client) ConfigUntrack() error {
	return c.do("DELETE", "/admin/config/source", nil, nil)
}

// ConfigSync applies the tracked config source (ErrConflict when none, or
// for a stale planDigest or refusals).
func (c *Client) ConfigSync(force bool, planDigest string) (jam.ConfigApplyResult, error) {
	var res jam.ConfigApplyResult
	err := c.doConfig("POST", configPath("/admin/config/sync", force, planDigest), jam.ConfigSourceBody{}, &res)
	return res, err
}
```

In `cmd/at-jam/main.go`, change `jam.WithConfig(st, jam.ConfigEnv{…}, log)` to `jam.WithConfig(st, kitFetcher, jam.ConfigEnv{CredExists: credExists, PoolConfigured: cfg.Pool != nil, Serve: portable}, log)`.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/... ./cmd/at-jam/ && go vet -tags integration ./...`
Expected: PASS (`TestConfigRoutesRejectBadUpload`'s 415 cases still hold; `gittest` tests skip without git).

- [ ] **Step 5: Commit**

```bash
git add internal/jam/config_admin.go internal/jam/config_admin_test.go internal/jam/adminclient/adminclient.go internal/jam/adminclient/config_test.go cmd/at-jam/main.go cmd/at-jam/configgit_test.go
git commit -m "feat(jam): /admin/config/source, /admin/config/sync and {source} diff/apply"
```

---

### Task 8: CLI — `config track|sync|untrack` and `--git`

**Files:**
- Modify: `cmd/at-jam/configgit.go`, `cmd/at-jam/main.go` (command Brief)
- Test: `cmd/at-jam/configgit_test.go`; Create: `cmd/at-jam/config_git_integration_test.go`

**Interfaces:**
- Consumes: the `adminclient` methods (Task 7), `jam.ConfigApplyResult.Source/Commit`, `jam.ConfigPlan.KitSources`, `configtar.Pack`, `gittest.Serve`, `resolveKitFetcher`, `parseServeConfig` (integration).
- Produces:
```go
type configTarget struct{ tree []byte; source string; sync bool }
func (t configTarget) diff(c *adminclient.Client, force bool) (jam.ConfigApplyResult, error)
func (t configTarget) apply(c *adminclient.Client, force bool, digest string) (jam.ConfigApplyResult, error)
func printConfigSource(w io.Writer, res jam.ConfigApplyResult) // "config <src> @ <sha7>" for git runs
func shortSHA(c string) string
// printConfigPlan also prints "kit-source  <path>  <source> @ <sha7>" lines
```

- [ ] **Step 1: Write the failing tests**

In `cmd/at-jam/configgit_test.go`, replace `TestConfigNeedsDir` with the tests below. Add the imports `"github.com/aethons-tools/cove/internal/jam/gitsource"` and `".../gitsource/gittest"`.

```go
// configGitServer is configServer with a real, anonymous kit fetcher.
func configGitServer(t *testing.T) (string, *jam.MemStore) {
	t.Helper()
	st := jam.NewMemStore()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	creds := func(string) bool { return true }
	ts := httptest.NewServer(jam.NewAdminHandler(st, nil, nil, jam.LoopbackAuthenticator{}, creds, nil, log, nil, nil,
		jam.WithConfig(st, &gitsource.Fetcher{}, jam.ConfigEnv{CredExists: creds}, log)))
	t.Cleanup(ts.Close)
	return ts.URL, st
}

func TestConfigTrackSyncUntrack(t *testing.T) {
	kitsURL, _, _ := gittest.Serve(t, map[string]string{"api/kit.yml": "kind: studio\nprompt: api\n"})
	cfgURL, first, update := gittest.Serve(t, map[string]string{
		"jam/users/bob.yml":       "kind: user\nversion: 1\n",
		"jam/kits/web/kit.yml":    "kind: studio\n",
		"jam/kits/api.source.yml": "kind: kit-source\nversion: 1\ngit: " + kitsURL + "//api@main\n",
	})
	url, st := configGitServer(t)
	src := cfgURL + "//jam@main"
	code, out, errs := runJam("config", "track", "--git", src, "--admin-url", url)
	if code != 0 || !strings.Contains(out, "tracking "+src) || !strings.Contains(out, "create  user bob") ||
		!strings.Contains(out, "kit-source  kits/api.source.yml  "+kitsURL+"//api@main @ ") {
		t.Fatalf("track: %d\n%s\n%s", code, out, errs)
	}
	if len(st.ListUsers()) != 0 {
		t.Fatal("track wrote")
	}
	code, out, errs = runJam("config", "sync", "--yes", "--admin-url", url)
	if code != 0 || !strings.Contains(out, "config "+src+" @ "+first[:7]) || !strings.Contains(out, "applied: 3 created") {
		t.Fatalf("sync: %d\n%s\n%s", code, out, errs)
	}
	if got, _ := st.GetConfigSource(); got.Commit != first {
		t.Fatalf("record %+v", got)
	}
	if k, _ := st.GetKit("api"); k.Source == nil || k.Source.Repo != kitsURL || k.Source.Dir != "api" {
		t.Fatalf("api tracked %+v", k.Source)
	}
	second := update(map[string]string{"jam/users/bob.yml": "kind: user\nversion: 1\nlogins: [bob@acme]\n"})
	code, out, errs = runJam("config", "apply", "--yes", "--admin-url", url) // no flag: the tracked source
	if code != 0 || !strings.Contains(out, "config "+src+" @ "+second[:7]) || !strings.Contains(out, "1 updated") {
		t.Fatalf("apply: %d\n%s\n%s", code, out, errs)
	}
	if got, _ := st.GetConfigSource(); got.Commit != second {
		t.Fatalf("record %+v", got)
	}
	if code, out, _ := runJam("config", "untrack", "--admin-url", url); code != 0 || !strings.Contains(out, "untracked") {
		t.Fatalf("untrack: %d %s", code, out)
	}
	if code, _, errs := runJam("config", "sync", "--yes", "--admin-url", url); code != 1 || !strings.Contains(errs, "no config source is tracked") {
		t.Fatalf("sync after untrack: %d %s", code, errs)
	}
	if len(st.ListConfigMarks()) != 3 {
		t.Fatalf("untrack dropped marks: %v", st.ListConfigMarks())
	}
}

func TestConfigDiffWithoutTrackedSource(t *testing.T) {
	url, _ := configServer(t)
	if code, _, errs := runJam("config", "diff", "--admin-url", url); code != 1 || !strings.Contains(errs, "no config source is tracked") {
		t.Fatalf("%d %s", code, errs)
	}
}

func TestConfigVerbFlags(t *testing.T) {
	for _, args := range [][]string{
		{"config", "diff", "--dir", "x", "--git", "https://github.com/a/b@main"},
		{"config", "apply", "--dir", "x", "--git", "https://github.com/a/b@main"},
		{"config", "track"},
		{"config", "track", "--dir", "x"},
		{"config", "sync", "--git", "https://github.com/a/b@main"},
		{"config", "untrack", "extra"},
	} {
		if code, _, _ := runJam(args...); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
}
```

Create `cmd/at-jam/config_git_integration_test.go`:

```go
//go:build integration

package main

import (
	"bytes"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/gitsource/gittest"
	"github.com/aethons-tools/cove/internal/secret"
)

// configGitJam is the real admin handler with the config API on a real
// Fetcher built, as serve builds it, from a kits.git entry for repoURL's
// server whose cred resolves to token.
func configGitJam(t *testing.T, store jam.Store, repoURL, token string) (string, *bytes.Buffer) {
	t.Helper()
	prefix := repoURL[:strings.LastIndex(repoURL, "/")+1]
	cfg, err := parseServeConfig([]byte("credentials:\n  kit-git:\nkits:\n  git:\n    - url: " + prefix + "\n      cred: kit-git\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.validateKits(); err != nil {
		t.Fatal(err)
	}
	f, err := resolveKitFetcher(cfg, map[string]secret.Spec{"kit-git": {Name: "kit-git", Value: token, Literal: true}})
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, log, nil, nil,
		jam.WithConfig(store, f, jam.ConfigEnv{CredExists: func(string) bool { return true }}, log))
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts.URL, &logs
}

// TestConfigGitEndToEnd: track and sync a private config repo through the
// real fetcher behind Basic auth, with its kit-source kit; a wrong token and
// a pasted credential URL never reach any output or log.
func TestConfigGitEndToEnd(t *testing.T) {
	const token = "s3cret-config-token-e2e"
	kitsURL, _, _ := gittest.Serve(t, map[string]string{"api/kit.yml": "kind: studio\nprompt: api\n"})
	cfgURL, first, _ := gittest.Serve(t, map[string]string{
		"users/bob.yml":       "kind: user\nversion: 1\n",
		"kits/api.source.yml": "kind: kit-source\nversion: 1\ngit: " + kitsURL + "//api@main\n",
	}, gittest.WithAuth("x-access-token", token))
	st := jam.NewMemStore()
	url, logs := configGitJam(t, st, cfgURL, token)
	src := cfgURL + "@main"
	if code, out, errs := runJam("config", "track", "--git", src, "--admin-url", url); code != 0 {
		t.Fatalf("track: %d\n%s\n%s", code, out, errs)
	}
	if code, out, errs := runJam("config", "sync", "--yes", "--admin-url", url); code != 0 || !strings.Contains(out, "applied: 2 created") {
		t.Fatalf("sync: %d\n%s\n%s", code, out, errs)
	}
	if got, _ := st.GetConfigSource(); got.Commit != first {
		t.Fatalf("record %+v", got)
	}
	k, _ := st.GetKit("api")
	if m := k.Meta[1].Source; m == nil || m.Repo != kitsURL || m.Dir != "api" || m.Commit == "" {
		t.Fatalf("provenance %+v", m)
	}

	wrongURL, wrongLogs := configGitJam(t, jam.NewMemStore(), cfgURL, "wrong-token")
	code, out, errs := runJam("config", "diff", "--git", src, "--admin-url", wrongURL)
	if code != 1 || !strings.Contains(errs, "auth failed") {
		t.Fatalf("wrong token: %d\n%s\n%s", code, out, errs)
	}
	pasted := strings.Replace(cfgURL, "://", "://x-access-token:"+token+"@", 1) + "@main"
	code, out2, errs2 := runJam("config", "track", "--git", pasted, "--admin-url", url)
	if code != 1 {
		t.Fatalf("pasted credential: %d", code)
	}
	for _, s := range []string{out, errs, out2, errs2, logs.String(), wrongLogs.String()} {
		if strings.Contains(s, token) || strings.Contains(s, "wrong-token") {
			t.Fatalf("a token leaked:\n%s", s)
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./cmd/at-jam/ -run 'TestConfig'`
Expected: FAIL (`config track` is a usage error; `config diff` without `--dir` exits 2).

- [ ] **Step 3: Implement**

In `cmd/at-jam/configgit.go`, add `"github.com/aethons-tools/cove/internal/jam/adminclient"` to the imports. Replace `cmdConfig`'s head through its final `return 0` with:

```go
// cmdConfig keeps a Jam's configuration in git: `config write <dir> [--all]`,
// `config diff|apply [--dir <path> | --git <src>]` (neither: the tracked
// source), `config track --git <src>`, `config sync`, `config untrack`.
func cmdConfig(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	const usage = "at-jam config: expected write <dir> [--all] [--force] | diff [--dir <path> | --git <src>] | apply [--dir <path> | --git <src>] [--yes] [--force] | track --git <src> | sync [--yes] [--force] | untrack"
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "write", "diff", "apply", "track", "sync", "untrack":
	default:
		fmt.Fprintln(stderr, usage)
		return 2
	}
	fs := flag.NewFlagSet("config "+sub, flag.ContinueOnError)
	client := clientFlags(fs, stderr)
	none := ""
	no := false
	all, yes, force := &no, &no, &no
	dir, git := &none, &none
	switch sub {
	case "write":
		all = fs.Bool("all", false, "write every live object, not only the managed ones")
		force = fs.Bool("force", false, "allow a directory outside a git working tree")
	case "diff":
		dir = fs.String("dir", "", "the config checkout to plan against the Jam")
		git = fs.String("git", "", "a config source <repo>[//<dir>]@<ref> to plan (default: the tracked source)")
	case "apply":
		dir = fs.String("dir", "", "the config checkout to apply")
		git = fs.String("git", "", "a config source <repo>[//<dir>]@<ref> to apply (default: the tracked source)")
		yes = fs.Bool("yes", false, "apply without the confirmation prompt")
		force = fs.Bool("force", false, "write over drift")
	case "track":
		git = fs.String("git", "", "the config source <repo>[//<dir>]@<ref> to track")
	case "sync":
		yes = fs.Bool("yes", false, "apply without the confirmation prompt")
		force = fs.Bool("force", false, "write over drift")
	}
	pos, code, ok := cli.ParseFlags(fs, rest, stdout, stderr)
	if !ok {
		return code
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "at-jam config %s: %v\n", sub, err)
		return 1
	}
	if sub == "write" {
		if len(pos) != 1 {
			fmt.Fprintln(stderr, "at-jam config write: expected one <dir>")
			return 2
		}
		c, err := client()
		if err != nil {
			return fail(err)
		}
		target := pos[0]
		abs, err := filepath.Abs(target)
		if err != nil {
			return fail(err)
		}
		if !*force && !insideGitWorkTree(abs) {
			return fail(fmt.Errorf("%s is not inside a git working tree (use --force to write anyway)", target))
		}
		exp, err := c.ConfigExportTree(*all)
		if err != nil {
			return fail(err)
		}
		if err := writeConfigTree(target, exp); err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "wrote %d files to %s (removed %d); review with git diff\n", len(exp.Files), target, len(exp.Remove))
		return 0
	}
	if len(pos) != 0 {
		fmt.Fprintf(stderr, "at-jam config %s: unexpected argument %q\n", sub, pos[0])
		return 2
	}
	if *dir != "" && *git != "" {
		fmt.Fprintf(stderr, "at-jam config %s: --dir and --git are mutually exclusive\n", sub)
		return 2
	}
	if sub == "track" && *git == "" {
		fmt.Fprintln(stderr, "at-jam config track: --git <repo>[//<dir>]@<ref> is required")
		return 2
	}
	c, err := client()
	if err != nil {
		return fail(err)
	}
	switch sub {
	case "untrack":
		if err := c.ConfigUntrack(); err != nil {
			return fail(err)
		}
		fmt.Fprintln(stdout, "config source untracked; managed objects and their marks are kept")
		return 0
	case "track":
		res, err := c.ConfigTrack(*git)
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "tracking %s\n", res.Source)
		printConfigSource(stdout, res)
		printConfigPlan(stdout, res.Plan)
		if len(res.Plan.Refusals) > 0 {
			return 1
		}
		return 0
	}
	t := configTarget{source: *git, sync: sub == "sync"}
	if *dir != "" {
		if t.tree, err = configtar.Pack(*dir); err != nil {
			return fail(err)
		}
	}
	// apply and sync preview with their own --force, so the reviewed plan
	// (and its digest) is the one the forced apply writes.
	res, err := t.diff(c, sub != "diff" && *force)
	if err != nil {
		return fail(err)
	}
	printConfigSource(stdout, res)
	printConfigPlan(stdout, res.Plan)
	if len(res.Plan.Refusals) > 0 {
		return 1
	}
	if sub == "diff" {
		return 0
	}
	n := jam.ConfigChanges(res.Plan)
	if n == 0 {
		fmt.Fprintln(stdout, "nothing to apply")
		return 0
	}
	if !*yes {
		word := "changes"
		if n == 1 {
			word = "change"
		}
		fmt.Fprintf(stdout, "Apply %d %s? [y/N] ", n, word)
		line, _ := bufio.NewReader(configStdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			fmt.Fprintln(stderr, "aborted")
			return 1
		}
	}
	applied, err := t.apply(c, *force, res.Plan.Digest)
	if err != nil {
		return fail(err)
	}
	if !applied.Applied {
		fmt.Fprintln(stdout, "nothing to apply")
		return 0
	}
	k := applied.Counts
	fmt.Fprintf(stdout, "applied: %d created, %d updated, %d deleted, %d adopted, %d forgotten\n",
		k[jam.ConfigCreate], k[jam.ConfigUpdate], k[jam.ConfigDelete], k[jam.ConfigAdopt], k[jam.ConfigForget])
	return 0
}

// configTarget is what a diff, apply or sync plans: a packed --dir tree, a
// --git source, or (neither) the tracked source — through /sync for sync.
type configTarget struct {
	tree   []byte
	source string
	sync   bool
}

func (t configTarget) diff(c *adminclient.Client, force bool) (jam.ConfigApplyResult, error) {
	if t.tree != nil {
		return c.ConfigDiff(t.tree, force)
	}
	return c.ConfigDiffSource(t.source, force)
}

func (t configTarget) apply(c *adminclient.Client, force bool, digest string) (jam.ConfigApplyResult, error) {
	switch {
	case t.tree != nil:
		return c.ConfigApply(t.tree, force, digest)
	case t.sync:
		return c.ConfigSync(force, digest)
	}
	return c.ConfigApplySource(t.source, force, digest)
}

// printConfigSource names the git source and commit a run planned.
func printConfigSource(w io.Writer, res jam.ConfigApplyResult) {
	if res.Commit != "" {
		fmt.Fprintf(w, "config %s @ %s\n", res.Source, shortSHA(res.Commit))
	}
}

func shortSHA(c string) string {
	if len(c) > 7 {
		return c[:7]
	}
	return c
}
```

At the top of `printConfigPlan`'s body, before the ops loop, add:

```go
	for _, ks := range p.KitSources {
		fmt.Fprintf(w, "kit-source  %s  %s @ %s\n", ks.Path, ks.Source, shortSHA(ks.Commit))
	}
```

In `cmd/at-jam/main.go`, change the `config` command's Brief to: `"keep the Jam's configuration in git: write it out (write <dir> [--all]); plan and apply a checkout or a git source (diff|apply [--dir <path> | --git <src>] [--yes] [--force]); follow one config source (track --git <src> | sync | untrack) via the admin API"`.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./cmd/at-jam/ && go test -tags integration -run 'TestConfigGitEndToEnd|TestKitGitEndToEnd' ./cmd/at-jam/`
Expected: PASS (`gittest` tests skip when git or git-http-backend is missing).

- [ ] **Step 5: Commit**

```bash
git add cmd/at-jam/configgit.go cmd/at-jam/configgit_test.go cmd/at-jam/config_git_integration_test.go cmd/at-jam/main.go
git commit -m "feat(at-jam): config track|sync|untrack and diff|apply --git"
```

---

### Task 9: Docs

**Files:**
- Create: `docs/usage/jam/config-git-sources.md`
- Modify: `docs/usage/jam/config-git.md`, `docs/usage/jam/kits-git.md`, `docs/usage/jam/INDEX.md`

**Interfaces:**
- Consumes: the behavior of Tasks 1–8 and the deviations above. This task changes no code.
- Produces: the leaf that owns the tracked config source and `.source.yml`, reachable from `INDEX.md`.

- [ ] **Step 1: Record the audit baseline**

Run: `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs | tail -1`
Expected: `… 710 error(s), …` (the baseline; the run must not end higher).

- [ ] **Step 2: Write the leaf**

Create `docs/usage/jam/config-git-sources.md`:

````markdown
---
summary: A Jam following a config repo in git — `config track|sync|untrack`, `config diff|apply --git`, kits referenced by `kits/<name>.source.yml`, provenance, locks, routes and errors.
read_when: You want a Jam to follow a config repo (track, sync, untrack, `--git`), are referencing a kit by its own git source (`kits/<name>.source.yml`), or a config sync failed to fetch, refused a source, or flagged a source kit as drifted.
owns: the tracked config source (record, sync, untrack), the `--git` verbs, the `/admin/config/source|sync` routes and `{source}` bodies, `kits/<name>.source.yml` and how it is fetched, planned and written, config-applied kit provenance and tracking
prereqs: config-git.md (layout, plan actions, marks); kits-git.md (source syntax, kits.git credentials, fetch limits)
tier: leaf
updated: 2026-10-22
---

# Config from git: the tracked source and kit references

A Jam can follow **one** config repo in git. Sources use the kit syntax,
`<repo>[//<dir>]@<ref>` ([kits-git.md](kits-git.md#the-source-repodirref)),
and the same fetcher and `kits.git` credentials. Jam's git access stays
read-only: `config write` still fills your checkout ([config-git.md](config-git.md)).

## Verbs

| Command | Does |
|---|---|
| `config track --git <src>` | Fetch and plan `<src>`, print the plan, and record `<src>` as the Jam's config source, even when the plan has refusals. A fetch failure records nothing. Re-tracking the same source keeps its last commit; another source replaces it. |
| `config sync [--yes] [--force]` | `apply` from the tracked source: print the plan, prompt (unless `--yes`), apply. |
| `config untrack` | Forget the source. Objects and their managed marks stay. |
| `config diff\|apply --git <src>` | Plan or apply any source once, without tracking it. |
| `config diff\|apply` (no flag) | Use the tracked source; exit 1 with `no config source is tracked` if there is none. |

`--git` and `--dir` are mutually exclusive (exit 2). A git run prints
`config <src> @ <commit[:7]>` first, then one `kit-source  <path>  <src> @
<commit[:7]>` line per kit reference. An apply from the tracked source
records the commit and time on it, in the same transaction as the plan.
That covers `sync`, a no-flag `apply`, and `--git` naming the same repo, dir
and ref. A sync whose repos moved without changing anything is all
`unchanged`: no new version, mark or audit entry, but the recorded commits
advance. The tracked source, like the marks, is **not** part of
`at-jam export`: a restored Jam tracks nothing until `config track`.

## Kit references: `kits/<name>.source.yml`

```yaml
# kits/api.source.yml
kind: kit-source
version: 1
git: https://github.com/acme/kits//api@main
```

- **Fetching.** Every diff and apply fetches the referenced kits before
  planning, `--dir` uploads included (with the serve's `kits.git` credentials).
  Each distinct repo and ref is fetched once, so two kits from one repo and ref
  share a commit. A tree may name at most 16 distinct kit sources, and one
  run's fetches share a 4-minute limit.
- **Planning.** The kit and its path parents in its repo are planned
  like inline kits (create, update, drift, adopt, delete) and become managed.
  Each tracks its repo at its own dir: `{repo, dir, ref, commit}`, also recorded
  on every new version. A registry parent (`base: {kit: base}`) may be an
  inline kit of the config repo.
- **Canonical form.** A kit's canonical form is its config plus its tracked
  `repo//dir@ref`. A `kit sync`, `kit untrack` or push of a managed kit is
  therefore an edit in Jam: the next config diff shows `update … (drift)`.
- **Inline kits are untracked.** An inline kit (`kits/<n>/kit.yml`) applied
  from git records `{config repo, <config dir>/kits/<n>, ref, commit}` and
  tracks nothing: the config repo owns it.
- **Writing.** `config write` renders every tracked kit as
  `kits/<n>.source.yml` and removes the managed kit's other form. A kit pinned
  to such a kit (`web@v3`) cannot be written: pin it by name (`web`).

## Refusals and errors

- `kits/<n>.source.yml: git: source: …`: an unusable source. A pasted
  credential URL is refused this way and never echoed.
- `kits/<n>.source.yml: kit <n> is also kits/<n>/kit.yml`: keep one form.
- `kits/<n>.source.yml: <fetch error>`: the fetcher's redacted message
  ([kits-git.md](kits-git.md#errors-and-troubleshooting)). This is a refusal,
  never a delete.
- `kits/<n>.source.yml: kit <a> is also <file>`: two references load
  different kits under one name.
- `kits/<n>.source.yml: kit <n>'s base.kit <ref> pins a version; …`: versions are per-Jam.
- `config source: <dir> is not a directory in the repo` (400): `//dir` is
  missing, a file or a symlink.
- `no config source is tracked (use config track --git <source>, or --dir)` (409).

Logs name a source as `repo//dir@ref` (an upload: `dir`), never with userinfo.
Audit: `config tracked` (operator, source, commit), `config untracked`
(operator, source), and `config applied` as for `--dir`.

## Routes

| Route | Does |
|---|---|
| `POST /admin/config/diff`, `/apply` | As in config-git.md; a `{"source": "<src>"}` JSON body (`""` = the tracked source) instead of a tar. |
| `PUT /admin/config/source` | `{source}`: fetch, plan (the answer) and track it. |
| `DELETE /admin/config/source` | Untrack (204, also when none). |
| `POST /admin/config/sync?force=1&plan=<digest>` | Apply the tracked source; body `{}` (any other Content-Type is 415). |

Statuses: refused, stale or untracked 409; bad source or dir 400; fetch,
auth or limit failure 502.

## Locks

A config run holds `configMu` from its first fetch to its write, and
`kitMu` only for the plan and the write. It never takes a per-kit lock;
kit operations never take `configMu`. Two config runs therefore never
interleave, and a kit push that lands between a config fetch and its plan
shows as drift.
````

- [ ] **Step 3: Update the neighbours**

In `docs/usage/jam/config-git.md`, make these exact replacements:
- In the frontmatter, set `updated: 2026-10-22`.
- Replace `Slice 1 reads a local checkout (`--dir`); git sources, `track`/`sync` and` and the line after it, `` `.source.yml` are not built yet.``, with:
  ```markdown
  A Jam can also follow a config repo in git, and reference kits by their own source: see [config-git-sources.md](config-git-sources.md).
  ```
- In the layout block, add after the `kits/<n>/kit.yml` line: `kits/<n>.source.yml              or a reference to the kit's own git source`.
- Replace `directories anything else is refused (`kits/<n>.source.yml`: `kit source` and the line after it, `references are not supported yet`; a `.yaml` extension: `use .yml`).`, with `directories anything else is refused (a `.yaml` extension: `use .yml`).`
- Replace `Kits are inline only: `kits/<n>/kit.yml`, with` with `Inline kits are `kits/<n>/kit.yml`, with`.
- Replace `written by `--dir` record provenance repo `local`, dir `kits/<name>`.` with `written by `--dir` record provenance repo `local`, dir `kits/<name>` (from git: the config repo, dir, ref and commit).`
- In the verbs table, replace `` `config diff --dir <path>` `` with `` `config diff --dir <path>` (or `--git`, see below) ``. Add a last row:
  ```markdown
  | `config track\|sync\|untrack` | Follow one config source: [config-git-sources.md](config-git-sources.md). |
  ```
- Replace `and no `forget` prints `nothing to apply` and does not prompt. Without `--dir` a verb exits 2` and the line after it, `` (`--dir is required (git sources are not supported yet)`). ``, with `and no `forget` prints `nothing to apply` and does not prompt. Without `--dir`, diff and apply use `--git` or the tracked source.`
- Replace `` `<project>/<role>`; the Jam context is `jam`) and the commit (`dir` here). `` with `` `<project>/<role>`; the Jam context is `jam`) and the commit (`dir` for `--dir`). ``
- Replace `` `application/json`, the future `--git` body, is 400). `` with:
  ```markdown
  `application/json` is a `{source}` body — [config-git-sources.md](config-git-sources.md#routes)).
  ```
- Replace the "Not yet" section body `` `--git` sources, `track`/`sync`, `.source.yml`, the UI page, drift attention. `` with `The UI page, managed badges, `config release`, drift attention.`

In `docs/usage/jam/kits-git.md`, add before `## Future work`:

```markdown
## In a config repo

A config repo can reference a kit by its source (`kits/<name>.source.yml`);
`config sync` then fetches and tracks it as a managed kit — see
[config-git-sources.md](config-git-sources.md). A `kit sync`, `kit untrack`
or push of such a kit is an edit in Jam: the next config diff shows drift.
```

In `docs/usage/jam/INDEX.md`, add after the `config-git.md` row, and set `updated: 2026-10-22`:

```markdown
| [config-git-sources.md](config-git-sources.md) | You want a Jam to follow a config repo in git (`config track\|sync\|untrack`, `--git`), are referencing a kit by its own source (`kits/<name>.source.yml`), or a config sync failed to fetch or flagged a source kit as drifted. |
```

- [ ] **Step 4: Audit**

Run: `wc -l docs/usage/jam/config-git.md docs/usage/jam/config-git-sources.md docs/usage/jam/kits-git.md docs/usage/jam/INDEX.md && python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs | grep -E 'config-git|kits-git|jam/INDEX|error\(s\)'`
Expected:
- each leaf is ≤ 200 lines and `INDEX.md` is ≤ 120;
- no error names `config-git-sources.md`, `config-git.md`, `kits-git.md` or `jam/INDEX.md`;
- the total is ≤ 710.

- [ ] **Step 5: Commit**

```bash
git add docs/usage/jam/config-git-sources.md docs/usage/jam/config-git.md docs/usage/jam/kits-git.md docs/usage/jam/INDEX.md
git commit -m "docs(jam): config from git — track/sync/untrack, --git and kits/<name>.source.yml"
```

---

## Self-review

- **Spec coverage:**
  - `track` / `sync` / `untrack` → Tasks 6–8.
  - `diff|apply --git` and the no-flag default → Tasks 6–8.
  - The tracked config source → Task 1.
  - The routes `PUT|DELETE /admin/config/source`, `POST /admin/config/sync` and `{source}` bodies → Task 7.
  - `.source.yml`, fetched before the transaction and planned like kits → Tasks 2, 3 and 5.
  - Tracked source recorded on the kit, real provenance and marks' commit → Task 5.
  - `config write` renders `.source.yml` → Task 4.
  - Locks: `configMu` → `kitMu`, no kit locks → Task 6 (`TestConcurrentSyncsAndKitPush`).
  - Audit → Task 6.
  - Errors, with no token echo → Tasks 2, 6, 7 and 8.
  - §8 "Git: track/sync over the hermetic smart-HTTP fixture; `.source.yml` kits" → Task 8 (`TestConfigTrackSyncUntrack`, `TestConfigGitEndToEnd`) and Task 7 (`TestConfigSourceMethods`).
  - The source round trip → Task 6 (`TestConfigRoundTripWithSourceKit`).
  - Docs → Task 9.
  - The slice-1 leftovers (marks' commit for `--git`, the raw source log) → Tasks 5 and 6.
- **Placeholder scan:** every code step carries its code. The one forward reference, `ConfigKitGroup`, is declared temporarily in Task 2 and moved in Task 3.
- **Type consistency:**
  - `KitFetcher` is the fetcher type everywhere; `repoFetcher` and `badDirFetcher` implement it.
  - `ConfigSource.Source()` returns `gitsource.Source` and is compared with `==`.
  - `ConfigApplyResult.Source/Commit` are set only in `applyConfigTreeLocked`.
  - `ConfigPlan.KitSources` is `[]ConfigKitSource`.
  - `WithConfig(store, f, env, log)` takes the same arguments at every call site: `main.go` and the three tests.
- **Review Focus:** each of the five lines names its test and the task that adds it.
