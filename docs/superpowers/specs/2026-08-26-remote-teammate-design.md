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
| Read-only workspace viewer | process bound to `127.0.0.1:<port>` **inside** the VM | Reached only through an authenticated `ssh -L` tunnel; no published port, no egress, no write path |
| Break-glass edit | existing `at-cove chat --raw` shell / VS Code Remote-SSH | Reuses the current SSH endpoint; nothing new |

\* `gateway.discord.gg` is only needed if a future variant uses the websocket gateway; the
polling design (§A) reaches only `discord.com`.

**No sealed-file edits.** The viewer tunnel forwards to VM-loopback, already permitted by
the nftables loopback-accept rule (the `forward`-chain drop governs docker-in-sandbox
traffic, not sshd's own `-L` tunnels). Discord is a plain additive allow-list entry. This
is a lighter security-review footprint than shadow-dirs, which had to edit a sealed
entrypoint and touch volume ownership.

## Component B — workspace viewer

**One job:** make an isolated-volume workspace observable — browse the tree, read files,
and see live *uncommitted* git state — read-only, over SSH.

### Shape

- A viewer service in the VM, bound to `127.0.0.1:<vmport>`, running as the `agent` user
  (read access to `/home/agent/workspace`). **No write endpoints exist** — read-only by
  construction, not configuration.
- A new host command, `at-cove view <container>`, opens
  `ssh -L <localport>:127.0.0.1:<vmport>` (extending `internal/sshargs` with a local-forward
  builder) and prints/opens the local URL. Because the sandbox publishes only `:2222`, the
  viewer is never exposed except through the operator's authenticated tunnel.
- **Git surfacing, two tiers:**
  - The viewer renders `git status` / `git diff` (uncommitted) / `git log` for the glance
    case (work-in-progress you cannot get from a remote).
  - `at-cove view --git-remote` prints a ready `ssh://…` remote URL so the operator can
    `git fetch` the sandbox workspace and diff with their own tools (committed history).
- **Break-glass edit:** documented, not built — `at-cove chat --raw` for a shell, or a
  VS Code Remote-SSH host entry pointing at the same SSH endpoint. "Occasional edit" needs
  no new write path.
- **Lifecycle:** the viewer is a service in the image, safe to leave always-on (read-only +
  loopback-bound); `at-cove view` ensures it is up and tunnels in.

### Open decision (resolve at B's plan)

How the viewer is realized. Three candidates carried into writing-plans:

1. **Purpose-built tiny Go read-only server** (provisional lean): a small service in this
   repo (like `at-task`) serving tree + file contents + git status/diff/log. Zero new
   runtime deps, no runtime egress, fits the sealed-image + hermetic-test model. More code
   to write and maintain; plainer UI.
2. **Vendored `filebrowser`-style binary** installed in the image, read-only mode. Richer UI
   immediately, less code; an external dependency to pin/update, larger image, no native git
   (add git separately).
3. **No bespoke viewer — lean on VS Code Remote-SSH + `git` over the existing SSH endpoint.**
   No new component at all: Remote-SSH gives tree browse, file view, built-in uncommitted
   diff, and search; git-over-SSH covers history. Cost: no always-on "glanceable web tab,"
   and it is read-write (read-only is a discipline, not enforced).

### Testing

Read-only HTTP handlers + git-command wrappers tested against a temporary repo, hermetic;
the `-L` argv addition unit-tested in `sshargs`; any real-ssh path behind the `integration`
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
