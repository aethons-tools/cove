---
summary: How a Jam session learns its context — the layered bundle compiled at raise, its always-on core and on-demand leaves, and how cove-master delivers it.
read_when: You are writing a kit prompt, debugging what a session was told, or changing how session context is compiled or delivered.
owns: session-context layers, delivery order and precedence, core budgets, the /agent-data/context layout, the AT_COVE_AGENT_CONTEXT_FILE handoff
prereqs: coves.md
tier: leaf
updated: 2026-10-02
---

# Session context

At raise Jam compiles a **session context bundle** (`internal/jam/sessionctx`) from
layers, in delivery order:

| Layer | Source | Core budget |
|-------|--------|-------------|
| Boilerplate | built in, per session kind (ephemeral / personal / standing): sandbox, turn model, intercom rules | 2400 B |
| Kit | the studio kit's `prompt` ([kits.md](kits.md)) | 800 B — `kit push` rejects more |

(Studio, Project, Role and Jam layers follow — see the [design spec](../../superpowers/specs/2026-10-02-session-context-layers-design.md).)

The **core** (`CORE.md`) states precedence once — hardening is absolute; otherwise
later layers win — then each non-empty layer's core and a one-line pointer to each of
its leaves. **Leaves** and `INDEX.md` hold the detail. An empty layer emits nothing.

## Delivery

1. `Supervisor.Raise` compiles the bundle; the launch prompt (task, brief, standing or
   personal prompt) stays the `claude -p` message on its own.
2. The launcher stages the bundle JSON at `/dev/shm/cove-agent-context` and exports
   `AT_COVE_AGENT_CONTEXT_FILE`.
3. cove-master writes it to `/agent-data/context/` (built beside it and swapped in;
   nothing from an earlier bundle survives) and runs every turn with
   `--append-system-prompt-file /agent-data/context/CORE.md --system-prompt-snapshot off`.
   `off` matters: by default claude replays the first turn's system prompt on every
   `--continue`.
4. No file, malformed JSON, or an unwritable directory → the agent runs without
   context (warned in `cove-master.log`), never not at all.

The bundle never carries secrets: only names of env keys and routes.
