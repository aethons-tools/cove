---
summary: Keeping a Jam's configuration in a git repo — the file layout, `config write|diff|apply --dir`, managed objects, drift and refusals, and replicating a Jam from a checkout.
read_when: You want a Jam's config reviewable in git, are standing up a replica from a checkout, a config diff/apply refused, flagged drift or listed something unmanaged, or you want the repo to stop managing an object (config release).
owns: the config repo layout and file kinds, the `at-jam config write|diff|apply|release` verbs, the plan actions and refusals, managed marks, drift and how it clears, the `/admin/config/diff|apply|export-tree|managed` routes
prereqs: serve.md (layered serve configs, host keys); backup.md (how this differs from export/import)
tier: leaf
updated: 2026-10-24
---

# A Jam's configuration in git

`at-jam config` turns a Jam's configuration into a directory of small YAML
files you review and merge like code, and plans/applies such a directory back
onto a Jam. For a one-file snapshot instead, see [backup.md](backup.md).
A Jam can also follow a config repo in git, and reference kits by their own
source: see [config-git-sources.md](config-git-sources.md).

## Layout

```
jam.yml                          portable serve settings (see serve.md)
context.md                       Jam-wide session context
destinations/<n>.yml  model-specs/<n>.yml  users/<n>.yml  connections/<n>.yml
kits/<n>/kit.yml                 today's studio-kit format
kits/<n>/…                       the rest of its directory (Dockerfile, scripts…; modes 0644/0755)
kits/<n>.source.yml              or a reference to the kit's own git source
projects/<n>/project.yml  projects/<n>/context.md  projects/<n>/roles/<r>.yml
```

Other top-level files (README, `.git`) are ignored. Inside the known
directories anything else is refused (a `.yaml` extension: `use .yml`).

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
(`---` twice) so it reads back as the core. A CRLF core is written with LF (as it is read). A YAML string yaml.v3 would not read back as written (a leading newline or space on a multi-line value, a tab) is written double-quoted, so every value Jam accepts round-trips. After upgrading past this, a managed object whose canonical form changed (a re-quoted string, a CRLF core) reads as drifted (`config.drift`) once, until its next apply.

Inline kits are `kits/<n>/kit.yml`, with path parents across `kits/` (`base: {kit: ../web-dev}`) rewritten to `name@vN`
on apply. An `@vN` is refused: in a role's `kit`, `<path>: kit <ref> pins a version; versions are per-Jam — name the kit`;
in a kit's `base.kit`, `kits/<n>/kit.yml: base.kit <ref> pins a version; versions are per-Jam — name the kit or use a path
parent`. Versions written by `--dir` record provenance repo `local`, dir `kits/<name>` (from git: the config repo, dir, ref
and commit). A kit is written as its directory: every file of its current version at its mode, with a parent that is also in the tree written `../<parent>` in `kit.yml` (apply re-pins it). A write mirrors `kits/<n>/`: it removes every other file there that the directory's `.dockerignore` does not exclude (the exported one, else the dir's own: a regular file under 64 KiB, kept with what it excludes, or the dir is left alone); a `kits/<n>/` left beside `kits/<n>.source.yml` is refused as left over — remove it. A kit written as `kits/<n>.source.yml` loses its `kits/<n>/`, and back. A kit too big for the config upload limits is still written; apply it from a git source. A repo written by an older Jam (`base.context-dir` and `kits/<n>/context/`) is refused until a `config write`: [the upgrade step](kits-directory.md#stored-as-trees). Checkouts keep git's modes under any serve umask, so a tracked kit fetched before under
a non-022 umask gets one new version on its next sync.
`config write` renders a kit's parent as a path parent only when its pin equals the parent's current version (apply
re-pins to current). A kit the write includes (managed, or any with `--all`) pinned behind its in-tree parent fails the
write (409 on the route): `kit <child> pins <parent>@v<N> but <parent> is at v<M>; versions are per-Jam — re-push <child>
against v<M> (or pin <parent> to v<N>) before config write`.

**Never in the repo:** token hashes, actors, grants, OIDC identities, accounts, instances, intercom state, channel
membership, pool state, credential values, the legacy roster. The file types have no field for them.

## The verbs

All take `--app` (settings/token profile), `--admin-url` (overrides the app's settings) and `--token` (operator token; env `AT_JAM_ADMIN_TOKEN`).

| Command | Does |
|---|---|
| `config write <dir> [--all] [--force]` | Render managed objects (`--all`: every live one) into `<dir>`, plus `jam.yml` whenever the serve has a portable config; remove files of managed objects Jam no longer has. Refuses a `<dir>` outside a git working tree without `--force`. |
| `config diff --dir <path>` (or `--git`, see below) | Plan the checkout against the Jam; writes nothing. Exit 1 on refusals. |
| `config apply --dir <path> [--yes] [--force]` | Plan, print, prompt `Apply N change(s)? [y/N]` (skipped by `--yes`), then apply in one transaction. `--force` writes over drift (the preview is planned forced too, so it shows `update … (drift)` and its digest matches the apply). |
| `config release <kind>/<name>` | Clear the object's managed mark (a role is `role/<project>/<role>`): the object stays, unmanaged — the next plan lists it as unmanaged, or adopts or refuses it if the repo still holds it. Not managed: exit 1 (`<kind> <name> is not managed`). |
| `config track\|sync\|untrack` | Follow one config source: [config-git-sources.md](config-git-sources.md). |

Operator loop: `config write --all` once, `git diff`, commit; afterwards edit, `config diff`, `config apply`. A plan with
no create/update/delete/adopt and no `forget` prints `nothing to apply` and does not prompt; it still sends the apply, which refreshes the marks of unchanged objects (after a `config write`, say). Without `--dir`, diff and apply use `--git` or the tracked source.

## The plan

A kit's plan entry compares its `kit.yml` and its file list (mode, size, sha256, path).
Printed as `action  kind name`, then `forget  kind name  (mark only)` lines, `N unchanged`, `unmanaged: …`,
`serve-config: …`, and a `refused:` block (each refusal indented).
A kit update prints its version diff (`kit.yml` lines, then the files) under its line; the Config page shows the same. Same tree, other source: the listing diff (its `# source:` line) instead.

