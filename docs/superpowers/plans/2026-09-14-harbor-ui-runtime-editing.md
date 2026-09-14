# Harbor Admin UI — Runtime Editing (raise/teardown) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add raise-a-managed-cove and teardown-a-cove actions to the admin UI's Coves page, reusing the day-job write foundation but calling the runtime `Supervisor`.

**Architecture:** Thread `*harbor.Supervisor` into `adminui.Handler` → `registerWrites`. New `POST /ui/coves` (raise) and `DELETE /ui/coves/{id}` (teardown) routes in `writes.go`, CSRF-guarded and gated like every other `/ui/` write, calling `sup.Raise`/`sup.Teardown`. Raise's returned token + launch secret are discarded (never shown or logged). The Coves page renders the raise form + teardown buttons only when a supervisor is configured (`CanEdit = sup != nil`); with no runtime it stays the read-only view and the routes 503.

**Tech Stack:** Go stdlib (`net/http`, `log/slog`, `html/template`), htmx (vendored). No new dependency.

## Global Constraints

- **Module:** `github.com/aethons-tools/cove`. Work stays in `internal/harbor/adminui`, `cmd/at-harbor/main.go`, and `docs/`.
- **Write-path:** cove writes go through `/ui/` inside the UI gate — never through `/admin/*` (unchanged). Call `sup.Raise`/`sup.Teardown`; do not reimplement supervisor logic.
- **CSRF:** every `POST`/`DELETE` under `/ui/` calls `guardWrite(w, r)` as its FIRST statement (fail-closed Origin check, already implemented) → 403 on mismatch.
- **No secret surface:** `sup.Raise` returns `(inst, token, launchSecret, err)` — discard `token` and `launchSecret` (`_`); never render or log them. Log only operator/id/project/role. The coves table shows only `CoveSummary` (already secret-free).
- **Supervisor-gated:** when `sup == nil`, the raise/teardown routes return 503 ("runtime supervisor not configured") and the Coves template omits the raise form + teardown buttons. The read-only page (no runtime) must be byte-for-byte unchanged.
- **Tests hermetic:** a fake implementing the exported `harbor.Launcher` + `harbor.NewSupervisor` + `harbor.NewFileStore`; `httptest`. No Docker/network/VM, no `integration` tag.
- **TDD:** failing test first, watch it fail, implement minimally, watch it pass, commit.
- **Build/test:** `go test ./internal/harbor/adminui/ ./cmd/at-harbor/`; `go build ./...`; `just lint`. (Cold cache: prefix `GOPROXY=https://proxy.golang.org GOSUMDB=off`.)
- **Commit trailer:** end every commit message with EXACTLY, verbatim regardless of the implementing model:
  ```
  Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv
  ```
- **Docs rule:** not done until `docs/usage/harbor/ui.md` is updated (Task 3).

## Reference — current shapes (already in the tree)

- `func Handler(store harbor.Store, log *slog.Logger) http.Handler` (`adminui/adminui.go:79`) — GET routes + `registerWrites(mux, store, log)`.
- `func registerWrites(mux *http.ServeMux, store harbor.Store, log *slog.Logger)` (`adminui/writes.go:82`) — has enroll/revoke/role/grant handlers + helpers `guardWrite`/`sameOrigin`/`splitCSV`/`overridesFrom`/`renderError`/`orDefaultProject`.
- `harbor.RaiseSpec{ActorID, Project, Role, Unit, Prompt}`; `sup.Raise(ctx, RaiseSpec) (Instance, token string, launchSecret string, err error)`; `sup.Teardown(ctx, id string) error`.
- `harbor.Launcher` interface: `Raise(ctx, RaiseSpec, LaunchCreds) (string, error)`, `Teardown(ctx, Instance) error`, `Probe(ctx, Instance) (Liveness, error)`, `Pause(ctx, Instance) error`, `Unpause(ctx, Instance) error`. `LaunchCreds{IdentityToken, LaunchSecret}`; `Liveness` consts `LivenessUnknown|Alive|Dead`.
- `func harbor.NewSupervisor(store Store, launcher Launcher, holder string, ttl, reconcile time.Duration, now func() time.Time, log *slog.Logger) *Supervisor`.
- `harbor.CoveSummaries(store)` renders the coves table; `renderFragment(w, "coves", "coves-table", data)` renders the live fragment.

---

### Task 1: thread the Supervisor + raise a cove

