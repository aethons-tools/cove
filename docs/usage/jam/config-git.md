---
summary: Keeping a Jam's configuration in a git repo — the file layout, `config write|diff|apply --dir`, managed objects, drift and refusals, and replicating a Jam from a checkout.
read_when: You want a Jam's config reviewable in git, are standing up a replica from a checkout, or a config diff/apply refused, flagged drift or listed something unmanaged.
owns: the config repo layout and file kinds, the `at-jam config write|diff|apply` verbs, the plan actions and refusals, managed marks, the `/admin/config/diff|apply|export-tree` routes
prereqs: serve.md (layered serve configs, host keys); backup.md (how this differs from export/import)
tier: leaf
updated: 2026-10-21
---

# A Jam's configuration in git

`at-jam config` turns a Jam's configuration into a directory of small YAML
files you review and merge like code, and plans/applies such a directory back
onto a Jam. For a one-file snapshot instead, see [backup.md](backup.md).
Slice 1 reads a local checkout (`--dir`); git sources, `track`/`sync` and
`.source.yml` are not built yet.

## Layout

```
jam.yml                          portable serve settings (see serve.md)
context.md                       Jam-wide session context
destinations/<n>.yml  model-specs/<n>.yml  users/<n>.yml  connections/<n>.yml
kits/<n>/kit.yml                 today's studio-kit format
projects/<n>/project.yml  projects/<n>/context.md  projects/<n>/roles/<r>.yml
```

Other top-level files (README, `.git`) are ignored. Inside the known
directories anything else is refused (`kits/<n>.source.yml`: `kit source
references are not supported yet`; a `.yaml` extension: `use .yml`).

Every non-kit file starts `kind:` (`destination`, `model-spec`, `user`,
`connection`, `project`, `role`) and `version: 1`. Unknown fields are refused
(`users/bob.yml: unknown field "oidc"`); a `name:` must equal the file name.
Output of `config write` is deterministic (2-space yaml, sorted maps, members
and rooms).

```yaml
# users/bob.yml
kind: user
version: 1
logins: [bob@acme]
```
```yaml
# connections/acme.yml      service, not kind: (kind is the file kind)
kind: connection
version: 1
service: discord
cred: acme-bot              # a credential NAME, never a value
```
```yaml
# model-specs/fast.yml      harness-version, not version: (version is the file's)
kind: model-spec
version: 1
type: claude
harness-version: "2.1.0"
principal: {credential: anthropic}
```
```yaml
# projects/app/project.yml  members, rooms, escalation, chat-service
kind: project
version: 1
members: [{user: bob, delivery: [{service: discord, address: "bob#1"}]}]
rooms: [{key: ops, bindings: [{connection: acme, ref: ACME-1}]}]
```
```yaml
# projects/app/roles/ic.yml   a role's context is inline YAML: context: {core, leaves}
kind: role
version: 1
kit: app
scope: {destinations: [gh], ttl: 1h}
```

`context.md` (Jam and project) is optional YAML front matter holding
`leaves:` (`name`, `read-when`, `body`) followed by the core as the markdown
body; the core's trailing newlines are trimmed and one is written. A core
that opens with `---` and has no leaves is written after empty front matter
(`---` twice) so it reads back as the core.

Kits are inline only: `kits/<n>/kit.yml`, with path parents across `kits/`
(`base: {kit: ../web-dev}`) rewritten to `name@vN` on apply. An `@vN` in a
role's `kit` or a kit's `base.kit` is refused (`<path>: <ref> pins a version;
versions are per-Jam — name the kit (or use a path parent)`). Versions
written by `--dir` record provenance repo `local`, dir `kits/<name>`.
`config write` renders a kit's parent as a path parent only when its pin
equals the parent's current version (apply re-pins to current). A kit pinned
behind fails the write (409 on the route): `kit <child> pins <parent>@v<N>
but <parent> is at v<M>; versions are per-Jam — re-push <child> against v<M>
(or pin <parent> to v<N>) before config write`.

**Never in the repo:** token hashes, actors, grants, OIDC identities,
accounts, instances, intercom state, channel membership, pool state,
credential values, the legacy roster. The file types have no field for them.

## The verbs