| Action | Meaning |
|---|---|
| `create` / `update` | In the repo, absent / different in Jam. A managed mark whose object is gone from Jam and present in the repo is a `create`; absent from both, it is a `forget`. |
| `forget` | A managed mark whose object is gone from Jam and the repo: apply removes the mark only, so a later unmanaged object of that name stays untouched. Counted as a change (`N forgotten`). |
| `update` + `(drift)` | The Jam object was edited since the last apply. A kit prints its version diff (Jam's tree → the repo's); any other kind a compact line diff (`--- jam` / `+++ repo`). Refused without `--force`. A drifted `delete` prints `jam:` and `repo:` blocks, its repo side `(not in the repo)`. |
| `update` + `(not managed)` | The repo copy of an unmanaged Jam object differs: refused (below), printed with the same diff, which the Config page shows too. |
| `adopt` | An unmanaged Jam object identical to the repo's: marked managed, nothing written. Adopted and updated (`update`) instead of refused: the seeded `claude-default` model spec, and a kit tracking the very repo, dir and ref its `kits/<n>.source.yml` names (its ref moved since Jam synced: a new version at the fetched commit). Never an inline kit or another source. |
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
`<project>/<role>`; the Jam context is `jam`) and the commit (`dir` for `--dir`).
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
and the seeded `claude-default` is adopted if identical to the repo's,
otherwise updated.

## Routes and limits

`config write|diff|apply|release` call these (behind the `/admin/*` operator gate):

| Route | Does |
|---|---|
| `POST /admin/config/diff?force=1` | Plan an uploaded tree (`configtar` tar.gz body); `force=1` plans as a forced apply (drift is an `update`, not a refusal). |
| `POST /admin/config/apply?force=1&plan=<digest>` | Apply; a refused or stale-digest plan is 409. |
| `GET /admin/config/export-tree?all=1` | Render the tree: `files`, `remove`, and `kits` (each `kits/<n>` dir's files: base64 `data`, `mode`; the writer mirrors the dir to them, an empty list to nothing but what its `.dockerignore` excludes: see [the layout](#layout)). A kit pinned behind its in-tree parent is 409. |
| `DELETE /admin/config/managed/{kind}/{name...}` | Release a mark (204); unknown kind 400, not managed 404.  |

Uploads: `Content-Type: application/gzip` (any other type is 415, so a
browser's no-cors `text/plain` POST to a loopback admin port is refused;
`application/json` is a `{source}` body —
[config-git-sources.md](config-git-sources.md#routes)). Gzip body ≤ 16 MiB,
unpacked files ≤ 16 MiB, ≤ 2000 entries, and the decompressed tar stream
bounded (413 beyond any); regular files and directories only, no absolute
or `..` paths (400).
Audit: a `config applied` log entry (source, commit, counts, operator,
forced) plus each object's usual `admin …` entry with `via=config`.

## Drift and release

An object is **drifted** when Jam's copy no longer has the digest its mark
recorded (edited in Jam since the apply), and **missing** when it was
removed in Jam. While any managed object is either, Jam holds the
`config.drift:jam` condition (info: Health tab only, never pushed — [monitoring.md](monitoring.md)). Serve
re-checks every minute and after every apply, sync or release; an edit in Jam therefore shows within a
minute. With no marks the check is skipped. If **any** kit fails to render
(managed or not), the check cannot compare, warns once per changed error and
keeps the condition's last state. A `kit sync` onto a managed
`.source.yml` kit that differs from the config repo raises it until the next
`config sync`. Three ways out: `config apply --force` (the repo wins);
`config write`, commit, `config apply` (Jam wins — the apply has nothing to
change and refreshes the marks); or `config release <kind>/<name>` (the repo
stops managing it). The admin UI shows all of this and badges managed
objects: [ui-config.md](ui-config.md).
