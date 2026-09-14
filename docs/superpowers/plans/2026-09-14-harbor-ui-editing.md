# Harbor Admin UI — Day-Job Editing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add mutating UI actions for the operator's day-job — enroll/revoke actors and manage roles + grants — to the harbor admin UI, behind the existing UI gate.

**Architecture:** New POST/DELETE routes under `/ui/` in `internal/harbor/adminui`, handled in-process against the `Store`, reusing the admin API's mutation logic (`harbor.Enroll`, `Store.PutRole`/`RemoveRole`/`AddGrant`/`RemoveGrant`). Every state-changing request is CSRF-guarded by an Origin/Referer-vs-Host check. Writes are attributed to the operator the UI gate resolved (session `sub`, or `local` on loopback), logged like the JSON API. htmx forms swap re-rendered table fragments; enroll shows a one-time token panel.

**Tech Stack:** Go stdlib (`net/http`, `net/url`, `html/template`, `log/slog`), htmx (already vendored). No new dependency.

## Global Constraints

- **Module:** `github.com/aethons-tools/cove`. Work stays in `internal/harbor/adminui`, `internal/harbor/browserauth`, `internal/harbor` (export rename), `cmd/at-harbor/main.go`, and `docs/`.
- **Write-path:** browser writes go through `/ui/` routes inside the UI gate — never through `/admin/*` (which stays the pure bearer API). Reuse the existing mutation functions; do not duplicate their validation logic beyond what a form needs.
- **CSRF:** every `POST`/`DELETE` under `/ui/` must pass an Origin/Referer-vs-Host check (fail-closed when both headers are absent) → `403` on mismatch. `GET` routes are unaffected.
- **No secret leak:** the enroll identity token appears only in the single enroll response body; never rendered into a list, persisted, or logged. Token hashes / launch secrets / credential values are never shown.
- **Read-only invariants preserved:** existing GET views and the browser-login flow are unchanged.
- **Tests hermetic:** `httptest` + `harbor.NewFileStore` (via the existing `newStore` helper). No Docker/network/VM, no `integration` tag.
- **TDD:** failing test first, watch it fail, implement minimally, watch it pass, commit.
- **Build/test:** `go test ./internal/harbor/... ./cmd/at-harbor/...`; `go build ./...`; `just lint`. (If a cold cache 403s on `golang.org/x/…`, prefix `GOPROXY=https://proxy.golang.org GOSUMDB=off`.)
- **Commit trailer:** end every commit message with EXACTLY, verbatim regardless of the implementing model:
  ```
  Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv
  ```
- **Docs rule:** not done until `docs/usage/harbor/ui.md` is updated (Task 6).

---

### Task 1: operator attribution — export `harbor.WithOperator`/`OperatorID`, attribute in the gate

**Files:**
- Modify: `internal/harbor/operator.go` (rename `withOperator`→`WithOperator`, `operatorID`→`OperatorID`)
- Modify: `internal/harbor/admin.go` (update the internal call sites of the renamed helpers)
- Modify: `internal/harbor/browserauth/gate.go` (attribute the operator into the request context)
- Test: `internal/harbor/browserauth/gate_test.go`

**Interfaces:**
- Produces:
  - `func WithOperator(r *http.Request, op Operator) *http.Request` and `func OperatorID(r *http.Request) string` in package `harbor` (exported; same behavior as the current unexported pair).
  - `Gate.Wrap` now stamps the resolved operator into the request context: `local` for loopback, the session `Operator` otherwise.

- [ ] **Step 1: Write the failing test**

Add to `internal/harbor/browserauth/gate_test.go`:

