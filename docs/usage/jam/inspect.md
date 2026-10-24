---
summary: Seeing everything a studio's agent is set up with from outside the container — `at-jam role inspect` / `studio inspect [--diff]` and their admin routes, the sections and sources, snapshots, image facts, and redaction with its known limits.
read_when: You want to know what a studio (or a raise of a role now) gets — its session-context files, egress, destinations, env, model-spec, kit and image, /agent-data seed, MCP servers or policy — or why a studio differs from its role.
owns: setup inspection (role/studio inspect, GET …/setup routes), setup snapshots, the image-facts reader, setup redaction rules and their known limits
prereqs: coves.md, session-context.md, connector.md
tier: leaf
updated: 2026-10-24
---

# Inspecting a studio's setup

`at-jam role inspect <project>/<role>` shows what a studio of the role raised
**now** would get. `at-jam studio inspect <studio>` shows what a running studio
**got**; `--diff` adds, per section, a line diff (`-` this studio, `+` a raise
now; `identity` is never diffed; a deleted role gives a note instead). Both take
`--json` and `--section <name>` (repeatable or comma-separated; an unknown name
is an error). A bare `<role>` is in the default project; a studio is its
session id or the label of a live manual session; an unknown role or studio is
404. Inspect never fails as a whole: a part that cannot be produced renders
`unavailable` with a fixed reason and the rest renders.

Routes (operator auth, like every `/admin/*` route; each call is logged
`setup inspected` with operator, subject and sections — never the body):

- `GET /admin/roles/{project}/{role}/setup?sections=a,b`
- `GET /admin/instances/{id}/setup?sections=a,b&diff=role`

## Sections

In this order:

| Section | Shows |
|---|---|
| `identity` | project, role, session kind, studio id, phase, raised at (a role render is a would-be ephemeral session: no unit, no owner) |
| `context` | every [session-context](session-context.md) file at its real path under `/agent-data/context/`, sizes, each layer's core budget used, compile warnings |
| `egress` | the effective allow-list — the role's list (marked when outside the kit ceiling) or the kit default — then the sealed base rows |
| `destinations` | per destination in scope: route, upstream, identity-in/apply, credential **name**, git routing, allow-paths, env keys |
| `env` | the [connector](connector.md) env an agent spawn gets (redacted), then the image `ENV` and its `COVE_SSHENV` transfer set |
| `model` | the resolved model-spec, as of now (never stale; not recorded at raise) |
| `kit` | kit ref, chain root first with provenance, build args, image tag, image id and labels |
| `seed` | what the image seeds into `/agent-data`: `CLAUDE.md` and its `@` imports, skills, `settings.json`, the `.refresh` list; other files by path and size only |
| `mcp` | the kit's MCP servers (no args, no header values) and the intercom tools |
| `policy` | allocation, standing, turn-end, collaborate, escalation; for a studio its idle override and alarms (name, schedule, next fire, gate set or not) |

Each section is headed `== <name> (<source>[, stale][, <at>])`. Sources:
`snapshot` (what Jam recorded delivering), `recomputed` (a studio part with no
snapshot — raised before recording; rendered from config now), `config` (as of
now), `image`, `unavailable` (with a fixed reason). `stale` (studios only)
marks a part whose delivered fingerprint differs from what the role would
deliver now. In `context` the BYTES column is the size **after** redaction,
and the text is shown redacted at the masked spots.

## Snapshots

Jam records the masked form of four parts **when it delivers them**, only when
the fingerprint changes: `context` (raise, `GET /context`), `connector` (raise,
`GET /connector`), `egress` (raise, each successful re-apply), `kit` (raise).
Only the latest per part is kept, capped at 64 KiB (truncated with a marker);
they go with the studio and are not part of `at-jam export`. A failed write is
logged, never fatal.

## Image facts

Read on the launcher host through the launcher's docker context, as argv
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
`token|secret|key|password|passwd|credential|authorization|auth` (any case)
reads `«masked»` unless it is only placeholders (`{token}`, `{base}`, `{host}`,
`{url}`, with at most a `Bearer `/`token ` scheme); `{base}`/`{host}` stay
placeholders. Credential-shaped text under any key is masked too (`Bearer …`,
`Authorization:` headers, `--token x` style flags, `NAME_TOKEN=…` assignments,
well-known token shapes such as `ghp_…`). The same rule covers image `ENV`,
build args, label values, `settings.json` and the `context` text. URLs lose
their userinfo, and their query/fragment shows as `?«masked»`. A label that is
not clean UTF-8 text reads `invalid`.

**Known limits** (not detected): a password with an unencoded space or `/`; a
quoted secret value containing spaces; a secret under a Unicode lookalike key
name; scheme-less `user:pass@host` values. Keep secrets out of those places.

Design: [the spec](../../superpowers/specs/2026-10-24-studio-setup-inspect-design.md).