**Files:**
- Modify: `internal/harbor/adminui/adminui.go` (`Handler` gains `sup`; `CanEdit` into coves + dashboard data; pass `sup` to `registerWrites`)
- Modify: `internal/harbor/adminui/writes.go` (`registerWrites` gains `sup`; add `POST /ui/coves`)
- Modify: `internal/harbor/adminui/templates/coves.html` (raise form behind `CanEdit`)
- Modify: `cmd/at-harbor/main.go` (pass `sup` to `adminui.Handler`)
- Test: `internal/harbor/adminui/coves_write_test.go` (new); update `adminui.Handler(...)` call sites in the adminui `_test.go` files

**Interfaces:**
- Produces:
  - `func Handler(store harbor.Store, log *slog.Logger, sup *harbor.Supervisor) http.Handler`
  - `POST /ui/coves` — form `id`+`role` required, `project`/`unit`/`prompt` optional; raises via `sup.Raise`, discards token+secret, returns the `coves-table` fragment. `sup == nil` → 503.
  - Coves + dashboard template data carry `"CanEdit": sup != nil`.

- [ ] **Step 1: Write the failing test**

Create `internal/harbor/adminui/coves_write_test.go`:

```go
package adminui_test

import (
	"context"
	"log/slog"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/harbor"
	"github.com/aethons-tools/cove/internal/harbor/adminui"
)

// fakeLauncher implements the exported harbor.Launcher for hermetic cove tests.
type fakeLauncher struct{}

func (fakeLauncher) Raise(_ context.Context, spec harbor.RaiseSpec, _ harbor.LaunchCreds) (string, error) {
	return "loc-" + spec.ActorID, nil
}
func (fakeLauncher) Teardown(_ context.Context, _ harbor.Instance) error         { return nil }
func (fakeLauncher) Probe(_ context.Context, _ harbor.Instance) (harbor.Liveness, error) {
	return harbor.LivenessAlive, nil
}
func (fakeLauncher) Pause(_ context.Context, _ harbor.Instance) error   { return nil }
func (fakeLauncher) Unpause(_ context.Context, _ harbor.Instance) error { return nil }

func newSup(t *testing.T, store harbor.Store) *harbor.Supervisor {
	t.Helper()
	return harbor.NewSupervisor(store, fakeLauncher{}, "test-holder",
		60*time.Second, 30*time.Second, time.Now,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// covePost issues a form POST with a matching Origin (passes CSRF).
func covePost(t *testing.T, h http.Handler, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://"+req.Host)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRaiseCove(t *testing.T) {
	store := newStore(t)
	if err := store.PutRole("acme", harbor.Role{Name: "worker"}); err != nil {
		t.Fatal(err)
	}
	h := adminui.Handler(store, testLogger(), newSup(t, store))
	rec := covePost(t, h, "/ui/coves", url.Values{"id": {"cove-1"}, "project": {"acme"}, "role": {"worker"}, "prompt": {"do the thing"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("raise = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	found := false
	for _, i := range store.ListInstances() {
		if i.ActorID == "cove-1" {
			found = true
		}
	}
	if !found {
		t.Fatal("instance cove-1 was not registered")
	}
	if !strings.Contains(rec.Body.String(), "cove-1") {
		t.Errorf("coves fragment should list the new cove; got:\n%s", rec.Body.String())
	}
}

func TestRaiseCoveValidation(t *testing.T) {
	store := newStore(t)
	h := adminui.Handler(store, testLogger(), newSup(t, store))
	rec := covePost(t, h, "/ui/coves", url.Values{"id": {"cove-x"}}) // missing role
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "role is required") {
		t.Fatalf("missing role = %d %q, want 400 + inline error", rec.Code, rec.Body.String())
	}
}

func TestRaiseCoveNoRuntime503(t *testing.T) {
	store := newStore(t)
	h := adminui.Handler(store, testLogger(), nil) // no supervisor
	rec := covePost(t, h, "/ui/coves", url.Values{"id": {"cove-1"}, "role": {"worker"}})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("raise with no runtime = %d, want 503", rec.Code)
	}
	// And the Coves page hides the raise form.
	page := get(t, h, "/ui/coves").Body.String()
	if strings.Contains(page, `hx-post="/ui/coves"`) {
		t.Error("read-only Coves page must not render the raise form")
	}
}

func TestRaiseCoveCSRF(t *testing.T) {
	store := newStore(t)
	h := adminui.Handler(store, testLogger(), newSup(t, store))
	req := httptest.NewRequest(http.MethodPost, "/ui/coves", strings.NewReader("id=x&role=worker"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://evil.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin raise = %d, want 403", rec.Code)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/harbor/adminui/ 2>&1 | head`
Expected: FAIL — `Handler` now needs 3 args (compile error until call sites updated) and `/ui/coves` POST doesn't exist.

