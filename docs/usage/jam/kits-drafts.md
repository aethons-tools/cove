---
summary: A kit's draft — one mutable tree per kit, opened from a version, with its problems listed; the ETag/If-Match rule and the open, status, discard, file read/write/remove and publish API.
read_when: You want to open, inspect, edit, discard or publish a kit's draft over the admin API (`/admin/kits/{name}/draft`), or a draft call answered 409, 412, 413, 415 or 428.
owns: kit drafts — opening one, the draft's status and problems, the ETag/If-Match rule, publishing a draft (origin draft, pin, drift), the /admin/kits/{name}/draft routes (files and publish included) and their audit lines
prereqs: kits.md, kits-directory.md
tier: leaf
updated: 2026-10-25
---

# Kit drafts

## The draft

A kit has at most one **draft**: a mutable copy of one version's files
(its tree, see [kits-directory.md](kits-directory.md#stored-as-trees)),
opened from *current* or a version you choose (its **base**). You change
files in the draft, then publish it as the kit's next version, or discard it.

- Every write is checked like a push: the paths, modes and sizes in
  [limits](kits-directory.md#limits). The draft's own
  [`.dockerignore`](kits-directory.md#dockerignore) applies too: a file it
  excludes is refused (`draft <kit>: <path>: excluded by .dockerignore`), and
  saving a `.dockerignore` drops the files it now excludes.
- `kit.yml` may be missing or invalid while you edit. The draft's status lists
  the **problems**: `kit <name>: kit.yml missing at the root`, a parse or
  validation error, a missing parent, a `base.context…` field. A draft with
  problems cannot be published.
- A draft is working state: it is not in `at-jam export`, `config write` or a
  config plan, and it does not block `kit rm`. Removing the kit removes its
  draft.
- Another version may land while a draft is open: the status then shows
  `base: 1, current: 2`. Publishing still makes the draft's tree the next
  version, without the other version's changes (no merge; it stays in the
  history).

## Concurrency

The draft's **ETag** is its tree hash, quoted (`"3f2a…"`). Every write must
send it back as `If-Match`, quoted or bare: without it the answer is 428, and
with a stale one 412 `draft <kit> changed since you loaded it (reload)`.
Nothing is merged: reload the status and make the edit again. `If-Match: *`,
weak tags and unbalanced quotes never match.

## Publish

Publish checks the draft as a push would, records its tree as the kit's next
version (origin `draft`: `"origin": "draft"` in `GET /admin/kits/{name}/history`)
and closes the draft.

- No change from *current*: 409 `draft <kit> has no changes`; the draft is kept.
- Problems: 400 `draft <kit> has problems: …`.
- Kits that follow this one go stale, as after any push. With
  `{"pin": true}`, each follower also gets a new version whose `base.kit`
  names the version it follows now (`app@v3`, rewritten in place), so it stays
  on it. The answer lists them in `pinned`. The pins and the published version
  are written together: if any follower cannot be pinned (409
  `kit <follower>: cannot pin its base.kit to …`), nothing is written.
- A config-managed kit publishes (as does a managed follower pinned), and the
  answer carries `drift: true` and
  `warning`: `this will drift from the config repo; config write it to keep the repo in sync`.

## API

Admin only, the same operator gate as the rest of `/admin`.

| Route | Does |
|---|---|
| `POST /admin/kits/{name}/draft` | Open, from `{"from": N}` (default current); `application/json`, else 415. 201 with the status; 409 `kit <name> already has a draft`; 400 for a version not stored as a tree. |
| `GET /admin/kits/{name}/draft` | The status: `base`, `current`, `tree`, `etag` (also the `ETag` header), `entries` (`path`, `mode`, `size`, `sha256`), `changes` from the base, `problems`, `managed`. |
| `DELETE /admin/kits/{name}/draft` | Discard (`If-Match`). 204. |
| `GET /admin/kits/{name}/draft/files/{path}` | One file's bytes (`application/octet-stream`, as an attachment, `no-store`), with `ETag` (the draft's) and `X-Kit-File-Mode`. |
| `PUT /admin/kits/{name}/draft/files/{path}[?mode=755]` | Write one file (`If-Match`; `application/octet-stream`, else 415; over the per-file limit 413). Without `mode` the file keeps its mode, a new one gets `0644`. |
| `DELETE /admin/kits/{name}/draft/files/{path}` | Remove one file (`If-Match`); 404 `draft <kit> has no file <path>`. |
| `PUT /admin/kits/{name}/draft` | Replace the whole tree with an uncompressed tar (`If-Match`; `application/x-tar`, else 415), read and filtered by its `.dockerignore` as a push upload is: see [kits-directory.md](kits-directory.md). 400 for a refused tar, 413 past the upload cap; `kit.yml` may be absent (a problem, not a refusal). |
| `POST /admin/kits/{name}/draft/publish` | Publish (`If-Match`; `{"pin": bool}`, `application/json`). Answers `version`, `base`, `pinned`, `drift`, `warning`. |

A write answers the new `etag` (also the `ETag` header), the files it
`changed` and the `problems`. A path is percent-encoded per segment; one
with an empty, `.` or `..` segment (never redirected) or a control or
format character is refused without being echoed.

`GET /admin/kits` marks a kit with an open draft `"draft": true`.

Each write that changes the draft logs `kit draft opened|saved|discarded`
with the kit, the operator and the tree's first 12 hex digits (a save also
the count of files `changed`), never content or a path. A publish logs
`kit draft published` with the kit, the operator, the new `version`, the
`base`, the count `pinned` and `drift`.
