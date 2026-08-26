---
kind: design-spec
subject: remote teammate — Discord-driven conversational agent + read-only workspace viewer for isolated sandboxes
status: draft
date: 2026-08-26
read-when: implementing or reviewing the workspace viewer (Component B) or the Discord teammate conductor loop (Component A)
---

# Remote teammate: Discord conversation + workspace visibility for isolated sandboxes

## Problem

An isolated (unshared) sandbox — the default `chat` workspace, a backend-managed named
volume — has two friction points that make it awkward to live with:

1. **The interface is an SSH TUI.** `at-cove chat` drops you into `claude` running over an
   `-tt` PTY inside the VM (`internal/connect/transport.go`). It works, but the CLI-in-a-
   sandbox-over-SSH experience is suboptimal for day-to-day driving, and it is single-user:
   teammates cannot see or join the conversation.

2. **You cannot see the workspace.** The isolated workspace is a Docker named volume, not
   exposed on the host filesystem — the operator's *only* window into it is the interactive
   SSH session. There is no way to glance at what the agent changed, watch work-in-progress,
   or review a diff without occupying the same TUI.

We want the isolated sandbox to behave like a **remote teammate**: you and your team
converse with it in Discord (replacing the SSH TUI as the daily interface), and you can
*see* its private workspace through a lightweight read-only web view, dropping to SSH only
to touch a file yourself.

## Vision & scope

Two independent components over the existing SSH transport, plus one additive egress hole.
Delivered as **one design, two plans, B first.**

- **Component B — workspace viewer** (ships first): read-only visibility into an
  isolated-volume workspace — browse the tree, read files, see live uncommitted git state —
  over an `ssh -L` tunnel. Small, independent, immediately useful.
- **Component A — Discord teammate loop**: a standing conversational agent in the sandbox,
  reachable by the operator and teammates in Discord, driven by a supervisor "conductor"
  that polls Discord and re-invokes `claude -p`.

**Explicitly deferred / out of scope:** unifying this with the autonomous `dispatch`
workers (they keep reporting via PR + tracker comment as today); read-write web editing
(break-glass edit is the existing SSH shell / Remote-SSH, not a new write path); a live
Discord gateway websocket (see §A "Polling, not the gateway").

## Boundary posture

The load-bearing hardening claim is unchanged: the agent is still non-root, still cannot
rewrite the sealed layer, and still reaches only allow-listed hosts. This design adds:

| New surface | Where it lives | Boundary impact |
|---|---|---|
| Discord egress (`discord.com`, `gateway.discord.gg`*) | squid allow-list, additive | One consciously-opened, hostname-filtered hole; no TLS interception |
| Bot token | worker/collaborator secret bucket, in-memory tmpfs, never logged; **held by the conductor, never in the agent's session env** | In-sandbox by explicit choice; scope it to one guild/channel, least privilege |
| Workspace visibility (VS Code Remote-SSH + git) | the operator's editor/git, over the **existing** SSH endpoint | No new component, no published port; the VS Code Server is pushed over SFTP (`localServerDownload: always`), so **no egress is added** |
| Break-glass edit | same Remote-SSH connection / `at-cove chat --raw` shell | Reuses the current SSH endpoint; nothing new |

\* `gateway.discord.gg` is only needed if a future variant uses the websocket gateway; the
polling design (§A) reaches only `discord.com`.

**No sealed-file edits.** Workspace visibility rides the existing SSH endpoint with no image
change and no allow-list change (see §B "Two facts"). Discord is a plain additive allow-list
entry. This is a lighter security-review footprint than shadow-dirs, which had to edit a
sealed entrypoint and touch volume ownership.

## Component B — workspace visibility (VS Code Remote-SSH + git)

**One job:** make an isolated-volume workspace observable — browse the tree, read files,
see live *uncommitted* git state, and occasionally edit — by connecting an editor and git
to the sandbox over the existing SSH endpoint, with **no new component in the image.**

### Resolved decision

Of the three candidates carried out of brainstorming (purpose-built Go viewer / vendored
`filebrowser` / no-bespoke-viewer), the **no-bespoke-viewer** path is chosen: lean on **VS
Code Remote-SSH** (tree browse, file view, built-in uncommitted diff, search, and the
occasional edit) plus **git-over-SSH** (committed history). No image service, no new egress,
no read-only server to maintain. "Read-only" becomes a discipline rather than an enforced
property — an accepted trade for zero new build and an already-familiar tool.