- [ ] **Step 3: Thread `sup` in `adminui.go`**

Change the signature and thread `CanEdit` into the coves + dashboard data:

```go
func Handler(store harbor.Store, log *slog.Logger, sup *harbor.Supervisor) http.Handler {
	mux := http.NewServeMux()
	canEdit := sup != nil

	mux.Handle("GET /ui/static/", http.StripPrefix("/ui/static/", http.FileServer(http.FS(staticFS))))

	mux.HandleFunc("GET /ui/{$}", func(w http.ResponseWriter, r *http.Request) {
		render(w, "dashboard", map[string]any{
			"Title": "Dashboard", "Coves": harbor.CoveSummaries(store),
			"Actors": harbor.RosterSummaries(store), "CanEdit": canEdit,
		})
	})

	mux.HandleFunc("GET /ui/coves", func(w http.ResponseWriter, r *http.Request) {
		data := map[string]any{"Title": "Coves", "Coves": harbor.CoveSummaries(store), "CanEdit": canEdit}
		if r.Header.Get("HX-Request") == "true" {
			renderFragment(w, "coves", "coves-table", data)
			return
		}
		render(w, "coves", data)
	})
	// ... roster/roles/kits/destinations GET routes unchanged ...

	registerWrites(mux, store, log, sup)
	return mux
}
```

(The other GET handlers are unchanged. Add `"CanEdit": canEdit` only to the dashboard and coves data maps.)

- [ ] **Step 4: Add `sup` + the raise handler in `writes.go`**

Change the signature: `func registerWrites(mux *http.ServeMux, store harbor.Store, log *slog.Logger, sup *harbor.Supervisor) {` and add:

```go
	mux.HandleFunc("POST /ui/coves", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		if sup == nil {
			http.Error(w, "runtime supervisor not configured", http.StatusServiceUnavailable)
			return
		}
		if err := r.ParseForm(); err != nil {
			renderError(w, http.StatusBadRequest, "invalid form")
			return
		}
		id := strings.TrimSpace(r.FormValue("id"))
		if id == "" {
			renderError(w, http.StatusBadRequest, "id is required")
			return
		}
		role := strings.TrimSpace(r.FormValue("role"))
		if role == "" {
			renderError(w, http.StatusBadRequest, "role is required")
			return
		}
		project := strings.TrimSpace(r.FormValue("project"))
		// Discard the returned identity token + launch secret: with a real launcher
		// harbor consumes them internally; they must never reach the browser or a log.
		_, _, _, err := sup.Raise(r.Context(), harbor.RaiseSpec{
			ActorID: id, Project: project, Role: role,
			Unit: strings.TrimSpace(r.FormValue("unit")), Prompt: r.FormValue("prompt"),
		})
		if err != nil {
			renderError(w, http.StatusBadRequest, err.Error())
			return
		}
		log.Info("ui cove raised", "operator", harbor.OperatorID(r), "id", id, "project", orDefaultProject(project), "role", role)
		renderFragment(w, "coves", "coves-table", map[string]any{"Coves": harbor.CoveSummaries(store), "CanEdit": true})
	})
```

- [ ] **Step 5: Add the raise form in `coves.html`**

Above the `coves-table` fragment (inside the page `content` define, not inside `coves-table`), add:

```html
{{if .CanEdit}}
<form hx-post="/ui/coves" hx-target="#coves" hx-swap="outerHTML">
  <input name="id" placeholder="cove id" required>
  <input name="project" placeholder="project (default)">
  <input name="role" placeholder="role" required>
  <input name="unit" placeholder="unit (optional)">
  <textarea name="prompt" placeholder="workload prompt (optional)"></textarea>
  <button type="submit">Raise cove</button>
</form>
{{end}}
```

Ensure the `coves-table`'s `<table>` has `id="coves"` so the form's `hx-target="#coves"` resolves (it already carries `id="coves"` from the read-only slice — verify).

- [ ] **Step 6: Update call sites**

- `cmd/at-harbor/main.go`: `gate.Wrap(adminui.Handler(st, log))` → `gate.Wrap(adminui.Handler(st, log, sup))` (the `sup` in scope, already passed to `NewAdminHandler`).
- Every `adminui.Handler(<store>, testLogger())` in the adminui `_test.go` files → add `, nil` (read-only) as the third arg. `grep -rn 'adminui.Handler(' internal/harbor/adminui/` finds them; the new cove tests pass `newSup(t, store)` instead of nil.

- [ ] **Step 7: Run tests to verify they pass**

