# Admin UI polish (option B) — design

Date: 2026-10-02 · Scope: `internal/jam/adminui` (this repo's Jam operator UI, `/ui/`)

## Problem

The operator UI is functionally complete but visually bare (≈20 lines of CSS, unlabeled
placeholder-only input rows above every table, no dark mode), and — the real defect —
**write errors are invisible**: the page uses htmx 2.0.4, which by default does not swap
4xx/5xx responses, so every `renderError` from `writes.go` is dropped. Only Role Request
works around it with a one-off `hx-on`.

## Goals

1. Errors and results from every htmx write are visible.
2. One coherent look shared in spirit with `/me/` (meui tokens, IBM Plex, light + dark).
3. Forms usable without guessing: labels, hints, collapsed "add" panels.
4. A dashboard that summarizes instead of duplicating the Studios page.
5. Leave seams for a later detail-page redesign (option C) without building it.

Non-goals: per-entity detail pages, cross-project navigation, search, a shared CSS
package between meui and adminui, new write handlers.

## Design

- **Chrome.** `layout.html` gets a topbar (brand + "Admin"), nav with an active marker
  (`aria-current="page"`, driven by a `Nav` key each page handler passes), a centered
  content column, and a `#flash` region. Styles are the meui token set (copied, not
  shared — sharing is a C follow-up) plus admin components: card-wrapped tables
  (horizontal scroll on narrow screens), buttons (primary/secondary/danger), chips,
  phase pills (`live` green; `raising`/`terminating` amber; `idled` grey; `lost`/`gone` red).
- **Errors.** A small inline script sets `htmx.config.responseHandling` so 4xx/5xx
  bodies are not swapped into the target, and a `htmx:responseError` listener writes
  the response body into `#flash` as a dismissible error banner. Success clears the
  flash. Role Request drops its bespoke `hx-on` and targets `#flash` for its success
  message. Handlers are unchanged (they already return escaped `<p class="error">`).
- **Forms.** Each create form moves into a `<details class="panel">` ("+ Raise studio",
  "+ Enroll actor", "+ Add role", "+ Push kit", "+ Add destination") with labeled
  fields in a responsive grid; forms reset after a successful request.
- **Dashboard.** Stat tiles — live, raising, needs-attention (`lost`/`terminating`),
  actors, roles, kits, destinations — each linking to its page; then the read-only
  studios table. Counts come from a pure `dashboardStats(store)`.
- **Roster.** One block per actor (id, expiry, Revoke), grants as chips with a remove
  ×, and a single per-actor "+ Grant" `<details>` replacing the always-visible
  add-grant row. The enroll token panel gets a Copy button.
- **Roles.** Destinations as chips, TTL shown as a duration (or "—"), Request as the
  primary action with its result in `#flash`.
- **Kits.** Pin uses a `<select>` of existing versions (map range is key-sorted);
  current version shown as a chip.
- **Intercom.** Filter bar restyled with the shared form grid; rendering unchanged.
- **C seams.** Rows carry `data-id`; entity names render through one `entity` template
  so detail links can be added in one place later.

## Testing

Existing tests stay green (they assert content and status codes). New tests:
`dashboardStats` counts; nav active marker; layout ships `#flash` + the error hook;
roster renders one "+ Grant" form per actor and a copy button on enroll; kits page
offers a version select. Manual check via `just dev-serve`.

## Docs

`docs/usage/jam/ui.md` — dashboard description, where errors/results appear, roster
grant flow.