### Two facts that make it work through the boundary

1. **No egress needed.** VS Code Remote-SSH normally bootstraps a node-based VS Code Server
   *on the remote host*, which would require allow-listing `update.code.visualstudio.com`,
   `*.vscode-cdn.net`, etc. Instead the operator sets the **client-side** setting
   `remote.SSH.localServerDownload: "always"`, so the server is downloaded on the operator's
   machine and pushed into the sandbox over SFTP. The squid allow-list is **untouched** — the
   boundary stays exactly as audited. (The sandbox already has glibc/Ubuntu 24.04 and an
   sshd with stock SFTP + TCP-forwarding + arbitrary-exec, so the pushed server runs.)

2. **A stable alias despite a rotating port.** The container publishes ssh on an *ephemeral*
   host port (`-p 127.0.0.1::2222`, discovered at connect time via `docker port`), and the
   host key regenerates every boot — so a hard-coded `Host` block would break on every
   `recreate`. The fix is a **`ProxyCommand`**: the generated OpenSSH `Host` block routes
   through `at-cove ssh-proxy <collaborator>`, a small subcommand that resolves the current
   port via the backend `Dial` and relays the byte stream. The alias (`cove-<container>`)
   therefore stays valid permanently; VS Code Remote-SSH and git-over-SSH both ride it. The
   rotating host key is handled by pointing the block at the *same* per-sandbox
   `known_hosts.d/<container>` file at-cove already uses, with `StrictHostKeyChecking
   accept-new`; `recreate` reaps that pin (`doDestroyInstance`), so reconnection re-pins the
   new key without a MITM prompt.

### Shape

- `at-cove view <collaborator>` prints an OpenSSH `Host` block (ProxyCommand-based) plus a
  ready `git remote add sandbox cove-<container>:/home/agent/workspace` line. `--write`
  upserts the block into `~/.ssh/config` as an idempotent, per-alias managed block so it is
  re-runnable and VS Code sees it.
- `at-cove ssh-proxy <collaborator>` is the ProxyCommand target: resolve → `Dial` → TCP
  relay. Diagnostics on stderr only; stdout is the raw ssh channel.
- **Break-glass edit** is the same connection — Remote-SSH edits in place, or `at-cove chat
  --raw` for a shell.

### Testing

Pure functions carry the logic and are hermetically tested: the `Host`-block renderer and
the `~/.ssh/config` managed-block upsert (golden output; insert / replace / idempotent), and
the relay copy loop (in-process localhost socket, no Docker/VM). Command resolution reuses
the existing `loadCurrentInstall`/`instanceFor`/`state.LoadFor` chain, tested via the `run()`
harness with a `runner.Fake`. Any real Remote-SSH round-trip stays behind the `integration`
build tag.

## Component A — Discord teammate loop

**One job:** turn a standing isolated sandbox into a conversational teammate reachable by
the operator and teammates in Discord.

### The conductor (supervisor model)

A new Go daemon — the **conductor** (`cmd/at-teammate`, `internal/teammate`) — runs in the
sandbox. It is a thin supervisor: it owns Discord I/O and the loop control, and hands the
*decision* of what to do next to the agent.

```
loop:
  batch = poll watched channels since last cursor      # each msg tagged {channel, author}
  input = render(batch)   # or "there are no messages"
  result = claude -p --resume <sid> --output-format json  <input>
  post result.messages → Discord   # [{channel, content}, …], conductor holds the token
  advance per-channel cursors
  switch result.action:
    "wait" → poll with backoff until batch non-empty, then re-enter
    "get"  → re-enter immediately (empty batch ok → "there are no messages")
    "exit" → stop
```

Key properties:

- **Batch on entry.** Each turn the agent is handed *all* pending messages at once, every
  message tagged with its **channel** and **author**. It is one `claude --resume` session (a
  merged, tagged inbox), so the agent disambiguates speakers/channels itself. This is what
  makes "communicate to/from teammates" a group conversation rather than a single-user pipe.
- **Agent-driven control.** The agent's JSON return carries `messages` (outbound intents)
  and one `action` ∈ {`exit`, `wait`, `get`}. `wait` sleeps until a human speaks; `get`
  re-enters immediately (e.g. to check for input after finishing a long task); `exit` tears
  down.
