---
summary: A kit's draft — one mutable tree per kit, opened from a version, with its problems listed; the ETag/If-Match rule, the `kit draft open|status|get|put|rm|sync|publish|discard` verbs, the open, status, discard, file read/write/remove and publish API, and the kit page's Draft tab in the admin UI.
read_when: You want to open, inspect, edit, discard or publish a kit's draft with `at-jam kit draft`, over the admin API (`/admin/kits/{name}/draft`) or on the kit page's Draft tab in the admin UI, or a draft call answered 409, 412, 413, 415 or 428.
owns: kit drafts — opening one, the draft's status and problems, the ETag/If-Match rule, publishing a draft (origin draft, pin, drift), the `kit draft` CLI verbs, the /admin/kits/{name}/draft routes (files and publish included) and their audit lines, the admin UI Draft tab (/app/admin/kits/<name>/draft) and its config-repo panel
prereqs: kits.md, kits-directory.md; ui-editing.md for the admin UI's write rules
tier: leaf
updated: 2026-10-25
---

# Kit drafts

## The draft

A kit has at most one **draft**: a mutable copy of one version's files
(its tree, see [kits-directory.md](kits-directory.md#stored-as-trees)),
opened from *current* or a version you choose (its **base**). You change
files in the draft, then publish it as the kit's next version, or discard it.

- Every save is checked like a push: the paths, modes and sizes in
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
  `kit <follower>: cannot pin its base.kit to …`, or
  `kit <follower> follows <kit> but is not stored as a tree; pin it by hand`),
  nothing is written.
- A kit whose current version is broken (it has no `kit.yml` to read) is not
  seen as a follower: `pin` neither pins it nor reports it.
- A pin is a local version, as a `kit push` is: a git-tracked follower's next
  `kit sync` imports its source again, or refuses as `local push newer` when it
  is a path ancestor ([kits-path-parents.md](kits-path-parents.md#refusals)).
- A follower's own draft opened before the pin publishes its tree as it is,
  unpinned `base.kit` included (nothing is merged).
- Publishing a config-managed kit, or pinning a managed follower, drifts it
  from the config repo: the answer carries `drift: true` and
  `warning`: `this will drift from the config repo; config write it to keep the repo in sync`.
- The version is written before the draft is closed. Should closing it fail,
  the publish still answers 200, its `warning` (after any drift warning, `; `
  between) `published v<N>; the draft could not be closed — discard it`.

## The `kit draft` verbs

```
at-jam kit draft open app [--from v2]                  # → opened draft app from v2 (etag "…")
at-jam kit draft status app [--json]                   # base and current, files, changes vs the base, problems
at-jam kit draft get app run.sh [-o run.sh]            # one file's bytes (stdout by default)
at-jam kit draft put app run.sh ./run.sh [--mode 755]  # write a file (- reads stdin)
at-jam kit draft rm app old.sh                         # remove a file
at-jam kit draft sync app ./app                        # mirror a directory into the draft (needs an open draft)
at-jam kit draft publish app [--pin]                   # → published draft app as v4 (tree 3f2a…)
at-jam kit draft discard app                           # drop the draft
```

Each write first fetches the status and sends its etag, so it overwrites an
edit made since you last looked (`sync` replaces the whole tree). With
`--etag <hash>`, a write is checked against the etag you already hold and
fails with 412 instead. `publish` prints the new version's tree prefix, and
its `warning` on stderr. The admin-client flags apply as for every
[`kit` verb](kits.md#the-kit-verbs).

- `status` prints `draft <kit>: base vN, current vM`, and when another
  version landed since the draft was opened, that publishing replaces it with
  the draft (nothing is merged). Names, paths and problems are printed escaped.
- `sync` reads the directory as `kit push` does
  ([kits-directory.md](kits-directory.md)) and replaces the
  draft's tree with it. It prints the files
  added, removed, modified or mode-changed, or `unchanged`. It needs an open
  draft: run `kit draft open` first.
- `get -o <file>` writes the file with its mode in the draft; an existing
  symlink there is replaced, never written through.
- `publish --pin` prints a `pinned …` line per follower pinned.
- A usage error (a flag of another verb included) exits 2; a refused call
  exits 1 with the server's text.

## In the admin UI

A kit's page (`/app/admin/kits/<name>`) has, while no draft is open, an
**Open draft from v<N>** button for the version on view, and a **Draft** tab
(`/app/admin/kits/<name>/draft`) whose form opens one from any version
(current preselected). The Kits table and the page head flag a kit whose
draft is open. Every write is a form under the admin UI's
[write rules](ui-editing.md) and sends the draft's ETag back from a hidden
field, so the [concurrency](#concurrency) rule holds in the browser too.

- **Draft** card: base and current versions, the last save's operator and
  time, **Discard** (it asks first), and a notice when a newer version has
  landed since the draft was opened.
- **Publish** card: the problems, if any (the button stays disabled, and a
  forged POST is still refused 400), the **Pin followers** box and, for a
  config-managed kit, the drift warning.
- **Files**: the draft's tree by directory, with each file's mode, size and
  its change against the base; files dropped since the base are listed last,
  struck through. **Add file** takes a path and either an upload or nothing
  (an empty text file).
- **Changes** diffs each file against the base, and `kit.yml` gets its own
  line diff, with a packed context masked on both sides.
- A file opens in a plain text editor when it is UTF-8 text of 1 MiB or
  less with no carriage returns; otherwise it shows a note, **Download** and
  a mode toggle. A `kit.yml` holding a packed context is never shown: the
  note says to download it, remove the context, and replace it. Any file can
  be replaced by an upload (the upload's own file name is never used),
  renamed or deleted.
- A save that lost a race answers with the editor still holding your text,
  unsaved, Save disabled, and a reload link.
- While the editor holds unsaved text, any other Draft-tab action, a link
  or leaving the page asks first (`You have unsaved changes in the editor.
  Discard them?`): each would re-render the tab without it.

Publishing a config-managed kit from the UI is allowed, with a step the
API does not have: the form shows the drift warning and needs the box
**I understand this drifts from the config repo** ticked (the server
refuses 400 `kit <name> is managed by the config repo: tick “…” to
publish` without it). When **Pin followers** would rewrite config-managed
followers, the form names them next to the box, and with Pin ticked the
same confirmation is needed (400 `pinning followers drifts config-managed
<kits> from the config repo: tick “…” to publish`). The box is checked
before the draft's existence and ETag, so an unticked publish of a gone or
stale draft answers this 400, not 404 or 412. The spec's "write to config repo instead" is read
as *publish, then write to the config repo*: after any publish that drifts
(the kit, or a managed follower it pinned), the result card lists the
config-repo files to expect — `kits/<name>/<path>` for each file that
changed, `kits/<follower>/kit.yml` per managed follower pinned, paths and
change kinds only — and says to run `at-jam config write <repo>` in the
checkout, check `git diff`, and commit. Every publish's result card shows
the new version, each follower pinned and any warning.

UI writes log `ui kit draft opened|saved|discarded|published`, shaped
like the API's audit lines below and likewise never naming content or a
path.

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
| `POST /admin/kits/{name}/draft/publish` | Publish (`If-Match`; `{"pin": bool}`, `application/json`). Answers `version`, `tree`, `base`, `pinned`, `drift`, `warning`. |

A write answers the new `etag` (also the `ETag` header), the files it
`changed` and the `problems`. A path is percent-encoded per segment; one
with an empty, `.` or `..` segment (never redirected) or a control or
format character is refused without being echoed.

`GET /admin/kits` marks a kit with an open draft `"draft": true`.

Each write that changes the draft logs `kit draft opened|saved|discarded`
with the kit, the operator and the tree's first 12 hex digits (a save also
the count of files `changed`), never content or a path. A publish logs
`kit draft published` with the kit, the operator, the new `version`, the
`base`, the `tree` prefix, the count `pinned` and `drift`. A store failure
answers a fixed 500 `internal error` and logs `kit draft: store error` (at
error, with the method and route); a draft not closed after a publish logs
`kit draft: published but not closed` at warn.
