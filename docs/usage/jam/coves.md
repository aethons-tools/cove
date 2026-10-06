---
summary: The managed-cove supervisor operator guide — Jam's runtime registry of raised studios (Phase/Activity, leases), the `at-jam studio raise|list|status|teardown` verbs, the `runtime:` serve-config block, and the Attach stream + its in-cove `cove-master` client.
read_when: You are raising or tearing down a managed studio through Jam, inspecting the runtime registry, tuning the supervisor's lease/reconcile timing, or configuring/running the in-cove `cove-master` client.
owns: the operator-facing managed-cove runtime story — the Instance registry (Phase vs Activity, leases), the `studio` verbs (formerly `cove`), the `runtime:` serve-config block, the Attach stream, and the `cove-master` client that dials it
prereqs: INDEX.md for the service overview; operators.md for the admin-client flags; roster.md for the role a studio is raised for
tier: leaf
updated: 2026-10-06
---

# Managed studios (the supervisor)

Jam keeps a **runtime registry** of the studios it manages — each a raised
Worker or standing Manager — and drives their lifecycle: raise → record → track
status → tear down, self-healing across Jam restarts. This is the spine the
Requisitioner and standing teammates build on.

> With a [`runtime.launcher`](serve.md#the-launcher-runtimelauncher) configured,
> `studio raise` starts a **real** studio on the Colima backend (see [Raising a real
> managed studio](#raising-a-real-managed-studio)). Without it, `at-jam serve` uses
> a **placeholder launcher** that records a live registry entry but starts no studio
> — useful for exercising the registry/supervisor in dev and tests.

## The model

A managed studio has a durable **identity** (a roster [Actor](roster.md) — token,
grants) and a runtime **Instance** keyed by that actor's id. The Instance carries
two status fields with different owners:

- **Phase** (Jam owns it): `raising → live → terminating → gone`, or
  `→ lost → terminating` when the reconciler finds it dead. A `live` studio
  waiting for a reply can also go `→ idled` — paused (`docker pause`), with
  lease-reaping suspended — and back `→ live` on reply or teardown past
  `wait-max` (or the role's [idle timeout](turn-end.md#idle-timeout)); see [intercom.md](intercom.md#waiting-for-a-reply-wake-on) for
  the wake-on/pause mechanics.
- **Activity** (the studio reports it, only while `live`): `running | holding |
  waiting | blocked | done`. Reporting `done` tells Jam to tear the studio down.
  `holding` means the turn ended but background tasks are still running — see
  [turn-end.md](turn-end.md#holding).

Each Instance is **leased** to the Jam process supervising it. A lease has a
TTL; the owner renews it, and if it expires another process may take over
(reconnect/failover). On restart, Jam re-adopts live Instances from the store
instead of abandoning them — so in-progress work survives a restart.

## The `studio` verbs

```
at-jam studio raise    --id spider-42 --role guest [--project acme] [--unit AET-9] [--prompt-file task.md]
at-jam studio list     # id  name  role  unit  phase  activity  lease-holder  connector  image
at-jam studio status   --id spider-42 --activity waiting
at-jam studio teardown --id spider-42
```

- `studio raise` starts a **new session** (a minted `ses_…` id, printed) and
  enrolls its identity (the role must exist — fail-closed), recording a `live`
  Instance. `--id` is the session's **label**: unique among live sessions (409
  otherwise), it addresses the session in `status`/`teardown` (as does its id),
  and is free again once the session ends — a later raise under it is a new
  session that inherits nothing. The role supplies scope, exactly as with
  [enroll](roster.md). `--prompt-file` supplies the workload prompt (read
  host-side, never on argv) — required by the real launcher; see below.
- `studio status` reports the studio's activity; `--activity done` triggers teardown.
- `studio teardown` tears the studio down and revokes its identity (idempotent).
- `connector` (in `studio list` and the Studios table) is `ok` when the studio's latest agent episode (agent process) started with its role's current connector, `stale` when a destination or grant changed since (it refreshes at the next episode — a wake delivered into a live episode does not refresh it), `unknown` when it never reported (an image built before the per-spawn refresh — re-raise it), `error` when Jam cannot compute the current connector to compare against (the studio's actor is no longer on the roster, or its role's destinations conflict). Because the git route is part of the fingerprint, a studio whose git-config rewrite failed reports the route actually in effect, so it shows `stale` until the rewrite lands on a later episode.
- `image` (in `studio list`, [`standing list`](standing-sessions.md#declaring-one), the Studios table and the role page's standing rows) is `ok` when the studio runs the image a raise for its role would run now, and `stale` when that image has changed since the raise. The image changes when the role's kit changes a build-affecting field, its [model-spec](model-specs.md) harness changes (CLI version, plugins), Jam's own build inputs change (a new at-jam binary, Jam host or launcher key — the launcher's assembly fingerprint), or the role stops raising a kit at all. Each Instance records the tag it was raised on (`image_tag`) and its kit ref (`kit`); the current tag is resolved by the same code a raise uses, cached per kit version and harness. Without both tags (a studio raised before tags were recorded, or a launcher that cannot name images) Jam compares the recorded kit: a different kit or build digest is `stale`, the same one `unknown`. It is also `unknown` when the role's current kit or model-spec does not resolve; the listing never fails on it, and the reason is logged at debug level (once per role per minute). Terminating, lost and gone studios show `-`. A stale studio keeps its old image until it is torn down and raised again; a standing one is moved to the current image with [`standing upgrade`](standing-sessions.md#upgrading-a-standing-session). `GET /admin/coves?project=&role=` lists one role's studios.

### Raising a real managed studio

With a [`runtime.launcher`](serve.md#the-launcher-runtimelauncher) configured,
`studio raise --prompt-file <f>` runs the whole lifecycle end to end: Jam starts a
Colima studio from the configured image, injects the agent connector — the client env
and git routing declared by the role's destinations ([connector.md](connector.md)) — plus the cove-master env, your prompt and the compiled [session context](session-context.md) over SSH, and starts
`cove-master`, which runs `claude -p` on the prompt. The studio reports
`running` → `waiting` over the [Attach stream](#the-attach-stream) after each turn,
and the supervisor tears it down when Jam ends the session ([turn-end.md](turn-end.md)). The **role must grant the `anthropic` and `git`
destinations** for the agent to reach them. This validates the managed-cove
lifecycle; automated result-handling (commit/push/PR after the agent) comes with the
Requisitioner. Without a `runtime.launcher`, `raise` records a placeholder Instance only
(no real studio).

> **Anthropic credentialing.** How the agent's `claude` authenticates follows the
> anthropic destination: identity-in `x-api-key` gives `ANTHROPIC_API_KEY`, and the
> [`pool:`](pool.md) configuration (identity-in `bearer`) gives `ANTHROPIC_AUTH_TOKEN`
> (a static bearer, no credentials file) and the broker swaps in a pooled
> subscription token. The connector carries the mode; the cove is unaware.

The raise sequence is: enroll the identity → assemble its connector (a conflict among the role's destinations fails the raise and revokes the identity) → start the container → wait for sshd →
**apply the role's egress policy** → start cove-master (and so the agent). The egress
step runs only for a role with a [policy](roster.md#role-egress) and lands before the
agent exists. It runs the sealed in-box helper as root, which enforces the kit's
ceiling. It **fails closed**: if the backend can't apply a policy, or the box refuses
a domain outside the ceiling, the raise fails (the error names the domain), the
container is removed and the identity revoked. It never falls back to the wider kit
default. A role with no policy skips the step and keeps the kit's list.

### The StudioKit and its kit-prepare protocol

The real launcher raises from a **[StudioKit](kits.md#the-studiokit)** — the
role's named kit, or the seeded `default` ([kits.md](kits.md#role-to-kit-and-the-default-kit)).
Its egress ceiling excludes Anthropic (COV-208). It is built on demand via a
**light-reference / lazy-prepare** handshake that tolerates a remote-building
substrate: each raise carries only the kit reference (`<id>@v<n>`); on an inventory
**miss** the launcher raises no container and reports not-ready; Jam resolves the
full kit from the [registry](kits.md), calls the launcher's `PrepareKit` to build it
**on the substrate backend** (the colima daemon, so the image lands where
`RunEphemeral` runs it) and retries. The image is tagged by the kit's build-digest
plus the launcher's assembly fingerprint ([kits.md](kits.md#the-studiokit)),
so a prompt-only edit reuses the cached image while a Jam upgrade rebuilds it. An in-progress build does not block
— the raise defers to the next reconcile tick. The build context is **data** (the
kit plus resources compiled into the `at-jam` binary and the launcher's key), with
**no source kit directory**, so the build can move to a remote substrate. The
[session context](session-context.md) is compiled at raise, outside the image. A Dockerfile-context base
is not yet buildable (deferred).

### Egress drift

Each Instance records the [role egress policy](roster.md#role-egress) it runs under
(`egress`). Each reconcile pass re-applies the role's current policy to a `live`
studio whose lease it holds when the two differ (`--kit-default` for a cleared one);
`idled` studios are skipped and get it on **resume**, before they are `live` again.
Failures count in `egress_failures`; the teardown rule is in
[roster.md](roster.md#role-egress). Logs carry domain counts, never lists.

Managed studios are also raised **automatically** by the [resident
Requisitioner](requisitioner.md) — an always-on loop that polls a tracker and raises one
per ready ticket — not only by this manual `studio raise` verb.

A raised studio's agent also gets a brokered [intercom MCP](intercom.md) — `read`/`send`
on its own ticket — so it can converse (ask, leave a status) on the ticket it's working.
A studio is not one-shot: after each turn it **waits** (Activity `waiting`) and Jam
**wakes** it to resume (`claude --continue`) when a reply lands, an alarm fires, or its
idle timeout passes — see [turn-end.md](turn-end.md).

All `studio` verbs take the admin-client flags (`--app`/`--admin-url`/`--token`);
see [operators.md](operators.md).

## Tuning the supervisor (`runtime:`)

Optional serve-config block (see [serve.md](serve.md) for the whole config):

```yaml
runtime:
  lease-ttl: 60s            # how long a lease is valid without renewal
  reconcile-interval: 30s   # reconcile + renew cadence (must be < lease-ttl)
```

Defaults are `60s` / `30s`. `reconcile-interval` must be strictly less than
`lease-ttl` so a live owner always renews before its own lease expires.

Design rationale (the identity/runtime split, the lease/steal model, the
reconciler) lives in
[`../../superpowers/specs/2026-09-12-harbor-cove-supervisor.md`](../../superpowers/specs/2026-09-12-harbor-cove-supervisor.md).

## The Attach stream

A managed studio holds one bidirectional gRPC stream to Jam — its **Attach**
stream. It authenticates the stream with two credentials: its actor **identity
token** (the same token the broker checks) **and** its **per-instance launch
secret**, minted at `raise` time. Over the stream the studio sends Activity
reports up, heartbeats to renew its lease, and the fingerprint of the connector its
latest agent turn applied (see [cove-master](#cove-master-the-in-cove-client)); Jam pushes lifecycle
**control** down — teardown or wake. Jam serves the Attach gRPC on its
cove-facing **:443 TLS** endpoint, multiplexed with the broker by `content-type`
(`application/grpc`) — so the stream fits within a hardened studio's 443-only
egress, no separate port required (see [serve.md](serve.md)).
The stream also carries the agent's session events up and their acks down; see
[session-events.md](session-events.md).

## cove-master (the in-cove client)

`cove-master` is the studio's side of the Attach stream: the `internal/covemaster`
client library plus the `cove-master` binary (`cmd/cove-master`). It dials
`jam.host:443` over **TLS** — reaching it through the studio's squid `CONNECT`
proxy (grpc-go's built-in dialer honors the studio's `https_proxy`; Jam's cert
is validated against the system trust store) — authenticates the stream, reports
Activity up, and reacts to control (teardown, wake) pushed down — reconnecting
with backoff across transient drops. The client imports only the generated
`attachpb` types and grpc, never `internal/jam`, so it stays a lean,
server-free dependency for whatever process embeds it.

It reads its configuration from the environment (no SSH, no host
orchestration):

```
AT_JAM_RUNTIME_ADDR       Jam's cove-facing Attach address, jam.host:443 (TLS, via the cove's proxy)
AT_JAM_IDENTITY_TOKEN     the cove's identity token
AT_JAM_LAUNCH_SECRET      the per-instance launch secret, minted at raise time
AT_COVE_WORKDIR           the agent's cwd (default /home/agent/workspace)
AT_COVE_AGENT_PROMPT_FILE path to the file holding the agent's prompt (required)
AT_COVE_AGENT_CONTEXT_FILE path to the compiled session context JSON (optional; see session-context.md)
AT_COVE_RESIDENT          "1"/"true" → resident mode (set by the launcher for personal and standing sessions only)
AT_COVE_SESSION_KIND      ephemeral|personal|standing — picks the resume prompt (standing sessions have no owner)
AT_JAM_BASE_URL           https://<jam host>; when set, the agent's connector is re-fetched (GET /connector) before every spawn, and — with a context file — the session context (GET /context) before every later spawn and wake
AT_JAM_CONNECTOR          the raise-time connector (JSON, no token): fallback + owned env keys
```

Each `AT_JAM_*` variable falls back to its pre-rename name, which the launcher
also sets for older images — see [renamed-from-harbor.md](renamed-from-harbor.md).

cove-master runs the agent headless in **episodes** (`internal/agentrun`).
Everything specific to the agent CLI — argv, stdin encoding, parsing stdout
into turn/background events, the pre-flight check — sits behind agentrun's
`Harness` interface; the only implementation, and the default, is Claude. An
episode is one `claude -p --input-format stream-json --output-format stream-json
--verbose --dangerously-skip-permissions` process in `AT_COVE_WORKDIR` (the
permission flags come from the role's [model-spec policy](model-spec-policy.md);
this is `claude-default`'s; plus
`--append-system-prompt-file /agent-data/context/CORE.md --system-prompt-snapshot off`
when a [session context](session-context.md) is in effect); the prompt
is its first stdin message. cove-master reports `running` and watches the
stream-json output: it closes stdin only when the agent's turn has ended **and**
no background task (`run_in_background` Bash, background subagents, Monitors) is
outstanding, so backgrounding works. A turn that ends with tasks still running
holds stdin open for at most `BackgroundWait` (30m), then closes it and claude
stops the stragglers (logged at WARN). When the process exits, its turn is over — no result file is read on the Jam path. Every
episode also passes `--mcp-config /dev/shm/cove-agent-mcp.json --strict-mcp-config`:
before **every** spawn the harness **(re)generates** that one config — the guaranteed
`messaging` server plus the kit's [`mcp-servers`](kits.md#mcp-servers-cov-240) —
and the spec's `--settings` file, since the agent shares the cove's filesystem and may
have deleted them (a missing `--settings` file fails every later `claude` at startup);
it **fails loud** if it can't (the baked kit file is missing — a stale image —
or invalid, or the config can't be written) rather than launch a silently
toolless agent (COV-190). The same pre-flight checks `claude --version` against
the role's [model-spec](model-specs.md#what-a-cove-applies) (`claude-default` adds
no flags, so its coves launch with exactly the argv above). With the config written it proceeds:

Every episode then ends the same way, for every session kind: the client reports `waiting` and blocks on a **wake**, which resumes the agent with `claude --continue` and a prompt naming [why it woke](turn-end.md#wake-reasons). A turn that **exited non-zero** (a crashed or auth/model-failed `claude`) is logged at **WARN** and still waits — the cause is in the agent's own `cove-master.log` (stderr) or `agent-stream.jsonl` (stdout). The session ends only when Jam tears it down: [`end`](turn-end.md#ending-a-session), the role's [idle timeout](turn-end.md#idle-timeout), `wait-max`, or a resident session's release/removal.

A Jam **teardown** cancels the run, which sends the agent `SIGTERM` and then
`SIGKILL` after a grace period. A `wake` that arrives while an episode is live goes **into** it: between turns it
is written to stdin as the resume prompt at once; mid-turn, any number of wakes are
coalesced into **one** resume prompt written when the turn ends. A wake that is still
owed when the process exits — coalesced but undelivered, or written but not yet
acted on (the write failed, or the agent exited before starting that turn) — is
kept, so the next wait resumes at once.

**Connector refresh.** Before every episode (agent spawn) — the first, and each resume after the process exited —
cove-master re-fetches its connector (`GET /connector`, [connector.md](connector.md))
and starts that episode with the current env and git routing, so a destination or grant
edit reaches a running studio at its next episode (never within one: a wake delivered into a live episode runs under the env that episode started with). If the fetch fails it
keeps the last connector it applied and logs a warning; a failed git-route rewrite is likewise logged and retried every episode until it lands. It reports the applied
connector's fingerprint up the Attach stream; Jam compares it to the role's current
connector for the `connector` column ([verbs](#the-studio-verbs)).
The connector also carries the role's resolved [model-spec](model-specs.md#what-a-cove-applies):
an episode whose spec changed is re-checked by the harness first (CLI version; a
mismatch fails the run loud), then launched with the spec's model, effort,
provider env and settings — a spec edit reaches a studio at its next episode too.

**Context refresh.** Beside the connector, cove-master re-fetches the session
context (`GET /context`) before every later episode *and* before each wake it writes
into a live episode, swapping `/agent-data/context` and telling the agent which
layers changed — see [session-context.md](session-context.md#refresh).

**Post-mortem on teardown.** Just before the container is removed (only a
standing session's state volumes outlive it), the launcher grabs the **tail of `cove-master`'s log**
(`/agent-data/cove-master.log`, cove-master's own log plus `claude`'s stderr; the agent's stdout stream lives in `/agent-data/agent-stream.jsonl`, which is never read) over SSH and
records it at `WARN` (`cove agent log (tail, captured on teardown)`, keyed by
`id`). So a cove that died — a crash, a `claude` auth failure, an egress-blocked
model call, or a one-shot exit from a stale image — leaves its reason in Jam's
log instead of vanishing with the container. It is strictly best-effort: an
unreachable cove or a missing log never blocks the teardown. An **idled** cove is
`docker pause`d — SSH into a frozen container hangs — so teardown **unpauses it
first** (idempotently) before the capture and removal. `Pause`/`Unpause` are
idempotent (pausing an already-paused cove, or unpausing a running one, is a
no-op success), so the idle ladder never fails a reconcile on a cove it already
paused.

**Resident mode (personal and standing sessions).** `AT_COVE_RESIDENT=1` — which the launcher
sets only for a [personal](personal-sessions.md) or [standing](standing-sessions.md) session — now only picks the
resume prompt (every session waits after every episode, above). While an episode is still live (its turn over but a
background task outstanding — [`holding`](turn-end.md#holding)), a wake is written into
it instead — see the wake paragraph above. A resident session ends only when a teardown cancels the
run: the owner's release for a personal session, or the name's removal for a
standing one. Jam's wake-on engine never tears a resident session down for
`wait-max`; only a personal session's optional [idle-ladder reclaim](personal-sessions.md#the-idle-ladder) does.
A restarted standing session resumes its conversation; others start fresh
([standing-state.md](standing-state.md)).

> **Still deferred:** cove-master becoming the image entrypoint under its own non-root account
> (collapsing the SSH/systemd boot).

Design rationale (the package boundary, the Workload seam, the reconnect model,
and the agent wrapper's lifecycle mapping) lives in
[`../../superpowers/specs/2026-09-13-cove-master-client.md`](../../superpowers/specs/2026-09-13-cove-master-client.md)
and [`../../superpowers/specs/2026-09-13-cove-agent-wrapper.md`](../../superpowers/specs/2026-09-13-cove-agent-wrapper.md).