Run: `go test ./internal/harbor/adminui/ ./cmd/at-harbor/ -v 2>&1 | tail -25`
Expected: PASS (raise happy/validation/503/CSRF + all prior adminui tests with the added nil arg).

- [ ] **Step 8: Commit**

```bash
git add internal/harbor/adminui/ cmd/at-harbor/main.go
git commit -m "$(cat <<'EOF'
feat(harbor/adminui): raise a managed cove from the UI (supervisor-gated)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv
EOF
)"
```

---

### Task 2: teardown a cove

**Files:**
- Modify: `internal/harbor/adminui/writes.go` (add `DELETE /ui/coves/{id}`)
- Modify: `internal/harbor/adminui/templates/coves.html` (per-row teardown button behind `CanEdit`)
- Test: `internal/harbor/adminui/coves_write_test.go`

**Interfaces:**
- Produces: `DELETE /ui/coves/{id}` — `sup.Teardown`; returns the `coves-table` fragment; `sup == nil` → 503.

- [ ] **Step 1: Write the failing test**

Add to `coves_write_test.go`:

```go
func TestTeardownCove(t *testing.T) {
	store := newStore(t)
	if err := store.PutRole("acme", harbor.Role{Name: "worker"}); err != nil {
		t.Fatal(err)
	}
	h := adminui.Handler(store, testLogger(), newSup(t, store))
	// Raise one first.
	if rec := covePost(t, h, "/ui/coves", url.Values{"id": {"cove-2"}, "project": {"acme"}, "role": {"worker"}}); rec.Code != http.StatusOK {
		t.Fatalf("setup raise = %d", rec.Code)
	}
	// Teardown it.
	req := httptest.NewRequest(http.MethodDelete, "/ui/coves/cove-2", nil)
	req.Header.Set("Origin", "http://"+req.Host)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("teardown = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	// The instance is gone or transitioning (fake Teardown → supervisor drops/marks it).
	for _, i := range store.ListInstances() {
		if i.ActorID == "cove-2" && i.Phase != harbor.PhaseTerminating && i.Phase != harbor.PhaseGone {
			t.Errorf("cove-2 still %s after teardown, want terminating/gone/removed", i.Phase)
		}
	}
}

func TestTeardownCoveNoRuntime503(t *testing.T) {
	store := newStore(t)
	h := adminui.Handler(store, testLogger(), nil)
	req := httptest.NewRequest(http.MethodDelete, "/ui/coves/x", nil)
	req.Header.Set("Origin", "http://"+req.Host)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("teardown with no runtime = %d, want 503", rec.Code)
	}
}
```

(If `sup.Teardown` on the fake removes the instance outright, the loop simply finds nothing — also acceptable. Adjust the assertion to whatever the fake+supervisor actually do; the point is teardown succeeded and the cove is not left `live`.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/harbor/adminui/ -run TestTeardownCove -v`
Expected: FAIL — `DELETE /ui/coves/{id}` not registered.

- [ ] **Step 3: Add the teardown handler** to `registerWrites`:

```go
	mux.HandleFunc("DELETE /ui/coves/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		if sup == nil {
			http.Error(w, "runtime supervisor not configured", http.StatusServiceUnavailable)
			return
		}
		id := r.PathValue("id")
		if err := sup.Teardown(r.Context(), id); err != nil {
			renderError(w, http.StatusInternalServerError, err.Error())
			return
		}
		log.Info("ui cove torn down", "operator", harbor.OperatorID(r), "id", id)
		renderFragment(w, "coves", "coves-table", map[string]any{"Coves": harbor.CoveSummaries(store), "CanEdit": true})
	})
```

- [ ] **Step 4: Add the teardown button** in `coves.html`'s `coves-table` rows (use `$.CanEdit` since `.` is the row). Add a conditional Actions header cell and body cell:

```html
{{if $.CanEdit}}<td><button hx-delete="/ui/coves/{{.ID}}" hx-target="#coves" hx-swap="outerHTML"
    hx-confirm="Tear down {{.ID}}?">Teardown</button></td>{{end}}
```

Add the matching conditional `<th>Actions</th>` in the header row so column counts line up, and bump any `colspan` on the empty-state row when `$.CanEdit`.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/harbor/adminui/ -v 2>&1 | tail -20`
Expected: PASS (teardown + 503 + all prior).

- [ ] **Step 6: Commit**

```bash
git add internal/harbor/adminui/
git commit -m "$(cat <<'EOF'
feat(harbor/adminui): teardown a cove from the Coves page (confirmed)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv
EOF
)"
```

---

### Task 3: Documentation

**Files:**
- Modify: `docs/usage/harbor/ui.md` (extend the Editing section)
- Test: the docs-audit checker

