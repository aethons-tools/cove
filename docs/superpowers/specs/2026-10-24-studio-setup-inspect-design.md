# Studio setup inspection

**Status:** design approved in brainstorming (2026-10-23); spec, pre-plan.
**Owner:** Jam service (admin-spider).
**Builds on:** session context ([`session-context.md`](../../usage/jam/session-context.md)), kits and kit chains ([`kits.md`](../../usage/jam/kits.md)), destinations and the connector ([`connector.md`](../../usage/jam/connector.md)), the colima launcher.

## Summary

An operator can see, from outside the container, everything an agent is set up
with: the session-context files exactly as rendered and at their real paths, the
egress allow-list, destinations, the env the agent gets, the model spec, the kit
chain and image, the files the image seeds into `/agent-data`, MCP servers, and
role policy. One renderer produces this **setup** for a role ("what a studio
raised now would get") and for a running studio ("what this studio got"), and a
diff between them shows what a studio is missing. Secret values never appear.

## Decisions (brainstorming record)

1. **Both subjects, one renderer.** `role inspect` renders the would-be setup;
   `studio inspect` the delivered one; `--diff` compares them.
2. **Delivered = recorded snapshot, recompute as fallback.** Jam records a
   masked snapshot of what it actually delivers; a studio raised before this
   (or a part never delivered) falls back to a recomputed render, labelled so.
3. **Jam's inputs plus image facts.** Read the built image on the launcher host
   (env, labels, digest, seeded files) without starting a container. No probe of
   the running container.
4. **CLI and API first, then a UI tab** (two slices).

## 1. The setup

`StudioSetup` is a list of sections, each `{name, source, stale, at, body}`:
`source` is `snapshot` | `recomputed` | `image` | `config` | `unavailable`;
`stale` (studios only) is true when the delivered fingerprint differs from what
the role would deliver now; `at` is when a snapshot was taken.

| Section | Contents | Role render | Studio |
|---|---|---|---|
| `identity` | project, role, session kind, studio id, phase, raised at | role | instance |
| `context` | every file the cove receives at its real path — `/agent-data/context/CORE.md`, `INDEX.md`, each leaf, the kit's `tools.md` — with byte size and the layer budget used | `sessionctx.Compile` for a would-be session of the role | snapshot of the last `GET /context` served |
| `egress` | the effective allow-list (kit ceiling ∩ role scope), sealed base entries marked `sealed` | computed | snapshot of the last successful `ApplyEgress`; checked against `Instance.Egress` |
| `destinations` | per destination in the role's scope: route, upstream, identity-in and apply methods (preset name or spec), credential **name**, git routing, allow-paths, env template keys | config | config (as of now; marked stale if the connector fingerprint moved) |
| `env` | the connector env an agent spawn gets (templates rendered, the identity token shown as `«identity token»`), then the image `ENV` and its `COVE_SSHENV` transfer set | rendered | snapshot of the last `GET /connector`; checked against `Instance.Connector`; image part from inspect |
| `model` | model spec: harness version, model, provider, harness settings | config | config at raise time if recorded, else now |
| `kit` | kit chain root-first with versions and provenance (repo, dir, ref, commit), build args, image tag, digest, labels | config (+ image if the stage is cached) | `Instance.Kit`, `KitChain`, `ImageTag` + image |
| `seed` | what the image seeds into `/agent-data`: `CLAUDE.md` and the files it imports (full text), skills (name and description), `settings.json`, the `.refresh` list | image if cached, else `unavailable` | image |
| `mcp` | MCP servers (name, URL or command; no secrets) and the brokered intercom tools offered | kit + Jam | same |
| `policy` | allocation, standing sessions, turn-end (idle timeout, on-idle), alarms, collaborate rights, the project's escalation chain | role, project | role + instance (idle override, alarms) |

**Redaction.** No secret value is rendered or stored. Credentials appear by
name. The identity token in connector env is `«identity token»`. Any other env
value whose key matches `(?i)token|secret|key|password|passwd|credential` is
`«masked»` unless it is a template that renders only non-secret placeholders.
Snapshots store the masked form only. Repo URLs never carry userinfo (the
sources already refuse it); labels are re-parsed before display, `invalid`
otherwise.

## 2. Snapshots

- A per-instance record holds four parts — `context`, `egress`, `connector`,
  `kit` — each `{at, fingerprint, body}` with the body already masked.
- Jam writes a part **when it delivers it**: serving `GET /context`, a
  successful `ApplyEgress`, serving `GET /connector`, and the raise (kit and
  image tag). A write happens only when the part's fingerprint differs from the
  stored one, so a cove refreshing context every wake does not write each time.
- Sizes are bounded by existing budgets (context ≤ ~20 KiB); each body is
  capped at 64 KiB, truncated with a marker past that.
- Storage: a new Postgres table (migration) and the MemStore map, with
  storetest conformance; dropped when the instance is gone; not part of
  `at-jam export` (runtime state).
- A snapshot write failure is logged and never fails the delivery.

## 3. Image facts

- Read on the launcher host through the docker context the launcher already
  uses: `docker image inspect` for `Config.Env`, labels and digest.
- Seed: `docker create` (never started), `docker cp <ctr>:/home/agent/.init-agent-data -`
  as a tar stream, `docker rm`. The container never runs.
- Caps: 1 MiB total, ≤ 200 entries; regular files only; text files (valid UTF-8,
  ≤ 64 KiB) shown in full, others by name and size.
- Cached per image digest in memory (bounded LRU); repeated inspects are free.
- Image gone, a placeholder launcher, or a docker error: the section is
  `unavailable` with a fixed reason; inspect continues.
- Commands are built as argv (no shell) through the existing runner, so tests use
  `runner.Fake`.

## 4. Surfaces

**Slice 1 — API and CLI**

- `GET /admin/roles/{project}/{role}/setup` — the would-be setup.
- `GET /admin/instances/{id}/setup` — the delivered setup; `?diff=role` adds,
  per section, a line diff against the role's render (the existing shared
  `linediff`).
