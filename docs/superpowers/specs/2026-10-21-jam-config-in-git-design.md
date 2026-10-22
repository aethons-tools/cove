# A Jam's configuration in git

**Status:** design approved in brainstorming (2026-10-21); spec, pre-plan.
**Owner:** Jam service (admin-spider).
**Builds on:** kit git sources and kit sets ([`2026-10-20-kit-git-sources-and-inheritance-design.md`](2026-10-20-kit-git-sources-and-inheritance-design.md));
the config backup ([`backup.md`](../../usage/jam/backup.md)); the serve config ([`serve.md`](../../usage/jam/serve.md)).

## Summary

A Jam's whole configuration can live in a git repo as readable, one-file-per-object
YAML: kits, destinations, model specs, users, connections, projects (escalation,
members, rooms, context) and their roles, the Jam-wide session context, and the
portable part of the serve config. Jam **applies** the repo (from a checkout or a
tracked git source) and **writes** its current config into a checkout for the
operator to commit. A repo plus a host's own `jam.local.yml`, credentials and
certificates is enough to stand up a replica Jam elsewhere.

`at-jam export`/`import` (the JSON backup) stays as it is: it is a full,
id-preserving backup for restore onto an empty Jam, not a reviewable source.

## Decisions (brainstorming record)

1. **Both directions.** Jam applies the repo (the main path) and writes its config
   into a checkout (to capture UI edits). The UI and admin API keep working.
2. **Managed set.** Objects that came from the repo are *managed*; apply fully
   reconciles them (create, update, delete). Objects created in Jam and never
   written to the repo are *unmanaged* and never touched; the diff lists them.
3. **People by name.** The repo holds users (name, logins), project memberships
   and connections (kind, credential name). Never: tokens, OIDC subjects,
   service-account links, grants. Each person signs in or links accounts on each
   Jam.
4. **Serve config = repo file + host overlay.** The repo's `jam.yml` holds the
   portable settings; each host adds `jam.local.yml`. Apply never changes the
   running serve config; the diff reports when it differs.
5. **Kits inline or by reference.** `kits/<name>/kit.yml` in the config repo
   (the default), or `kits/<name>.source.yml` naming the kit's own git source.
6. **Writing to git is the operator's.** `config write <dir>` fills a local
   working tree; the operator reviews (`git diff`), commits and pushes. Jam's git
   access stays read-only.

## 1. Repo layout

```
jam.yml                           # portable serve config
context.md                        # Jam-wide session context
destinations/<name>.yml
model-specs/<name>.yml
users/<name>.yml
connections/<name>.yml
kits/<name>/kit.yml               # inline kit, today's studio-kit format
kits/<name>.source.yml            # or a reference: {kind: kit-source, git: <source>}
projects/<name>/project.yml
projects/<name>/context.md
projects/<name>/roles/<role>.yml
```

- **Files.** YAML; every non-kit file carries `kind:` (`destination`,
  `model-spec`, `user`, `connection`, `kit-source`, `project`, `role`) and
  `version: 1`. Unknown fields are refused. A file's name is the object's name; a
  `name:` field, if present, must match.
- **Shape.** Each kind's fields are the admin API's authoring shape with every id
  replaced by a name: a role's `kit` and `model-spec`; escalation targets
  (`user:<name>`); a membership's user; a room binding's connection.
- **Projects.** `project.yml` holds escalation (default and by category), chat
  service, resources, members (user name + delivery profiles) and rooms (key,
  label, bindings). The legacy roster is not written. `context.md` is the
  project's authored context layer.
- **Roles.** `roles/<role>.yml` holds scope (incl. egress), kit, model spec,
  allocation (incl. standing sessions), turn-end, collaborate, and its context
  layer inline (`context:`).
- **Never in the repo** (the file types have no field for them): token hashes
  and agent actors, grants, OIDC identities, accounts (service-identity links),
  instances, intercom history and cursors, channel membership, pool state,
  credential values.
- **Context markdown.** `context.md` files are the layer's prompt and notes in
  the session-context authoring format ([`session-context-authoring.md`](../../usage/jam/session-context-authoring.md));
  the same budgets apply.

## 2. Verbs

