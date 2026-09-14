# harbor: config-plane editing for the admin UI (kits + destinations)

**Status:** design approved, pre-plan
**Foundation:** the admin UI write foundation (`internal/harbor/adminui/writes.go` — `guardWrite`/`sameOrigin`/`renderError`/`orDefaultProject`, the gate + operator attribution), the read-only Kits/Destinations views (`adminui.go`, `templates/kits.html`, `templates/destinations.html`), and the admin API's kit/destination mutation logic (`internal/harbor/admin.go`, `Store.PushKit`/`PinKit`/`RemoveKit`/`RoleReferencingKit`, `Store.AddDestination`/`RemoveDestination`). **Design history:** `docs/superpowers/specs/2026-09-14-harbor-ui-editing.md` (the day-job write foundation this reuses).

## Problem / goal

The admin UI can edit the roster (enroll/revoke, roles, grants) and drive the runtime (raise/teardown), but the **config plane** — the kit registry and the broker's destinations — is still CLI-only (`at-harbor kit …`, `at-harbor destination …`). This slice adds those writes to the Kits and Destinations pages, reusing the established write foundation, finishing the config-plane editing surface.

Deferred, and explicitly **not** in this slice (each is a separate project, not a thin UI increment): **audit-log browsing** (there is no queryable audit store today — "audit log" is `slog` lines; browsing needs a new persistence layer) and **enrollment self-service** (a different auth model — a non-operator enrolling themselves). Both are filed as board tickets.

## Scope decisions (settled in brainstorming)

- **Kits:** push a new version (name + config), pin a version, delete a kit.
- **Destinations:** add (name, route, upstream, identity-in, cred-name, apply, repo-scoped), remove.
- **`credExists` threaded:** the destination add-handler validates that a non-empty `cred-name` resolves, exactly as `POST /admin/destinations` does — so `adminui.Handler` gains a `credExists func(string) bool` param.
- **Same security foundation:** every write is inside the UI gate, `guardWrite` (Origin check, fail-closed) first, operator-attributed audit logs.
- **No secret surface:** a kit config *references* secrets by name (no values); `cred-name` is a reference key, never a secret value. Logs carry kit name/version and destination name/route only. The destinations table stays as it is (name/route/upstream/repo-scoped — no cred-name), consistent with the read-only slice.

## Architecture

### Threading `credExists`

`adminui.Handler` becomes:

```
func Handler(store harbor.Store, log *slog.Logger, sup *harbor.Supervisor, credExists func(string) bool) http.Handler
```

`cmd/at-harbor/main.go` already builds `credExists := func(n string) bool { _, ok := specs[n]; return ok }` for `NewAdminHandler` — it passes the same value here. `registerWrites` gains the `credExists` argument. Tests pass a stub (e.g. `func(string) bool { return true }`, or one that recognizes a specific name for the negative case).

### Routes (in `internal/harbor/adminui/writes.go`)

| Method + route | Action | Underlying call |
|---|---|---|
| `POST /ui/kits` | push a kit version | `store.PushKit(name, config)` |
| `POST /ui/kits/{name}/pin` | pin a version | `store.PinKit(name, version)` |
| `DELETE /ui/kits/{name}` | delete a kit | `store.RemoveKit(name)` (guarded by `RoleReferencingKit`) |
| `POST /ui/destinations` | add a destination | `credExists` check → `store.AddDestination(d)` |
| `DELETE /ui/destinations/{name}` | remove a destination | `store.RemoveDestination(name)` |

Each handler: `guardWrite(w, r)` first (CSRF) → `ParseForm` → validate → mutate → operator-attributed `log.Info` (no secret/config body) → return the re-rendered `kits-table` or `destinations-table` fragment. Errors render inline via `renderError`.

