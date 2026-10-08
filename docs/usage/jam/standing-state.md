---
summary: A standing session's persisted state — the labeled Docker volumes (`-agent-data`, `-workspace`) that outlive its studio (the `-docker` cache is the studio's), how a restarted session resumes its conversation (`.cove-conversation`, `claude --continue`), and which operations delete the state (only the reconciler's sweep and reset).
read_when: You need to know what survives a standing session's restart or upgrade, where its conversation and workspace live, why a restarted session did or didn't resume, or which volumes Jam may delete.
owns: a standing session's state volumes (names, mounts, labels, the per-Jam sweep scope), conversation resume on restart, and the rule that no teardown deletes state
prereqs: standing-sessions.md for declarations, reset and dismissal
tier: leaf
updated: 2026-10-06
---

# Standing session state

A standing studio's conversation and workspace live in named Docker volumes
that outlive the container, so a restart (a crash, a Jam restart, an
[upgrade](standing-sessions.md#upgrading-a-standing-session)) resumes where it
left off. Only the standing reconciler deletes them.

A standing studio mounts two named Docker volumes, named after its container
(`atcove-cove-<session id>`) like an `at-cove create` sandbox's:
`<container>-agent-data` at `/agent-data` (the agent's `CLAUDE_CONFIG_DIR`: its
conversations, settings, logs) and `<container>-workspace` at the agent's working
directory (`/home/agent/workspace` by default). They are the **session's**. A
`docker: true` launcher's `<container>-docker` cache at `/var/lib/docker` is the
**studio's**: every teardown removes it, so a session's new studio (restart,
upgrade) starts with a clean Docker store. Jam creates the session's volumes
before the container, labeled `harbor.cove.state=<session id>` and
`harbor.cove.jam=<this Jam's runtime address>`; a Jam only ever sweeps volumes
carrying its own address, so two Jams sharing a docker host can't purge each
other's (changing the runtime address orphans the old volumes rather than
deleting them). The container runs with `--rm`,
which removes only anonymous volumes, so these outlive it and re-attach when the
session is set up again in a new studio (same session id). The image's entrypoint seeds
`/agent-data` only once (its `.seeded` guard) and refreshes what the image's
`.refresh` lists; a fresh workspace volume comes up agent-owned (Docker copies
the image's agent-owned directory into an empty volume). The agent's stream log
there (`agent-stream.jsonl`) is rotated to `.1` at start once over 16 MiB.

Once the agent first replies, cove-master writes `/agent-data/.cove-conversation`.
When a restarted standing session finds that marker, its first episode runs
`claude --continue` and, instead of the declared prompt (which the conversation
already holds), gets a restart notice: it was restarted, its conversation and
workspace are intact, `read` the intercom for what arrived meanwhile, then
continue. If that resumed episode fails quickly without the agent replying (no
conversation to continue), cove-master drops the marker and retries once, fresh,
with the declared prompt. Without the marker it starts fresh.

No teardown deletes the session's state — a dead or Lost studio, a Jam restart, a
`standing upgrade`, an idle reap, a hand `studio teardown` all keep it. Only the
reconciler does, by the declarations: every pass it removes the labeled volumes of
any session no longer a declaration's (see [Dismissal](standing-sessions.md#dismissal)), and [Reset](standing-sessions.md#reset)
removes a declared one's. Ephemeral and [personal](personal-sessions.md)
sessions mount no volumes and always start fresh.
