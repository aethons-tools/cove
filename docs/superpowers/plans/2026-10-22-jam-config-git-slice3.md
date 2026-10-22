# A Jam's configuration in git — slice 3 (UI and drift) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** An operator sees and steers a Jam's config repo from the admin UI. A **Config** tab shows the tracked source, its last applied commit and time, every managed object with its state (in sync, drifted, missing), and the rendered plan. **Diff**, **Apply** (bound to the plan on screen, with a force checkbox only when there is drift), **Track**, **Untrack** and **Release** work from it. Object pages carry a `managed · config @abc1234` badge, and every write to a managed object asks "This will drift from the config repo" before it is sent. `at-jam config release <kind>/<name>` (CLI, admin route, client, UI) clears a mark. The `config.drift:jam` condition (info, on Health) is open while any managed object has drifted or gone missing, and clears on apply, release, or write-and-apply.

**Architecture:**
- **Drift is the planner's digest, nothing new.** `ConfigMarkStates` renders the live Jam with slice 1's `exportObjects(stateFromSnapshot(store.ExportConfig(), nil), nil)` and compares each `configObject.Digest()` with its mark. The planner, `config write` and drift therefore share one canonical form.
- **One condition, checked on a timer and after every config write.** `RunConfigDriftCheck` runs in serve every minute (`ConfigDriftEvery`), under `configMu`. `applyConfigTreeLocked` (every non-dry-run apply and sync, including the metadata-only write) and `ReleaseConfigMark` re-check at once, so the condition clears in the same request. The tracker reaches them as `ConfigEnv.Conditions`.
- **Release is a marks-only write.** `ReleaseConfigMark` is `Store.ApplyConfig(ConfigWrite{RemoveMarks: …})` under `configMu`. No store change is needed.
- **The UI drives the existing entry points.** The Config page calls `ApplyConfigSource` (diff = `DryRun`, apply = `WantDigest`), `TrackConfigSource`, `UntrackConfigSource` and `ReleaseConfigMark` in-process, as the kit pages call `TrackKit`/`SyncKit`. Errors map through `jam.ConfigStatus`.
- **Badges and warnings come from the frame.** Every render (page or fragment) carries the managed marks in its `frame`. Two template funcs read them: `managed kind name` (the badge) and `driftNote kind name` (the warning). The warning is an `hx-confirm` on each write control of a managed object, the way destructive actions already confirm.
- **Plans show both sides of drift.** A drifted delete refused without `--force` now emits its op (`delete`, drift, Jam's side) as a refused drifted update already does. `ConfigPlan.HasDrift()` decides whether the force checkbox shows.
- **Issue 010 folds in.** The CLI's `--dir` apply no longer exits early when nothing changes. The server's metadata-only write then refreshes stale marks, which is what makes write-and-apply clear drift from a checkout.

**Tech Stack:** Go 1.26; `html/template` + htmx (adminui); the existing `jam` store (MemStore + Postgres via pgx), `condition.Tracker`, `gitsource`, `configtar`, `adminclient`.

**Spec:** [`docs/superpowers/specs/2026-10-21-jam-config-in-git-design.md`](../specs/2026-10-21-jam-config-in-git-design.md) §2 (`config release`, `DELETE /admin/config/managed/{kind}/{name}`), §6 (Config page, object pages, attention) and slice 3. It builds on [`2026-10-21-jam-config-git-slice1.md`](2026-10-21-jam-config-git-slice1.md) and [`2026-10-22-jam-config-git-slice2.md`](2026-10-22-jam-config-git-slice2.md).

## Spec deviations and interpretations

1. **The drift warning is an `hx-confirm` on every write control of a managed object.** It covers save forms, deletes, kit push/pin/sync/track/untrack, user rename and logins, project rename, chat service, members, rooms, escalation and context, role scope, egress, allocation, standing declare/dismiss and context, and the Jam context.
   - The text is `This will drift from the config repo (managed · config @<commit[:7]>).` plus ` Save anyway?` (or the control's own question). The UI already confirms destructive actions this way (`hx-confirm`), so this needs no second round trip and no server state.
   - It is client-side only. The admin JSON API and CLI writes do not warn. Drift is still caught by the next plan and by `config.drift`.
   - It shows whether or not the object has already drifted.
   - Controls that touch nothing in the repo file do not warn: a user's OIDC identities and accounts, standing reset/upgrade, grants, and creating a new role in a managed project (the role is its own, unmanaged object).
   - List-table deletes (kits, destinations, model-specs) warn too. List pages carry no badge.
2. **Badge text and placement.** `managed · config @<commit[:7]>` links to `/ui/config`, and its title is the apply time. A `--dir` mark reads `managed · config @dir`.
   - The badge sits in the head of the destination, model-spec, kit, user, project (overview), role pages, and in the Jam context card on the Dashboard.
   - **Connections have no UI page**, so their managed state shows only on the Config page.
3. **One condition key, `config.drift:jam`.** Condition keys need a subject after the colon (`condition.keyRE`).
   - It is info severity, so it shows on the Health tab only: never a badge, never pushed (monitoring.md).
   - Its detail lists the objects (`destination gh (drifted)`, names only). Its fix names the three ways out.
   - **"Drift" includes `missing`**: a managed object removed in Jam since its apply. The live digest is absent, which is not the mark's.
4. **When drift is computed.**
   - Serve checks every minute (`ConfigDriftEvery`) and once at start, which also reconciles a condition reloaded from Postgres.
   - Every non-dry-run apply or sync (including a zero-change one) and every release re-checks before answering.
   - With no marks the check is skipped. Otherwise it costs one config snapshot plus rendering every object, the same as a plan without a tree.
   - There is no hook on ordinary UI or API writes. **An edit in Jam raises the condition within a minute, not at once.**
   - The checker holds `configMu`, so it never compares marks and objects from either side of an apply. The Config page computes states on load without the lock (display only).
   - If the live Jam cannot be rendered (a kit whose current version does not parse), the checker logs a warning and **keeps the condition's last state**. The page shows the states as unknown. The planner refuses such a Jam the same way (`jam: …`).
5. **A refused drifted delete now shows Jam's side.** Without `--force`, `planDeletes` emits `delete` with `Drift` and `Before` (and no write) next to its refusal, as `classify` already does for updates. `ConfigPlan.HasDrift()` is true for any drifted op. The CLI prints the repo side as `(not in the repo)`. Digests of refused plans change, but those are never applied.
6. **The Config page works on the tracked source only.**
   - It is a Jam tab, **Config** (`/ui/config`), between Specs and Intercom.
   - **Diff** plans the tracked source. **Apply** applies the reviewed plan of the tracked source, which is the sync. There is no separate Sync button: a sync without review would bypass the digest binding.
   - `--git <other>` and `--dir` stay CLI-only.
   - **Track** is offered only when nothing is tracked. To switch sources, Untrack then Track (or `config track` in the CLI).
   - Without a fetcher (`WithKitFetcher` unset) the page is read-only, plus Untrack and Release.
7. **Digest binding.** Apply posts the digest of the plan on screen (`plan=`), and the page refuses an Apply without one (400). The force checkbox re-plans with `force=1` and so shows the forced plan, whose digest the Apply then carries along with `force=1`. A Jam that changed after the Diff answers 409, and nothing is written.
8. **"Last applied commit and time"** come from the tracked source's record (`ConfigSource.Commit/AppliedAt`). With no source tracked (a `--dir` Jam), the page shows the newest mark's commit and time.
9. **`config release <kind>/<name>`.**
   - A role is `role/<project>/<role>`. The route is `DELETE /admin/config/managed/{kind}/{name...}`, so the role name keeps its slash.
   - An unknown kind is 400 and an object with no mark is 404 (`<kind> <name> is not managed`). Success is 204.
   - It holds `configMu` and leaves the object as it is. The next plan lists the object as unmanaged, or adopts or refuses it if the repo still holds it.
   - Audit: `config released` (operator, kind, name).
   - In the UI, Release is a button on each Config-page row, not on the badges.
10. **Issue 010 is in this slice.** A `--dir` apply with nothing to change now calls the server, without a prompt, and still prints `nothing to apply`. Without it, `config write` + `config apply --dir` (the spec's write-and-apply) would never refresh the stale marks, and `config.drift` would never clear from a checkout.
11. **No `GET /admin/config/source`.** Slice 2's deviation 13 expected the page to add it. The page reads the store in-process like every UI page, and no CLI verb needs the route, so it is not added.
12. **Plan diffs.** A kit's canonical form is its stored config JSON. The page indents it (keeping a `# source:` line) before the line diff, reusing the kit page's `lineDiff`. A create op does not link to the object, because the object does not exist yet.
13. **Docs.** The page, badges and warning get a new leaf, `docs/usage/jam/ui-config.md`, because `ui-pages.md` is already over its 200-line budget. `ui-attention.md` is unchanged: info conditions are never badges.

## Global Constraints

- Spec §6, verbatim:
  - **Config page** (Admin): tracked source, last applied commit and time; **Diff** (the rendered plan), **Apply** (htmx, `originGuard`, a `force` checkbox shown only with drift), **Track**, **Untrack**.
  - **Object pages:** a `managed · config @abc1234` badge; saving an edit to a managed object shows "this will drift from the config repo" before it saves.
  - **Attention:** `config.drift` (info) while any managed object's live digest ≠ its mark; clears on apply, release or write-and-apply.
- Spec §2: `config release <kind>/<name>` — Clear an object's managed mark (it becomes unmanaged). The route is `DELETE /admin/config/managed/{kind}/{name}`.
- **No second digest.** Drift uses `exportObjects` + `configObject.Digest` and nothing else. The UI drives `ApplyConfigSource`, `TrackConfigSource`, `UntrackConfigSource` and `ReleaseConfigMark`, and never plans or writes on its own.
- **Locks.** `configMu` is taken before `kitMu`. Release and the drift checker take `configMu` only.
- **UI writes** go through `originGuard` (403 cross-origin, before any fetch or write), log `ui config …` with the operator, and never render a credential value or a URL with userinfo. Source labels come from `configSourceLabel` (via results) or `gitsource.Source.String()` of a parsed source.
- **Errors** are specific, never echo a token, and pass through `jam.ConfigStatus`: refused, stale or untracked 409; bad input 400; not managed 404; fetch failure 502; store 500 with a generic message, logged.
- **Tests** are hermetic. Postgres tests use the `integration` tag. Use `GOPROXY=https://proxy.golang.org,direct`.
- **Docs** change in the same change as the behavior. `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs` reports no more than **714** errors: 712 on main plus 2 for this plan file.

## Review Focus

1. **Releasing a role, whose name holds a slash** (`role/app/ic`), from the route, the client and the UI. It must release exactly that role, and a second release is 404. *(Task 3: `TestConfigReleaseRoute`; Task 7: `TestConfigPageRelease`.)*
2. **The Jam changes between Diff and Apply** (a teammate's edit, a new user). Apply answers 409 and writes nothing. Toggling force shows the forced plan, and Apply then carries that plan's digest. *(Task 8: `TestConfigApplyIsBoundToTheShownPlan`, `TestConfigPageForceOnlyWithDrift`.)*
3. **A managed object deleted in Jam.** It is `missing`, it raises `config.drift`, and its Config row does not link a 404 page. *(Task 2: `TestConfigMarkStates`; Task 7: `TestConfigPageShowsSourceAndManaged`.)*
4. **`config write` then `config apply --dir` with nothing to change.** Drift clears, and the next real repo change is not refused as drift (issue 010). *(Task 2: `TestWriteAndApplyClearsConfigDrift`; Task 4: `TestConfigWriteThenApplyRefreshesMark`.)*
5. **A kit in Jam whose current version does not parse.** The drift check neither crashes nor flaps (the condition keeps its state), and the Config page says the states are unknown. *(Task 2: `TestCheckConfigDriftKeepsStateOnError`; Task 7: `TestConfigPageDriftUnknown`.)*

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/jam/config_plan_delete.go`, `config_plan.go` (modify) | a refused drifted delete's op; `ConfigPlan.HasDrift`; `ConfigEnv.Conditions` |
| `internal/jam/config_drift.go` (create) | `ConfigMarkState`, `ConfigMarkStates`, `ConfigDriftKey`, `ConfigDriftEvery`, `CheckConfigDrift`, `checkConfigDriftLocked`, `RunConfigDriftCheck` |
| `internal/jam/config_apply.go` (modify) | re-check drift after an apply's write |
| `internal/jam/config_release.go` (create) | `ReleaseConfigMark` |
| `internal/jam/config_admin.go` (modify) | `DELETE /admin/config/managed/{kind}/{name...}` |
| `internal/jam/adminclient/adminclient.go` (modify) | `ConfigRelease` |
| `cmd/at-jam/configgit.go` (modify) | `config release`; the zero-change `--dir` apply; the drifted-delete print |
| `cmd/at-jam/main.go` (modify) | the drift checker goroutine; one `cfgEnv` for the admin API and the UI |
| `internal/jam/adminui/managed.go` (create) | `managedView`, `managedOf`, `driftNoteOf`, `shortConfigCommit`, `configTime` |
| `internal/jam/adminui/nav.go`, `adminui.go` (modify) | `frame.Marks`, the `managed`/`driftNote` funcs, the Config tab, `WithConfigEnv`, the `config` page |
| `internal/jam/adminui/context_edit.go`, `role_detail.go` (modify) | `contextPanel.Kind/Name`, `roleDetail.ConfigName` |
| `internal/jam/adminui/config_page.go` (create) | the Config page: data, handlers, plan view |
| `internal/jam/adminui/templates/config.html` (create); `layout.html`, `kit.html`, `destination(s).html`, `model_spec(s).html`, `kits.html`, `user.html`, `project.html`, `role.html`, `context_panel.html` (modify) | the page, the badge, the warnings |
| `docs/usage/jam/ui-config.md` (create); `config-git.md`, `config-git-sources.md`, `monitoring.md`, `ui.md`, `ui-editing.md`, `INDEX.md` (modify) | docs |

---

### Task 1: Drifted deletes show Jam's side; `ConfigPlan.HasDrift`

**Files:**
- Modify: `internal/jam/config_plan_delete.go` (`planDeletes`), `internal/jam/config_plan.go` (new method), `cmd/at-jam/configgit.go` (`printConfigPlan`)
- Test: `internal/jam/config_plan_delete_test.go`, `cmd/at-jam/configgit_test.go`

**Interfaces:**
- Consumes: slice 1's `planner.op`, `planner.refuseDrift`, `ConfigOp`, `ConfigPlan`.
- Produces:
```go
func (o ConfigPlan) HasDrift() bool // any op flagged Drift (refused or forced)
```

- [ ] **Step 1: Write the failing tests**

Append to `internal/jam/config_plan_delete_test.go`:

```go
// A drifted delete refused without --force still shows Jam's side in the
// plan (and writes nothing), as a refused drifted update does: the reviewer
// sees what a forced delete would drop, and the UI knows to offer force.
func TestPlanDeleteDriftShowsJamSide(t *testing.T) {
	st := appliedFull(t)
	d, _ := findDestination(st, "gh")
	d.Note = "edited"
	_ = st.AddDestination(d)
	p := planOn(t, st, treeOf(t, without(fullTreeFiles, "destinations/gh.yml")), false)
	o := opOf(p, ConfigKindDestination, "gh")
	if o.Action != ConfigDelete || !o.Drift || !strings.Contains(o.Before, "edited") || o.After != "" || !p.HasDrift() {
		t.Fatalf("%+v", o)
	}
	if slices.Contains(p.write.RemoveMarks, ConfigKey{ConfigKindDestination, "gh"}) || len(p.write.RemoveDestinations) != 0 {
		t.Fatal("a refused delete writes")
	}
	if planOn(t, appliedFull(t), treeOf(t, fullTreeFiles), false).HasDrift() {
		t.Fatal("a clean re-plan has drift")
	}
}
```

Append to `cmd/at-jam/configgit_test.go`:

```go
func TestPrintConfigPlanDriftedDelete(t *testing.T) {
	var b strings.Builder
	printConfigPlan(&b, jam.ConfigPlan{Ops: []jam.ConfigOp{{Kind: jam.ConfigKindUser, Name: "bob", Action: jam.ConfigDelete,
		Drift: true, Before: "# users/bob.yml\nkind: user\n"}}})
	want := "delete  user bob  (drift)\n  jam:\n    # users/bob.yml\n    kind: user\n  repo:\n    (not in the repo)\n"
	if !strings.HasPrefix(b.String(), want) {
		t.Fatalf("got %q", b.String())
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/ -run TestPlanDeleteDriftShowsJamSide && GOPROXY=https://proxy.golang.org,direct go test ./cmd/at-jam/ -run TestPrintConfigPlanDriftedDelete`
Expected: FAIL to compile with `p.HasDrift undefined`. The CLI test fails with `repo:\n    \n` in place of `(not in the repo)`.

- [ ] **Step 3: Implement**

In `internal/jam/config_plan_delete.go`, `planDeletes`, replace

```go
			drift := cur.Digest() != p.marks[key].Digest
			if drift && !p.in.Force {
				p.refuseDrift(key)
				continue
			}
```

with

```go
			drift := cur.Digest() != p.marks[key].Digest
			if drift && !p.in.Force {
				p.refuseDrift(key)
				p.op(key, ConfigDelete, true, cur.Canon, nil) // the diff shows Jam's side; no write
				continue
			}
```

In `internal/jam/config_plan.go`, after `Counts`:

```go
// HasDrift reports whether any op is flagged drift: an object edited in Jam
// since its last apply that the plan updates or deletes. A forced plan keeps
// the flag, so the force choice stays visible.
func (o ConfigPlan) HasDrift() bool {
	return slices.ContainsFunc(o.Ops, func(op ConfigOp) bool { return op.Drift })
}
```

In `cmd/at-jam/configgit.go`, `printConfigPlan`, replace

```go
		if op.Drift {
			fmt.Fprintf(w, "  jam:\n%s\n  repo:\n%s\n", indent(op.Before), indent(op.After))
		}
```

with

```go
		if op.Drift {
			repo := indent(op.After)
			if op.After == "" { // a delete: the repo no longer has it
				repo = "    (not in the repo)"
			}
			fmt.Fprintf(w, "  jam:\n%s\n  repo:\n%s\n", indent(op.Before), repo)
		}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/ ./cmd/at-jam/`
Expected: PASS. `TestPlanDeleteDrift` still passes: its forced op is unchanged.

- [ ] **Step 5: Commit**

```bash
git add internal/jam/config_plan.go internal/jam/config_plan_delete.go internal/jam/config_plan_delete_test.go cmd/at-jam/configgit.go cmd/at-jam/configgit_test.go
git commit -m "feat(jam): a refused drifted delete shows Jam's side; ConfigPlan.HasDrift

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: Mark states and the `config.drift` condition

**Files:**
- Create: `internal/jam/config_drift.go`, `internal/jam/config_drift_test.go`
- Modify: `internal/jam/config_plan.go` (`ConfigEnv`), `internal/jam/config_apply.go` (`applyConfigTreeLocked`), `cmd/at-jam/main.go`

**Interfaces:**
- Consumes: `exportObjects`, `stateFromSnapshot`, `configObject.Digest`, `configMu`, `condition.Tracker` (`Raise`, `Clear`, `IsOpen`, `Get`), `condition.Info`.
- Produces:
```go
const (
	ConfigInSync  = "in-sync"
	ConfigDrifted = "drifted"
	ConfigMissing = "missing"
)
const ConfigDriftKey = "config.drift:jam"
const ConfigDriftEvery = time.Minute
type ConfigMarkState struct {
	ConfigMark
	State string `json:"state"` // ConfigInSync | ConfigDrifted | ConfigMissing
}
func ConfigMarkStates(store Store) ([]ConfigMarkState, error) // nil, nil without marks
func CheckConfigDrift(store Store, t *condition.Tracker, log *slog.Logger) // takes configMu; t nil: no-op
func checkConfigDriftLocked(store Store, t *condition.Tracker, log *slog.Logger)
func RunConfigDriftCheck(ctx context.Context, store Store, t *condition.Tracker, every time.Duration, log *slog.Logger)
// ConfigEnv gains: Conditions *condition.Tracker // config.drift is re-checked after each apply; nil = none
```

- [ ] **Step 1: Write the failing tests**

Create `internal/jam/config_drift_test.go`:

```go
package jam

import (
	"context"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/condition"
	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

// editGH changes destination gh in Jam: it drifts from its mark.
func editGH(t *testing.T, st *MemStore, note string) {
	t.Helper()
	d, ok := findDestination(st, "gh")
	if !ok {
		t.Fatal("no destination gh")
	}
	d.Note = note
	if err := st.AddDestination(d); err != nil {
		t.Fatal(err)
	}
}

func TestConfigMarkStates(t *testing.T) {
	if s, err := ConfigMarkStates(NewMemStore()); s != nil || err != nil {
		t.Fatalf("no marks: %v %v", s, err)
	}
	st := appliedFull(t)
	states, err := ConfigMarkStates(st)
	if err != nil || len(states) != len(st.ListConfigMarks()) {
		t.Fatalf("%d states for %d marks: %v", len(states), len(st.ListConfigMarks()), err)
	}
	for _, s := range states {
		if s.State != ConfigInSync {
			t.Fatalf("%s right after apply: %s", s.Key(), s.State)
		}
	}
	editGH(t, st, "edited in Jam")
	if err := st.SetJamContext(sessionctx.Layer{}); err != nil { // the Jam context is gone from the live Jam
		t.Fatal(err)
	}
	states, err = ConfigMarkStates(st)
	if err != nil {
		t.Fatal(err)
	}
	want := map[ConfigKey]string{{ConfigKindDestination, "gh"}: ConfigDrifted, {ConfigKindJamContext, "jam"}: ConfigMissing}
	for _, s := range states {
		w, ok := want[s.Key()]
		if !ok {
			w = ConfigInSync
		}
		if s.State != w {
			t.Errorf("%s: %s, want %s", s.Key(), s.State, w)
		}
	}
}

func TestCheckConfigDriftRaisesAndApplyClears(t *testing.T) {
	st := appliedFull(t)
	tr := condition.New(condition.Options{})
	CheckConfigDrift(st, tr, nil)
	if tr.IsOpen(ConfigDriftKey) {
		t.Fatal("raised with nothing drifted")
	}
	editGH(t, st, "edited in Jam")
	CheckConfigDrift(st, tr, nil)
	c, ok := tr.Get(ConfigDriftKey)
	if !ok || c.Severity != condition.Info || c.Summary != "1 managed object(s) differ from the config repo" ||
		c.Detail != "destination gh (drifted)" || c.Fix == "" {
		t.Fatalf("%+v %v", c, ok)
	}
	// apply --force restores the repo's version and clears it in the same call.
	env := allCreds
	env.Conditions = tr
	opt := dirOpts()
	opt.Force = true
	if _, err := ApplyConfigTree(context.Background(), st, nil, treeOf(t, fullTreeFiles), env, opt, nil, "alice"); err != nil {
		t.Fatal(err)
	}
	if tr.IsOpen(ConfigDriftKey) {
		t.Fatal("apply --force left config.drift open")
	}
}

// config write then apply (Jam wins): the plan has nothing to change, but
// its metadata-only write refreshes the stale mark, and the check clears.
func TestWriteAndApplyClearsConfigDrift(t *testing.T) {
	st := appliedFull(t)
	tr := condition.New(condition.Options{})
	editGH(t, st, "kept")
	CheckConfigDrift(st, tr, nil)
	if !tr.IsOpen(ConfigDriftKey) {
		t.Fatal("not raised")
	}
	exp, err := ExportConfigTree(st.ExportConfig(), st.ListConfigMarks(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	env := allCreds
	env.Conditions = tr
	res, err := ApplyConfigTree(context.Background(), st, nil, treeOf(t, exp.Files), env, dirOpts(), nil, "alice")
	if err != nil || res.Applied {
		t.Fatalf("write-and-apply: %+v %v", res, err)
	}
	if tr.IsOpen(ConfigDriftKey) {
		t.Fatal("write-and-apply left config.drift open")
	}
}

// A live Jam that cannot be rendered (a kit that does not parse) leaves the
// condition as it was: no crash, no flapping.
func TestCheckConfigDriftKeepsStateOnError(t *testing.T) {
	st := appliedFull(t)
	tr := condition.New(condition.Options{})
	editGH(t, st, "edited in Jam")
	CheckConfigDrift(st, tr, nil)
	if _, err := st.PushKit("bad", "listen: :443"); err != nil { // not a studio kit
		t.Fatal(err)
	}
	if _, err := ConfigMarkStates(st); err == nil {
		t.Fatal("an unparseable kit rendered")
	}
	CheckConfigDrift(st, tr, nil)
	if !tr.IsOpen(ConfigDriftKey) {
		t.Fatal("an unrenderable Jam cleared config.drift")
	}
}

func TestRunConfigDriftCheckChecksAtStart(t *testing.T) {
	st := appliedFull(t)
	editGH(t, st, "edited in Jam")
	tr := condition.New(condition.Options{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { RunConfigDriftCheck(ctx, st, tr, time.Hour, nil); close(done) }()
	for deadline := time.Now().Add(5 * time.Second); !tr.IsOpen(ConfigDriftKey) && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if !tr.IsOpen(ConfigDriftKey) {
		t.Fatal("RunConfigDriftCheck did not check at start")
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/ -run 'ConfigMarkStates|ConfigDrift'`
Expected: FAIL to compile with `undefined: ConfigMarkStates`, `undefined: CheckConfigDrift`, and `env.Conditions undefined`.

- [ ] **Step 3: Implement**

In `internal/jam/config_plan.go`, add `"github.com/aethons-tools/cove/internal/jam/condition"` to the imports and a field to `ConfigEnv`:

```go
type ConfigEnv struct {
	CredExists     func(string) bool
	PoolConfigured bool
	Serve          map[string]any     // the running serve's portable config; nil = unknown
	Conditions     *condition.Tracker // config.drift is re-checked after every apply; nil = none
}
```

Create `internal/jam/config_drift.go`:

```go
package jam

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/jam/condition"
)

// A managed object's state against its mark.
const (
	ConfigInSync  = "in-sync"
	ConfigDrifted = "drifted" // edited in Jam since the apply that set the mark
	ConfigMissing = "missing" // removed in Jam since
)

// ConfigDriftKey is the config.drift condition's key: one for the Jam.
const ConfigDriftKey = "config.drift:jam"

// ConfigDriftEvery is how often serve re-checks drift; applies, syncs and
// releases re-check at once.
const ConfigDriftEvery = time.Minute

// configDriftFix is the condition's fix: the three ways out.
const configDriftFix = "Review with at-jam config diff (or the Config page), then: config apply --force to restore the repo's version, " +
	"config write + commit + config apply to keep Jam's, or config release <kind>/<name> to stop managing it."

// ConfigMarkState is a managed mark and how its live object compares.
type ConfigMarkState struct {
	ConfigMark
	State string `json:"state"` // ConfigInSync | ConfigDrifted | ConfigMissing
}

// ConfigMarkStates compares every managed mark with its live object's
// digest, in the canonical form the planner uses (exportObjects): in sync,
// drifted or missing. It renders every live object once (nothing without
// marks). Marks are read before the snapshot; hold configMu for a reading
// no apply lands inside.
func ConfigMarkStates(store Store) ([]ConfigMarkState, error) {
	marks := store.ListConfigMarks()
	if len(marks) == 0 {
		return nil, nil
	}
	live, err := exportObjects(stateFromSnapshot(store.ExportConfig(), nil), nil)
	if err != nil {
		return nil, fmt.Errorf("config drift: %w", err)
	}
	out := make([]ConfigMarkState, 0, len(marks))
	for _, mk := range marks {
		state := ConfigInSync
		switch o, ok := live[mk.Key()]; {
		case !ok:
			state = ConfigMissing
		case o.Digest() != mk.Digest:
			state = ConfigDrifted
		}
		out = append(out, ConfigMarkState{ConfigMark: mk, State: state})
	}
	return out, nil
}

// CheckConfigDrift raises config.drift (info) while any managed object has
// drifted or gone missing, and clears it otherwise. It holds configMu, so
// it never compares marks and objects from either side of an apply. t nil:
// a no-op.
func CheckConfigDrift(store Store, t *condition.Tracker, log *slog.Logger) {
	if t == nil {
		return
	}
	configMu.Lock()
	defer configMu.Unlock()
	checkConfigDriftLocked(store, t, log)
}

// checkConfigDriftLocked is CheckConfigDrift for a caller holding configMu.
// A Jam it cannot render leaves the condition as it was.
func checkConfigDriftLocked(store Store, t *condition.Tracker, log *slog.Logger) {
	if t == nil {
		return
	}
	states, err := ConfigMarkStates(store)
	if err != nil {
		if log != nil {
			log.Warn("config drift: check failed; config.drift unchanged", "err", err.Error())
		}
		return
	}
	var lines []string
	for _, s := range states {
		if s.State != ConfigInSync {
			lines = append(lines, s.Key().String()+" ("+s.State+")")
		}
	}
	if len(lines) == 0 {
		t.Clear(ConfigDriftKey)
		return
	}
	t.Raise(condition.Condition{Key: ConfigDriftKey, Severity: condition.Info,
		Summary: fmt.Sprintf("%d managed object(s) differ from the config repo", len(lines)),
		Detail:  strings.Join(lines, "\n"), // names only; the tracker clips it
		Fix:     configDriftFix})
}

// RunConfigDriftCheck checks drift now and every `every` until ctx ends.
func RunConfigDriftCheck(ctx context.Context, store Store, t *condition.Tracker, every time.Duration, log *slog.Logger) {
	if t == nil {
		return
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		CheckConfigDrift(store, t, log)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
```

In `internal/jam/config_apply.go`, `applyConfigTreeLocked`, replace the tail from `if !changes {` with:

```go
	if !changes {
		// Nothing to apply; still record what moved without changing an
		// object — the .source.yml kits' tracked commits, the config
		// source's commit, the stale marks of unchanged objects: no
		// version, no other mark, no audit entry.
		if meta := (ConfigWrite{Kits: w.Kits, Marks: plan.refreshMarks, ConfigSource: w.ConfigSource}); !meta.Empty() {
			if err := store.ApplyConfig(meta); err != nil {
				return res, fmt.Errorf("config apply: write: %w", err)
			}
		}
		checkConfigDriftLocked(store, env.Conditions, log) // a refreshed mark can clear config.drift
		return res, nil
	}
	if err := store.ApplyConfig(w); err != nil {
		return res, fmt.Errorf("config apply: write: %w", err)
	}
	res.Applied = true
	logConfigApply(log, actor, opt, plan)
	checkConfigDriftLocked(store, env.Conditions, log)
	return res, nil
}
```

In `cmd/at-jam/main.go`, after `go conds.Run(context.Background(), 5*time.Second)`:

```go
	// config.drift (docs/usage/jam/config-git.md#drift-and-release): checked
	// now, every minute, and after each config apply or release.
	go jam.RunConfigDriftCheck(context.Background(), st, conds, jam.ConfigDriftEvery, log)
```

and in the `jam.WithConfig(...)` admin option, add the tracker to the env:

```go
			jam.WithConfig(st, kitFetcher, jam.ConfigEnv{CredExists: credExists, PoolConfigured: cfg.Pool != nil, Serve: portable, Conditions: conds}, log))
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/... ./cmd/at-jam/ && GOPROXY=https://proxy.golang.org,direct go vet ./cmd/at-jam/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/jam/config_drift.go internal/jam/config_drift_test.go internal/jam/config_plan.go internal/jam/config_apply.go cmd/at-jam/main.go
git commit -m "feat(jam): config mark states and the config.drift condition

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: `config release` in jam, the admin route and the client

**Files:**
- Create: `internal/jam/config_release.go`, `internal/jam/config_release_test.go`
- Modify: `internal/jam/config_admin.go` (`WithConfig`), `internal/jam/adminclient/adminclient.go`
- Test: `internal/jam/config_admin_test.go`, `internal/jam/adminclient/config_test.go`

**Interfaces:**
- Consumes: `configMu`, `configKindOrder`, `clipName`, `writeErr`, `checkConfigDriftLocked` (Task 2), `Store.ApplyConfig(ConfigWrite{RemoveMarks})`.
- Produces:
```go
func ReleaseConfigMark(store Store, key ConfigKey, t *condition.Tracker, log *slog.Logger, actor string) error // 400 unknown kind, 404 not managed
// route: DELETE /admin/config/managed/{kind}/{name...} → 204
func (c *Client) ConfigRelease(kind, name string) error // a role's name is "<project>/<role>"; ErrNotFound when unmanaged
```

- [ ] **Step 1: Write the failing tests**

Create `internal/jam/config_release_test.go`:

```go
package jam

import (
	"net/http"
	"slices"
	"testing"

	"github.com/aethons-tools/cove/internal/jam/condition"
)

func TestReleaseConfigMark(t *testing.T) {
	st := appliedFull(t)
	tr := condition.New(condition.Options{})
	editGH(t, st, "edited in Jam")
	CheckConfigDrift(st, tr, nil)
	gh := ConfigKey{ConfigKindDestination, "gh"}
	if err := ReleaseConfigMark(st, gh, tr, nil, "alice"); err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(st.ListConfigMarks(), func(m ConfigMark) bool { return m.Key() == gh }) {
		t.Fatal("mark kept")
	}
	if _, ok := findDestination(st, "gh"); !ok {
		t.Fatal("release removed the object")
	}
	if tr.IsOpen(ConfigDriftKey) {
		t.Fatal("release of the only drifted object left config.drift open")
	}
	// A repo without it now leaves it alone: unmanaged, not a delete.
	p := planOn(t, st, treeOf(t, without(fullTreeFiles, "destinations/gh.yml")), false)
	if opOf(p, ConfigKindDestination, "gh").Action != "" || !slices.Contains(p.Unmanaged, gh) {
		t.Fatalf("after release: %+v unmanaged=%v", opOf(p, ConfigKindDestination, "gh"), p.Unmanaged)
	}
	if err := ReleaseConfigMark(st, gh, tr, nil, "alice"); WriteStatus(err, 0) != http.StatusNotFound || err.Error() != "config release: destination gh is not managed" {
		t.Fatalf("second release: %v", err)
	}
	if err := ReleaseConfigMark(st, ConfigKey{"widget", "x"}, tr, nil, "alice"); WriteStatus(err, 0) != http.StatusBadRequest {
		t.Fatalf("unknown kind: %v", err)
	}
}
```

Append to `internal/jam/config_admin_test.go`:

```go
// A role's name keeps its slash in the route.
func TestConfigReleaseRoute(t *testing.T) {
	st := appliedFull(t)
	ts := configServer(t, st)
	ic := ConfigKey{ConfigKindRole, "app/ic"}
	sendJSON(t, "DELETE", ts.URL+"/admin/config/managed/role/app/ic", "", http.StatusNoContent, nil)
	for _, mk := range st.ListConfigMarks() {
		if mk.Key() == ic {
			t.Fatal("role app/ic still managed")
		}
	}
	if len(st.ListConfigMarks()) != len(fullTreeFiles)-1-1 { // every file is one object but projects/app/context.md (part of the project); minus the released role
		t.Fatalf("release removed other marks: %v", st.ListConfigMarks())
	}
	sendJSON(t, "DELETE", ts.URL+"/admin/config/managed/role/app/ic", "", http.StatusNotFound, nil)
	sendJSON(t, "DELETE", ts.URL+"/admin/config/managed/widget/x", "", http.StatusBadRequest, nil)
}
```

Append to `TestConfigRoundTrip` in `internal/jam/adminclient/config_test.go`, after the export checks:

```go
	if err := c.ConfigRelease(jam.ConfigKindUser, "bob"); err != nil {
		t.Fatalf("ConfigRelease = %v", err)
	}
	if len(store.ListConfigMarks()) != 0 {
		t.Fatalf("marks after release: %v", store.ListConfigMarks())
	}
	if err := c.ConfigRelease(jam.ConfigKindUser, "bob"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second ConfigRelease = %v, want ErrNotFound", err)
	}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/ ./internal/jam/adminclient/ -run 'Release|ConfigRoundTrip'`
Expected: FAIL to compile with `undefined: ReleaseConfigMark` and `c.ConfigRelease undefined`.

- [ ] **Step 3: Implement**

Create `internal/jam/config_release.go`:

```go
package jam

import (
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/aethons-tools/cove/internal/jam/condition"
)

// ReleaseConfigMark clears key's managed mark: the object stays as it is,
// unmanaged — the next plan lists it as unmanaged, or adopts or refuses it
// if the repo still holds it. An unknown kind is 400, an object with no mark
// 404. It holds configMu (no apply interleaves), then re-checks config.drift
// (t nil: none).
func ReleaseConfigMark(store Store, key ConfigKey, t *condition.Tracker, log *slog.Logger, actor string) error {
	if !slices.Contains(configKindOrder, key.Kind) {
		return writeErr(http.StatusBadRequest, "config release: unknown kind (want one of %s)", strings.Join(configKindOrder, ", "))
	}
	configMu.Lock()
	defer configMu.Unlock()
	if !slices.ContainsFunc(store.ListConfigMarks(), func(m ConfigMark) bool { return m.Key() == key }) {
		return writeErr(http.StatusNotFound, "config release: %s %s is not managed", key.Kind, clipName(key.Name))
	}
	if err := store.ApplyConfig(ConfigWrite{RemoveMarks: []ConfigKey{key}}); err != nil {
		return fmt.Errorf("config release: %w", err)
	}
	if log != nil {
		log.Info("config released", "operator", actor, "kind", key.Kind, "name", key.Name)
	}
	checkConfigDriftLocked(store, t, log)
	return nil
}
```

In `internal/jam/config_admin.go`, add to the `WithConfig` doc comment's route list:

```go
//	DELETE /admin/config/managed/{kind}/{name}       release a mark (a role's name is <project>/<role>)
```

and, inside the returned `func(mux *http.ServeMux)`, after the export-tree route:

```go
		mux.HandleFunc("DELETE /admin/config/managed/{kind}/{name...}", func(w http.ResponseWriter, r *http.Request) {
			key := ConfigKey{Kind: r.PathValue("kind"), Name: r.PathValue("name")}
			if err := ReleaseConfigMark(store, key, env.Conditions, log, OperatorID(r)); err != nil {
				fail(w, r, "release", err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		})
```

In `internal/jam/adminclient/adminclient.go`, after `ConfigExportTree`:

```go
// ConfigRelease clears kind/name's managed mark (a role's name is
// "<project>/<role>"); ErrNotFound when it has none.
func (c *Client) ConfigRelease(kind, name string) error {
	segs := strings.Split(name, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return c.do("DELETE", "/admin/config/managed/"+url.PathEscape(kind)+"/"+strings.Join(segs, "/"), nil, nil)
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/...`
Expected: PASS. If the mark count in `TestConfigReleaseRoute` is off, count the marks `appliedFull` leaves (`len(st.ListConfigMarks())` before the release) and assert one fewer. Never loosen the test to `!= 0`.

- [ ] **Step 5: Commit**

```bash
git add internal/jam/config_release.go internal/jam/config_release_test.go internal/jam/config_admin.go internal/jam/config_admin_test.go internal/jam/adminclient/adminclient.go internal/jam/adminclient/config_test.go
git commit -m "feat(jam): config release — clear a managed mark (route and client)

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: CLI — `config release`; a zero-change `apply --dir` still applies (issue 010)

**Files:**
- Modify: `cmd/at-jam/configgit.go` (`cmdConfig`)
- Test: `cmd/at-jam/configgit_test.go`

**Interfaces:**
- Consumes: `adminclient.(*Client).ConfigRelease` (Task 3); the server's metadata-only write (slice 2, `plan.refreshMarks`).
- Produces: `at-jam config release <kind>/<name>`. Exit 0 on success, 1 when the server refuses (not managed, unknown kind), and 2 on a malformed argument.

- [ ] **Step 1: Write the failing tests**

Append to `cmd/at-jam/configgit_test.go`:

```go
func TestConfigRelease(t *testing.T) {
	url, st := configServer(t)
	repo := gitDir(t)
	mustWrite(t, filepath.Join(repo, "users/bob.yml"), "kind: user\nversion: 1\n", 0o644)
	if code, out, errs := runJam("config", "apply", "--dir", repo, "--yes", "--admin-url", url); code != 0 {
		t.Fatalf("apply: %d %s %s", code, out, errs)
	}
	code, out, errs := runJam("config", "release", "user/bob", "--admin-url", url)
	if code != 0 || !strings.Contains(out, "released user bob") || len(st.ListConfigMarks()) != 0 {
		t.Fatalf("release: %d\n%s\n%s marks=%v", code, out, errs, st.ListConfigMarks())
	}
	findUser(t, st, "bob") // the object stays
	if code, _, errs := runJam("config", "release", "user/bob", "--admin-url", url); code != 1 || !strings.Contains(errs, "user bob is not managed") {
		t.Fatalf("second release: %d %s", code, errs)
	}
	for _, bad := range [][]string{{"bob"}, {"user/"}, {"/bob"}, {}, {"user/bob", "extra"}} {
		args := append([]string{"config", "release"}, bad...)
		if code, _, _ := runJam(append(args, "--admin-url", url)...); code != 2 {
			t.Errorf("release %v: exit %d, want 2", bad, code)
		}
	}
}

// config write captures a Jam-side edit; the following apply --dir has
// nothing to change but still reaches the server, whose metadata-only write
// refreshes the stale mark — so the next real repo change is not refused as
// drift (issue 010), and there is no prompt.
func TestConfigWriteThenApplyRefreshesMark(t *testing.T) {
	url, st := configServer(t)
	repo := gitDir(t)
	mustWrite(t, filepath.Join(repo, "users/bob.yml"), "kind: user\nversion: 1\n", 0o644)
	if code, out, errs := runJam("config", "apply", "--dir", repo, "--yes", "--admin-url", url); code != 0 {
		t.Fatalf("apply: %d %s %s", code, out, errs)
	}
	cfgMust(t, st.SetUserLogins(findUser(t, st, "bob").ID, []string{"bob@jam"}))
	if code, out, errs := runJam("config", "write", repo, "--admin-url", url); code != 0 {
		t.Fatalf("write: %d %s %s", code, out, errs)
	}
	code, out, errs := runJam("config", "apply", "--dir", repo, "--admin-url", url) // no --yes: must not prompt
	if code != 0 || !strings.Contains(out, "nothing to apply") || strings.Contains(out, "[y/N]") {
		t.Fatalf("zero-change apply: %d\n%s\n%s", code, out, errs)
	}
	mustWrite(t, filepath.Join(repo, "users/bob.yml"), "kind: user\nversion: 1\nlogins: [bob@repo]\n", 0o644)
	code, out, errs = runJam("config", "apply", "--dir", repo, "--yes", "--admin-url", url)
	if code != 0 || strings.Contains(out, "drift") || !strings.Contains(out, "1 updated") {
		t.Fatalf("next change: %d\n%s\n%s", code, out, errs)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./cmd/at-jam/ -run 'TestConfigRelease|TestConfigWriteThenApplyRefreshesMark'`
Expected: FAIL. `release` exits 2 with the usage text. The second apply in the refresh test exits 1 with `user bob drifted`.

- [ ] **Step 3: Implement**

In `cmd/at-jam/configgit.go`:

1. Doc comment and usage. The comment's verb list gains `` `config release <kind>/<name>` ``, and the `usage` constant gains ` | release <kind>/<name>` at its end.
2. The subcommand switch becomes `case "write", "diff", "apply", "track", "sync", "untrack", "release":`.
3. After the `if sub == "write" { … }` block, add:

```go
	if sub == "release" {
		if len(pos) != 1 {
			fmt.Fprintln(stderr, "at-jam config release: expected one <kind>/<name> (a role: role/<project>/<role>)")
			return 2
		}
		kind, name, ok := strings.Cut(pos[0], "/")
		if !ok || kind == "" || name == "" {
			fmt.Fprintln(stderr, "at-jam config release: expected one <kind>/<name> (a role: role/<project>/<role>)")
			return 2
		}
		c, err := client()
		if err != nil {
			return fail(err)
		}
		if err := c.ConfigRelease(kind, name); err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "released %s %s: it stays in Jam, unmanaged\n", kind, name)
		return 0
	}
```

4. Delete the early exit and its comment:

```go
	// A git target with nothing to change still applies: the server makes
	// the metadata-only write (tracked commits, the config source's commit).
	if n == 0 && t.tree != nil {
		fmt.Fprintln(stdout, "nothing to apply")
		return 0
	}
```

and put this comment above `if n > 0 && !*yes {`:

```go
	// A plan with nothing to change still applies, without a prompt: the
	// server's metadata-only write refreshes stale marks (after a config
	// write, say) and, from git, the tracked commits.
```

`if !applied.Applied || n == 0 { fmt.Fprintln(stdout, "nothing to apply") … }` below already prints the outcome.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./cmd/at-jam/`
Expected: PASS. `TestConfigWriteThenApplyReplicates` (second apply prints `nothing to apply`, no prompt) still passes.

- [ ] **Step 5: Commit**

```bash
git add cmd/at-jam/configgit.go cmd/at-jam/configgit_test.go
git commit -m "feat(at-jam): config release; a zero-change apply --dir refreshes marks

Closes issue 010 (--dir config apply should refresh stale marks like git applies do).

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: UI — managed badges and drift warnings: the mechanism, destinations, model specs, kits

**Files:**
- Create: `internal/jam/adminui/managed.go`, `internal/jam/adminui/managed_test.go`
- Modify: `internal/jam/adminui/nav.go` (`frame`, `frameSource`, `frameFor`, `fragmentFrame`), `internal/jam/adminui/adminui.go` (`funcs`, `frameFuncs`)
- Modify templates: `layout.html`, `destination.html`, `destinations.html`, `model_spec.html`, `model_specs.html`, `kit.html`, `kits.html`

**Interfaces:**
- Consumes: `jam.Store.ListConfigMarks`, `jam.ConfigKey`, `jam.ConfigMark`; for test fixtures, `jam.LoadConfigTree` and `jam.ApplyConfigTree`.
- Produces (later tasks use these):
```go
type managedView struct{ Commit, Applied string }
func managedOf(marks map[jam.ConfigKey]jam.ConfigMark, kind, name string) *managedView // nil = unmanaged
func driftNoteOf(marks map[jam.ConfigKey]jam.ConfigMark, kind, name string) string   // "" = unmanaged
func shortConfigCommit(c string) string // 7 chars
func configTime(t time.Time) string     // "2006-01-02 15:04 UTC"
// frame gains: Marks map[jam.ConfigKey]jam.ConfigMark
// template funcs: managed kind name → *managedView; driftNote kind name → string
// template: {{template "managed-badge" (managed "<kind>" <name>)}}
// test helpers (managed_test.go): managedSHA, managedTree, applyManaged, managedUI, managedBadge, driftWarning, writeTag, warns
```

- [ ] **Step 1: Write the failing test**

Create `internal/jam/adminui/managed_test.go`:

```go
package adminui_test

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
	"github.com/aethons-tools/cove/internal/jam/gitsource"
)

// managedSHA is the config commit the fixtures apply from.
const managedSHA = "abc1234def5678abc1234def5678abc1234def56"

const (
	managedBadge = "managed · config @abc1234"
	driftWarning = "This will drift from the config repo (managed · config @abc1234)."
)

// managedTree holds every kind with a UI page once.
var managedTree = map[string]string{
	"users/bob.yml":             "kind: user\nversion: 1\nlogins: [bob@example.com]\n",
	"destinations/gh.yml":       "kind: destination\nversion: 1\nroute: /gh/\nupstream: https://api.github.com\n",
	"model-specs/fast.yml":      "kind: model-spec\nversion: 1\ntype: claude\nharness-version: 2.1.3\nprincipal: {credential: anthropic}\nclaude: {provider: anthropic}\n",
	"kits/web-dev/kit.yml":      "kind: studio\nname: web-dev\nprompt: one\n",
	"projects/app/project.yml":  "kind: project\nversion: 1\nmembers: [{user: bob}]\n",
	"projects/app/roles/ic.yml": "kind: role\nversion: 1\nkit: web-dev\nmodel-spec: fast\nscope: {destinations: [gh], ttl: 1h}\n",
	"context.md":                "Be kind.\n",
}

// applyManaged applies files to store from https://github.com/acme/jam@main
// at managedSHA, as a sync would.
func applyManaged(t *testing.T, store jam.Store, files map[string]string) {
	t.Helper()
	dir := t.TempDir()
	for p, b := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(b), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tree, err := jam.LoadConfigTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	res, err := jam.ApplyConfigTree(context.Background(), store, nil, tree, jam.ConfigEnv{CredExists: anyCred},
		jam.ConfigApplyOptions{Source: gitsource.Source{Repo: "https://github.com/acme/jam", Ref: "main"}, Commit: managedSHA, Now: time.Unix(1000, 0)},
		nil, "test")
	if err != nil || !res.Applied {
		t.Fatalf("apply: %+v %v", res, err)
	}
}

// managedUI is a Jam with managedTree applied, and its UI.
func managedUI(t *testing.T, opts ...adminui.Option) (http.Handler, jam.Store) {
	t.Helper()
	store := newStore(t)
	applyManaged(t, store, managedTree)
	return adminui.Handler(store, testLogger(), nil, nil, anyCred, nil, opts...), store
}

// writeTag is the opening tag of the first element whose attr (hx-post,
// hx-delete) is exactly action; "" when there is none.
func writeTag(body, attr, action string) string {
	i := strings.Index(body, attr+`="`+action+`"`)
	if i < 0 {
		return ""
	}
	start := strings.LastIndex(body[:i], "<")
	end := strings.Index(body[i:], ">")
	return body[start : i+end+1]
}

// warns reports whether page's write control attr=action confirms the drift
// warning first; it fails the test when the control is missing.
func warns(t *testing.T, body, attr, action string) bool {
	t.Helper()
	tag := writeTag(body, attr, action)
	if tag == "" {
		t.Fatalf("no element with %s=%q in:\n%s", attr, action, body)
	}
	return strings.Contains(tag, `hx-confirm="`) && strings.Contains(tag, driftWarning)
}

type warnCase struct {
	page, attr, action string
	want               bool
}

func checkWarns(t *testing.T, h http.Handler, cases []warnCase) {
	t.Helper()
	for _, c := range cases {
		if got := warns(t, get(t, h, c.page).Body.String(), c.attr, c.action); got != c.want {
			t.Errorf("%s: %s=%s warns=%v, want %v", c.page, c.attr, c.action, got, c.want)
		}
	}
}

func TestManagedSpecsBadgeAndDriftWarning(t *testing.T) {
	f := &srcFetcher{t: t, refs: map[string]string{"main": srcSHA1}, kits: map[string]string{srcSHA1: srcKitV1}}
	h, store := managedUI(t, adminui.WithKitFetcher(f))
	if err := jam.CreateDestination(store, jam.Destination{Name: "own", Route: "/own/", Upstream: "https://own.example"}, anyCred); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PushKit("web-dev", "kind: studio\nname: web-dev\nprompt: two\n"); err != nil { // v2: v1 offers Pin
		t.Fatal(err)
	}
	checkWarns(t, h, []warnCase{
		{"/ui/destinations/gh", "hx-post", "/ui/destinations/gh", true},
		{"/ui/destinations/gh", "hx-delete", "/ui/destinations/gh", true},
		{"/ui/destinations/own", "hx-post", "/ui/destinations/own", false},
		{"/ui/destinations/own", "hx-delete", "/ui/destinations/own", false},
		{"/ui/destinations", "hx-delete", "/ui/destinations/gh", true},
		{"/ui/destinations", "hx-delete", "/ui/destinations/own", false},
		{"/ui/model-specs/fast", "hx-post", "/ui/model-specs/fast", true},
		{"/ui/model-specs/fast", "hx-delete", "/ui/model-specs/fast", true},
		{"/ui/model-specs", "hx-delete", "/ui/model-specs/fast", true},
		{"/ui/kits/web-dev", "hx-post", "/ui/kits/web-dev/versions", true},
		{"/ui/kits/web-dev", "hx-post", "/ui/kits/web-dev/pin", true},
		{"/ui/kits/web-dev", "hx-post", "/ui/kits/web-dev/source", true},
	})
	for page, want := range map[string]bool{"/ui/destinations/gh": true, "/ui/model-specs/fast": true, "/ui/kits/web-dev": true, "/ui/destinations/own": false} {
		body := get(t, h, page).Body.String()
		if got := strings.Contains(body, `class="chip managed" href="/ui/config"`) && strings.Contains(body, ">"+managedBadge+"</a>"); got != want {
			t.Errorf("%s: badge %v, want %v", page, got, want)
		}
	}
	// A save's re-rendered body keeps the badge and the warning.
	rec := post(t, h, "/ui/destinations/gh", url.Values{"route": {"/gh/"}, "upstream": {"https://api.github.com"}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), ">"+managedBadge+"</a>") || !warns(t, rec.Body.String(), "hx-post", "/ui/destinations/gh") {
		t.Fatalf("save fragment: %d\n%s", rec.Code, rec.Body.String())
	}
	// A managed kit that tracks its own source: Sync and Untrack warn too.
	trackedKit(t, store, f, "https://github.com/acme/kits")
	checkWarns(t, h, []warnCase{
		{"/ui/kits/web-dev", "hx-post", "/ui/kits/web-dev/sync", true},
		{"/ui/kits/web-dev", "hx-delete", "/ui/kits/web-dev/source", true},
	})
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/adminui/ -run TestManagedSpecsBadgeAndDriftWarning`
Expected: FAIL. `/ui/destinations/gh: hx-post=/ui/destinations/gh warns=false, want true` (and the same for every managed row), and `badge false, want true`.

- [ ] **Step 3: Implement the mechanism**

Create `internal/jam/adminui/managed.go`:

```go
package adminui

import (
	"time"

	"github.com/aethons-tools/cove/internal/jam"
)

// managedView is a managed object's badge: the config commit its mark was
// applied from (7 chars; "dir" for a --dir apply) and when.
type managedView struct {
	Commit, Applied string
}

// shortConfigCommit is a config commit as the UI shows it, like the CLI's.
func shortConfigCommit(c string) string {
	if len(c) > 7 {
		return c[:7]
	}
	return c
}

// configTime is a config apply's time as the UI shows it.
func configTime(t time.Time) string { return t.UTC().Format("2006-01-02 15:04 UTC") }

// managedOf is kind/name's badge; nil when the config repo does not manage it.
func managedOf(marks map[jam.ConfigKey]jam.ConfigMark, kind, name string) *managedView {
	mk, ok := marks[jam.ConfigKey{Kind: kind, Name: name}]
	if !ok {
		return nil
	}
	return &managedView{Commit: shortConfigCommit(mk.Commit), Applied: configTime(mk.AppliedAt)}
}

// driftNoteOf is the warning a write to managed kind/name confirms before it
// is sent; "" for an unmanaged object.
func driftNoteOf(marks map[jam.ConfigKey]jam.ConfigMark, kind, name string) string {
	m := managedOf(marks, kind, name)
	if m == nil {
		return ""
	}
	return "This will drift from the config repo (managed · config @" + m.Commit + ")."
}
```

In `internal/jam/adminui/nav.go`:
- In `frame`, after `Items []attnItem`, add `Marks map[jam.ConfigKey]jam.ConfigMark // the managed marks: badges and drift warnings`.
- Add after `items()`:

```go
// marks is every managed mark by object.
func (s frameSource) marks() map[jam.ConfigKey]jam.ConfigMark {
	out := map[jam.ConfigKey]jam.ConfigMark{}
	for _, mk := range s.store.ListConfigMarks() {
		out[mk.Key()] = mk
	}
	return out
}
```

- In `frameFor`, change `f := frame{Kind: meta.Kind, All: items, Brand: src.brand()}` to `f := frame{Kind: meta.Kind, All: items, Brand: src.brand(), Marks: src.marks()}`.
- In `fragmentFrame` (in `adminui.go`), change `f := frame{Kind: meta.Kind, All: src.items()}` to `f := frame{Kind: meta.Kind, All: src.items(), Marks: src.marks()}`.

In `internal/jam/adminui/adminui.go`, add to `funcs` (the defaults a fragment without a frame renders with), after `"agentHref": …`:

```go
	"managed":   func(kind, name string) *managedView { return nil },
	"driftNote": func(kind, name string) string { return "" },
```

and to the map `frameFuncs` returns, after `"agentHref"`:

```go
		"managed":   func(kind, name string) *managedView { return managedOf(f.Marks, kind, name) },
		"driftNote": func(kind, name string) string { return driftNoteOf(f.Marks, kind, name) },
```

In `templates/layout.html`, add after the `.chip button:hover` style rule:

```css
    .chip.managed{background:var(--accent-soft);color:var(--accent);text-decoration:none}
```

and after `{{define "badge"}}…{{end}}`:

```
{{/* A config-managed object's badge (managed.go); links the Config page. */}}
{{define "managed-badge"}}{{with .}}<a class="chip managed" href="/ui/config" title="Applied from the config repo {{.Applied}}; edits here drift from it">managed · config @{{.Commit}}</a>{{end}}{{end}}
```

- [ ] **Step 4: Put the badge and warnings on the spec pages**

Each edit below replaces the exact text on the left with the text on the right.

`templates/destination.html`:
- `  <h1>{{.Dest.Name}}</h1>` → `  <h1>{{.Dest.Name}}</h1>{{template "managed-badge" (managed "destination" .Dest.Name)}}`
- `      hx-confirm="Delete destination {{.Dest.Name}}?{{with .Uses}} {{len .}} role(s) list it.{{end}}"` → `      hx-confirm="Delete destination {{.Dest.Name}}?{{with .Uses}} {{len .}} role(s) list it.{{end}}{{with driftNote "destination" .Dest.Name}} {{.}}{{end}}"`
- `      <form hx-post="/ui/destinations/{{.Dest.Name}}" hx-target="#dest" hx-swap="outerHTML">` → `      <form hx-post="/ui/destinations/{{.Dest.Name}}" hx-target="#dest" hx-swap="outerHTML"{{with driftNote "destination" .Dest.Name}} hx-confirm="{{.}} Save anyway?"{{end}}>`

`templates/destinations.html`:
- `          hx-confirm="Delete destination {{.Name}}?{{if .UsedBy}} {{.UsedBy}} role(s) list it.{{end}}">Delete</button></td>` → `          hx-confirm="Delete destination {{.Name}}?{{if .UsedBy}} {{.UsedBy}} role(s) list it.{{end}}{{with driftNote "destination" .Name}} {{.}}{{end}}">Delete</button></td>`

`templates/model_spec.html`:
- `  <h1>{{.Spec.Name}}</h1>` → `  <h1>{{.Spec.Name}}</h1>{{template "managed-badge" (managed "model-spec" .Spec.Name)}}`
- `      hx-confirm="Delete model-spec {{.Spec.Name}}?"` → `      hx-confirm="Delete model-spec {{.Spec.Name}}?{{with driftNote "model-spec" .Spec.Name}} {{.}}{{end}}"`
- `      <form hx-post="/ui/model-specs/{{.Spec.Name}}" hx-target="#spec" hx-swap="outerHTML">` → `      <form hx-post="/ui/model-specs/{{.Spec.Name}}" hx-target="#spec" hx-swap="outerHTML"{{with driftNote "model-spec" .Spec.Name}} hx-confirm="{{.}} Save anyway?"{{end}}>`

`templates/model_specs.html`:
- `          hx-confirm="Delete model-spec {{.Name}}?">Delete</button></td>` → `          hx-confirm="Delete model-spec {{.Name}}?{{with driftNote "model-spec" .Name}} {{.}}{{end}}">Delete</button></td>`

`templates/kit.html`. `$name` is set at the top of `kit-body`:
- `  <h1>{{.Name}}</h1>\n  <span class="chip accent">current v{{.Current}}</span>` → `  <h1>{{.Name}}</h1>{{template "managed-badge" (managed "kit" .Name)}}\n  <span class="chip accent">current v{{.Current}}</span>`
- `      hx-confirm="Delete kit {{.Name}} and all its versions?"` → `      hx-confirm="Delete kit {{.Name}} and all its versions?{{with driftNote "kit" .Name}} {{.}}{{end}}"`
- `hx-confirm="Make v{{.N}} the current version of {{$name}}? Roles bound to it raise v{{.N}} from now on.">` → `hx-confirm="Make v{{.N}} the current version of {{$name}}? Roles bound to it raise v{{.N}} from now on.{{with driftNote "kit" $name}} {{.}}{{end}}">`
- `<button class="small primary" hx-post="/ui/kits/{{$name}}/sync" hx-disabled-elt="this" hx-target="#kit" hx-swap="outerHTML">Sync</button>` → `<button class="small primary" hx-post="/ui/kits/{{$name}}/sync" hx-disabled-elt="this" hx-target="#kit" hx-swap="outerHTML"{{with driftNote "kit" $name}} hx-confirm="{{.}} Sync anyway?"{{end}}>Sync</button>`
- `            hx-confirm="Stop tracking the source of {{$name}}? Its versions are kept.">Untrack</button>` → `            hx-confirm="Stop tracking the source of {{$name}}? Its versions are kept.{{with driftNote "kit" $name}} {{.}}{{end}}">Untrack</button>`
- `<form hx-post="/ui/kits/{{$name}}/source" hx-disabled-elt="find button[type=submit]" hx-target="#kit" hx-swap="outerHTML">` → `<form hx-post="/ui/kits/{{$name}}/source" hx-disabled-elt="find button[type=submit]" hx-target="#kit" hx-swap="outerHTML"{{with driftNote "kit" $name}} hx-confirm="{{.}} Track anyway?"{{end}}>`
- `<form hx-post="/ui/kits/{{.Name}}/versions" hx-target="#kit" hx-swap="outerHTML">` → `<form hx-post="/ui/kits/{{.Name}}/versions" hx-target="#kit" hx-swap="outerHTML"{{with driftNote "kit" .Name}} hx-confirm="{{.}} Push anyway?"{{end}}>`

`templates/kits.html`:
- `                hx-confirm="Delete kit {{.Name}} and all its versions?">Delete</button>{{end}}</td>` → `                hx-confirm="Delete kit {{.Name}} and all its versions?{{with driftNote "kit" .Name}} {{.}}{{end}}">Delete</button>{{end}}</td>`

- [ ] **Step 5: Run the tests to verify they pass**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/adminui/`
Expected: PASS. The existing kit, destination and model-spec page tests are unchanged, because their objects are unmanaged and so get no badge and no `hx-confirm`.

- [ ] **Step 6: Commit**

```bash
git add internal/jam/adminui/managed.go internal/jam/adminui/managed_test.go internal/jam/adminui/nav.go internal/jam/adminui/adminui.go internal/jam/adminui/templates/layout.html internal/jam/adminui/templates/destination.html internal/jam/adminui/templates/destinations.html internal/jam/adminui/templates/model_spec.html internal/jam/adminui/templates/model_specs.html internal/jam/adminui/templates/kit.html internal/jam/adminui/templates/kits.html
git commit -m "feat(adminui): managed badges and drift warnings on destinations, model specs and kits

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: UI — badges and drift warnings on users, projects, roles and the Jam context

**Files:**
- Modify: `internal/jam/adminui/context_edit.go` (`contextPanel`, `jamPanel`), `internal/jam/adminui/projects.go` (`buildProjectDetail`), `internal/jam/adminui/role_detail.go` (`roleDetail`, `buildRoleDetail`)
- Modify templates: `user.html`, `project.html`, `role.html`, `context_panel.html`
- Test: `internal/jam/adminui/managed_test.go`

**Interfaces:**
- Consumes: Task 5's `managed` and `driftNote` funcs, the `managed-badge` template, and `managedUI`/`checkWarns`; `userID(t, store, name)` (users_test.go).
- Produces:
```go
// contextPanel gains: Kind, Name string // the layer's config object: jam-context/jam, project/<p>, role/<p>/<r>
// roleDetail gains:   ConfigName string // "<project>/<role>": the role's config name
```

- [ ] **Step 1: Write the failing test**

Append to `internal/jam/adminui/managed_test.go`:

```go
func TestManagedRegistryBadgeAndDriftWarning(t *testing.T) {
	h, store := managedUI(t)
	mustCreateProject(t, store, "side")
	bob := userID(t, store, "bob")
	ub := "/ui/users/" + string(bob)
	checkWarns(t, h, []warnCase{
		{ub, "hx-post", ub + "/name", true},
		{ub, "hx-post", ub + "/logins", true},
		{ub, "hx-delete", ub, true},
		{ub, "hx-post", ub + "/oidc", false}, // OIDC identities are not in the repo
		{"/ui/projects/app", "hx-post", "/ui/projects/app/rename", true},
		{"/ui/projects/app", "hx-post", "/ui/projects/app/chat-service", true},
		{"/ui/projects/app", "hx-post", "/ui/projects/app/context", true},
		{"/ui/projects/app/members", "hx-post", "/ui/projects/app/members", true},
		{"/ui/projects/app/members", "hx-delete", "/ui/projects/app/members/" + string(bob), true},
		{"/ui/projects/app/intercom", "hx-post", "/ui/projects/app/channels", true},
		{"/ui/projects/app/escalation", "hx-post", "/ui/projects/app/escalation", true},
		{"/ui/projects/side", "hx-post", "/ui/projects/side/rename", false},
		{"/ui/projects/app/roles/ic", "hx-delete", "/ui/roles/app/ic", true},
		{"/ui/projects/app/roles/ic", "hx-post", "/ui/roles/app/ic/scope", true},
		{"/ui/projects/app/roles/ic", "hx-post", "/ui/roles/app/ic/egress", true},
		{"/ui/projects/app/roles/ic", "hx-post", "/ui/roles/app/ic/allocation", true},
		{"/ui/projects/app/roles/ic", "hx-post", "/ui/roles/app/ic/standing", true},
		{"/ui/projects/app/roles/ic", "hx-post", "/ui/roles/app/ic/context", true},
		{"/ui/", "hx-post", "/ui/jam/context", true},
		{"/ui/", "hx-delete", "/ui/jam/context", true},
	})
	for page, want := range map[string]bool{ub: true, "/ui/projects/app": true, "/ui/projects/app/roles/ic": true, "/ui/": true, "/ui/projects/side": false} {
		if got := strings.Contains(get(t, h, page).Body.String(), ">"+managedBadge+"</a>"); got != want {
			t.Errorf("%s: badge %v, want %v", page, got, want)
		}
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/adminui/ -run TestManagedRegistryBadgeAndDriftWarning`
Expected: FAIL, with `warns=false, want true` for every managed row.

- [ ] **Step 3: Carry the config names**

In `internal/jam/adminui/context_edit.go`, add a field to `contextPanel`:

```go
	Kind, Name            string // the layer's config object (jam-context/jam, project/<p>, role/<p>/<r>): its badge and drift warning
```

and in `jamPanel`, after `p.ID = "jam-context"`:

```go
	p.Kind, p.Name = jam.ConfigKindJamContext, "jam"
```

In `internal/jam/adminui/projects.go`, `buildProjectDetail`, after the `d.Context = newContextPanel(…)` line:

```go
	d.Context.Kind, d.Context.Name = jam.ConfigKindProject, name
```

In `internal/jam/adminui/role_detail.go`, add to `roleDetail`, after `RolesHref`:

```go
	ConfigName           string // "<project>/<role>": the role's config name (badge, drift warning)
```

In `buildRoleDetail`, right after the line `d.Context = newContextPanel("role", "/ui/roles/"+project+"/"+name+"/context", …)` (`project` is already defaulted by `orDefaultProject` there):

```go
	d.ConfigName = project + "/" + name
	d.Context.Kind, d.Context.Name = jam.ConfigKindRole, d.ConfigName
```

- [ ] **Step 4: Put the badge and warnings on the templates**

`templates/user.html` (inside `user-body`, where `$u := .User` and `$base := userURL $u.ID`):
- `<div class="page-head"><h1 class="mono">{{$u.Name}}</h1>` → `<div class="page-head"><h1 class="mono">{{$u.Name}}</h1>{{template "managed-badge" (managed "user" $u.Name)}}`
- `hx-confirm="Remove user {{$u.Name}}? They leave every project; history keeps showing them as removed.">Remove user</button>` → `hx-confirm="Remove user {{$u.Name}}? They leave every project; history keeps showing them as removed.{{with driftNote "user" $u.Name}} {{.}}{{end}}">Remove user</button>`
- `<div class="body"><form hx-post="{{$base}}/name" hx-target="#user" hx-swap="outerHTML">` → `<div class="body"><form hx-post="{{$base}}/name" hx-target="#user" hx-swap="outerHTML"{{with driftNote "user" $u.Name}} hx-confirm="{{.}} Rename anyway?"{{end}}>`
- `<div class="body"><form hx-post="{{$base}}/logins" hx-target="#user" hx-swap="outerHTML">` → `<div class="body"><form hx-post="{{$base}}/logins" hx-target="#user" hx-swap="outerHTML"{{with driftNote "user" $u.Name}} hx-confirm="{{.}} Save anyway?"{{end}}>`

`templates/project.html` (`$p` is the project's name in every section template):
- `  <h1>{{.Project.Name}}</h1>\n  {{if .ChatService}}` → `  <h1>{{.Project.Name}}</h1>{{template "managed-badge" (managed "project" $p)}}\n  {{if .ChatService}}`
- `hx-post="/ui/projects/{{$p}}/rename" hx-swap="none">` → `hx-post="/ui/projects/{{$p}}/rename" hx-swap="none"{{with driftNote "project" $p}} hx-confirm="{{.}} Rename anyway?"{{end}}>`
- `      hx-confirm="Delete project {{$p}}? Its roster, escalation and chat service go with it; its channels are archived (history stays readable) and its name is freed."` → the same with `{{with driftNote "project" $p}} {{.}}{{end}}` inserted before the closing `"`
- `<form class="inline" hx-post="/ui/projects/{{$p}}/chat-service" hx-target="#project" hx-swap="outerHTML">` → `<form class="inline" hx-post="/ui/projects/{{$p}}/chat-service" hx-target="#project" hx-swap="outerHTML"{{with driftNote "project" $p}} hx-confirm="{{.}} Save anyway?"{{end}}>`
- **Replace all** (two forms, member edit and add): `hx-post="/ui/projects/{{$p}}/members" hx-target="#project" hx-swap="outerHTML">` → `hx-post="/ui/projects/{{$p}}/members" hx-target="#project" hx-swap="outerHTML"{{with driftNote "project" $p}} hx-confirm="{{.}} Save anyway?"{{end}}>`
- `hx-confirm="Remove {{.Name}} from {{$p}}?">Remove</button>` → `hx-confirm="Remove {{.Name}} from {{$p}}?{{with driftNote "project" $p}} {{.}}{{end}}">Remove</button>`
- `hx-confirm="Remove room {{.Name}} from {{$p}}?">Remove</button></td></tr>` → `hx-confirm="Remove room {{.Name}} from {{$p}}?{{with driftNote "project" $p}} {{.}}{{end}}">Remove</button></td></tr>`
- `<form hx-post="/ui/projects/{{$p}}/channels" hx-target="#project" hx-swap="outerHTML">` → `<form hx-post="/ui/projects/{{$p}}/channels" hx-target="#project" hx-swap="outerHTML"{{with driftNote "project" $p}} hx-confirm="{{.}} Save anyway?"{{end}}>`
- `<form hx-post="/ui/projects/{{$p}}/escalation" hx-target="#project" hx-swap="outerHTML">` → `<form hx-post="/ui/projects/{{$p}}/escalation" hx-target="#project" hx-swap="outerHTML"{{with driftNote "project" $p}} hx-confirm="{{.}} Save anyway?"{{end}}>`
- In `chain-form` (its `.Project` is the project name): `<form hx-post="/ui/projects/{{.Project}}/escalation" hx-target="#project" hx-swap="outerHTML">` → `<form hx-post="/ui/projects/{{.Project}}/escalation" hx-target="#project" hx-swap="outerHTML"{{with driftNote "project" .Project}} hx-confirm="{{.}} Save anyway?"{{end}}>`, and `hx-confirm="Clear the {{if .Chain.Category}}{{.Chain.Category}}{{else}}default{{end}} chain?">Clear</button>` → `hx-confirm="Clear the {{if .Chain.Category}}{{.Chain.Category}}{{else}}default{{end}} chain?{{with driftNote "project" .Project}} {{.}}{{end}}">Clear</button>`

`templates/role.html` (inside `role-body`; `$` is the `roleDetail`):
- `  <h1>{{.Name}}</h1>\n  <div class="facts">` → `  <h1>{{.Name}}</h1>{{template "managed-badge" (managed "role" .ConfigName)}}\n  <div class="facts">`
- `      hx-confirm="Delete role {{.Name}} from {{.Project}}?"` → `      hx-confirm="Delete role {{.Name}} from {{.Project}}?{{with driftNote "role" .ConfigName}} {{.}}{{end}}"`
- `<form hx-post="{{$base}}/scope" hx-target="#role" hx-swap="outerHTML">` → `<form hx-post="{{$base}}/scope" hx-target="#role" hx-swap="outerHTML"{{with driftNote "role" $.ConfigName}} hx-confirm="{{.}} Save anyway?"{{end}}>`
- `<form hx-post="{{$base}}/egress" hx-target="#role" hx-swap="outerHTML">` → the same pattern (`… hx-swap="outerHTML"{{with driftNote "role" $.ConfigName}} hx-confirm="{{.}} Save anyway?"{{end}}>`)
- `hx-confirm="Drop {{.Name}}'s egress policy and use the kit default?">Reset to kit default</button>` → `hx-confirm="Drop {{.Name}}'s egress policy and use the kit default?{{with driftNote "role" $.ConfigName}} {{.}}{{end}}">Reset to kit default</button>`
- `<form hx-post="{{$base}}/allocation" hx-target="#role" hx-swap="outerHTML">` → the same pattern
- `hx-confirm="Dismiss standing agent {{.Name}}? It stops, and its conversation and workspace are deleted.">Dismiss</button></td>` → `hx-confirm="Dismiss standing agent {{.Name}}? It stops, and its conversation and workspace are deleted.{{with driftNote "role" $.ConfigName}} {{.}}{{end}}">Dismiss</button></td>`
- `<form hx-post="{{$base}}/standing" hx-target="#role" hx-swap="outerHTML">` → the same pattern

`templates/context_panel.html`:
- `  <header><h2>Agent context</h2><span class="sub">{{.Scope}} — what every agent is told when raised</span></header>` → `  <header><h2>Agent context</h2><span class="sub">{{.Scope}} — what every agent is told when raised</span>{{if eq .Kind "jam-context"}}{{template "managed-badge" (managed .Kind .Name)}}{{end}}</header>`
- `    <form hx-post="{{.Action}}" hx-target="#{{.Target}}" hx-swap="outerHTML">` → `    <form hx-post="{{.Action}}" hx-target="#{{.Target}}" hx-swap="outerHTML"{{with driftNote .Kind .Name}} hx-confirm="{{.}} Save anyway?"{{end}}>`
- `hx-confirm="Remove this {{.Scope}} agent context?">Clear</button>` → `hx-confirm="Remove this {{.Scope}} agent context?{{with driftNote .Kind .Name}} {{.}}{{end}}">Clear</button>`

- [ ] **Step 5: Run the tests to verify they pass**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/adminui/`
Expected: PASS. The context panels' existing tests are unchanged: their layers are unmanaged.

- [ ] **Step 6: Commit**

```bash
git add internal/jam/adminui/context_edit.go internal/jam/adminui/projects.go internal/jam/adminui/role_detail.go internal/jam/adminui/managed_test.go internal/jam/adminui/templates/user.html internal/jam/adminui/templates/project.html internal/jam/adminui/templates/role.html internal/jam/adminui/templates/context_panel.html
git commit -m "feat(adminui): managed badges and drift warnings on users, projects, roles and the Jam context

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7: UI — the Config page: source, managed objects, Release, Untrack

**Files:**
- Create: `internal/jam/adminui/config_page.go`, `internal/jam/adminui/templates/config.html`, `internal/jam/adminui/config_page_test.go`
- Modify: `internal/jam/adminui/adminui.go` (`pages`, `options`, `WithConfigEnv`, `Handler`), `internal/jam/adminui/nav.go` (`jamTabs`), `templates/layout.html` and `templates/kit.html` (move `src-repo`/`src-commit` into the layout), `cmd/at-jam/main.go`

**Interfaces:**
- Consumes: `jam.ConfigMarkStates`, `jam.ConfigMarkState`, `jam.ConfigInSync/ConfigMissing` (Task 2); `jam.ReleaseConfigMark` (Task 3); `jam.UntrackConfigSource`, `jam.ConfigStatus`, `Store.GetConfigSource`; `newSourceView`, `destURL`, `specURL`, `kitURL`, `projectURL`, `roleURL`, `shortConfigCommit`, `configTime` (Task 5); `renderFragment`, `renderError`.
- Produces:
```go
func WithConfigEnv(env jam.ConfigEnv) Option
type configPage struct {
	Title       string
	Source      *configSourceView // nil = none tracked
	Rows        []configMarkRow
	Drifted     int
	StatesErr   bool
	LastCommit  string
	LastApplied string
	CanGit      bool
	Plan        *planView // Task 8
}
type configSourceView struct{ sourceView; Label, Applied string }
type configMarkRow struct{ Kind, Name, Href, Commit, Applied, State, ReleaseURL string }
func buildConfigPage(store jam.Store, canGit bool, log *slog.Logger) configPage
func configObjectHref(kind, name string) string // "" for a connection
func configReleaseURL(kind, name string) string
func renderConfigBody(w http.ResponseWriter, r *http.Request, d configPage, msg string)
func configFail(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error)
func registerConfig(mux *http.ServeMux, store jam.Store, f jam.KitFetcher, env jam.ConfigEnv, log *slog.Logger, guardWrite func(http.ResponseWriter, *http.Request) bool)
// routes: GET /ui/config; DELETE /ui/config/source; DELETE /ui/config/managed/{kind}/{name...}
```

- [ ] **Step 1: Write the failing tests**

Create `internal/jam/adminui/config_page_test.go`:

```go
package adminui_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

// trackManaged records the source managedUI's objects came from.
func trackManaged(t *testing.T, store jam.Store) {
	t.Helper()
	if err := store.SetConfigSource(&jam.ConfigSource{Repo: "https://github.com/acme/jam", Ref: "main", Commit: managedSHA, AppliedAt: time.Unix(1000, 0)}); err != nil {
		t.Fatal(err)
	}
}

func TestConfigPageShowsSourceAndManaged(t *testing.T) {
	h, store := managedUI(t)
	trackManaged(t, store)
	for _, d := range store.ListDestinations() {
		if d.Name == "gh" {
			d.Note = "edited in Jam"
			if err := store.AddDestination(d); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := store.SetJamContext(sessionctx.Layer{}); err != nil { // the managed Jam context goes missing
		t.Fatal(err)
	}
	body := get(t, h, "/ui/config").Body.String()
	for _, want := range []string{
		`<a href="/ui/config" aria-current="page">Config`,
		`href="https://github.com/acme/jam"`,
		`href="https://github.com/acme/jam/commit/` + managedSHA + `"`,
		"1970-01-01 00:16 UTC",
		`href="/ui/destinations/gh"`, `href="/ui/projects/app/roles/ic"`, `href="/ui/users/bob"`,
		`<span class="chip st-drifted">drifted</span>`, `<span class="chip st-missing">missing</span>`, "in sync",
		"2 differ from the repo",
		`hx-delete="/ui/config/managed/role/app/ic"`,
		`hx-delete="/ui/config/source"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("config page missing %q", want)
		}
	}
	if strings.Contains(body, `href="/ui/#jam-context"`) {
		t.Error("a missing object links its page")
	}
}

func TestConfigPageRelease(t *testing.T) {
	h, store := managedUI(t)
	rec := del(t, h, "/ui/config/managed/role/app/ic")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "released role app/ic") {
		t.Fatalf("release: %d %s", rec.Code, rec.Body.String())
	}
	for _, mk := range store.ListConfigMarks() {
		if mk.Kind == jam.ConfigKindRole {
			t.Fatalf("role still managed: %+v", mk)
		}
	}
	if rec := del(t, h, "/ui/config/managed/role/app/ic"); rec.Code != http.StatusNotFound {
		t.Fatalf("second release: %d", rec.Code)
	}
	req := httptest.NewRequest(http.MethodDelete, "/ui/config/managed/user/bob", nil)
	req.Header.Set("Origin", "http://evil.example")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || len(store.ListConfigMarks()) == 0 {
		t.Fatalf("cross-origin release: %d", rec.Code)
	}
}

func TestConfigPageUntrack(t *testing.T) {
	h, store := managedUI(t)
	trackManaged(t, store)
	rec := del(t, h, "/ui/config/source")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "No config source is tracked") {
		t.Fatalf("untrack: %d %s", rec.Code, rec.Body.String())
	}
	if _, ok := store.GetConfigSource(); ok || len(store.ListConfigMarks()) == 0 {
		t.Fatal("untrack must forget the source and keep the marks")
	}
}

// A Jam the drift check cannot render: the page says so instead of failing.
func TestConfigPageDriftUnknown(t *testing.T) {
	h, store := managedUI(t)
	if _, err := store.PushKit("bad", "listen: :443"); err != nil {
		t.Fatal(err)
	}
	rec := get(t, h, "/ui/config")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Could not compare with the live Jam") || !strings.Contains(rec.Body.String(), "unknown") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/adminui/ -run TestConfigPage`
Expected: FAIL. `GET /ui/config` is 404, so `config page missing …` is reported for every item.

- [ ] **Step 3: Implement the page**

In `internal/jam/adminui/nav.go`, `jamTabs`, insert after the Specs row:

```go
	{"config", "Config", "/ui/config"},
```

In `internal/jam/adminui/adminui.go`:
- `pages` gains `"config": mustParse(jamPage("config"), "config.html"),`.
- `options` gains `configEnv jam.ConfigEnv`.
- Add the option:

```go
// WithConfigEnv gives the Config page the planner's view of the serve — the
// env the admin API's WithConfig gets: its portable serve config, and the
// conditions an apply or a release re-checks config.drift on. Unset fields
// default to the Handler's credExists and WithPoolConfigured.
func WithConfigEnv(env jam.ConfigEnv) Option {
	return func(o *options) { o.configEnv = env }
}
```

- In `Handler`, after `registerAccess(…)`:

```go
	cfgEnv := o.configEnv
	if cfgEnv.CredExists == nil {
		cfgEnv.CredExists = credExists
	}
	cfgEnv.PoolConfigured = cfgEnv.PoolConfigured || o.poolConfigured
	registerConfig(mux, store, o.kitFetcher, cfgEnv, log, guardWrite)
```

Move the two shared source templates. Cut these two lines (and the blank line between them) from the end of `templates/kit.html`, and paste them into `templates/layout.html` after the `managed-badge` define:

```
{{define "src-repo"}}{{if .RepoLink}}<a class="mono" href="{{.RepoLink}}" rel="noopener noreferrer">{{.Repo}}</a>{{else}}<span class="mono">{{.Repo}}</span>{{end}}{{end}}
{{define "src-commit"}}{{if .Commit}}{{if .CommitLink}}<a class="mono" href="{{.CommitLink}}" rel="noopener noreferrer">{{.Commit}}</a>{{else}}<span class="mono">{{.Commit}}</span>{{end}}{{else}}<span class="unset">—</span>{{end}}{{end}}
```

Create `internal/jam/adminui/config_page.go`:

```go
package adminui

import (
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
)

// configSourceView is the tracked config source on the Config page.
type configSourceView struct {
	sourceView
	Label   string // <repo>[//<dir>]@<ref>
	Applied string // the last apply from it; "" = none since tracked
}

// configMarkRow is one managed object on the Config page.
type configMarkRow struct {
	Kind, Name string
	Href       string // its page; "" for a connection or a missing object
	Commit     string // the config commit it was applied from (7 chars)
	Applied    string
	State      string // jam.ConfigInSync|ConfigDrifted|ConfigMissing; "" = unknown
	ReleaseURL string
}

// configPage is the Config page payload.
type configPage struct {
	Title       string
	Source      *configSourceView // nil = none tracked
	Rows        []configMarkRow
	Drifted     int    // rows drifted or missing
	StatesErr   bool   // the live Jam could not be compared (see the log)
	LastCommit  string // the newest mark's commit (7 chars)
	LastApplied string // and time
	CanGit      bool   // a fetcher: Diff, Apply and Track are offered
	Plan        *planView
}

// configObjectHref is a config object's page; "" for a connection (no page).
func configObjectHref(kind, name string) string {
	switch kind {
	case jam.ConfigKindDestination:
		return destURL(name)
	case jam.ConfigKindModelSpec:
		return specURL(name)
	case jam.ConfigKindUser:
		return "/ui/users/" + url.PathEscape(name)
	case jam.ConfigKindKit:
		return kitURL(name)
	case jam.ConfigKindProject:
		return projectURL(name)
	case jam.ConfigKindRole:
		p, r, _ := strings.Cut(name, "/")
		return roleURL(p, r)
	case jam.ConfigKindJamContext:
		return "/ui/#jam-context"
	}
	return ""
}

// configReleaseURL is the UI's release route for kind/name (a role's name
// keeps its slash).
func configReleaseURL(kind, name string) string {
	segs := strings.Split(name, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return "/ui/config/managed/" + url.PathEscape(kind) + "/" + strings.Join(segs, "/")
}

// buildConfigPage gathers the Config page: the tracked source, every managed
// object with its state, and the newest apply.
func buildConfigPage(store jam.Store, canGit bool, log *slog.Logger) configPage {
	d := configPage{Title: "Config", CanGit: canGit}
	if cs, ok := store.GetConfigSource(); ok {
		v := &configSourceView{sourceView: *newSourceView(&jam.KitSource{Repo: cs.Repo, Dir: cs.Dir, Ref: cs.Ref, Commit: cs.Commit}),
			Label: cs.Source().String()}
		if !cs.AppliedAt.IsZero() {
			v.Applied = configTime(cs.AppliedAt)
		}
		d.Source = v
	}
	states, err := jam.ConfigMarkStates(store)
	if err != nil {
		log.Warn("ui config: drift not computed", "err", err.Error())
		d.StatesErr = true
		states = nil
		for _, mk := range store.ListConfigMarks() {
			states = append(states, jam.ConfigMarkState{ConfigMark: mk}) // State "" = unknown
		}
	}
	var newest time.Time
	for _, s := range states {
		row := configMarkRow{Kind: s.Kind, Name: s.Name, Href: configObjectHref(s.Kind, s.Name), Commit: shortConfigCommit(s.Commit),
			Applied: configTime(s.AppliedAt), State: s.State, ReleaseURL: configReleaseURL(s.Kind, s.Name)}
		if s.State == jam.ConfigMissing {
			row.Href = ""
		}
		if s.State != "" && s.State != jam.ConfigInSync {
			d.Drifted++
		}
		if s.AppliedAt.After(newest) {
			newest, d.LastCommit = s.AppliedAt, row.Commit
		}
		d.Rows = append(d.Rows, row)
	}
	if !newest.IsZero() {
		d.LastApplied = configTime(newest)
	}
	return d
}

// renderConfigBody answers a Config-page write with the re-rendered body,
// plus an out-of-band #flash message when msg is set.
func renderConfigBody(w http.ResponseWriter, r *http.Request, d configPage, msg string) {
	renderFragment(w, r, "config", "config-body", d)
	if msg != "" {
		_, _ = w.Write([]byte(`<div id="flash" hx-swap-oob="innerHTML"><p class="ok">` + template.HTMLEscapeString(msg) + `</p></div>`))
	}
}

// configFail answers a config operation's error as the admin API maps it
// (jam.ConfigStatus), logging store failures.
func configFail(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	status, msg := jam.ConfigStatus(err)
	if status == http.StatusInternalServerError {
		log.Error("ui config: store error", "operator", jam.OperatorID(r), "err", err.Error())
	}
	renderError(w, status, msg)
}

func registerConfig(mux *http.ServeMux, store jam.Store, f jam.KitFetcher, env jam.ConfigEnv, log *slog.Logger, guardWrite func(http.ResponseWriter, *http.Request) bool) {
	canGit := f != nil
	mux.HandleFunc("GET /ui/config", func(w http.ResponseWriter, r *http.Request) {
		render(w, r, "config", buildConfigPage(store, canGit, log))
	})
	mux.HandleFunc("DELETE /ui/config/source", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		if err := jam.UntrackConfigSource(store, log, jam.OperatorID(r)); err != nil {
			configFail(w, r, log, err)
			return
		}
		log.Info("ui config untracked", "operator", jam.OperatorID(r))
		renderConfigBody(w, r, buildConfigPage(store, canGit, log), "config source untracked; managed objects and their marks stay")
	})
	mux.HandleFunc("DELETE /ui/config/managed/{kind}/{name...}", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		key := jam.ConfigKey{Kind: r.PathValue("kind"), Name: r.PathValue("name")}
		if err := jam.ReleaseConfigMark(store, key, env.Conditions, log, jam.OperatorID(r)); err != nil {
			configFail(w, r, log, err)
			return
		}
		log.Info("ui config released", "operator", jam.OperatorID(r), "kind", key.Kind, "name", key.Name)
		renderConfigBody(w, r, buildConfigPage(store, canGit, log), "released "+key.Kind+" "+key.Name+"; it stays in Jam, unmanaged")
	})
}
```

`planView` is Task 8's. For this task to compile alone, add at the bottom of `config_page.go`:

```go
// planView is a rendered config plan (Task 8 fills it in).
type planView struct{}
```

Task 8 replaces it.

Create `internal/jam/adminui/templates/config.html`:

```html
{{define "content"}}
<style>
  #config .sections{display:grid;grid-template-columns:repeat(auto-fit,minmax(420px,1fr));gap:16px;align-items:start}
  #config .full{grid-column:1/-1}
  #config section.card>header{display:flex;align-items:baseline;gap:10px;padding:12px 16px;border-bottom:1px solid var(--border);flex-wrap:wrap}
  #config section.card>header h2{margin:0}
  #config section.card>.body{padding:12px 16px}
  #config dl.kv{display:grid;grid-template-columns:max-content 1fr;gap:6px 18px;margin:0}
  #config dl.kv dt{color:var(--muted);font-size:13px}
  #config dl.kv dd{margin:0;word-break:break-all}
  #config .unset{color:var(--faint)}
  #config .chip.st-drifted,#config .chip.st-missing{background:var(--wait-soft);color:var(--wait)}
  #config .form-actions{display:flex;gap:8px;margin-top:10px}
  #config pre.diff{margin:0;padding:8px 12px;overflow-x:auto;font:12.5px/1.55 "IBM Plex Mono",ui-monospace,monospace;background:var(--surface-2)}
  #config pre.diff span{display:block;white-space:pre}
  #config .d-add{background:var(--live-soft);color:var(--live)}
  #config .d-del{background:var(--bad-soft);color:var(--bad)}
  #config .d-skip{color:var(--faint);font-style:italic}
  #config label.check{flex-direction:row;align-items:center;gap:8px;margin:8px 0;font-size:13px}
  #config label.check input{width:auto}
  @media (max-width:720px){#config .sections{grid-template-columns:1fr}}
</style>
<div class="page-head"><h1>Config</h1><span class="sub">This Jam's configuration in git — the repo it follows, the objects the repo manages, and the plan to apply it</span></div>
{{template "config-body" .}}
{{end}}

{{define "config-body"}}
<div id="config">
<div class="sections">
  <section class="card">
    <header><h2>Source</h2><span class="sub">the config repo this Jam follows</span></header>
    <div class="body">
    {{with .Source}}
      <dl class="kv">
        <dt>Repo</dt><dd>{{template "src-repo" .}}</dd>
        {{if .Dir}}<dt>Dir</dt><dd class="mono">{{.Dir}}</dd>{{end}}
        <dt>Ref</dt><dd class="mono">{{.Ref}}</dd>
        <dt>Last applied</dt><dd>{{template "src-commit" .}}{{with .Applied}} <span class="unset">at {{.}}</span>{{end}}</dd>
      </dl>
      <div class="form-actions">
        <button class="small danger" hx-delete="/ui/config/source" hx-target="#config" hx-swap="outerHTML"
          hx-confirm="Stop following {{.Label}}? Managed objects and their marks stay.">Untrack</button>
      </div>
    {{else}}
      <p class="unset">No config source is tracked{{with $.LastCommit}} — the objects below were last applied from config @{{.}} at {{$.LastApplied}}{{end}}.</p>
      {{if not $.CanGit}}<p class="unset">Git actions are not available on this serve; use <code>at-jam config apply --dir</code>.</p>{{end}}
    {{end}}
    </div>
  </section>

  <section class="card full">
    <header><h2>Managed objects</h2><span class="sub">{{len .Rows}} from the config repo{{with .Drifted}} · <span class="chip st-drifted">{{.}} differ from the repo</span>{{end}}{{with .LastCommit}} · last applied config @<span class="mono">{{.}}</span> at {{$.LastApplied}}{{end}}</span></header>
    {{if .StatesErr}}<div class="body"><div class="banner error">Could not compare with the live Jam (see the serve log); states are unknown.</div></div>{{end}}
    <table>
      <thead><tr><th>Object</th><th>From</th><th>Applied</th><th>State</th><th></th></tr></thead>
      <tbody>
      {{range .Rows}}<tr>
        <td><span class="chip">{{.Kind}}</span> {{if .Href}}<a class="mono" href="{{.Href}}">{{.Name}}</a>{{else}}<span class="mono">{{.Name}}</span>{{end}}</td>
        <td class="mono">@{{.Commit}}</td>
        <td>{{.Applied}}</td>
        <td>{{if eq .State "in-sync"}}<span class="unset">in sync</span>{{else if .State}}<span class="chip st-{{.State}}">{{.State}}</span>{{else}}<span class="unset">unknown</span>{{end}}</td>
        <td class="actions"><button class="small danger" hx-delete="{{.ReleaseURL}}" hx-target="#config" hx-swap="outerHTML"
          hx-confirm="Stop managing {{.Kind}} {{.Name}} from the config repo? It stays in Jam, unmanaged.">Release</button></td>
      </tr>{{else}}<tr><td colspan="5" class="empty">Nothing is managed yet — apply a config repo, or run <code>at-jam config apply --dir</code>.</td></tr>{{end}}
      </tbody>
    </table>
  </section>
</div>
<div id="config-plan"></div>
</div>
{{end}}
```

In `cmd/at-jam/main.go`, inside `if cfg.AdminListen != "" {`, right after `credExists := cfg.credConfigured`:

```go
		// One planner view of the serve for the config admin API and the UI's
		// Config page; config.drift is re-checked on conds after each apply.
		cfgEnv := jam.ConfigEnv{CredExists: credExists, PoolConfigured: cfg.Pool != nil, Serve: portable, Conditions: conds}
```

Then append `, adminui.WithConfigEnv(cfgEnv)` to the `uiOpts := []adminui.Option{…}` literal, and replace the `jam.WithConfig(…)` option (as Task 2 left it) with:

```go
			jam.WithConfig(st, kitFetcher, cfgEnv, log))
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/adminui/ ./cmd/at-jam/ && GOPROXY=https://proxy.golang.org,direct go vet ./cmd/at-jam/`
Expected: PASS. The kit page tests still find `src-repo`/`src-commit`, now from the layout.

- [ ] **Step 5: Commit**

```bash
git add internal/jam/adminui/config_page.go internal/jam/adminui/config_page_test.go internal/jam/adminui/templates/config.html internal/jam/adminui/adminui.go internal/jam/adminui/nav.go internal/jam/adminui/templates/layout.html internal/jam/adminui/templates/kit.html cmd/at-jam/main.go
git commit -m "feat(adminui): the Config page — source, managed objects and their drift, Release, Untrack

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 8: UI — Diff, Apply and Track on the Config page

**Files:**
- Modify: `internal/jam/adminui/config_page.go` (replace the `planView` stub; add `registerConfigGit`), `internal/jam/adminui/templates/config.html`
- Test: `internal/jam/adminui/config_page_test.go`

**Interfaces:**
- Consumes: `jam.ApplyConfigSource`, `jam.TrackConfigSource`, `jam.ConfigApplyOptions`, `jam.ConfigApplyResult`, `jam.ConfigChanges`, `ConfigPlan.HasDrift` (Task 1), `jam.ConfigStatus`; `formSource` (kits.go), `lineDiff`/`diffLine` (kit_detail.go); Task 7's `buildConfigPage`, `renderConfigBody`, `configFail`, `configObjectHref`.
- Produces:
```go
type planOpView struct {
	jam.ConfigOp
	Href     string // "" for a create
	Lines    []diffLine
	TooLarge bool
}
type planView struct {
	Source, Commit string
	Forced         bool
	Digest         string
	Ops            []planOpView
	Unchanged      int
	Forget         []jam.ConfigKey
	Unmanaged      []jam.ConfigKey
	KitSources     []jam.ConfigKitSource
	Refusals       []string
	Serve          jam.ServeConfigDiff
	HasDrift       bool
	Changes        int
}
func newPlanView(res jam.ConfigApplyResult, forced bool) *planView
func canonDisplay(kind, canon string) string
func applyMsg(res jam.ConfigApplyResult) string
// routes (only with a fetcher): POST /ui/config/diff (force), POST /ui/config/apply (plan, force), POST /ui/config/source (source)
```

- [ ] **Step 1: Write the failing tests**

Append to `internal/jam/adminui/config_page_test.go`. Add `"context"`, `"net/url"`, `"os"`, `"path/filepath"` and `"regexp"` to its imports, plus `"github.com/aethons-tools/cove/internal/ident"`, `"github.com/aethons-tools/cove/internal/jam/adminui"`, `"github.com/aethons-tools/cove/internal/jam/condition"` and `"github.com/aethons-tools/cove/internal/jam/gitsource"`.

```go
// cfgTreeFetcher serves one config repo: every ref resolves to sha, holding files.
type cfgTreeFetcher struct {
	t       *testing.T
	sha     string
	files   map[string]string
	fetches int
}

func (f *cfgTreeFetcher) Resolve(context.Context, gitsource.Source) (string, error) { return f.sha, nil }

func (f *cfgTreeFetcher) Fetch(_ context.Context, _ gitsource.Source, fn func(root, commit string) error) error {
	f.fetches++
	root := f.t.TempDir()
	for p, b := range f.files {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, []byte(b), 0o644); err != nil {
			return err
		}
	}
	return fn(root, f.sha)
}

func configUI(t *testing.T, f jam.KitFetcher, conds *condition.Tracker) (http.Handler, jam.Store) {
	t.Helper()
	store := newStore(t)
	return adminui.Handler(store, testLogger(), nil, nil, anyCred, nil, adminui.WithKitFetcher(f),
		adminui.WithConfigEnv(jam.ConfigEnv{Conditions: conds}), adminui.WithConditions(conds, "")), store
}

var planDigestRE = regexp.MustCompile(`name="plan" value="([0-9a-f]{64})"`)

func planDigest(t *testing.T, body string) string {
	t.Helper()
	m := planDigestRE.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no plan digest in:\n%s", body)
	}
	return m[1]
}

func TestConfigPageTrackDiffApply(t *testing.T) {
	f := &cfgTreeFetcher{t: t, sha: managedSHA, files: map[string]string{"users/bob.yml": "kind: user\nversion: 1\n"}}
	h, store := configUI(t, f, nil)
	body := get(t, h, "/ui/config").Body.String()
	if !strings.Contains(body, `hx-post="/ui/config/source"`) || strings.Contains(body, `hx-post="/ui/config/diff"`) {
		t.Fatalf("untracked page:\n%s", body)
	}
	rec := post(t, h, "/ui/config/source", url.Values{"source": {"https://github.com/acme/jam@main"}})
	body = rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("track: %d %s", rec.Code, body)
	}
	for _, want := range []string{"tracking https://github.com/acme/jam@main", `<span class="chip">create</span>`, "users/bob.yml",
		`hx-post="/ui/config/diff"`, `hx-post="/ui/config/apply"`, "+ kind: user"} {
		if !strings.Contains(body, want) {
			t.Errorf("track answer missing %q", want)
		}
	}
	if _, ok := store.GetConfigSource(); !ok || len(store.ListUsers()) != 0 {
		t.Fatal("track must record the source and apply nothing")
	}
	rec = post(t, h, "/ui/config/apply", url.Values{"plan": {planDigest(t, body)}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "applied config @abc1234: 1 created") || !strings.Contains(rec.Body.String(), `href="/ui/users/bob"`) {
		t.Fatalf("apply: %d %s", rec.Code, rec.Body.String())
	}
	if src, _ := store.GetConfigSource(); len(store.ListUsers()) != 1 || src.Commit != managedSHA {
		t.Fatalf("users=%v source=%+v", store.ListUsers(), src)
	}
}

func TestConfigApplyIsBoundToTheShownPlan(t *testing.T) {
	f := &cfgTreeFetcher{t: t, sha: managedSHA, files: map[string]string{"users/bob.yml": "kind: user\nversion: 1\n"}}
	h, store := configUI(t, f, nil)
	trackManaged(t, store)
	body := post(t, h, "/ui/config/diff", url.Values{}).Body.String()
	if strings.Contains(body, `name="force"`) {
		t.Fatal("force offered without drift")
	}
	digest := planDigest(t, body)
	if rec := post(t, h, "/ui/config/apply", url.Values{}); rec.Code != http.StatusBadRequest {
		t.Fatalf("apply without a plan: %d", rec.Code)
	}
	// The Jam changes after the review: the plan on screen is stale.
	if _, err := store.CreateUser(jam.User{ID: ident.New(ident.User), Name: "carol", Status: jam.StatusLive}); err != nil {
		t.Fatal(err)
	}
	rec := post(t, h, "/ui/config/apply", url.Values{"plan": {digest}})
	if rec.Code != http.StatusConflict || len(store.ListUsers()) != 1 {
		t.Fatalf("stale apply: %d users=%v", rec.Code, store.ListUsers())
	}
}

func TestConfigPageForceOnlyWithDrift(t *testing.T) {
	conds := condition.New(condition.Options{})
	f := &cfgTreeFetcher{t: t, sha: managedSHA, files: map[string]string{"users/bob.yml": "kind: user\nversion: 1\nlogins: [bob@repo]\n"}}
	h, store := configUI(t, f, conds)
	applyManaged(t, store, map[string]string{"users/bob.yml": "kind: user\nversion: 1\n"})
	trackManaged(t, store)
	if err := store.SetUserLogins(userID(t, store, "bob"), []string{"bob@jam"}); err != nil {
		t.Fatal(err)
	}
	jam.CheckConfigDrift(store, conds, nil)
	if !conds.IsOpen(jam.ConfigDriftKey) {
		t.Fatal("drift not raised")
	}
	body := post(t, h, "/ui/config/diff", url.Values{}).Body.String()
	if !strings.Contains(body, "user bob drifted (edited in Jam since config @abc1234)") || !strings.Contains(body, `name="force"`) ||
		strings.Contains(body, `hx-post="/ui/config/apply"`) {
		t.Fatalf("drifted diff:\n%s", body)
	}
	body = post(t, h, "/ui/config/diff", url.Values{"force": {"1"}}).Body.String()
	if strings.Contains(body, "drifted (edited") || !strings.Contains(body, `<input type="hidden" name="force" value="1">`) || !strings.Contains(body, " checked") {
		t.Fatalf("forced diff:\n%s", body)
	}
	rec := post(t, h, "/ui/config/apply", url.Values{"plan": {planDigest(t, body)}, "force": {"1"}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "1 updated") {
		t.Fatalf("forced apply: %d %s", rec.Code, rec.Body.String())
	}
	if u, _ := store.GetUser(userID(t, store, "bob")); len(u.Logins) != 1 || u.Logins[0] != "bob@repo" {
		t.Fatalf("logins %v", u.Logins)
	}
	if conds.IsOpen(jam.ConfigDriftKey) {
		t.Fatal("the apply left config.drift open")
	}
}

func TestConfigPageNoFetcherNoGitActions(t *testing.T) {
	h, store := managedUI(t)
	trackManaged(t, store)
	body := get(t, h, "/ui/config").Body.String()
	for _, absent := range []string{`hx-post="/ui/config/diff"`, `hx-post="/ui/config/source"`, `hx-post="/ui/config/apply"`} {
		if strings.Contains(body, absent) {
			t.Errorf("no fetcher, yet %s", absent)
		}
	}
	if rec := post(t, h, "/ui/config/diff", url.Values{}); rec.Code == http.StatusOK {
		t.Fatal("diff answered without a fetcher")
	}
}

func TestConfigPageGitCrossOriginRefused(t *testing.T) {
	f := &cfgTreeFetcher{t: t, sha: managedSHA, files: map[string]string{"users/bob.yml": "kind: user\nversion: 1\n"}}
	h, store := configUI(t, f, nil)
	trackManaged(t, store)
	for _, p := range []string{"/ui/config/diff", "/ui/config/apply", "/ui/config/source"} {
		req := httptest.NewRequest(http.MethodPost, p, strings.NewReader("source=https://github.com/acme/jam@main&plan=x"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "http://evil.example")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("POST %s cross-origin = %d, want 403", p, rec.Code)
		}
	}
	if f.fetches != 0 {
		t.Errorf("refused requests fetched %d times", f.fetches)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/adminui/ -run 'TestConfigPage|TestConfigApply'`
Expected: FAIL. `untracked page` (no Track form), and `POST /ui/config/source` answers 404/405.

- [ ] **Step 3: Implement the plan view and the routes**

In `internal/jam/adminui/config_page.go`, delete the `planView` stub. Add `"bytes"`, `"encoding/json"` and `"fmt"` to the imports, and add:

```go
// planOpView is one op the plan changes, as the Config page shows it.
type planOpView struct {
	jam.ConfigOp
	Href     string     // the object's page; "" for a create (it does not exist yet)
	Lines    []diffLine // Before → After
	TooLarge bool
}

// planView is a config plan rendered for review: the ops that change
// something (with their diffs), the rest counted or listed, and the digest
// Apply posts back.
type planView struct {
	Source, Commit string // as planned (commit: 7 chars)
	Forced         bool   // planned as a forced apply
	Digest         string
	Ops            []planOpView
	Unchanged      int
	Forget         []jam.ConfigKey
	Unmanaged      []jam.ConfigKey
	KitSources     []jam.ConfigKitSource // commits: 7 chars
	Refusals       []string
	Serve          jam.ServeConfigDiff
	HasDrift       bool // the force checkbox is offered
	Changes        int  // jam.ConfigChanges
}

func newPlanView(res jam.ConfigApplyResult, forced bool) *planView {
	p := res.Plan
	v := &planView{Source: res.Source, Commit: shortConfigCommit(res.Commit), Forced: forced, Digest: p.Digest,
		Forget: p.Forget, Unmanaged: p.Unmanaged, Refusals: p.Refusals, Serve: p.ServeConfig,
		HasDrift: p.HasDrift(), Changes: jam.ConfigChanges(p)}
	for _, ks := range p.KitSources {
		ks.Commit = shortConfigCommit(ks.Commit)
		v.KitSources = append(v.KitSources, ks)
	}
	for _, op := range p.Ops {
		if op.Action == jam.ConfigUnchanged {
			v.Unchanged++
			continue
		}
		ov := planOpView{ConfigOp: op}
		if op.Action != jam.ConfigCreate {
			ov.Href = configObjectHref(op.Kind, op.Name)
		}
		if op.Before != "" || op.After != "" {
			ov.Lines, ov.TooLarge = lineDiff(canonDisplay(op.Kind, op.Before), canonDisplay(op.Kind, op.After))
		}
		v.Ops = append(v.Ops, ov)
	}
	return v
}

// canonDisplay is a canonical form as the plan diff shows it: a kit's stored
// config JSON indented one key per line (its "# source:" line kept); every
// other kind's YAML as is.
func canonDisplay(kind, canon string) string {
	if kind != jam.ConfigKindKit || canon == "" {
		return canon
	}
	body, src, tracked := strings.Cut(canon, "\n# source: ")
	var b bytes.Buffer
	if json.Indent(&b, []byte(body), "", "  ") != nil {
		return canon
	}
	if tracked {
		b.WriteString("\n# source: " + src)
	}
	return b.String()
}

// applyMsg is an apply's outcome for the flash.
func applyMsg(res jam.ConfigApplyResult) string {
	at := ""
	if res.Commit != "" {
		at = " config @" + shortConfigCommit(res.Commit)
	}
	if !res.Applied {
		return "nothing to apply" + at
	}
	k := res.Counts
	return fmt.Sprintf("applied%s: %d created, %d updated, %d deleted, %d adopted, %d forgotten", at,
		k[jam.ConfigCreate], k[jam.ConfigUpdate], k[jam.ConfigDelete], k[jam.ConfigAdopt], k[jam.ConfigForget])
}

// registerConfigGit mounts the Config page's git actions on the tracked
// source: Diff (a dry run), Apply (the reviewed plan's digest), Track.
func registerConfigGit(mux *http.ServeMux, store jam.Store, f jam.KitFetcher, env jam.ConfigEnv, log *slog.Logger, guardWrite func(http.ResponseWriter, *http.Request) bool) {
	mux.HandleFunc("POST /ui/config/diff", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		if err := r.ParseForm(); err != nil {
			renderError(w, http.StatusBadRequest, "invalid form")
			return
		}
		force := r.FormValue("force") == "1" // plans as a forced apply would, so the digests match
		res, err := jam.ApplyConfigSource(r.Context(), store, f, gitsource.Source{}, env,
			jam.ConfigApplyOptions{DryRun: true, Force: force, Now: time.Now()}, log, jam.OperatorID(r))
		if err != nil {
			configFail(w, r, log, err)
			return
		}
		renderFragment(w, r, "config", "config-plan", newPlanView(res, force))
	})
	mux.HandleFunc("POST /ui/config/apply", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		if err := r.ParseForm(); err != nil {
			renderError(w, http.StatusBadRequest, "invalid form")
			return
		}
		digest := strings.TrimSpace(r.FormValue("plan"))
		if digest == "" {
			renderError(w, http.StatusBadRequest, "plan is required: run Diff, review it, then Apply")
			return
		}
		force := r.FormValue("force") == "1"
		res, err := jam.ApplyConfigSource(r.Context(), store, f, gitsource.Source{}, env,
			jam.ConfigApplyOptions{Force: force, WantDigest: digest, Now: time.Now()}, log, jam.OperatorID(r))
		if err != nil {
			configFail(w, r, log, err)
			return
		}
		log.Info("ui config applied", "operator", jam.OperatorID(r), "source", res.Source, "commit", res.Commit, "applied", res.Applied, "forced", force)
		renderConfigBody(w, r, buildConfigPage(store, true, log), applyMsg(res))
	})
	mux.HandleFunc("POST /ui/config/source", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		src, ok := formSource(w, r)
		if !ok {
			return
		}
		res, err := jam.TrackConfigSource(r.Context(), store, f, src, env, time.Now(), log, jam.OperatorID(r))
		if err != nil {
			configFail(w, r, log, err)
			return
		}
		log.Info("ui config tracked", "operator", jam.OperatorID(r), "source", res.Source, "commit", res.Commit)
		d := buildConfigPage(store, true, log)
		d.Plan = newPlanView(res, false)
		renderConfigBody(w, r, d, "tracking "+res.Source)
	})
}
```

(add `"github.com/aethons-tools/cove/internal/jam/gitsource"` to the imports). At the end of `registerConfig`, add:

```go
	if canGit {
		registerConfigGit(mux, store, f, env, log, guardWrite)
	}
```

- [ ] **Step 4: Add the Diff button, Track form and plan to the template**

In `templates/config.html`:

1. Inside `{{with .Source}}`'s `<div class="form-actions">`, before the Untrack button:

```html
        {{if $.CanGit}}<form class="inline" hx-post="/ui/config/diff" hx-target="#config-plan" hx-swap="innerHTML" hx-disabled-elt="find button"><button type="submit" class="primary small">Diff</button></form>{{end}}
```

2. In the `{{else}}` branch, replace `{{if not $.CanGit}}…{{end}}` with:

```html
      {{if $.CanGit}}
      <form hx-post="/ui/config/source" hx-target="#config" hx-swap="outerHTML" hx-disabled-elt="find button[type=submit]">
        <label>Source <span class="hint"><code>https://host/org/repo[//dir]@ref</code> — fetched and planned (nothing is applied), then tracked</span>
          <input name="source" required autocomplete="off" spellcheck="false" placeholder="https://github.com/acme/jam-config@main"></label>
        <div class="form-actions"><button type="submit" class="primary">Track</button></div>
      </form>
      {{else}}<p class="unset">Git actions are not available on this serve; use <code>at-jam config apply --dir</code>.</p>{{end}}
```

3. Replace `<div id="config-plan"></div>` with `<div id="config-plan">{{with .Plan}}{{template "config-plan" .}}{{end}}</div>`.

4. Append:

```html
{{define "config-plan"}}
<section class="card full">
  <header><h2>Plan</h2><span class="sub">{{.Source}}{{with .Commit}} @ <span class="mono">{{.}}</span>{{end}} · {{.Changes}} change(s), {{.Unchanged}} unchanged{{if .Forced}} · forced{{end}}</span></header>
  <div class="body">
    {{with .Refusals}}<div class="banner error"><div><strong>Refused — nothing can be applied until the repo or Jam changes:</strong><ul>{{range .}}<li>{{.}}</li>{{end}}</ul></div></div>{{end}}
    {{if .HasDrift}}<label class="check"><input type="checkbox" name="force" value="1"{{if .Forced}} checked{{end}}
        hx-post="/ui/config/diff" hx-trigger="change" hx-target="#config-plan" hx-swap="innerHTML" hx-disabled-elt="this">
      Force: overwrite the objects edited in Jam since their apply with the repo's version</label>{{end}}
    {{with .KitSources}}<p class="sub">Kit sources: {{range $i, $k := .}}{{if $i}}, {{end}}<span class="mono">{{$k.Path}}</span> {{$k.Source}} @ <span class="mono">{{$k.Commit}}</span>{{end}}</p>{{end}}
  </div>
  <table>
    <thead><tr><th>Action</th><th>Object</th><th>File</th></tr></thead>
    <tbody>
    {{range .Ops}}
      <tr><td><span class="chip">{{.Action}}</span>{{if .Drift}} <span class="chip st-drifted">drift</span>{{end}}</td>
        <td><span class="chip">{{.Kind}}</span> {{if .Href}}<a class="mono" href="{{.Href}}">{{.Name}}</a>{{else}}<span class="mono">{{.Name}}</span>{{end}}</td>
        <td class="mono">{{.Path}}</td></tr>
      {{if or .Lines .TooLarge}}<tr><td colspan="3"><details{{if .Drift}} open{{end}}><summary>{{if .Drift}}Jam (−) against the repo (+){{else}}Changes{{end}}</summary>
        {{if .TooLarge}}<span class="unset">Too large to diff here.</span>{{else}}<pre class="diff">{{range .Lines}}{{if eq .Op "+"}}<span class="d-add">+ {{.Text}}</span>{{else if eq .Op "-"}}<span class="d-del">- {{.Text}}</span>{{else if eq .Op "…"}}<span class="d-skip">  … {{.Text}}</span>{{else}}<span>  {{.Text}}</span>{{end}}{{end}}</pre>{{end}}
      </details></td></tr>{{end}}
    {{else}}<tr><td colspan="3" class="empty">Nothing to change.</td></tr>{{end}}
    </tbody>
  </table>
  <div class="body">
    {{with .Forget}}<p class="sub">Forget (mark only, the object is gone from Jam and the repo): {{range $i, $k := .}}{{if $i}}, {{end}}{{$k.Kind}} {{$k.Name}}{{end}}</p>{{end}}
    {{with .Unmanaged}}<p class="sub">Unmanaged (left alone): {{range $i, $k := .}}{{if $i}}, {{end}}{{$k.Kind}} {{$k.Name}}{{end}}</p>{{end}}
    {{if eq .Serve.State "differs"}}<p class="sub">Serve config: differs (restart to apply): {{range $i, $k := .Serve.Keys}}{{if $i}}, {{end}}<span class="mono">{{$k}}</span>{{end}}</p>
    {{else if .Serve.State}}<p class="sub">Serve config: {{.Serve.State}}</p>{{end}}
    {{if not .Refusals}}
    <form hx-post="/ui/config/apply" hx-target="#config" hx-swap="outerHTML" hx-disabled-elt="find button"
        hx-confirm="Apply {{.Changes}} change(s) to this Jam{{if .Forced}}, overwriting the drifted objects{{end}}?">
      <input type="hidden" name="plan" value="{{.Digest}}">
      {{if .Forced}}<input type="hidden" name="force" value="1">{{end}}
      <div class="form-actions"><button type="submit" class="primary">Apply this plan</button></div>
    </form>
    {{end}}
  </div>
</section>
{{end}}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/adminui/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/jam/adminui/config_page.go internal/jam/adminui/config_page_test.go internal/jam/adminui/templates/config.html
git commit -m "feat(adminui): Diff, Apply (digest-bound, force only with drift) and Track on the Config page

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 9: Docs

**Files:**
- Create: `docs/usage/jam/ui-config.md`
- Modify: `docs/usage/jam/config-git.md`, `docs/usage/jam/config-git-sources.md`, `docs/usage/jam/monitoring.md`, `docs/usage/jam/ui.md`, `docs/usage/jam/ui-editing.md`, `docs/usage/jam/INDEX.md`

**Interfaces:**
- Consumes: the behavior of Tasks 1–8. Follow the docs-author skill: each fact has one owner and the others link to it.
- Produces: no code.

- [ ] **Step 1: Create `docs/usage/jam/ui-config.md`**

```markdown
---
summary: The admin UI's Config tab (/ui/config) — the tracked config source and its last apply, the managed objects and their state (in sync, drifted, missing), Diff/Apply/Track/Untrack/Release — and the `managed · config @<commit>` badge and drift warning on object pages.
read_when: You are using the admin UI's Config tab to review or apply the tracked config repo, see a `managed · config @…` badge or a "This will drift from the config repo" prompt on an object page, or want to stop the repo managing an object from the UI.
owns: the Config page (source card, managed-objects table and states, the rendered plan, Apply's digest binding and force checkbox, Track/Untrack/Release from the UI), the managed badge and the drift-on-save warning
prereqs: config-git.md (plan actions, marks, drift); config-git-sources.md (the tracked source); ui-editing.md (the write banner, CSRF and audit rules)
tier: leaf
updated: 2026-10-22
---

# The Config tab and managed objects

Jam's **Config** tab (`/ui/config`, between Specs and Intercom) is the UI for
a Jam's configuration in git ([config-git.md](config-git.md)). It works on the
**tracked source** ([config-git-sources.md](config-git-sources.md)); `--git`
other sources and `--dir` checkouts stay in the CLI.

## The page

- **Source** — the tracked repo, dir and ref, and the commit and time of the
  last apply from it, linked on GitHub and GitLab. **Untrack** forgets it
  (objects and marks stay). With none tracked, a **Track** form fetches and
  plans a source, shows the plan and records it; nothing is applied.
- **Managed objects** — every object the repo manages: the commit it was
  applied from, when, and its state: `in sync`, `drifted` (edited in Jam
  since), or `missing` (removed in Jam since). The header counts those that
  differ, and the newest apply (covering a `--dir` Jam). A missing object and
  a connection (no page) are not linked. **Release** stops the repo managing
  one, as `config release` does. If the live Jam cannot be compared (a kit
  that does not parse), states read `unknown`.

## Diff and Apply

**Diff** plans the tracked source and renders the plan: each changing op
with its file and a line diff (a drifted op opens Jam's side against the
repo's; a kit's config is shown indented), kit sources and their commits,
forgotten marks, unmanaged objects, the serve-config comparison, and any
refusals. **Apply** sends back the digest of the plan on screen: if Jam or
the repo changed since, it answers 409 and writes nothing — Diff again. It
is offered only when nothing is refused.

When the plan has drift, a **Force** checkbox re-plans as a forced apply
(drift becomes `update`/`delete`, not a refusal); Apply then carries that
plan's digest and `force`. The checkbox does not appear otherwise. Without a
git fetcher on the serve, Diff, Apply and Track are not offered.

## Badges and the drift warning

An object the repo manages shows a **`managed · config @<commit[:7]>`** badge
(`@dir` for a `--dir` apply) by its name, linking here: destinations,
model-specs, kits, users, projects, roles, and the Jam context card on the
Dashboard. Every write to one — save, delete, rename, member, room,
escalation and context edits, kit push, pin, sync, track and untrack — first
asks **"This will drift from the config repo (managed · config @…).
Save anyway?"**. Fields the repo does not hold (a user's OIDC identities and
accounts, standing reset/upgrade, grants) do not ask. The prompt is the
browser's; the admin API and CLI do not ask. A drifted object raises
`config.drift` within a minute ([config-git.md](config-git.md#drift-and-release)).

Every write here obeys the UI's gate, CSRF origin check and audit logging
([ui-editing.md](ui-editing.md)); errors show in the page banner with the
admin API's status and text.
```

- [ ] **Step 2: Update `config-git.md`**

1. Frontmatter:
   - `read_when` becomes: `You want a Jam's config reviewable in git, are standing up a replica from a checkout, a config diff/apply refused, flagged drift or listed something unmanaged, or you want the repo to stop managing an object (config release).`
   - `owns` becomes: `the config repo layout and file kinds, the at-jam config write|diff|apply|release verbs, the plan actions and refusals, managed marks, drift and how it clears, the /admin/config/diff|apply|export-tree|managed routes`
2. Verbs table: add after the `config apply` row:

```markdown
| `config release <kind>/<name>` | Clear the object's managed mark (a role is `role/<project>/<role>`): the object stays, unmanaged — the next plan lists it as unmanaged, or adopts or refuses it if the repo still holds it. Not managed: exit 1 (`<kind> <name> is not managed`). |
```

3. Operator loop: the sentence ending `prints `` `nothing to apply` `` and does not prompt.` gets this appended before its period:

```markdown
; it still sends the apply, which refreshes the marks of unchanged objects (after a `config write`, say)
```

4. The plan table's `update` + `(drift)` row: append to its meaning cell:

```markdown
 A drifted `delete` is printed the same way, its repo side `(not in the repo)`.
```

5. Routes table: add the row below, and change the sentence `` `config write|diff|apply` call these `` to `` `config write|diff|apply|release` call these ``:

```markdown
| `DELETE /admin/config/managed/{kind}/{name}` | Release a mark (204); unknown kind 400, not managed 404. |
```

6. Replace the `## Not yet` section (its heading and its one line) with:

```markdown
## Drift and release

An object is **drifted** when Jam's copy no longer has the digest its mark
recorded (edited in Jam since the apply), and **missing** when it was
removed in Jam. While any managed object is either, Jam holds the
`config.drift:jam` condition (info: on the Health tab, never pushed —
[monitoring.md](monitoring.md)). Serve re-checks every minute, and at once
after every apply, sync or release. Three ways out: `config apply --force`
(the repo wins); `config write`, commit, `config apply` (Jam wins — the
apply has nothing to change and refreshes the marks); or `config release
<kind>/<name>` (the repo stops managing it). The admin UI shows all of this
on its Config tab, and badges managed objects: [ui-config.md](ui-config.md).
```

- [ ] **Step 3: Update the other owners**

`config-git-sources.md`: after the paragraph under the Verbs table, add:

```markdown
The admin UI's Config tab tracks, untracks, diffs and applies the tracked source too: [ui-config.md](ui-config.md).
```

`monitoring.md`: add this row to the v1 conditions table:

```markdown
| `config.drift:jam` | info | a config-managed object was edited or removed in Jam since its apply ([config-git.md](config-git.md#drift-and-release)) | an apply, sync or release leaves none drifted (checked every minute and after each) |
```

`ui.md`:
- The tabs sentence becomes `Jam's tabs are **Dashboard · Agents · Users · Specs · Config · Intercom · Health**`.
- After the Health bullet, add the bullet below.
- Keep the file ≤ 200 lines.

```markdown
- **Config** (`/ui/config`) — the tracked config repo, its managed objects and their drift, Diff/Apply: [ui-config.md](ui-config.md).
```

`ui-editing.md`: at the end of `### Config plane (kits, destinations, model-specs)`, add:

```markdown
Objects a config repo manages carry a `managed · config @<commit>` badge, and every write to one asks first — see [ui-config.md](ui-config.md).
```

`docs/usage/jam/INDEX.md`: after the `ui-pages.md` row, add the row below. Then insert `` , or want the repo to stop managing an object (`config release`) `` before the final period of the `config-git.md` row's text.

```markdown
| [ui-config.md](ui-config.md) | You are using the admin UI's Config tab (tracked config repo, managed objects and drift, Diff/Apply/Track/Untrack/Release), or see a `managed · config @…` badge or a drift prompt on an object page. |
```

- [ ] **Step 4: Run the docs audit**

Run: `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs | tail -1`
Expected: `… — 714 error(s), …` or fewer, with no new error naming `usage/jam/`. Also run `wc -l docs/usage/jam/ui.md docs/usage/jam/config-git.md docs/usage/jam/ui-config.md`: each must be ≤ 200.

- [ ] **Step 5: Run everything**

Run: `just test && just lint`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add docs/usage/jam/ui-config.md docs/usage/jam/config-git.md docs/usage/jam/config-git-sources.md docs/usage/jam/monitoring.md docs/usage/jam/ui.md docs/usage/jam/ui-editing.md docs/usage/jam/INDEX.md
git commit -m "docs(jam): the Config tab, managed badges, config release and config.drift

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

## Self-review

- **Spec coverage.**
  - The Config page's source, last commit and time are Task 7; Diff, Apply (htmx, `originGuard`, force only with drift, digest) and Track are Task 8; Untrack is Task 7.
  - Badges for every managed kind are Tasks 5 and 6. Connections have no page (deviation 2).
  - The drift warning before save is Tasks 5 and 6 (deviation 1).
  - `config release` is in the CLI (Task 4), the route and client (Task 3) and the UI (Task 7).
  - `config.drift` (info) and its clearing on apply (Task 2), release (Task 3) and write-and-apply (Tasks 2 and 4) are covered.
  - Docs are Task 9.
  - The slice 2 leftover `GET /admin/config/source` is consciously dropped (deviation 11).
- **Reuse.**
  - Drift calls `exportObjects` + `configObject.Digest` only.
  - The UI calls `ApplyConfigSource`, `TrackConfigSource`, `UntrackConfigSource` and `ReleaseConfigMark`, and maps errors with `jam.ConfigStatus`.
  - The plan diff reuses `lineDiff`; the source card reuses `newSourceView` and the moved `src-repo`/`src-commit` templates; the Track form reuses `formSource`.
- **Placeholder scan.** No `TBD` or "similar to". Task 7's `planView` stub is explicit and replaced in Task 8.
- **Type consistency.**
  - `ConfigEnv.Conditions` (Task 2) is used in Tasks 3, 7 and 8.
  - `ReleaseConfigMark(store, key, t, log, actor)` is defined in Task 3 and called in Task 7.
  - `ConfigMarkStates` / `ConfigMarkState.State` / `ConfigInSync|ConfigDrifted|ConfigMissing` are defined in Task 2 and used in Task 7.
  - `HasDrift` is defined in Task 1 and used in Task 8.
  - `shortConfigCommit` / `configTime` are defined in Task 5 and used in Tasks 7 and 8.
  - `managedSHA`, `applyManaged`, `managedUI` and `trackManaged` are test helpers from Tasks 5 and 7, reused in Task 8.
  - The kit page's 12-char `shortCommit` is unchanged, which is why the config one has its own name.
- **Review Focus.** Each of the five lines names its test, and each test sits in the task that owns the code.
