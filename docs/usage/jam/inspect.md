---
summary: Seeing everything a studio's agent is set up with from outside the container — `jam role inspect` / `studio inspect [--diff]`, their admin routes and the admin console's Setup section, the sections and sources, snapshots, image facts, and redaction with its known limits.
read_when: You want to know what a studio (or a raise of a role now) gets — its session-context files, egress, destinations, env, model-spec, kit and image, /agent-data seed, MCP servers or policy — or why a studio differs from its role — in the CLI, the admin API or the Setup section of an agent or role page.
owns: setup inspection (role/studio inspect, GET …/setup routes), setup snapshots, the image-facts reader, setup redaction rules and their known limits, the admin console's Setup section on agent and role pages
prereqs: coves.md, session-context.md, connector.md
tier: leaf
updated: 2026-11-01
---

# Inspecting a studio's setup

`jam role inspect <project>/<role>` shows what a studio of the role raised
**now** would get. `jam studio inspect <studio>` shows what a running studio
**got**; `--diff` adds, per section, a line diff (`-` this studio, `+` a raise
now; `identity` is never diffed; a deleted role gives a note instead). Both take
`--json` and `--section <name>` (repeatable or comma-separated; an unknown name
is an error). A bare `<role>` is in the default project; a studio is its
session id or the label of a live manual session; an unknown role or studio is
404. Inspect never fails as a whole: a part that cannot be produced renders
`unavailable` with a fixed reason and the rest renders.

Routes (operator auth, like every `/admin/*` route; each call is logged
`setup inspected` with operator, subject and sections — never the body):

- `GET /admin/roles/{project}/{role}/setup?sections=a,b[&manager=M]`
- `GET /admin/instances/{id}/setup?sections=a,b&diff=role`

`role inspect <p>/<r> --manager <name>` (admin `?manager=`) reads the image
facts from that manager instead of the default. An unknown manager is 404, an
unavailable one 503 (checked before anything renders). `studio inspect` has no
`--manager`: a studio's facts always come from the manager it runs on; when
that manager is not connected or no longer registered, the image parts render
`unavailable` with that reason and the rest still renders.

## In the admin UI

