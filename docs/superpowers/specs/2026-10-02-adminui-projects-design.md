# Admin UI projects — design

Date: 2026-10-02 · Scope: `internal/jam/adminui`. Follows #295 (Project first-class).

## Decisions (review)

- Two slices: **A** view + lifecycle (this), **B** editing roster / escalation / chat service.
- Projects tab sits right after Dashboard.

## Slice A

- `/ui/projects`: name (link), roles, actors with grants, studios, roster size, chat service;
  create (name; 409 duplicate, 400 blank → `#flash`; success opens the page); delete disabled
  with the blocking reference (first role, else first grant — the store's rule), else
  confirmed.
- `/ui/projects/{name}`: roles, actors (with their roles here), roster humans (handle, login,
  delivery, OIDC identity) and channels, escalation (default + per-category chains), chat
  service, studios (`coves-tbl`). 404 page when absent.
- Every free-text project field (raise studio, enroll, add grant, new role) becomes a picker
  of existing projects plus `default` (preselected) — unknown projects now 404 on write.
- Project names link to the project page (roles table, studios table, role breadcrumb/chip).
- Dashboard gains a Projects tile.

## Slice B

- `jam.PutRosterHuman` carries the human rules from the JSON handler (login and Discord user
  id unique per project, delivery/identity valid) under a lock; the API and UI share it. The
  CLI's spec parsers move to jam with formatters (delivery, OIDC, escalation tier), so the
  UI's pre-filled forms use the CLI syntax.
- Project page edits (each swaps the re-rendered `project-body`): humans add/edit/remove,
  channels add/remove, escalation chains edit/add/clear (empty chains hidden), chat service
  select. Escalation targets not on the roster are flagged, not blocked.
