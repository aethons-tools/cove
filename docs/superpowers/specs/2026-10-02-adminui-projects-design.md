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
