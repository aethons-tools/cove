---
summary: The managed-cove supervisor operator guide — Jam's runtime registry of raised studios (Phase/Activity, leases), the `at-jam studio raise|list|status|teardown` verbs, the `runtime:` serve-config block, and the Attach stream + its in-cove `cove-master` client.
read_when: You are raising or tearing down a managed studio through Jam, inspecting the runtime registry, tuning the supervisor's lease/reconcile timing, or configuring/running the in-cove `cove-master` client.
owns: the operator-facing managed-cove runtime story — the Instance registry (Phase vs Activity, leases), the `studio` verbs (formerly `cove`), the `runtime:` serve-config block, the Attach stream, and the `cove-master` client that dials it
prereqs: INDEX.md for the service overview; operators.md for the admin-client flags; roster.md for the role a studio is raised for
tier: leaf
updated: 2026-10-02
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
  `wait-max`; see [intercom.md](intercom.md#waiting-for-a-reply-wake-on) for
  the wake-on/pause mechanics.
- **Activity** (the studio reports it, only while `live`): `running | waiting |
  blocked | done`. Reporting `done` tells Jam to tear the studio down.

Each Instance is **leased** to the Jam process supervising it. A lease has a
TTL; the owner renews it, and if it expires another process may take over
(reconnect/failover). On restart, Jam re-adopts live Instances from the store
instead of abandoning them — so in-progress work survives a restart.

## The `studio` verbs

```
at-jam studio raise    --id spider-42 --role guest [--project acme] [--unit AET-9] [--prompt-file task.md]
at-jam studio list     # id  role  unit  phase  activity  lease-holder
at-jam studio status   --id spider-42 --activity waiting
at-jam studio teardown --id spider-42
```

- `studio raise` enrolls the identity (the role must exist — fail-closed) and
  records a `live` Instance. The role supplies scope, exactly as with
  [enroll](roster.md). `--prompt-file` supplies the workload prompt (read
  host-side, never on argv) — required by the real launcher; see below.
- `studio status` reports the studio's activity; `--activity done` triggers teardown.
- `studio teardown` tears the studio down and revokes its identity (idempotent).

### Raising a real managed studio

With a [`runtime.launcher`](serve.md#the-launcher-runtimelauncher) configured,
`studio raise --prompt-file <f>` runs the whole lifecycle end to end: Jam starts a
Colima studio from the configured image, injects the agent connector (Anthropic + git
through Jam) plus the cove-master env + your prompt over SSH, and starts
`cove-master`, which runs `claude -p` on the prompt. The studio reports
`running` → `done` over the [Attach stream](#the-attach-stream), and the supervisor
tears it down when the agent finishes (or on `error`/`needs-input`, which report a
brief `waiting` first). The **role must grant the `anthropic` and `git`
destinations** for the agent to reach them. This validates the managed-cove
lifecycle; automated result-handling (commit/push/PR after the agent) comes with the
Requisitioner. Without a `runtime.launcher`, `raise` records a placeholder Instance only
(no real studio).

> **Anthropic credentialing.** How the agent's `claude` authenticates depends on
> the serve config: by default the connector sets `ANTHROPIC_API_KEY` (the identity
> on `x-api-key`), but with a [`pool:`](pool.md) block the connector sets
> `ANTHROPIC_AUTH_TOKEN` instead (the identity as a static bearer, no credentials
> file), and the broker swaps in a pooled subscription token. The launcher chooses
> the mode; the cove is unaware.

The raise sequence is: enroll the identity → start the container → wait for sshd →
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
`RunEphemeral` runs it) and retries. The image is tagged by the kit's build-digest,
so a prompt-only edit reuses the cached image. An in-progress build does not block
— the raise defers to the next reconcile tick. The build context is **data** (the
kit plus resources compiled into the `at-jam` binary and the launcher's key), with
**no source kit directory**, so the build can move to a remote substrate. The
session prompt is composed at raise, outside the image. A Dockerfile-context base
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
A studio is no longer strictly one-shot: on `needs-input` it **suspends** (Activity `waiting`)
and Jam **wakes** it to resume (`claude --continue`) when a reply lands on its ticket,
bounded by `wait-max` — see [intercom.md](intercom.md#waiting-for-a-reply-wake-on).

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
reports up and heartbeats to renew its lease; Jam pushes lifecycle
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
AT_COVE_WORKDIR           the agent's cwd + where .at-task/worker-result.json is read (default /home/agent/workspace)
AT_COVE_AGENT_PROMPT_FILE path to the file holding the agent's prompt (required)
AT_COVE_RESIDENT          "1"/"true" → resident mode (set by the launcher for personal and standing sessions only)
```

Each `AT_JAM_*` variable falls back to its pre-rename name, which the launcher
also sets for older images — see [renamed-from-harbor.md](renamed-from-harbor.md).

cove-master runs the agent as a **headless one-shot** (`internal/agentrun`):
it spawns `claude -p --output-format stream-json --verbose --dangerously-skip-permissions "<prompt>"` in `AT_COVE_WORKDIR`,
reports `running`, and when the agent exits reads `.at-task/worker-result.json`
(the same contract as the dispatch worker). Before spawning, it **fails loud if
the `--mcp-config` file is missing** (a stale image without
`/etc/claude-code/mcp.json`) rather than launch a silently toolless agent
(COV-190) — the run ends with a logged error instead of an agent with no intercom
tools. On a present config it proceeds:

- `ok` → the client reports `done` and the supervisor tears the studio down.
- `needs-input` → the client reports `waiting` and blocks until Jam sends a
  **wake**. The wake resumes the agent with `claude --continue` and a resume
  prompt, which starts another turn. If no wake arrives within `MaxWait` (30m by
  default), the unit ends and reports `done`. Jam's wake-on engine sends the
  wake when a reply lands; see
  [intercom.md](intercom.md#waiting-for-a-reply-wake-on).
- `error` / no result → `done` with the failure logged.

(Resident mode, below, replaces all three outcomes with a wait.)

A Jam **teardown** cancels the run, which sends the agent `SIGTERM` and then
`SIGKILL` after a grace period. A `wake` that arrives while the agent is still
running is held (at most one), so the next `needs-input` wait resumes at once;
further wakes are dropped.

**Post-mortem on teardown.** Just before the container (and its `/agent-data`
volume) is removed, the launcher grabs the **tail of `cove-master`'s log**
(`/agent-data/cove-master.log`, cove-master's own log plus `claude`'s stderr; the agent's stdout stream lives in `/agent-data/agent-stream.jsonl`, which is never read) over SSH and
records it at `WARN` (`cove agent log (tail, captured on teardown)`, keyed by
`id`). So a cove that died — a crash, a `claude` auth failure, an egress-blocked
model call, or a one-shot exit from a stale image — leaves its reason in Jam's
log instead of vanishing with the volume. It is strictly best-effort: an
unreachable cove or a missing log never blocks the teardown. An **idled** cove is
`docker pause`d — SSH into a frozen container hangs — so teardown **unpauses it
first** (idempotently) before the capture and removal. `Pause`/`Unpause` are
idempotent (pausing an already-paused cove, or unpausing a running one, is a
no-op success), so the idle ladder never fails a reconcile on a cove it already
paused.

**Resident mode (personal and standing sessions).** With `AT_COVE_RESIDENT=1` — which the launcher
sets only for a [personal](personal-sessions.md) or [standing](standing-sessions.md) session — the agent never ends on its
own: after **every** turn (`ok`, `needs-input`, `error`, or no worker-result) the
client logs the outcome, reports `waiting`, and blocks on a **wake** or a teardown only
— there is no `MaxWait`. A turn that **exited non-zero and wrote no worker-result**
(a crashed or auth/model-failed `claude`) is logged at **WARN** — the session still
waits for its owner, but the failure is loud, not mistaken for a healthy idle wait;
the cause is in the agent's own `cove-master.log` (stderr) or `agent-stream.jsonl` (stdout). A wake resumes the agent with `claude --continue` and a prompt
to `read` the reply and carry on. The session ends only when a teardown cancels the
run: the owner's release for a personal session, or the name's removal for a
standing one. Jam's wake-on engine never tears a resident session down for
`wait-max`; only a personal session's optional [idle-ladder reclaim](personal-sessions.md#the-idle-ladder) does.

> **Still deferred:** cove-master becoming the image entrypoint under its own non-root account
> (collapsing the SSH/systemd boot).

Design rationale (the package boundary, the Workload seam, the reconnect model,
and the agent wrapper's lifecycle mapping) lives in
[`../../superpowers/specs/2026-09-13-cove-master-client.md`](../../superpowers/specs/2026-09-13-cove-master-client.md)
and [`../../superpowers/specs/2026-09-13-cove-agent-wrapper.md`](../../superpowers/specs/2026-09-13-cove-agent-wrapper.md).