```go
func TestGateAttributesOperator(t *testing.T) {
	// Loopback → "local".
	var gotLoopback string
	g := Gate{Sess: nil, LoginPath: "/ui/auth/login", Log: discard()}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/ui/roster", nil)
	req.RemoteAddr = "127.0.0.1:5000"
	g.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotLoopback = harbor.OperatorID(r)
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)
	if gotLoopback != "local" {
		t.Errorf("loopback operator = %q, want local", gotLoopback)
	}

	// Off-loopback valid session → the token's sub.
	idp := newFakeIdP(t)
	auth, err := harbor.NewOIDCAuthenticator(context.Background(), idp.url, "aud", "")
	if err != nil {
		t.Fatal(err)
	}
	var gotSession string
	gs := Gate{Sess: &SessionVerifier{Auth: auth}, LoginPath: "/ui/auth/login", Log: discard()}
	rec = httptest.NewRecorder()
	sreq := httptest.NewRequest("GET", "/ui/roster", nil)
	sreq.RemoteAddr = "203.0.113.7:5555"
	sreq.AddCookie(&http.Cookie{Name: SessionCookie, Value: idp.mintAccess(t, "aud", "auth0|alice", time.Now().Add(time.Hour))})
	gs.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSession = harbor.OperatorID(r)
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, sreq)
	if gotSession != "auth0|alice" {
		t.Errorf("session operator = %q, want auth0|alice", gotSession)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/harbor/browserauth/ -run TestGateAttributesOperator -v`
