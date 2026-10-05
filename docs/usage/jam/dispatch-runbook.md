---
summary: End-to-end runbook for standing up Jam's Linear-driven dispatch on a single Colima host — the ordered procedure (serve → destinations → role → dispatch-label gate) plus the field gotchas that bite a first real run (egress, cert-name, flat-vs-grouped labels).
read_when: You are bringing up a real Jam dispatch loop for the first time (or reproducing one) and want the ordered steps and the traps, not the per-field reference.
owns: the ordered end-to-end dispatch stand-up procedure and its field gotchas; it links to the reference docs it stitches together and never restates their schemas
prereqs: serve.md, roster.md, requisitioner.md, coves.md, ../at-cove-config.md#jam — this runbook orders them, it does not replace them
tier: leaf
updated: 2026-10-05
---

# Runbook: Jam dispatch, end to end

The shortest correct path to a working **Linear ticket → raised studio** loop on a
single host with Colima, and the traps that actually bite. Each step links to the
doc that owns the detail; this runbook only owns the **order** and the
**gotchas**. Read [requisitioner.md](requisitioner.md) for the model.

## Prerequisites

- Colima running; a studio image built via `at-cove install` (its `install.json`).
- A Linear workspace + team; a Linear API token (Jam's own).
- Anthropic + git credentials Jam will broker.
- A DNS name for Jam with a TLS cert (see the cert gotcha below).

## The procedure

1. **Postgres (required)** — `just dev-up` raises the dev
   Postgres; the `store-postgres` block holds the control plane *and* the
   intercom log (the durable squawk Log). See [serve.md](serve.md#postgres-store-store-postgres)
   and [`dev/`](../../../dev/README.md).
2. **`at-jam serve`** — write the serve config: cove-facing `listen: :443` +
   `tls`, loopback `admin-listen` (no OIDC needed on loopback), `store-postgres`,
   `credentials` (anthropic + git + the DB password), and a `runtime.launcher`
   pointed at your `install.json`. Start it (`:443` needs privilege). Full schema:
   [serve.md](serve.md).
3. **Destinations** — `at-jam destination add` for `anthropic` and `git`. Each `--cred-name` must resolve to a `credentials:`
   entry, **validated at add time** — so add the credential to the serve config
   and restart before adding the destination. [serve.md](serve.md#destinations).
4. **Role** — `at-jam role add --name worker --destinations anthropic,git
   --ttl 24h`. The git credential's own scope bounds which repos it reaches. A role with **no `--ttl` mints non-expiring
   tokens** — always set one for ephemeral studios. [roster.md](roster.md).
5. **Requisitioner** — add `runtime.requisitioner` (`role`, `max-concurrent`,
   `tracker-token-cred`, `linear.team` + `states`). Restart serve. It polls the
   `ready` state and raises one studio per **dispatch-labeled** ticket, bounded by
   the cap. [requisitioner.md](requisitioner.md).
6. **Trigger** — label a ticket `dispatch:go`, move it to your `ready` state, and
   watch `at-jam studio list`. The kit registry ([kits.md](kits.md)) is **not**
   required — the launcher raises from `install.json`, not a registered kit.

## Gotchas (each one cost a real debugging loop)

- **Dispatch is opt-in per ticket.** Only issues carrying a label matching
  `dispatch-label-prefix` (default `dispatch:`) are raised. Point `states.ready`
  at a *dedicated* column or a shared "Todo" will sweep the whole backlog into
  studios. See [requisitioner.md](requisitioner.md).
- **Use a FLAT `dispatch:*` label, not a Linear label _group_.** A grouped label's
  API `name` is just the child (e.g. `spider`), *without* the `dispatch:` prefix,
  so the gate never matches and nothing raises. Create a flat label named literally
  `dispatch:go` — same shape as `class:attended`.
- **Studio egress must allow the Jam host.** The raised studio reaches Jam
  through its own squid proxy; if `jam-host` isn't on the studio's egress
  allow-list, every brokered call (and the Attach stream) dies and the studio does
  nothing. Widen egress in the kit/image.
- **The dialed name must match the TLS cert.** `runtime.launcher.jam-host` /
  `runtime-addr` must be a name the broker cert's SAN covers — the studio validates
  Jam's cert against its system trust store (the launcher injects no CA). A
  cert for `local.aethons.tools` will not satisfy a studio told to dial
  `jam.local.aethons.tools`. Match them, or reissue the cert (wildcard/SAN).
- **`store-postgres` password must resolve.** The DB password is a named
  `credentials:` entry; serve fails closed on connect. (A prior bug dropped the
  credential's name so the password resolved empty — fixed; if you see
  `password authentication failed` on a correct password, confirm you're on a
  build past that fix.)
- **The studio image and the host `at-jam` must be the same build.** The
  intercom wire endpoint was hard-renamed `/messages` → `/squawks` (no
  back-compat shim), so a post-rename studio calling `/squawks` against a
  pre-rename `serve` (or vice versa) gets a 404 and the agent silently has no
  intercom tools. If a studio's tools 404, rebuild **both** the image (`at-cove
  install`) and the host binary (`just build`) from the same commit.
- **Discord replies must be real replies; a wait-for-reply task ends its turn.** Reply-routing matches an inbound message by the message id it
  *replies to*, so a **bare** post in the channel carries no reference and is
  dropped by design (that's also how Jam's own echoed posts don't
  mis-route). Use Discord's reply-to-message. And a task that waits for a reply
  should have the agent **end its turn** (it then waits, idles, and wake-on resumes
  it when the reply lands) instead of busy-polling `read` in a single long turn.

## Known issues (open)

- **COV-189** — a cove-master/agent that **crashes without reporting `Done`**
  (while the container's `sshd` stays alive) is not reaped, since `Probe` checks
  container liveness, not agent liveness. The normal complete-and-report path
  works; this is the crashed-without-`Done` edge case.

> **Full comms loop verified.** A studio built from a current image drives the whole
> Discord path end to end: `send(to=channel:…)` → posted to Discord → the human's
> **reply** routed back → agent `read` + `commit` → (on a wait-for-reply task)
> **idle → wake-on-reply → resume** → ack → done → teardown. COV-188 (tools not
> registering) was a *stale image* missing `/etc/claude-code/mcp.json` (since COV-240
> cove-master generates the MCP config per episode; that file is gone); COV-190
> (a missing `--mcp-config` now fails loud) is merged — rebuild the image **and**
> host binary from the same commit if a studio comes up toolless.

For *why* Jam is built this way, follow the design-history pointer in
[INDEX.md](INDEX.md).