- [ ] **Step 1: Extend the Editing section** in `docs/usage/harbor/ui.md`:

```markdown
### Runtime (coves)

When harbor is configured with a runtime supervisor (`runtime:` in the serve
config — see [coves.md](coves.md)), the Coves page can also:

- **Raise a managed cove** — id, role, optional project/unit and a workload
  prompt. Harbor handles the cove's identity token and launch secret internally;
  they are never shown in the browser (use the CLI `at-harbor cove raise` for
  manual wiring).
- **Tear down a cove** (confirmed).

Without a runtime supervisor, the Coves page is view-only. Setting a cove's
activity is not a UI action — that is reported by the cove itself. These actions
obey the same gate, CSRF, and audit-logging as the roster edits above.
```

- [ ] **Step 2: Run the docs audit**

Run: `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs --index OVERVIEW.md`
Expected: no NEW findings touching `ui.md` (pre-existing `superpowers/` backlog out of scope).

- [ ] **Step 3: Commit**

```bash
git add docs/usage/harbor/ui.md
git commit -m "$(cat <<'EOF'
docs(harbor): document raise/teardown cove actions in the admin UI

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv
EOF
)"
```

---

### Task 4: Full verification pass

**Files:** none.

- [ ] **Step 1: Full tests** — Run: `GOPROXY=https://proxy.golang.org GOSUMDB=off go test ./...` — Expected: all PASS.
- [ ] **Step 2: Build** — Run: `go build ./...` — Expected: clean.
- [ ] **Step 3: Vet + lint** — Run: `go vet ./internal/harbor/... ./cmd/at-harbor/...` then `just lint` — Expected: clean.
- [ ] **Step 4: No-secret-leak grep** — Run: `grep -nE 'log\.(Info|Warn|Error|Debug)' internal/harbor/adminui/writes.go` and confirm the two cove log lines carry only operator/id/project/role — never a token or launch secret. Also confirm `sup.Raise`'s token/secret returns are discarded (`_, _, _`). Expected: no secret material logged.
- [ ] **Step 5: CSRF + 503 spot-check** — confirm `POST /ui/coves` and `DELETE /ui/coves/{id}` each call `guardWrite` first and the `sup == nil` 503 guard second. Expected: both present in both handlers.

---

## Self-Review

**1. Spec coverage:**
- Raise + teardown as `/ui/` routes calling the Supervisor → Tasks 1 & 2. ✓
- Supervisor threaded into `Handler`/`registerWrites` → Task 1. ✓
- Raise secrets discarded, never shown/logged → Task 1 handler (`_, _, _`), Task 4 grep. ✓
- `CanEdit`-gated controls + 503 + read-only fallback → Task 1 (`canEdit`, dashboard+coves data, `TestRaiseCoveNoRuntime503`), Task 2 (`TestTeardownCoveNoRuntime503`). ✓
- CSRF/gate/attribution reused → `guardWrite` first, `harbor.OperatorID` in logs; `TestRaiseCoveCSRF`. ✓
- Status-setting excluded → no such route added. ✓
- Hermetic tests (fake `Launcher` + `NewSupervisor`) → Task 1 test helpers. ✓
- Docs → Task 3. ✓

**2. Placeholder scan:** Template edits reference the existing `coves.html`/`coves-table` (already in the tree from the read-only slice) and give the exact new markup; all Go handler/test code is complete. The Task 2 teardown assertion notes the fake-dependent instance end-state explicitly rather than guessing. No TBD/TODO.

**3. Type consistency:**
- `Handler(store, log, sup)` — Task 1 defines; main + all test call sites updated same task. ✓
- `registerWrites(mux, store, log, sup)` — Task 1 defines; Task 2 adds a handler into the same function. ✓
- `sup.Raise(ctx, harbor.RaiseSpec{...}) (Instance, string, string, error)` — 4 returns, token+secret discarded `_, _, _`. ✓
- `harbor.Launcher` fake implements all five methods with the exact signatures; `harbor.NewSupervisor(...)` 7-arg call matches. ✓
- `"CanEdit"` data key ↔ `{{if .CanEdit}}` (form) and `{{if $.CanEdit}}` (inside the `coves-table` range) — Tasks 1 & 2. ✓
- `renderFragment(w, "coves", "coves-table", data)` — existing signature, reused. ✓

Note carried to execution: the write handlers' fragment render passes `"CanEdit": true` (they only run when `sup != nil`), so teardown buttons persist in the swapped fragment; the GET handlers pass `canEdit` (the real value) so the read-only page stays button-free.