All take `--app` (settings/token profile), `--admin-url` (overrides the app's settings) and `--token` (operator token; env `AT_JAM_ADMIN_TOKEN`).

| Command | Does |
|---|---|
| `config write <dir> [--all] [--force]` | Render managed objects (`--all`: every live one) into `<dir>`, plus `jam.yml` whenever the serve has a portable config; remove files of managed objects Jam no longer has. Refuses a `<dir>` outside a git working tree without `--force`. |
| `config diff --dir <path>` | Plan the checkout against the Jam; writes nothing. Exit 1 on refusals. |
| `config apply --dir <path> [--yes] [--force]` | Plan, print, prompt `Apply N change(s)? [y/N]` (skipped by `--yes`), then apply in one transaction. `--force` writes over drift (the preview is planned forced too, so it shows `update … (drift)` and its digest matches the apply). |

Operator loop: `config write --all` once, `git diff`, commit; afterwards edit,
`config diff`, `config apply`. A plan with no create/update/delete/adopt
and no `forget` prints `nothing to apply` and does not prompt. Without `--dir` a verb exits 2
(`--dir is required (git sources are not supported yet)`).

## The plan

Printed as `action  kind name`, then `forget  kind name  (mark only)` lines, `N unchanged`, `unmanaged: …`,
`serve-config: …`, and a `refused:` block (each refusal indented).

| Action | Meaning |
|---|---|
| `create` / `update` | In the repo, absent / different in Jam. A managed mark whose object is gone from Jam and present in the repo is a `create`; absent from both, it is a `forget`. |
| `forget` | A managed mark whose object is gone from Jam and the repo: apply removes the mark only, so a later unmanaged object of that name stays untouched. Counted as a change (`N forgotten`). |
| `update` + `(drift)` | The Jam object was edited since the last apply. Printed with `jam:` and `repo:` blocks; refused without `--force`. |
| `adopt` | An unmanaged Jam object identical to the repo's: marked managed, nothing written. The seeded `claude-default` model spec differs from the repo's → `update`, not a refusal. |
| `delete` | Managed, no longer in the repo. Subject to the guards below. |
| `unchanged` / `unmanaged` | Nothing to do / live but not in the repo's care: never touched, never deleted. |

Refusal texts (any one blocks the whole apply; nothing is written):

- `<kind> <name> exists and is not managed; config write --all it into the repo, or rename`
- `<kind> <name> drifted (edited in Jam since config @<commit[:7]>); re-run with --force or config write it first`
- `cannot delete <kind> <name>: <reason>`, guarding: a role with a live
  instance; a kit a role binds (`role app/ic binds it`) or another kit builds
  on; a model spec a role resolves to; a user who owns a live personal
  session, is named in a remaining escalation tier, or is a **member of any
  project** (managed or not); a project with live instances, unremoved
  members, unmanaged roles, standing sessions or grants; a connection with
  accounts, a binding or a chat service.

Order: creates/updates run users, connections → destinations, model specs →
kits (parents first) → projects → roles → Jam context; deletes the reverse.
Loading, planning and validation precede the single `Store.ApplyConfig`
write, so a failure writes nothing.

**Managed marks** record which objects the repo owns (kind + name; a role is
`<project>/<role>`; the Jam context is `jam`) and the commit (`dir` here).
They live in the Jam, not the repo, and are **not** part of `at-jam export`
([backup.md](backup.md)): a restored Jam starts with none, and its next apply
adopts identical objects. Errors are file-located and never echo credential
values or unvalidated text (a type error is `line N: a value has the wrong type`).

## Replicating a Jam

Serve the replica with `--config jam.yml --config jam.local.yml` (portable
settings from the repo, host keys in the local file; see
[serve.md](serve.md#layering-serve-configs) for the overlay, host keys and the
`same | differs | absent | unknown` comparison `config diff` reports), supply
the credentials the destinations and connections name, then
`config apply --dir`. People sign in again (OIDC identities are not copied)
and the seeded `claude-default` is adopted.

## Routes and limits

`config write|diff|apply` call these (behind the `/admin/*` operator gate):

| Route | Does |
|---|---|
| `POST /admin/config/diff?force=1` | Plan an uploaded tree (`configtar` tar.gz body); `force=1` plans as a forced apply (drift is an `update`, not a refusal). |
| `POST /admin/config/apply?force=1&plan=<digest>` | Apply; a refused or stale-digest plan is 409. |
| `GET /admin/config/export-tree?all=1` | Render the tree (`files`, `remove`). |

Uploads: gzip body ≤ 16 MiB and unpacked files ≤ 16 MiB, ≤ 2000
entries (413 beyond any); regular files and directories only, no absolute
or `..` paths (400).
Audit: a `config applied` log entry (source, commit, counts, operator,
forced) plus each object's usual `admin …` entry with `via=config`.

## Not yet

`--git` sources, `track`/`sync`, `.source.yml`, the UI page, drift attention.