CLI (`at-jam config …`), admin API (`/admin/config/…`) and a Config page in the UI.

| Verb | Effect |
|---|---|
| `config diff [--git <src> \| --dir <path>]` | Load, validate and plan; print the plan. Writes nothing. With neither flag, uses the tracked source. |
| `config apply [--git <src> \| --dir <path>] [--yes] [--force]` | Plan, then execute it in one transaction. The CLI shows the plan and asks unless `--yes`. `--force` overrides drift (§3). |
| `config track --git <src>` | Record the Jam's one config source, then `diff` it. |
| `config sync` | `apply` from the tracked source. |
| `config untrack` | Forget the source. Objects and their managed marks stay. |
| `config release <kind>/<name>` | Clear an object's managed mark (it becomes unmanaged). |
| `config write <dir> [--all]` | Write the managed objects (and, with `--all`, the unmanaged ones) and the portable serve config into a working tree. |

- **Source syntax, fetcher, credentials:** exactly the kit sources'
  (`<repo>[//<dir>]@<ref>`, `gitsource.Fetcher`, `kits.git` prefixes). `--dir`
  needs no credentials and is the bootstrap path for a replica.
- **Admin routes:** `POST /admin/config/diff` and `/apply` (`{source}` or an
  uploaded tree for `--dir`, as a tar the CLI packs, under the kit context limits
  scaled to 16 MiB); `PUT`/`DELETE /admin/config/source`; `POST
  /admin/config/sync`; `GET /admin/config/export-tree` (what `write` renders);
  `DELETE /admin/config/managed/{kind}/{name}`.

## 3. Planning and apply

**The plan** is a pure function of (repo tree, Jam's current config, managed
marks) → ordered operations `create | update | delete | unchanged` per object,
plus `unmanaged` objects, `refusals`, and `serve-config: same | differs`. Diff and
apply compute the same plan; apply refuses to run a plan with refusals.

**Managed marks.** Each managed object has a mark `{commit, digest}`: the source
commit (or `dir`) it was last applied from and the digest of its canonical form
as applied. Marks live in a new `config_marks` table (Postgres) / map (Mem), keyed
by kind and name, and are written in the apply transaction.

| Case | Plan |
|---|---|
| In repo, not in Jam | create (mark set) |
| In repo, managed, canonical form differs | update |
| …and Jam's live digest ≠ the mark (edited in Jam since apply) | update flagged **drift**; refused without `--force`; the diff shows both sides |
| In repo, exists unmanaged, same canonical form | adopt (mark set, no write) |
| In repo, exists unmanaged, different | refusal: `<kind> <name> exists and is not managed; config write --all it into the repo, or rename` |
| Managed, not in repo | delete |
| Unmanaged, not in repo | `unmanaged` (listed, untouched) |

**Order and validation.** Creates and updates run users, connections →
destinations, model specs → kits (root first) → projects (members, rooms,
escalation) → roles; deletes run in reverse. Every object passes the admin API's
own validation, with references resolved against the *planned* state: a role's
kit and model spec, escalation and member users, room-binding connections,
kit chains (cycle, depth ≤ 4), destination rules, context budgets.

**Delete guards** (refusals, naming the user of the object): a role with live
instances; a kit a role or another kit references; a model spec a role binds; a
user with live sessions or named in a remaining escalation tier; a project with
live instances; a connection a room binding or a live account uses.

**Kits.**
- Inline kits import as one set from the repo's commit through the slice 3
  machinery (`resolveKitSet` over the config checkout): only changed kits get a
  version, with provenance `{repo, dir, commit}` (`dir` for `--dir`: `local`).
  Path parents work across `kits/`.
- A `.source.yml` kit runs today's track + sync for its source inside the plan;
  its fetch happens before the transaction.
- `@vN` pins in a repo-managed role or kit are refused (versions are per-Jam);
  path parents are pinned by rewriting, as today.
- Images are not built by apply; they build on raise, as today.

**Atomicity.** All fetching, loading and validation happen first; the plan is
then written in one store transaction (the `PutKitDocs` pattern, extended to
every kind). Any failure writes nothing. Apply holds `kitMu` and a new
`configMu` (taken first), so two applies, or an apply and a kit write, never
interleave.

