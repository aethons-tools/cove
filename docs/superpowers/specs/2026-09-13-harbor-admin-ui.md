# harbor: admin UI — read-only observability (first slice)

**Status:** design approved, pre-plan
**Foundation:** the harbor admin API (`internal/harbor/admin.go`), its store/supervisor, and the operator-auth gate (`internal/harbor/operator.go`, `oidc.go`). **Design history:** `docs/superpowers/specs/2026-09-10-harbor-design.md` (this is the first cut of sub-project #5, "UI + observability").

## Summary

Give harbor a human-facing, **read-only observability surface** — a server-rendered web UI, served by `at-harbor` itself, that shows the live cove runtime and the control-plane roster/roles/kits/destinations at a glance. It sits on the admin API's existing store/supervisor and its existing auth gate; it adds **no mutation paths and no new auth surface**.

This is the walking skeleton for the harbor UI: prove the serving + auth + render + live-update loop against real state before any write path is exposed through the browser. Reaching the UI from a remote, TLS+OIDC harbor via a browser session (auth-code redirect + signed cookie) and any mutation (enroll, raise, teardown, edit) are explicit **non-goals** of this slice and are deferred to follow-ups.

After this slice, an operator running (or SSH-tunnelled to) a harbor opens `http://127.0.0.1:8081/ui/` in a browser and watches the fleet — coves changing phase/activity live, the roster and its effective scopes, registered roles/kits/destinations — without running a CLI verb.

## Scope decisions (settled in brainstorming)

- **First increment = read-only observability.** No enroll/raise/teardown/edit from the UI.
- **Form = server-rendered Go (`html/template`) + htmx.** No JS build step, no SPA, no npm. htmx is a single vendored, embedded JS file.
- **Auth/exposure = loopback-only, reuse the existing gate.** The UI is mounted behind the same `authMiddleware` that already wraps the JSON API. On loopback, `LoopbackAuthenticator` trusts the local operator. Off-loopback, the existing OIDC **bearer** gate applies unchanged — so a browser cannot reach an off-loopback UI yet, and that is acceptable for this slice. No session cookies, no redirect flow.
- **Live updates = htmx polling**, not SSE — the supervisor is left untouched and tests stay trivial.
- **Namespace = `/ui/*`**, with `/` redirecting to `/ui/`; static assets under `/ui/static/`. Keeps the UI cleanly separated from the JSON `/admin/*` API.
- **Poll interval = 3s** (a single constant, easy to tune later).

## Architecture

### Package: `internal/harbor/adminui`

A new package isolated from `admin.go`, with one constructor:

```go
func Handler(store Store, sup *Supervisor) http.Handler
```

It returns a mux of the UI routes. It reads state by calling `Store`/`Supervisor` methods **directly, in-process** — exactly as the JSON handlers do — never by self-calling the HTTP API through `adminclient`. Templates are embedded with `//go:embed templates/*.html`; htmx is embedded and served as a static asset.

### Mounting and auth

`NewAdminHandler` composes the existing JSON API mux (`/admin/*`) and the new UI mux (`/ui/*`, `/`, `/ui/static/*`) into one handler, wrapped by the **same** `authMiddleware` it already applies. Concretely, the current `NewAdminHandler` builds one `*http.ServeMux` and returns `authMiddleware(auth, log, mux)`; the UI routes are registered on that same mux (or a composed parent mux) so a single auth gate covers everything. The existing `GET /admin/login-config` auth exemption is preserved. The JSON API's behavior and wire contract are unchanged.

Because the gate is unchanged, the UI's exposure story is exactly the admin API's: loopback ⇒ open to the local operator; off-loopback ⇒ requires TLS + OIDC bearer (unreachable from a plain browser, by design, this slice).

### Shared summary builders

The roster view needs each actor's grants with their **effective** destinations/repos after role resolution — logic currently inlined in `admin.go`'s `GET /admin/roster` handler (the `EffectiveScope(g, role)` loop). Extract it into a small helper (e.g. `rosterSummaries(store Store) []ActorSummary`) that both the JSON handler and the UI render from, so the two surfaces never drift. This is the only refactor in scope; no unrelated restructuring.

### Views (all read-only, all GET)

| Route | Renders |
|-------|---------|
| `GET /ui/` | Dashboard: the live cove table + a roster summary |
| `GET /ui/coves` | Cove runtime table: id, project/role, unit, phase, activity, lease holder, raised-at, last-seen |
| `GET /ui/roster` | Actors with expiry and each grant's effective destinations/repos |
| `GET /ui/roles` | Roles per project: destinations, repos, TTL, bound kit |
| `GET /ui/kits` | Kits: name, current version, version count |
| `GET /ui/destinations` | Destinations: name, route, upstream (never a credential) |
| `GET /ui/static/htmx.min.js` | The vendored, embedded htmx runtime |

### Live updates

The cove table carries `hx-get="/ui/coves"` + `hx-trigger="every 3s"` + `hx-swap="outerHTML"` targeting the table element. The `/ui/coves` (and dashboard) handler branches on the `HX-Request` header: a plain request returns the full page; an htmx request returns just the table fragment. One template, two entry points — no duplicated markup. No supervisor changes: each poll is an ordinary in-process read of `store.ListInstances()`.

### Data flow

```
browser ──GET /ui/coves──▶ authMiddleware (loopback ok) ──▶ adminui.Handler
                                                              │
                                        store.ListInstances() │  (and ListActors/
                                        + rosterSummaries()   │   GetRole/ListRoles/
                                                              │   ListKits/ListDestinations)
                                                              ▼
                                             html/template render → HTML (page or fragment)
```

### What never reaches the UI

Tokens, token hashes, launch secrets, and resolved credential values are never rendered — the UI reuses the same summary types the JSON API already scrubs (`ActorSummary`, `CoveSummary` carry no secrets; `Destination` shows route/upstream, not the injected credential). No new field is surfaced that the JSON API doesn't already expose.

## Testing (hermetic)

Standard-library `httptest` against `adminui.Handler` (or the composed `NewAdminHandler`) with a populated in-memory `Store` and the `Supervisor`/`Fake` seam already used elsewhere. No Docker, network, or live VM.

- `GET /ui/coves` with seeded instances renders the known rows (assert a cove id and its phase appear in the body).
- The htmx path (`HX-Request: true`) returns **just** the table fragment, not the full page chrome.
- `GET /ui/roster` shows an actor's effective destinations/repos (exercises the shared `rosterSummaries` helper).
- A non-loopback `RemoteAddr` is still refused by the gate (the UI inherits `LoopbackAuthenticator`).
- No secret leaks: a response body never contains a token/hash/launch-secret for a seeded actor/cove.

Keep every test hermetic (driving the existing fakes); no `integration`-tagged test is needed for this slice.

## Vendoring htmx

`htmx.min.js` is committed once into `internal/harbor/adminui/` and embedded via `//go:embed`. Fetching it to vendor requires CDN egress (`cdnjs.cloudflare.com`), which has been added to the sandbox kit. The committed file is pinned to an exact htmx version recorded in the plan; there is no runtime fetch — the asset is served from the binary.

## Docs

Per the repo rule, the same change updates `docs/`. A new leaf `docs/usage/harbor/ui.md` (owns: the admin UI — what it shows, how to reach it, the loopback-only exposure story) is added and linked from `docs/usage/harbor/INDEX.md`. It cross-links serve.md (exposure/gate) rather than restating it.

## Non-goals (explicit)

- Browser session auth (OIDC auth-code + signed cookie) for remote/off-loopback access.
- Any mutation from the UI (enroll, revoke, grant, raise, report, teardown, edit roles/kits/destinations).
- SSE/websocket live streaming (polling only).
- Audit-log browsing and enrollment self-service (later cuts of sub-project #5).