Expected: FAIL — `harbor.OperatorID` undefined (and the gate doesn't attribute yet).

- [ ] **Step 3: Export the helpers in `operator.go`**

Rename in `internal/harbor/operator.go`:

```go
// WithOperator returns r carrying op, so handlers can attribute a change to the
// authenticated operator in the audit log.
func WithOperator(r *http.Request, op Operator) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), operatorCtxKey{}, op))
}

// OperatorID returns the authenticated operator's id from the request context,
// or "" if the request was not authenticated.
func OperatorID(r *http.Request) string {
	op, _ := r.Context().Value(operatorCtxKey{}).(Operator)
	return op.ID
}
```

In `internal/harbor/admin.go`, update the internal uses: `withOperator(` → `WithOperator(` and `operatorID(` → `OperatorID(` (there are calls in `authMiddleware` and every mutation handler's log line). A repo-wide `grep -rn 'withOperator\|operatorID' internal/harbor/` finds them all; update each, then confirm none of the old names remain.

- [ ] **Step 4: Attribute the operator in `gate.go`**

```go
func (g Gate) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if harbor.IsLoopbackRequest(r) {
			next.ServeHTTP(w, harbor.WithOperator(r, harbor.Operator{ID: "local"}))
			return
		}
		if g.Sess == nil {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if op, ok := g.Sess.verify(r); ok {
			next.ServeHTTP(w, harbor.WithOperator(r, op))
			return
		}
		http.Redirect(w, r, g.LoginPath, http.StatusFound)
	})
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/harbor/... ./cmd/at-harbor/...`
Expected: PASS (new attribution test; the rename compiles everywhere; existing admin tests unaffected — behavior identical).

- [ ] **Step 6: Commit**

```bash
git add internal/harbor/operator.go internal/harbor/admin.go internal/harbor/browserauth/gate.go internal/harbor/browserauth/gate_test.go
git commit -m "$(cat <<'EOF'
refactor(harbor): export WithOperator/OperatorID; gate attributes operator

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv
EOF
)"
```

---

### Task 2: write foundation + enroll actor

**Files:**
- Modify: `internal/harbor/adminui/adminui.go` (add `log *slog.Logger` to `Handler`; call `registerWrites`)
- Create: `internal/harbor/adminui/writes.go` (`sameOrigin`, `guardWrite`, `registerWrites`, the enroll handler, `renderError`)
- Modify: `internal/harbor/adminui/templates/roster.html` (enroll form; `roster-table` fragment; `enroll-result` panel)
- Modify: `cmd/at-harbor/main.go` (pass `log` to `adminui.Handler`)
- Test: `internal/harbor/adminui/writes_test.go`; update `adminui.Handler(...)` call sites in `internal/harbor/adminui/*_test.go`

**Interfaces:**
- Consumes: `harbor.Enroll(store, id, project, role, overrides, now)`, `harbor.OperatorID(r)` (Task 1).
- Produces:
  - `func Handler(store harbor.Store, log *slog.Logger) http.Handler` (added `log` param).
  - `POST /ui/enrollments` — form `id`, `project`, `role`, optional `destinations`/`repos` (comma-separated overrides); on success renders the `enroll-result` panel containing the identity token once; on validation error renders an inline error (400).
  - `func sameOrigin(r *http.Request) bool` — Origin/Referer host == `r.Host` (fail-closed).

- [ ] **Step 1: Update the test call sites + add the write test**

First, thread the logger through the existing adminui tests. Add a helper at the top of `internal/harbor/adminui/adminui_test.go`:

```go
func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
```

(add `io` and `log/slog` to that file's imports), then replace every `adminui.Handler(<store>)` with `adminui.Handler(<store>, testLogger())` across the adminui `_test.go` files (`grep -rn 'adminui.Handler(' internal/harbor/adminui/` finds them).

Create `internal/harbor/adminui/writes_test.go`:

```go
package adminui_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/harbor"
	"github.com/aethons-tools/cove/internal/harbor/adminui"
)

// post issues a form POST with a matching Origin (passes the CSRF check).
func post(t *testing.T, h http.Handler, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://"+req.Host)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestEnrollCreatesActorAndShowsTokenOnce(t *testing.T) {
	store := newStore(t)
	if err := store.PutRole("acme", harbor.Role{Name: "worker"}); err != nil {
		t.Fatal(err)
	}
	h := adminui.Handler(store, testLogger())
	rec := post(t, h, "/ui/enrollments", url.Values{"id": {"spider-1"}, "project": {"acme"}, "role": {"worker"}})
	if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("enroll = %d, want 200/201", rec.Code)
	}
	// The actor now exists.
	found := false
	for _, a := range store.ListActors() {
		if a.ID == "spider-1" {
			found = true
		}
	}
	if !found {
		t.Fatal("actor spider-1 was not created")
	}
	// The token is in this response body...
	body := rec.Body.String()
	if !strings.Contains(strings.ToLower(body), "token") || len(body) < 20 {
		t.Errorf("enroll response should show the token panel; got:\n%s", body)
	}
	// ...but never appears on the roster afterward.
	roster := get(t, h, "/ui/roster").Body.String()
	if strings.Contains(roster, "spider-1") == false {
		t.Error("roster should list the new actor")
	}
	// (No token value is asserted persisted — Store keeps only the hash.)
}

func TestEnrollRejectsCrossOrigin(t *testing.T) {
	store := newStore(t)
	h := adminui.Handler(store, testLogger())
	req := httptest.NewRequest(http.MethodPost, "/ui/enrollments", strings.NewReader("id=x&role=worker"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://evil.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin enroll = %d, want 403", rec.Code)
	}
	// No Origin and no Referer → also refused (fail-closed).
	req2 := httptest.NewRequest(http.MethodPost, "/ui/enrollments", strings.NewReader("id=x&role=worker"))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("no-Origin enroll = %d, want 403", rec2.Code)
	}
}

func TestEnrollValidationError(t *testing.T) {
	store := newStore(t)
	h := adminui.Handler(store, testLogger())
	rec := post(t, h, "/ui/enrollments", url.Values{"id": {""}, "role": {"worker"}}) // missing id
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing id = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "id is required") {
		t.Errorf("expected inline error; got:\n%s", rec.Body.String())
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/harbor/adminui/ 2>&1 | head`
Expected: FAIL — `Handler` now needs a logger arg (compile error until call sites are updated) and `/ui/enrollments` doesn't exist.

- [ ] **Step 3: Add the `log` param + `registerWrites` in `adminui.go`**

Change the signature and register writes at the end of `Handler`:

```go
func Handler(store harbor.Store, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	// ... all existing GET routes unchanged ...
	registerWrites(mux, store, log)
	return mux
}
```

Add `"log/slog"` to `adminui.go` imports.

- [ ] **Step 4: Create `writes.go`**

```go
package adminui

import (
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/harbor"
)

// sameOrigin reports whether a state-changing request's Origin (or, absent that,
// Referer) host matches the request Host. Fail-closed: neither header → false.
func sameOrigin(r *http.Request) bool {
	check := func(v string) (bool, bool) {
		if v == "" {
			return false, false
		}
		u, err := url.Parse(v)
		return err == nil && u.Host == r.Host, true
	}
	if ok, present := check(r.Header.Get("Origin")); present {
		return ok
	}
	if ok, present := check(r.Header.Get("Referer")); present {
		return ok
	}
	return false
}

// guardWrite enforces the CSRF Origin check; it writes a 403 and returns false
// when the request must be refused.
func guardWrite(w http.ResponseWriter, r *http.Request) bool {
	if !sameOrigin(r) {
		http.Error(w, "cross-origin request refused", http.StatusForbidden)
		return false
	}
	return true
}

// splitCSV parses a comma-separated form field into a trimmed, non-empty slice.
func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// overridesFrom builds a *harbor.Override from optional comma-separated fields,
// or nil when both are empty (inherit the role's scope).
func overridesFrom(dests, repos string) *harbor.Override {
	d, rp := splitCSV(dests), splitCSV(repos)
	if len(d) == 0 && len(rp) == 0 {
		return nil
	}
	return &harbor.Override{Destinations: d, Repos: rp}
}

// renderError renders an inline error fragment with the given status.
func renderError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	// Escaped by html/template's HTMLEscapeString via a tiny inline render.
	_, _ = w.Write([]byte(`<p class="error">` + template.HTMLEscapeString(msg) + `</p>`))
}

func registerWrites(mux *http.ServeMux, store harbor.Store, log *slog.Logger) {
	mux.HandleFunc("POST /ui/enrollments", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
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
		overrides := overridesFrom(r.FormValue("destinations"), r.FormValue("repos"))
		token, err := harbor.Enroll(store, id, project, role, overrides, time.Now())
		if err != nil {
			renderError(w, http.StatusBadRequest, err.Error())
			return
		}
		log.Info("ui enrolled", "operator", harbor.OperatorID(r), "id", id, "project", project, "role", role)
		renderFragment(w, "roster", "enroll-result", map[string]any{"ID": id, "Token": token})
	})
}
```

Add `"html/template"` to `writes.go` imports for `renderError`'s `template.HTMLEscapeString`.

- [ ] **Step 5: Add the roster template pieces**

In `internal/harbor/adminui/templates/roster.html`, wrap the existing table in a named fragment and add the enroll form + the result panel. Sketch:

```html
{{define "content"}}
<h1>Roster</h1>
<form hx-post="/ui/enrollments" hx-target="#roster-panel" hx-swap="innerHTML">
  <input name="id" placeholder="actor id" required>
  <input name="project" placeholder="project (default)">
  <input name="role" placeholder="role" required>
  <input name="destinations" placeholder="destination overrides (optional, comma-separated)">
  <input name="repos" placeholder="repo overrides (optional, comma-separated)">
  <button type="submit">Enroll</button>
</form>
<div id="roster-panel"></div>
{{template "roster-table" .}}
{{end}}

{{define "roster-table"}}
<table id="roster">
  ... existing roster rows, ranging over .Actors ...
</table>
{{end}}

{{define "enroll-result"}}
<div class="token-panel">
  <p><strong>Enrolled {{.ID}}.</strong> Copy this identity token now — it is not shown again:</p>
  <code>{{.Token}}</code>
</div>
{{end}}
```

Keep the existing roster table markup (from the read-only slice) inside `roster-table`; only the wrapping `define` + the form/panel are new.

- [ ] **Step 6: Pass the logger in `main.go`**

In `cmd/at-harbor/main.go`, change `gate.Wrap(adminui.Handler(st))` to `gate.Wrap(adminui.Handler(st, log))`.

- [ ] **Step 7: Run tests to verify they pass**

Run: `go test ./internal/harbor/adminui/ ./cmd/at-harbor/ -v 2>&1 | tail -20`
Expected: PASS (enroll happy/token-once, CSRF reject, validation; all prior adminui tests with the updated call sites).

- [ ] **Step 8: Commit**

```bash
git add internal/harbor/adminui/ cmd/at-harbor/main.go
git commit -m "$(cat <<'EOF'
feat(harbor/adminui): enroll actor from the UI (CSRF-guarded, one-time token)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv
EOF
)"
```

---

### Task 3: revoke actor

**Files:**
- Modify: `internal/harbor/adminui/writes.go` (add the revoke handler)
- Modify: `internal/harbor/adminui/templates/roster.html` (a revoke button per row; roster-table fragment re-render)
- Test: `internal/harbor/adminui/writes_test.go`

**Interfaces:**
- Consumes: `store.RemoveActor(id)`, `harbor.RosterSummaries(store)`.
- Produces: `DELETE /ui/enrollments/{id}` — removes the actor; returns the re-rendered `roster-table` fragment.

- [ ] **Step 1: Write the failing test**

```go
func TestRevokeActor(t *testing.T) {
	store := newStore(t)
	if err := store.AddActor(harbor.Actor{ID: "spider-2", TokenHash: "h"}); err != nil {
		t.Fatal(err)
	}
	h := adminui.Handler(store, testLogger())
	req := httptest.NewRequest(http.MethodDelete, "/ui/enrollments/spider-2", nil)
	req.Header.Set("Origin", "http://"+req.Host)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke = %d, want 200", rec.Code)
	}
	if _, ok := findActor(store, "spider-2"); ok {
		t.Error("actor should be gone after revoke")
	}
	if strings.Contains(rec.Body.String(), "spider-2") {
		t.Error("returned roster fragment should not list the revoked actor")
	}
}
```

(Add a small `findActor(store, id) (harbor.Actor, bool)` helper to the test file, or inline the `ListActors` scan.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/harbor/adminui/ -run TestRevokeActor -v`
Expected: FAIL — `DELETE /ui/enrollments/{id}` not registered (404/405).

- [ ] **Step 3: Add the revoke handler** to `registerWrites` in `writes.go`

```go
	mux.HandleFunc("DELETE /ui/enrollments/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		id := r.PathValue("id")
		if err := store.RemoveActor(id); err != nil {
			renderError(w, http.StatusNotFound, err.Error())
			return
		}
		log.Info("ui revoked", "operator", harbor.OperatorID(r), "id", id)
		renderFragment(w, "roster", "roster-table", map[string]any{"Actors": harbor.RosterSummaries(store)})
	})
```

- [ ] **Step 4: Add the revoke button** in `roster.html`'s `roster-table` rows:

```html
<button hx-delete="/ui/enrollments/{{.ID}}" hx-target="#roster" hx-swap="outerHTML"
        hx-confirm="Revoke {{.ID}}? This invalidates its token.">Revoke</button>
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/harbor/adminui/ -v 2>&1 | tail -15`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/harbor/adminui/
git commit -m "$(cat <<'EOF'
feat(harbor/adminui): revoke actor from the roster (confirmed)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv
EOF
)"
```

---

### Task 4: create / delete role

**Files:**
- Modify: `internal/harbor/adminui/writes.go` (role create + delete handlers)
- Modify: `internal/harbor/adminui/templates/roles.html` (add-role form; `roles-table` fragment; delete button)
- Test: `internal/harbor/adminui/writes_test.go`

**Interfaces:**
- Consumes: `store.PutRole(project, role)`, `store.RemoveRole(project, name)`, `store.GetKit(name)`, `roleRows(store)`.
- Produces:
  - `POST /ui/roles` — form `project`, `name`, `destinations`, `repos`, `ttl-seconds`, `kit`; validates a named `kit` exists (mirroring the API); returns the `roles-table` fragment on success.
  - `DELETE /ui/roles/{project}/{name}` — removes the role; returns the `roles-table` fragment.

- [ ] **Step 1: Write the failing test**

```go
func TestCreateAndDeleteRole(t *testing.T) {
	store := newStore(t)
	h := adminui.Handler(store, testLogger())

	rec := post(t, h, "/ui/roles", url.Values{"project": {"acme"}, "name": {"review"}, "destinations": {"git"}, "ttl-seconds": {"3600"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("create role = %d, want 200", rec.Code)
	}
	if _, ok := store.GetRole("acme", "review"); !ok {
		t.Fatal("role acme/review not created")
	}

	// kit that doesn't exist → 400 inline error
	bad := post(t, h, "/ui/roles", url.Values{"project": {"acme"}, "name": {"x"}, "kit": {"ghost"}})
	if bad.Code != http.StatusBadRequest || !strings.Contains(bad.Body.String(), "kit") {
		t.Fatalf("bad kit = %d %q, want 400 mentioning kit", bad.Code, bad.Body.String())
	}

	del := httptest.NewRequest(http.MethodDelete, "/ui/roles/acme/review", nil)
	del.Header.Set("Origin", "http://"+del.Host)
	drec := httptest.NewRecorder()
	h.ServeHTTP(drec, del)
	if drec.Code != http.StatusOK {
		t.Fatalf("delete role = %d, want 200", drec.Code)
	}
	if _, ok := store.GetRole("acme", "review"); ok {
		t.Error("role should be gone after delete")
	}
}
```

- [ ] **Step 2: Run test to verify it fails** — Run: `go test ./internal/harbor/adminui/ -run TestCreateAndDeleteRole -v` — Expected: FAIL (routes absent).

- [ ] **Step 3: Add the role handlers** to `registerWrites` (add `"strconv"` to `writes.go` imports):

```go
	mux.HandleFunc("POST /ui/roles", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		if err := r.ParseForm(); err != nil {
			renderError(w, http.StatusBadRequest, "invalid form")
			return
		}
		name := strings.TrimSpace(r.FormValue("name"))
		if name == "" {
			renderError(w, http.StatusBadRequest, "name is required")
			return
		}
		kit := strings.TrimSpace(r.FormValue("kit"))
		if kit != "" {
			if _, ok := store.GetKit(kit); !ok {
				renderError(w, http.StatusBadRequest, "kit does not exist")
				return
			}
		}
		ttl := 0
		if v := strings.TrimSpace(r.FormValue("ttl-seconds")); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				renderError(w, http.StatusBadRequest, "ttl-seconds must be an integer")
				return
			}
			ttl = n
		}
		project := strings.TrimSpace(r.FormValue("project"))
		role := harbor.Role{
			Name: name,
			Scope: harbor.Scope{
				Destinations: splitCSV(r.FormValue("destinations")),
				Repos:        splitCSV(r.FormValue("repos")),
				TTL:          time.Duration(ttl) * time.Second,
			},
			Kit: kit,
		}
		if err := store.PutRole(project, role); err != nil {
			renderError(w, http.StatusBadRequest, err.Error())
			return
		}
		log.Info("ui role put", "operator", harbor.OperatorID(r), "project", project, "role", name)
		renderFragment(w, "roles", "roles-table", map[string]any{"Roles": roleRows(store)})
	})

	mux.HandleFunc("DELETE /ui/roles/{project}/{name}", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		project, name := r.PathValue("project"), r.PathValue("name")
		if err := store.RemoveRole(project, name); err != nil {
			renderError(w, http.StatusNotFound, err.Error())
			return
		}
		log.Info("ui role removed", "operator", harbor.OperatorID(r), "project", project, "role", name)
		renderFragment(w, "roles", "roles-table", map[string]any{"Roles": roleRows(store)})
	})
```

- [ ] **Step 4: Add the roles template pieces** in `roles.html` — wrap the existing table in `{{define "roles-table"}}`, add an add-role form (`hx-post="/ui/roles"` targeting the table) and a per-row delete button (`hx-delete="/ui/roles/{{.Project}}/{{.Name}}"`, `hx-confirm`), mirroring roster.html's structure.

- [ ] **Step 5: Run tests to verify they pass** — Run: `go test ./internal/harbor/adminui/ -v 2>&1 | tail -15` — Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/harbor/adminui/
git commit -m "$(cat <<'EOF'
feat(harbor/adminui): create and delete roles from the UI

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv
EOF
)"
```

---

### Task 5: add / remove grant

**Files:**
- Modify: `internal/harbor/adminui/writes.go` (grant add + remove handlers)
- Modify: `internal/harbor/adminui/templates/roster.html` (per-actor add-grant form; remove-grant button on each grant row)
- Test: `internal/harbor/adminui/writes_test.go`

**Interfaces:**
- Consumes: `store.AddGrant(id, harbor.Grant{...})`, `store.RemoveGrant(id, project, role)`, `harbor.RosterSummaries(store)`.
- Produces:
  - `POST /ui/actors/{id}/grants` — form `project`, `role`, optional `destinations`/`repos` overrides; returns `roster-table`.
  - `DELETE /ui/actors/{id}/grants/{project}/{role}` — removes the grant; returns `roster-table`.

- [ ] **Step 1: Write the failing test**

```go
func TestAddAndRemoveGrant(t *testing.T) {
	store := newStore(t)
	if err := store.PutRole("acme", harbor.Role{Name: "worker", Scope: harbor.Scope{Destinations: []string{"git"}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddActor(harbor.Actor{ID: "spider-3", TokenHash: "h"}); err != nil {
		t.Fatal(err)
	}
	h := adminui.Handler(store, testLogger())

	rec := post(t, h, "/ui/actors/spider-3/grants", url.Values{"project": {"acme"}, "role": {"worker"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("add grant = %d, want 200", rec.Code)
	}
	a, _ := findActor(store, "spider-3")
	if len(a.Grants) != 1 || a.Grants[0].Role != "worker" {
		t.Fatalf("grants = %+v, want one worker grant", a.Grants)
	}

	del := httptest.NewRequest(http.MethodDelete, "/ui/actors/spider-3/grants/acme/worker", nil)
	del.Header.Set("Origin", "http://"+del.Host)
	drec := httptest.NewRecorder()
	h.ServeHTTP(drec, del)
	if drec.Code != http.StatusOK {
		t.Fatalf("remove grant = %d, want 200", drec.Code)
	}
	a2, _ := findActor(store, "spider-3")
	if len(a2.Grants) != 0 {
		t.Errorf("grants should be empty after remove, got %+v", a2.Grants)
	}
}
```

- [ ] **Step 2: Run test to verify it fails** — Run: `go test ./internal/harbor/adminui/ -run TestAddAndRemoveGrant -v` — Expected: FAIL (routes absent).

- [ ] **Step 3: Add the grant handlers** to `registerWrites`:

```go
	mux.HandleFunc("POST /ui/actors/{id}/grants", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		if err := r.ParseForm(); err != nil {
			renderError(w, http.StatusBadRequest, "invalid form")
			return
		}
		role := strings.TrimSpace(r.FormValue("role"))
		if role == "" {
			renderError(w, http.StatusBadRequest, "role is required")
			return
		}
		project := strings.TrimSpace(r.FormValue("project"))
		g := harbor.Grant{Project: project, Role: role, Overrides: overridesFrom(r.FormValue("destinations"), r.FormValue("repos"))}
		if err := store.AddGrant(r.PathValue("id"), g); err != nil {
			renderError(w, http.StatusNotFound, err.Error())
			return
		}
		log.Info("ui grant added", "operator", harbor.OperatorID(r), "id", r.PathValue("id"), "project", project, "role", role)
		renderFragment(w, "roster", "roster-table", map[string]any{"Actors": harbor.RosterSummaries(store)})
	})

	mux.HandleFunc("DELETE /ui/actors/{id}/grants/{project}/{role}", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		if err := store.RemoveGrant(r.PathValue("id"), r.PathValue("project"), r.PathValue("role")); err != nil {
			renderError(w, http.StatusNotFound, err.Error())
			return
		}
		log.Info("ui grant removed", "operator", harbor.OperatorID(r), "id", r.PathValue("id"), "project", r.PathValue("project"), "role", r.PathValue("role"))
		renderFragment(w, "roster", "roster-table", map[string]any{"Actors": harbor.RosterSummaries(store)})
	})
```

- [ ] **Step 4: Add the grant UI** in `roster.html`'s `roster-table`: under each actor, an add-grant form (`hx-post="/ui/actors/{{.ID}}/grants"`) and, on each grant row, a remove button (`hx-delete="/ui/actors/{{$id}}/grants/{{.Project}}/{{.Role}}"`, `hx-confirm`). Use the `{{$id := .ID}}` capture already present in the roster grant loop.

- [ ] **Step 5: Run tests to verify they pass** — Run: `go test ./internal/harbor/adminui/ -v 2>&1 | tail -20` — Expected: PASS (all writes + all prior).

- [ ] **Step 6: Commit**

```bash
git add internal/harbor/adminui/
git commit -m "$(cat <<'EOF'
feat(harbor/adminui): add and remove grants from the UI

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv
EOF
)"
```

---

### Task 6: Documentation

**Files:**
- Modify: `docs/usage/harbor/ui.md` (add an "Editing" section)
- Test: the docs-audit checker

- [ ] **Step 1: Add the "Editing" section** to `docs/usage/harbor/ui.md`:

```markdown
## Editing (day-job mutations)

Beyond viewing, the UI can do the roster day-job — the same actions as the CLI
verbs in [roster.md](roster.md):

- **Enroll** an actor (id, project, role, optional destination/repo overrides).
  The identity token is shown **once**, right after enrolling — copy it then; it
  is never shown again, stored in a list, or logged. For the full connection
  snippet (env vars / git config), use the CLI `at-harbor enroll`.
- **Revoke** an actor, **create/delete** a role, and **add/remove** a grant.

Every change obeys the same gate as the views (loopback, or an off-loopback
session with `require-scope`) and is recorded in harbor's audit log against the
operator who made it. Destructive actions ask for confirmation. State-changing
requests are refused unless they originate from the harbor UI itself (an
Origin/Referer check), so another site can't drive them through your browser.

Not editable from the UI (use the CLI): raising/tearing down coves, the kit
registry, and destinations.
```

- [ ] **Step 2: Run the docs audit** — Run: `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs --index OVERVIEW.md` — Expected: no NEW findings touching `ui.md` (pre-existing `superpowers/` backlog is out of scope).

- [ ] **Step 3: Commit**

```bash
git add docs/usage/harbor/ui.md
git commit -m "$(cat <<'EOF'
docs(harbor): document day-job editing in the admin UI

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv
EOF
)"
```

---

### Task 7: Full verification pass

**Files:** none.

- [ ] **Step 1: Full tests** — Run: `GOPROXY=https://proxy.golang.org GOSUMDB=off go test ./...` — Expected: all PASS.
- [ ] **Step 2: Build** — Run: `go build ./...` — Expected: clean.
- [ ] **Step 3: Vet + lint** — Run: `go vet ./internal/harbor/... ./cmd/at-harbor/...` then `just lint` — Expected: clean (gofmt/vet).
- [ ] **Step 4: No-secret-leak grep** — Run: `grep -rnE 'log\.(Info|Warn|Error|Debug)' internal/harbor/adminui/` and confirm no handler logs a token or token value (only ids/projects/roles/operator). The enroll handler must log the actor id, never the token. Expected: no token material logged.
- [ ] **Step 5: CSRF spot-check** — confirm every `POST`/`DELETE` handler in `writes.go` calls `guardWrite` as its first statement (grep `mux.HandleFunc\("(POST|DELETE)` in `writes.go` and eyeball each body). Expected: all six writes guarded.

---

## Self-Review

**1. Spec coverage:**
- Six mutations (enroll/revoke, role create/delete, grant add/remove) → Tasks 2–5. ✓
- Write-path under `/ui/` inside the gate, reusing mutation logic → all write handlers call `harbor.Enroll`/`store.*`. ✓
- CSRF Origin check (fail-closed) on every write → `guardWrite`/`sameOrigin`, Task 2, tested; Task 7 spot-check. ✓
- One-time token panel, never listed/logged → Task 2 enroll handler + `enroll-result` template; `TestEnrollCreatesActorAndShowsTokenOnce`; Task 7 grep. ✓
- Destructive confirmations → `hx-confirm` on revoke/delete/remove (Tasks 3–5). ✓
- Operator attribution / audit logs → Task 1 (gate attributes) + every write's `log.Info(... "operator", harbor.OperatorID(r) ...)`. ✓
- Authz unchanged (gate) → nothing new; gate reused. ✓
- htmx fragment swaps → `roster-table`/`roles-table` fragments (Tasks 2–5). ✓
- Docs → Task 6. ✓ Hermetic tests → all. ✓

**2. Placeholder scan:** Template steps (roster.html/roles.html edits) are described as sketches rather than full re-paste because they extend existing files whose current markup the implementer already has in the tree; the new `define` names, hx-attributes, and form fields are all given explicitly. Handler and helper Go code is complete. No TBD/TODO.

**3. Type consistency:**
- `Handler(store harbor.Store, log *slog.Logger)` — Task 2 defines; main + tests updated same task; used unchanged after. ✓
- `harbor.WithOperator`/`harbor.OperatorID` — Task 1 exports; gate + writes consume. ✓
- `registerWrites(mux *http.ServeMux, store harbor.Store, log *slog.Logger)` — Task 2 defines; Tasks 3–5 add handlers into the same function. ✓
- `sameOrigin`/`guardWrite`/`splitCSV`/`overridesFrom`/`renderError` — Task 2 defines; Tasks 3–5 reuse. ✓
- Fragment templates `roster-table` / `roles-table` / `enroll-result` — defined in Tasks 2/4; re-rendered by handlers via `renderFragment(w, page, tmpl, data)` (existing signature). ✓