**Ids.** Objects created by apply get fresh ids on the target; names in the repo
resolve to the target's ids. Removed (tombstoned) objects are not written; a name
whose object was removed in Jam is free to be created again.

**Audit.** `config applied` (source, commit, counts, actor, forced) and each
object's usual audit entry tagged `via: config`.

## 4. Serve config

- `at-jam serve --config <file>` is repeatable. Files merge in order: maps merge
  by key, scalars and lists replace. One `--config` behaves as today.
- **Host keys** may appear only in a later file, never in the repo's `jam.yml`
  (refused at diff, apply and serve start when the first file has them):
  `listen`, `admin-listen`, `ui-hosts`, `ui-origins`, `dev-identity`, `tls`,
  `admin-tls`, `store-postgres`, `state-dir`, `credentials-file`,
  `runtime.launcher`, `pool.store` and `previews.tls`. The list is one table in
  code (`jam.ServeHostKeys`).
- **Diff** compares the repo's `jam.yml` with the running serve's merged config
  minus host keys and reports `same` or `differs (restart to apply)` with the
  changed key paths. Apply never changes the running serve config.
- **Write** renders the running serve's portable part as `jam.yml`.

## 5. `config write`

- Deterministic: sorted keys, stable file names, a fixed field order; writing an
  unchanged Jam leaves `git diff` empty.
- Removes only files of managed objects Jam no longer has; never other files.
- Refuses a `<dir>` that is not inside a git working tree unless `--force`.
- Writing an unmanaged object (`--all`) does not mark it; the next apply of a
  repo that contains it marks it (a create over an unmanaged object with an
  identical canonical form is `adopt`, §3).

## 6. Surfaces

- **Config page** (Admin): tracked source, last applied commit and time;
  **Diff** (the rendered plan), **Apply** (htmx, `originGuard`, a `force`
  checkbox shown only with drift), **Track**, **Untrack**.
- **Object pages:** a `managed · config @abc1234` badge; saving an edit to a
  managed object shows "this will drift from the config repo" before it saves.
- **Attention:** `config.drift` (info) while any managed object's live digest ≠
  its mark; clears on apply, release or write-and-apply.

## 7. Errors

Specific, file-located, never echoing tokens or unvalidated repo text:

- `projects/app/roles/ic.yml: kit web-dev not found (not in the repo or Jam)`
- `cannot delete kit old: role app/ic binds it`
- `jam.yml: listen is a host key; put it in jam.local.yml`
- `destination anthropic drifted (edited in Jam since config @abc1234); re-run with --force or config write it first`
- `users/bob.yml: unknown field "oidc"`

## 8. Testing

- **Planner (pure):** every row of §3's table, ordering, planned-state
  references, every delete guard, pins refused, adopt.
- **Round trip:** a populated Jam → `write` → `apply --dir` into an empty Jam →
  the two Jams' config exports are equal modulo ids, actors, grants and
  identities. This is the replication check.
- **Atomicity:** Mem and Postgres (storetest), with an injected failure late in
  the plan.
- **Serve config:** merge order, host-key refusal, diff key paths.
- **Write:** determinism (write twice → identical tree), managed-only removal.
- **Git:** track/sync over the hermetic smart-HTTP fixture; `.source.yml` kits.
- **Concurrency:** apply vs a concurrent kit push (`configMu` → `kitMu`).

## Slices

1. **Files and round trip:** the file kinds and their codecs, `config write`,
   `config diff` and `apply --dir`, managed marks (`config_marks`), the planner,
   transactional apply, the serve-config overlay and host keys. Replication from a
   checkout works at the end of this slice.
2. **Git:** `config track | sync | untrack --git`, the tracked config source, and
   `kits/<name>.source.yml` references.
3. **UI and drift:** the Config page, managed badges, the drift warning on
   save, `config release`, and the `config.drift` attention condition.

## Out of scope

- Jam pushing commits or opening pull requests (decision 6; later, if wanted).
- Webhooks or polling for config changes.
- Moving portable serve settings into the database for live apply.
- Secrets, identities, grants and runtime state in the repo.
- Replacing `at-jam export`/`import`.