- **Token stays in the conductor.** Outbound messages are *intents* (`{channel, content}`);
  the conductor holds the bot token and makes the Discord API calls. The token lives
  in-sandbox (honoring the in-sandbox choice) but the **agent never sees it** and cannot
  exfiltrate it — a tighter residual risk than injecting the token into the agent's env.
- **Serialization is implicit.** One agent process, one turn at a time; the batch is the
  queue. No separate locking.
- **Progress.** `--output-format json`/`stream-json` lets the conductor post a "working…"
  message when a turn starts, since polling gives no native typing indicator.
- **Still has hands.** The teammate keeps `at-task` available; when asked to ship it opens a
  PR through the existing broker, otherwise it works in-workspace and the operator watches
  via Component B.

### Polling, not the gateway

The conductor **polls** Discord's REST API (`GET /channels/{id}/messages?after=<cursor>`)
rather than holding a live `wss://gateway.discord.gg` websocket. Rationale:

- **Fits the egress model.** squid's allow-list is built for HTTPS request/response;
  polling is plain `GET` over the path the wall already handles. A persistent gateway
  socket with heartbeats/resume/intents is exactly the long-lived tunnel squid is least
  happy holding.
- **Maps 1:1 onto the actions.** `wait` = poll-with-backoff until non-empty; `get` = poll
  once. The gateway would push events we'd have to buffer into a batch anyway — polling *is*
  the batch.
- **Hermetically testable.** A fake HTTP endpoint returning canned message pages tests the
  whole loop; no websocket machinery to fake.
- **Latency is a non-issue.** A 2–3s cadence on a handful of watched channels is
  imperceptible for a conversational teammate and inside Discord's rate limits.

The gateway would only earn its complexity for sub-second reactions or presence/voice,
which a working teammate does not need.

**Cursor & thread state:** the conductor persists a per-channel `after` cursor (last-seen
message id) across restarts, and discovers threads via `GET /guilds/{id}/threads/active` if
teammates open them.

### Config & lifecycle

- New kit config on a teammate-class collaborator: `discord: { channel-id,
  bot-token-secret }` plus `allowed-domains: [discord.com]`.
- The conductor is started at sandbox boot for that class; break-glass SSH `chat` still
  jumps in directly.

### Error handling

- A failed/timed-out turn posts the error summary to the originating channel and clears the
  turn (never wedges the loop).
- An egress denial mid-turn surfaces the existing "add host to the kit allow-list" guidance,
  but into Discord.
- Discord API errors (rate limit, transient 5xx) back off and retry the poll; repeated hard
  failure makes the conductor **exit loudly** and log a last gasp the SSH break-glass can
  read — a dead conductor is a silent sandbox, so it must be observable (fail-closed).

### Testing

Split the Discord protocol adapter (an interface: poll → messages, post → ack) from the
loop logic. Test the loop with a fake adapter + the existing `runner.Fake`, asserting: the
`claude -p --resume` argv, batch rendering with channel/author tags, the `exit`/`wait`/`get`
branching, cursor advancement, and that outbound intents become adapter `post` calls (token
never in argv/logs). Real Discord behind the `integration` build tag.

## Cross-cutting

- **Secrets.** The bot token flows through the existing resolver → in-memory tmpfs → env
  path; never disk, argv, or logs. It is the only new secret. Document scoping it to one
  guild/channel with least privilege. Held by the conductor, not the agent (§A).
- **Egress.** One additive allow-list entry (`discord.com`) baked via the teammate class's
  `allowed-domains`. No sealed-file edits.
- **Docs.** Update `docs/OVERVIEW.md` command surface (`view`, teammate creation), add a
  usage doc per component, keep `docs/usage/INDEX.md` in sync — per the repo's "no task done
  until docs updated" rule.
- **Security review.** Lighter than shadow-dirs: no `CAP_*` grant, no sealed-file edit, no
  host bind-mount. The two conscious changes (Discord egress; in-sandbox bot token, now
  confined to the conductor) are called out here so they are a deliberate, reviewed
  tradeoff, not a silent one.

## Implementation order

1. **Plan 1 — Component B (workspace viewer).** Small, independent, immediately useful;
   gives eyes-in before the larger Discord work. Resolves the viewer-realization open
   decision at its writing-plans stage.
2. **Plan 2 — Component A (Discord teammate conductor).**
