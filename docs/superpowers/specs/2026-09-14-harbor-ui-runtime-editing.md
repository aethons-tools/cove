# harbor: runtime editing for the admin UI (raise / teardown coves)

**Status:** design approved, pre-plan
**Foundation:** the admin UI (`internal/harbor/adminui`, incl. its `writes.go` write foundation + `coves-table` live fragment), the UI gate (`internal/harbor/browserauth`), and the runtime supervisor (`internal/harbor/supervisor.go` — `Supervisor.Raise`/`Teardown`, `NewSupervisor`, the `Launcher` seam). **Design history:** `docs/superpowers/specs/2026-09-13-harbor-admin-ui.md` (read-only coves view), `docs/superpowers/specs/2026-09-14-harbor-ui-editing.md` (the day-job write foundation this reuses).

## Problem / goal

The admin UI can view the live cove fleet and do the roster day-job, but raising and tearing down managed coves still requires the CLI (`at-harbor cove raise|teardown`). This slice adds those two runtime actions to the Coves page, reusing the day-job write foundation (gate, CSRF, operator attribution), so an operator can start and stop managed coves from the browser.

## Scope decisions (settled in brainstorming)

- **Actions:** raise a managed cove, teardown a cove. `POST /coves/{id}/status` is **excluded** — it is cove-reported activity, not an operator action.
- **Raise secrets:** `Supervisor.Raise` returns a one-time identity token + launch secret; with a real launcher, harbor consumes them internally. The UI raise **discards** them — nothing secret is rendered in the browser or logged. Manual-wire raising (needing the raw secrets) stays CLI-only.
- **Supervisor-gated availability:** the raise/teardown routes and the Coves-page controls appear only when a runtime supervisor is configured (`sup != nil`); otherwise the routes 503 and the page stays exactly the read-only view it is today.
- **Same security foundation:** writes reuse the existing UI gate (loopback, or off-loopback session with `require-scope`), the Origin-check CSRF guard (`guardWrite`/`sameOrigin`, first statement), and operator-attributed audit logs (`harbor.OperatorID`).

## Architecture

### Threading the Supervisor

`adminui.Handler` gains a `sup *harbor.Supervisor` param:

```
func Handler(store harbor.Store, log *slog.Logger, sup *harbor.Supervisor) http.Handler
```

`cmd/at-harbor/main.go` already has `sup` (it passes it to `NewAdminHandler`) — it passes the same value here. `registerWrites` gains the `sup` argument and registers the cove-write routes. When `sup == nil`, those routes are still registered but return **503** ("runtime supervisor not configured", matching the JSON API), and the Coves template omits the raise form + teardown buttons.

### Routes (in `internal/harbor/adminui/writes.go`)

| Method + route | Action | Underlying call |
|---|---|---|
| `POST /ui/coves` | raise a managed cove | `sup.Raise(ctx, harbor.RaiseSpec{ActorID, Project, Role, Unit, Prompt})` |
| `DELETE /ui/coves/{id}` | teardown a cove | `sup.Teardown(ctx, id)` |

Each handler: (1) `guardWrite(w, r)` first (CSRF); (2) if `sup == nil` → 503; (3) parse/validate; (4) call the supervisor; (5) log an operator-attributed audit line **without** any token/secret; (6) return the re-rendered `coves-table` fragment (the same fragment the live poll uses).

- **`POST /ui/coves`**: form fields `id` + `role` required (400 with inline error if missing); `project`, `unit`, `prompt` optional. Calls `sup.Raise`, **discards** the returned token + launch secret, logs `"ui cove raised"` (operator/id/project/role). On a raise error (bad role, launcher failure) → `renderError` 400.
- **`DELETE /ui/coves/{id}`**: calls `sup.Teardown(r.Context(), r.PathValue("id"))`, logs `"ui cove torn down"` (operator/id). On error → `renderError`. Teardown transitions the cove to `terminating` (async reconcile), so the returned fragment may still show it in that phase — accurate, not a bug.

### Coves page (`internal/harbor/adminui/templates/coves.html`)

- The `coves-table` fragment already exists (live, 3s-polled). Add a per-row **Teardown** button (`hx-delete="/ui/coves/{{.ID}}"`, target the table, `hx-swap="outerHTML"`, `hx-confirm`).
- Add a **raise form** above the table (`hx-post="/ui/coves"`): inputs `id`, `role` (required), `project`, `unit`, and a `prompt` **textarea**; target the table.
- Both controls render only when editing is enabled. The Coves GET handler and the write handlers pass a `CanEdit` flag (`sup != nil`) into the template data so the form/buttons are conditionally rendered; the read-only page (no runtime) is unchanged.

### Data flow

```
operator ─POST /ui/coves (form)─▶ UI gate (loopback/session) ─▶ guardWrite (CSRF)
  ├ sup == nil? → 503
  ├ validate id+role (else 400 inline)
  ├ sup.Raise(RaiseSpec{...}) → (inst, token, secret)   [token+secret discarded]
  ├ log "ui cove raised" (operator/id/project/role)
  └ renderFragment coves-table (CoveSummaries(store), CanEdit=true)
```

### Security

- **No new secret surface:** the raise token + launch secret are discarded — never rendered, never logged. The coves table shows only the scrubbed `CoveSummary` (already secret-free). The `prompt` rides in the POST body (not argv, not logged).
- **Gate + CSRF + attribution:** reused unchanged from the day-job slice; every cove write is gated, Origin-checked (fail-closed) first, and audit-logged against the operator.
- **`/admin/*` and the CLI:** unchanged; browser cove-writes go only through `/ui/`.

## Testing (hermetic)

The adminui test package defines a **minimal fake implementing the exported `harbor.Launcher`** (Raise/Teardown/Probe/Pause/Unpause — the `harbor` package's own `fakeLauncher` is test-internal), builds a real `harbor.NewSupervisor(store, fake, holder, ttl, reconcile, now, log)`, and drives the UI handler:

- **raise:** `POST /ui/coves` (matching Origin, form with id+role) → the instance appears in `store.ListInstances()` and the returned `coves-table` fragment; the response body and captured logs contain **no** token/launch-secret value.
- **teardown:** `DELETE /ui/coves/{id}` → `sup.Teardown` invoked (instance transitions toward terminating/gone per the fake); returns the fragment.
- **validation:** `POST /ui/coves` missing `role` → 400 inline error.
- **no-runtime:** `adminui.Handler(store, log, nil)` → `POST /ui/coves` returns 503, and the Coves page GET does not render the raise form / teardown buttons.
- **CSRF:** a cove write with a mismatched/absent Origin → 403.
- **gate composition:** an off-loopback cove write with no session → refused, no raise (reuses the existing gate test pattern).

All hermetic — fake `Launcher` + `FileStore`, no Docker/network/VM, no `integration` tag.

## Docs

Update `docs/usage/harbor/ui.md`'s Editing section: note that when a runtime supervisor is configured, the Coves page can raise a managed cove (id/role/project/unit/prompt) and tear one down (confirmed); raise secrets are handled by harbor and never shown (use the CLI for manual-wire raising); without a runtime the Coves page is view-only. Cross-link `coves.md` for the CLI equivalents and the lifecycle model.

## Non-goals

- Setting cove activity/status from the UI (cove-reported, not an operator action).
- Surfacing the raise identity token / launch secret in the browser (managed-cove default; CLI covers manual wiring).
- Pause/unpause/suspend controls, log/stream viewing, or per-cove detail pages.
- Kit-registry and destination writes (still CLI).