A running studio's agent page and a role page end with a **Setup** section,
read only when you open it (each read is an image read on the launcher host).
It shows the sections below, one collapsible block each, headed with its
source, `stale vs role` and the snapshot time. `context` lists each file at its real path, **raw** — never
rendered as markdown, so nothing in it is hidden or fetched. Text is escaped
as in the CLI (a bidi override shows as `\u202e`). Opening it never holds up the
agent or role page, and a read is never retried or repeated on focus. Chips
filter to one section (the API's `?sections=`). An image-manager selector
shows when two or more managers are connected. On a studio, a `stale vs role`
button loads that section's diff against a raise now in place, and **Diff vs
role** (`?diff=role`) diffs every section. Read-only, behind the same admin auth;
each load is logged `ui setup inspected` (operator, subject, sections).

## Sections

In this order:

| Section | Shows |
|---|---|
| `identity` | project, role, session kind, studio id, phase, raised at (a role render is a would-be ephemeral session: no unit, no owner) |
| `context` | every [session-context](session-context.md) file at its real path under `/agent-data/context/`, sizes, each layer's core budget used, compile warnings |
| `egress` | the effective allow-list — the role's list (marked when outside the kit ceiling) or the kit default — then the sealed base rows |
| `destinations` | per destination in scope: route, upstream, identity-in/apply, credential **name**, git routing, allow-paths, env keys |
| `env` | the [connector](connector.md) env an agent spawn gets (redacted), then the image `ENV` and its `JAM_SSHENV` transfer set |
| `model` | the resolved model-spec, as of now (never stale; not recorded at raise) |
| `kit` | kit ref, chain root first with provenance and each link's tree (`tree <12 hex>, N files`; `1 file`; `not loadable` for a broken or unstored version), build args, image tag, image id and labels |
| `seed` | what the image seeds into `/agent-data`: `CLAUDE.md` and its `@` imports, skills, `settings.json`, the `.refresh` list; other files by path and size only |
| `mcp` | the kit's MCP servers (no args, no header values) and the intercom tools |
| `policy` | allocation, standing, turn-end, collaborate, escalation; for a studio its idle override and alarms (name, schedule, next fire, gate set or not) |

Each section is headed `== <name> (<source>[, stale][, <at>])`. A section with a `title` (JSON) is headed by it instead of its name. Sources:
`snapshot` (what Jam recorded delivering), `recomputed` (a studio part with no
snapshot — raised before recording; rendered from config now), `config` (as of
now), `image`, `unavailable` (with a fixed reason). `stale` (studios only)
marks a part whose delivered fingerprint differs from what the role would
deliver now. In `context` the BYTES column is the size **after** redaction,
and the text is shown redacted at the masked spots. Body lines are indented
two spaces (diff text two spaces after its `+`/`-`/` ` op), so only headers,
reasons and verdicts start a line; `--json` bodies are raw.

## Snapshots

Jam records the masked form of four parts **when it delivers them**, only when
the fingerprint changes: `context` (raise, `GET /context`), `connector` (raise,
`GET /connector`; shown in the `env` section), `egress` (raise, each successful re-apply), `kit` (raise).
Only the latest per part is kept, capped at 64 KiB (truncated with a marker);
they go with the studio and are not part of `jam export`. A failed write is
logged, never fatal.

## Image facts

Read on the manager's host (a studio's own manager; a legacy record with no manager means `local`) through that manager's docker context, as argv
through the runner: `docker image inspect` (env, labels, id) and, for the seed,
`docker create --pull=never` (never started), `docker cp <ctr>:/home/agent/.init-agent-data -`,
`docker rm -f -v`. Only regular files; caps: 1 MiB total, 200 entries, text
≤ 64 KiB. Facts are cached per image id (16-image LRU); a docker error is not
cached, so the next inspect retries. An image without the seed dir reads
`image seeds nothing`. Image gone, docker failing, or no launcher
(placeholder): the image parts are `unavailable` and the rest renders. There
is **no server-side docker timeout**: a hung daemon blocks the inspect (the
admin client gives up after 60 s).

## Redaction

No secret value is shown or stored; everything goes through `RedactEnv`
(`internal/jam/setup_redact.go`). Credentials appear by name. The identity
token reads `«identity token»`. A value under a key matching
`token|secret|key|password|passwd|credential|authorization` (any case,
anywhere in the key), or holding `auth`, `pass` or `pwd` as a whole segment
(`X_AUTH`, `DB_PASS`, `smtp.pwd`; not `AUTHOR` or `PASSPORT_NO`), reads `«masked»` unless it is only placeholders (`{token}`, `{base}`, `{host}`,
`{url}`, with at most a `Bearer `/`token ` scheme); `{base}`/`{host}` stay
placeholders. Credential-shaped text under any key is masked too (`Bearer …`,
`Authorization:` headers, `--token x` / `--api.key=x` style flags,
`NAME_TOKEN=…` assignments, well-known token shapes such as `ghp_…`,
`sk-ant-…`, a 36-character `npm_…`). The same rule covers image `ENV`, build
args, label values, `settings.json` and each line of the seed's Markdown.
Authored `context` prose — every line of the core, files and warnings — is
masked for token shapes, `NAME_TOKEN=…` assignments and URLs, but not for flags or
`Authorization:` mentions, so docs text stays readable. URLs lose
their userinfo, and their query/fragment shows as `?«masked»`. A label that is
not clean UTF-8 text reads `invalid`.

**Known limits** (not detected): a password with an unencoded space or `/`; a
quoted secret value containing spaces; a secret under a Unicode lookalike key
name; scheme-less `user:pass@host` values. Keep secrets out of those places.

Design: [the spec](../../superpowers/specs/2026-10-24-studio-setup-inspect-design.md).