- **`POST /ui/kits`**: `name` + `config` required (400 inline if missing); `store.PushKit` returns the new version; log `"ui kit pushed"` (operator/name/version). Config is not logged.
- **`POST /ui/kits/{name}/pin`**: `version` parsed via `strconv.Atoi` (400 on non-integer); `store.PinKit` (404/400 inline on unknown version); log `"ui kit pinned"`.
- **`DELETE /ui/kits/{name}`**: mirror the API — if `store.RoleReferencingKit(name)` reports a referencing role, render a **409** inline ("kit is referenced by role …"); else `store.RemoveKit`; log `"ui kit removed"`.
- **`POST /ui/destinations`**: `name`, `route`, `upstream` required; `identity-in` + `apply` are one of `bearer|basic-password|x-api-key` (`harbor.ApplyMethod`); `repo-scoped` a checkbox; if `cred-name` non-empty and `!credExists(cred-name)` → 400 inline ("cred-name does not resolve to a configured credential"); `store.AddDestination`; log `"ui destination added"` (operator/name/route/upstream — never cred-name).
- **`DELETE /ui/destinations/{name}`**: `store.RemoveDestination`; log `"ui destination removed"`.

### UX (htmx, progressive)

- `templates/kits.html` and `templates/destinations.html` each wrap their existing table in a named fragment (`kits-table` / `destinations-table`), add an **add form**, and per-row action buttons.
  - Kits: an add form (name + config `<textarea>`), a per-row **pin** control (version input) and a **delete** button (`hx-confirm`). The table already shows name / current version / version count.
  - Destinations: an add form (name, route, upstream, `identity-in` `<select>`, `cred-name`, `apply` `<select>`, `repo-scoped` checkbox) and a per-row **delete** button (`hx-confirm`).
- Writes swap the re-rendered fragment; validation/`409`/`400` errors render inline. `renderError` escapes its message (`html/template`); all interpolated values render through `html/template` auto-escaping.

### Security

- **No secret surface:** kit config (a recipe referencing secrets by name) and `cred-name` (a reference) are not secret values; nothing secret is logged or newly rendered. The destinations table is unchanged (no cred-name column).
- **Gate + CSRF + attribution:** reused unchanged; every write is gated, Origin-checked (fail-closed) first, and audit-logged against the operator.
- **`/admin/*` and the CLI:** unchanged; browser config-writes go only through `/ui/`.

## Testing (hermetic)

`httptest` + `harbor.NewFileStore` + a stub `credExists`, driving the UI handler:

- **kit push:** `POST /ui/kits` (name+config) creates/advances the kit (`store.GetKit` / `ListKits`); returns the `kits-table` fragment; config value not in any log.
- **kit pin:** `POST /ui/kits/{name}/pin` with a valid version repoints current; a non-integer version → 400 inline.
- **kit delete:** `DELETE /ui/kits/{name}` removes it; a kit referenced by a role → **409** inline, not removed.
- **destination add:** `POST /ui/destinations` with a resolvable `cred-name` (stub returns true) adds it (`store.ListDestinations`); an unresolvable `cred-name` (stub returns false) → **400** inline, not added.
- **destination remove:** `DELETE /ui/destinations/{name}` removes it.
- **CSRF:** a config write with a mismatched/absent Origin → 403.
- **gate composition:** an off-loopback config write with no session → refused, no mutation (reuses the existing gate-test pattern).
- **no-leak:** no secret/credential value in any write response or log line.

All hermetic — `Store` fakes only, no network/VM, no `integration` tag.

## Docs

Extend `docs/usage/harbor/ui.md`'s Editing section: kit push/pin/delete and destination add/remove; a kit referenced by a role cannot be deleted (409); these obey the same gate/CSRF/audit as the roster and runtime edits. Cross-link `kits.md` and `serve.md#destinations` for the CLI equivalents and the field semantics rather than restating them.

## Non-goals

- **Audit-log browsing** (needs a queryable store — separate project; board ticket filed).
- **Enrollment self-service** (different auth model — separate project; board ticket filed).
- Editing a kit config in place beyond push-a-new-version; showing/diffing historical kit configs in the UI (the CLI covers `kit show`/`versions`).
- Adding a cred-name column to the destinations table.
- Editing an existing destination in place beyond add/remove.
