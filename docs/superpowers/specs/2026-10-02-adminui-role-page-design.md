# Admin UI role page — design

Date: 2026-10-02 · Scope: `internal/jam/adminui` (`/ui/`), plus `internal/jam` role writes in slice B.
First slice of the admin-UI "option C" redesign (see `2026-10-02-adminui-polish-design.md`).

## Decisions

- Credential **names** are shown (references, not secrets); credential values never are.
- Ship in two slices: A (view-only detail page), then B (edit in place).

## Slice A — view

`GET /ui/roles/{project}/{name}` (404 page when missing), linked from the Roles table,
roster grant chips and studio rows. Shows: destinations with the effective credential
(role mapping, else destination default) and route; addressing (empty = can't squawk);
egress (managed list, or kit default); allocation caps + idle ladder with unset values
labeled; standing sessions with their studio's phase; holders (override-marked); the
role's studios (non-polling `coves-tbl`). Header: Request session, Delete. Data comes
from a pure `buildRoleDetail(store, project, name)`.

## Slice B — edit

- Move role read-modify-write logic (put-keeping-other-fields, egress set/clear, standing
  add/remove) into `jam` functions behind one shared lock with typed errors mapped to
  HTTP status; refactor the JSON handlers onto them unchanged; the UI calls the same
  functions (closing today's race: the UI role put skips `roleMu`).
- Per-section pre-filled forms (scope, allocation, egress, standing), each posting to
  `/ui/roles/{p}/{n}/{section}` and swapping only its section; errors in `#flash`.
- Create on the Roles page redirects to the new role's page; the blank re-put form is
  no longer the edit path.