- `?sections=a,b` limits the sections on both.
- `at-jam role inspect <project>/<role> [--json] [--section …]` and
  `at-jam studio inspect <studio> [--diff] [--json] [--section …]`.
- Text output: one block per section headed `== <name> (<source>[, stale][, <at>])`;
  context files print in full under their path; tables for egress and
  destinations; diffs as unified line diffs.
- Admin auth as every `/admin/*` route; each call logged
  `setup inspected` (operator, subject, sections) — never the body.

**Slice 2 — UI**

- A **Setup** tab on the studio page and the role page: collapsible sections,
  context files shown rendered (markdown) or raw, a "stale vs role" badge that
  opens the diff. Read-only; escaping through html/template; same admin auth.

## 5. Errors

Inspect never fails as a whole: a section that cannot be produced carries
`source: unavailable` and a fixed reason (`image not present on the launcher
host`, `no snapshot (studio raised before setup recording); recomputed`, …).
An unknown role or instance is 404. No error echoes a secret or unvalidated
repo text.

## 6. Testing

- Golden tests of the text render for a role and a studio.
- Redaction: a fixture full of credentials, tokens and secret-named env vars;
  assert no value appears in the API output, the text render or the stored
  snapshots.
- Snapshot write-on-change (no write on identical refresh), cap/truncation,
  drop on instance gone; Mem/Postgres parity (storetest).
- Image reader: `runner.Fake` asserting the docker argv (never `start`/`run`),
  tar parsing caps, cache by digest, `unavailable` paths.
- Diff: a role edited after raise shows `stale` and the expected line diff.

## Slices

1. **Setup model, snapshots, image reader, API and CLI.**
2. **UI Setup tab.**

## Out of scope

- Probing a running container (`exec`).
- Editing anything from the inspect views.
- Historical snapshots (only the latest per part is kept).
