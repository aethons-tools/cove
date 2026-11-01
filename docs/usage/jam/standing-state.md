---
summary: A session's persisted state (every kind since uniform sessions; the file keeps its standing-era name) — the labeled Docker volumes (`-agent-data`, `-workspace`) that outlive its studio (the `-docker` cache is the studio's), how a restarted or recovered session resumes its conversation (`.cove-conversation`, `claude --continue`), and which operations delete the state (only the sweep, once the session is gone, and a standing reset).
read_when: You need to know what survives a session's restart, recovery or upgrade, where its conversation and workspace live, why a restarted session did or didn't resume, or which volumes Jam may delete.
owns: a session's state volumes (names, mounts, labels, the per-Jam sweep scope), conversation resume on restart, and the rule that no teardown deletes state
prereqs: standing-sessions.md for declarations, reset and dismissal
tier: leaf
updated: 2026-10-26
---

# Session state

Every session's conversation and workspace live in named Docker volumes that
outlive the container, so a restart (a standing session's re-raise or
[upgrade](standing-sessions.md#upgrading-a-standing-session), or any session's
[recovery](coves.md#recovery) after its studio died) resumes where it left off.
Only the standing reconciler's sweep deletes them, once the session is gone.

A studio mounts two named Docker volumes, named after its container
(`jam-studio-<session id>`):
`<container>-agent-data` at `/agent-data` (the agent's `CLAUDE_CONFIG_DIR`: its
conversations, settings, logs) and `<container>-workspace` at the agent's working
directory (`/home/agent/workspace` by default). They are the **session's**. A
`docker: true` launcher's `<container>-docker` cache at `/var/lib/docker` is the
**studio's**: every teardown removes it, so a session's new studio (restart,
upgrade) starts with a clean Docker store. Jam creates the session's volumes
before the container, labeled `jam.studio.state=<session id>` and
`jam.studio.jam=<this Jam's runtime address>`; a Jam only ever sweeps volumes
carrying its own address, so two Jams sharing a docker host can't purge each
other's (changing the runtime address orphans the old volumes rather than
deleting them). Pre-rename volumes are moved at serve start
([renamed-from-at.md](renamed-from-at.md)). The container runs with `--rm`,
which removes only anonymous volumes, so these outlive it and re-attach when the
session is set up again in a new studio (same session id). The image's entrypoint seeds
`/agent-data` only once (its `.seeded` guard) and refreshes what the image's
`.refresh` lists; a fresh workspace volume comes up agent-owned (Docker copies
the image's agent-owned directory into an empty volume). The agent's stream log
there (`agent-stream.jsonl`) is rotated to `.1` at start once over 16 MiB.

Once the agent first replies, jam-studio-agent writes `/agent-data/.cove-conversation`.
When a restarted session finds that marker, its first episode runs
`claude --continue` and, instead of the declared prompt (which the conversation
already holds), gets a restart notice: it was restarted, its conversation and
workspace are intact, `read` the intercom for what arrived meanwhile, then
continue. If that resumed episode fails quickly without the agent replying (no
conversation to continue), jam-studio-agent drops the marker and retries once, fresh,
with its raise prompt. Without the marker it starts fresh.

No teardown deletes the session's state — a dead or Lost studio, a Jam restart, a
`standing upgrade`, an idle reap, a hand `studio teardown` all keep it. Only the
standing reconciler's sweep does: every pass it removes the labeled volumes of
any session that has no instance any more (ended — a recovering session keeps
its instance and so its state) and is no standing declaration's (see
[Dismissal](standing-sessions.md#dismissal)), and [Reset](standing-sessions.md#reset)
removes a declared one's. Every kind mounts the volumes: ephemeral (ticket) and
[personal](personal-sessions.md) sessions too.
