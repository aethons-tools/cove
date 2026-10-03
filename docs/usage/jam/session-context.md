---
summary: How a Jam session learns its context — the layered bundle compiled at raise and refreshed while it runs (GET /context), its always-on core and on-demand leaves, and how cove-master delivers it.
read_when: You are writing a kit prompt, debugging what a session was told (or why an edit did or did not reach it), or changing how session context is compiled, delivered or refreshed.
owns: session-context layers, delivery order and precedence, core budgets, the /agent-data/context layout, the AT_COVE_AGENT_CONTEXT_FILE handoff, GET /context and the refresh notices
prereqs: coves.md
tier: leaf
updated: 2026-10-03
---

# Session context

At raise Jam compiles a **session context bundle** (`internal/jam/sessionctx`) from
layers, in delivery order:

| Layer | Source | Core budget |
|-------|--------|-------------|
| Boilerplate | built in, per session kind (ephemeral / personal / standing): sandbox, turn model, intercom rules (the default recipient, if any), and for ephemeral sessions the `worker-result.json` contract | 2400 B |
| Kit | the studio kit's `prompt` as the core, its `notes` as leaves, and a generated `kit/tools.md` from its `build-args` ([kits.md](kits.md)) | 800 B — `kit push` rejects more; a kit stored before the budget is truncated at raise, its full text kept as `kit/CORE-full.md` |
| Studio | generated at raise: granted destinations (upstream, env keys, git routing, each destination's [note](connector.md#notes-for-sessions)), effective egress (the role's policy, else the kit ceiling), message targets | 1600 B — never truncated: long lists move to `studio/destinations.md`, `egress.md`, `targets.md` |
| Project | authored: the project's goals and [resources](session-context-authoring.md) | 1200 B |
| Role | authored: rules for the role | 1200 B |
| Jam | authored: Jam-wide standing rules | 800 B |

Every layer follows edits while a session runs ([Refresh](#refresh)). The kit layer
keeps the image the session was raised with — its build-args (`kit/tools.md`) and
egress ceiling — while a newer kit version's `prompt` and `notes` do reach it; a
new image needs a re-raise.

Raise logs a warning when the kit prompt restates an egress host or a message
target — those belong to the Studio layer.

The authored layers are written with `at-jam context` — see [session-context-authoring.md](session-context-authoring.md). Design: [the spec](../../superpowers/specs/2026-10-02-session-context-layers-design.md).

The **core** (`CORE.md`) states precedence once — hardening is absolute; otherwise
later layers win — then each non-empty layer's core and a one-line pointer to each of
its leaves. **Leaves** and `INDEX.md` hold the detail. An empty layer emits nothing.

## Delivery

1. `Supervisor.Raise` compiles the bundle; the launch prompt (task, brief, standing or
   personal prompt) stays the agent's first stdin message on its own.
2. The launcher stages the bundle JSON at `/dev/shm/cove-agent-context` and exports
   `AT_COVE_AGENT_CONTEXT_FILE`.
3. cove-master writes it to `/agent-data/context.d/<first 12 hex of the fingerprint>/` and points the
   symlink `/agent-data/context` at it in one rename (older versions are then
   removed, so a reader always sees one complete tree), and starts every episode
   (claude process) with
   `--append-system-prompt-file /agent-data/context/CORE.md --system-prompt-snapshot off`.
   `off` matters: by default claude replays the first turn's system prompt on every
   `--continue`. Within one episode the system prompt is fixed; see [coves.md](coves.md).
4. No file or malformed JSON → the agent runs without context, exactly as before
   (`cove-master.log` then has no `session context applied` line). An unwritable
   directory is warned in `cove-master.log` and also runs without it. Either way any
   earlier bundle in `/agent-data/context/` is removed, so a stale `CORE.md` never
   makes `SANDBOX.md` treat the session as a Jam one.

## Refresh

Jam serves a running session's bundle, recompiled from the current config, at
`GET /context` (the identity bearer, like `/connector`; 401 for an unknown
identity, 404 without a registered instance). cove-master fetches it:

- **before every later episode** — that episode's system prompt is current, and its
  first message gains: *Session context changed (role, studio) since your last turn —
  your system prompt is current; re-open any leaf you rely on.*
- **before every wake it writes into a live episode** — the files are current but the
  system prompt is fixed for the episode, so the wake text gains: *Session context
  changed (role) — re-read /agent-data/context/CORE.md now; your system prompt
  catches up at your next episode.* A burst of coalesced wakes carries it once.

Only changed layers are named (a leaf edit counts). An unreachable or older Jam (no
`/context`) keeps the last bundle, logged in `cove-master.log`, never failing the
episode.

The bundle never carries secrets: only names of env keys and routes.
