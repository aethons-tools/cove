# harbor: day-job editing for the admin UI (enroll/revoke + roles/grants)

**Status:** design approved, pre-plan
**Foundation:** the read-only admin UI (`internal/harbor/adminui`, `docs/usage/harbor/ui.md`), its UI gate + browser login (`internal/harbor/browserauth`), and the admin API mutation logic (`internal/harbor/admin.go`, `harbor.Enroll`, `Store.PutRole`/`RemoveRole`/`AddGrant`/`RemoveGrant`). **Design history:** `docs/superpowers/specs/2026-09-13-harbor-admin-ui.md` (this lifts that spec's "mutation from the UI" non-goal), `docs/superpowers/specs/2026-09-13-harbor-ui-oidc-login.md`.

## Problem / goal

The admin UI is read-only. An operator still drops to the CLI (`at-harbor enroll`/`revoke`/`role`/`grant`/`ungrant`) for their day-to-day roster work. This slice adds **mutating** UI actions for that day-job — enroll/revoke actors and manage roles + grants — so the common workflow is doable in the browser, while keeping the security posture the read-only UI established.

Out of scope for this slice: runtime mutations (raise/teardown coves), kit registry writes, destination writes, audit-log browsing, enrollment self-service. (Runtime editing is the next slice.)

## Scope decisions (settled in brainstorming)

- **Mutations:** enroll actor, revoke actor, create role, delete role, add grant, remove grant.
- **Write-path:** new POST/DELETE routes under `/ui/`, handled **in-process against the `Store`** behind the **same UI gate** (loopback-or-session), reusing the existing mutation logic and audit-log lines. `/admin/*` stays the pure bearer API; browser writes never route through it.
- **CSRF:** an **Origin/Referer-vs-Host check** on every state-changing request (rejects cross-origin POST/DELETE and the loopback CSRF vector), with `SameSite=Lax` as defense-in-depth.
- **One-time token:** enroll returns the actor's identity token once; the UI shows it (with the actor id) in a dismissible panel with a "won't be shown again" warning, and never re-fetches, persists, or logs it. (The full connection snippet — env vars / git config — is out of scope here: it needs the broker's public base URL, which the UI process doesn't cleanly have. The token is the once-only secret; the CLI's `enroll` still prints the full snippet.)
- **Destructive actions** (revoke, delete role, remove grant) require a confirmation step.
- **Authz:** unchanged — the UI gate already enforces `require-scope` for off-loopback sessions and trusts loopback as the local operator. No new authz surface.

## Architecture

### Write routes (in `internal/harbor/adminui`)

New handlers registered on the UI mux, all under `/ui/` so they sit inside the UI gate:

| Method + route | Action | Underlying call |
|---|---|---|
| `POST /ui/enrollments` | enroll actor | `harbor.Enroll(store, id, project, role, overrides, now)` |
| `DELETE /ui/enrollments/{id}` | revoke actor | `store.RemoveActor(id)` |
| `POST /ui/roles` | create/replace role | `store.PutRole(project, role)` (validates `kit` exists, mirroring the API) |
| `DELETE /ui/roles/{project}/{name}` | delete role | `store.RemoveRole(project, name)` |
| `POST /ui/actors/{id}/grants` | add grant | `store.AddGrant(id, grant)` |
| `DELETE /ui/actors/{id}/grants/{project}/{role}` | remove grant | `store.RemoveGrant(id, project, role)` |

These reuse the exact functions the `/admin/*` JSON handlers call, so the two surfaces validate and mutate identically. Each handler:
1. enforces the CSRF Origin check (below) and rejects on failure (403),
2. parses the form (`application/x-www-form-urlencoded`), validating required fields with inline errors on failure (400 + a rendered error fragment),
3. performs the mutation, logging an audit line (`ui enrolled` / `ui role put` / …) with the operator id,
4. returns the updated table fragment (roster or roles) on success — except enroll, which returns the one-time-token panel.

The `adminui.Handler` signature gains what it needs to write: it already takes `store harbor.Store`; `harbor.Store` already exposes every mutation above, so no new dependency is required. `harbor.Enroll` is already exported.

### CSRF: Origin check

A small helper gates every state-changing method:

```
func sameOrigin(r *http.Request) bool
```

For `POST`/`DELETE`, compare the `Origin` header's host to `r.Host`; if `Origin` is absent, fall back to the `Referer` host; if neither is present, **reject** (fail-closed). A mismatch → `403`. Applied to all `/ui/` write routes (a middleware wrapping them, or a guard at the top of each handler). `GET` routes are unaffected. This protects both the session path (in addition to `SameSite=Lax`) and the loopback path (which has no cookie/SameSite shield).

### Operator attribution

The UI gate resolves the operator on every allowed request (the session `sub`, or `local` for loopback). It stashes that id in the request context (an exported `browserauth` context helper), and the write handlers read it for audit logging — mirroring `admin.go`'s `withOperator`/`operatorID` for the JSON API. Read handlers are unaffected.

### UX (htmx, progressive)

- Roster and Roles pages gain **inline forms** (enroll / add-role / add-grant) and per-row action buttons (revoke / delete / remove-grant).
- Forms use `hx-post`/`hx-delete`, targeting the page's table region; a successful write swaps in the re-rendered fragment (`RosterSummaries` / `roleRows`), so the table reflects the change without a full reload.
- **Destructive actions** carry `hx-confirm="…"` so the browser confirms before the request.
- **Enroll success** swaps in a one-time panel: the identity token, the actor id, and a prominent "copy now — this token is not shown again" warning. Dismissing it returns to the roster. The token is present only in that single response body; it is never rendered into any list, stored, or logged.
- **Validation errors** render inline next to the form (e.g. "id is required", "role does not exist", "kit does not exist"), reusing the API's error messages.

### What stays out of scope / unchanged

- `/admin/*` JSON API and the CLI verbs: unchanged.
- The read-only views and the browser-login flow: unchanged.
- No new secret is rendered beyond the one-time enroll token (which the CLI already prints once); token hashes/launch secrets/credential values are never shown.

## Testing (hermetic)

`httptest` + `harbor.NewFileStore` (via the existing `newStore` helper), driving the UI handler directly:

- **enroll:** `POST /ui/enrollments` with a valid form creates the actor (`store.Lookup`/`ListActors` confirms) and the response body contains the identity token **once**; a second view of the roster never shows it.
- **revoke:** `DELETE /ui/enrollments/{id}` removes the actor; the returned roster fragment no longer lists it.
- **role create/delete:** `POST /ui/roles` then `DELETE /ui/roles/{project}/{name}` add/remove the role (`store.GetRole`); a `POST` naming a non-existent kit → 400 with the inline error.
- **grant add/remove:** `POST`/`DELETE` on `/ui/actors/{id}/grants…` mutate the actor's grants; the roster fragment shows the effective scope.
- **CSRF:** a write with a mismatched `Origin` → 403; with no `Origin`/`Referer` → 403; with a matching `Origin` → success.
- **gate composition:** writes are reachable on loopback with no cookie, and off-loopback only with a valid session (unauthenticated off-loopback write → the gate's 302/refuse, never a mutation).
- **attribution:** an audit log line for each mutation carries the operator id.
- **no-leak:** no token hash / launch secret / credential value appears in any write response or log; the enroll token never appears in a log line.

All hermetic — `Store` fakes only, no network/VM, no `integration` tag.

## Docs

Update `docs/usage/harbor/ui.md`: add an "Editing" section — which day-job mutations the UI supports, the one-time-token behavior (token + id shown once; use the CLI `enroll` for the full connection snippet), that destructive actions confirm, and that writes obey the same gate/scope as reads. Cross-link `roster.md` (the CLI equivalents) rather than restating the RBAC model.

## Non-goals

- Raise/teardown coves and other runtime mutations (next slice).
- Kit-registry and destination writes from the UI.
- Editing an existing role/grant in place beyond create/replace + delete (PutRole already replaces; no separate "edit" affordance in this slice).
- Bulk actions, search/filter, pagination.
- Audit-log browsing and enrollment self-service.
