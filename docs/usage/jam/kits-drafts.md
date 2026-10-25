---
summary: A kit's draft — one mutable tree per kit, opened from a version, with its problems listed; the ETag/If-Match rule and the open, status and discard API.
read_when: You want to open, inspect or discard a kit's draft over the admin API (`/admin/kits/{name}/draft`), or a draft call answered 409, 412, 415 or 428.
owns: kit drafts — opening one, the draft's status and problems, the ETag/If-Match rule, the /admin/kits/{name}/draft routes and their audit lines
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

- `kit.yml` may be missing or invalid while you edit. The draft's status lists
  the **problems**: `kit <name>: kit.yml missing at the root`, a parse or
  validation error, a missing parent, a `base.context…` field. A draft with
  problems cannot be published.
- A draft is working state: it is not in `at-jam export`, `config write` or a
  config plan, and it does not block `kit rm`. Removing the kit removes its
  draft.

## Concurrency

The draft's **ETag** is its tree hash, quoted (`"3f2a…"`). Every write must
send it back as `If-Match`, quoted or bare: without it the answer is 428, and
with a stale one 412 `draft <kit> changed since you loaded it (reload)`.
Nothing is merged: reload the status and make the edit again. `If-Match: *`,
weak tags and unbalanced quotes never match.

## API

Admin only, the same operator gate as the rest of `/admin`.

| Route | Does |
|---|---|
| `POST /admin/kits/{name}/draft` | Open, from `{"from": N}` (default current); `application/json`, else 415. 201 with the status; 409 `kit <name> already has a draft`; 400 for a version not stored as a tree. |
| `GET /admin/kits/{name}/draft` | The status: `base`, `current`, `tree`, `etag` (also the `ETag` header), `entries` (`path`, `mode`, `size`, `sha256`), `changes` from the base, `problems`, `managed`. |
| `DELETE /admin/kits/{name}/draft` | Discard (`If-Match`). 204. |

`GET /admin/kits` marks a kit with an open draft `"draft": true`.

Each write logs `kit draft opened|discarded` with the kit, the operator and
the tree's first 12 hex digits, never content.
