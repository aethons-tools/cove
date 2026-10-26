# Studio Manager: split the studio launcher out of Jam

**Status:** design (brainstorm complete), pending approval, pre-plan
**Scope:** move everything substrate-specific (today, Colima) out of the `at-jam serve` process into a separate **Studio Manager** service. The manager dials into Jam the way a studio does. Jam routes each studio to one of several managers. Jam itself can then run in a container.
**Builds on:**
- the `jam.Launcher` seam (`internal/jam/supervisor.go:98`)
- the Colima launcher (`internal/jam/launcher`, `internal/backend/colima`)
- the Attach gRPC server and the cove-facing `:443` mux (`internal/jam/attach`, `cmd/at-jam/main.go:2867`)
- [2026-09-29 launcher abstraction](2026-09-29-cove-launcher-abstraction-design.md), which already anticipates a remote launcher

**Does not change:** `at-cove`; the studio-side `cove-master` dial-back (Attach and Tunnel); the human `ssh` right (cert plus tunnel through Attach); the roster, broker, and `Decide` model.

## Why

Jam is the central management service. The launcher inside it is a very specific implementation: Colima on a Mac, driven through the docker CLI and SSH on loopback. A deployed Jam will rarely use that. Today Jam has to run on the Colima host, with docker, SSH keys, and the Colima docker context. That ties Jam to the host.

The immediate goal is to **run Jam in a container** next to its dependencies (Postgres and Garage, already in `dev/docker-compose.yml`), so it is easy to manage. A containerized Jam is also a deployable Jam. Jam and the manager are treated as isolated even when they share a machine.

## Goals

1. A new **Studio Manager** binary owns all substrate work:
   - kit builds, draft test builds, and image inspection;
   - raise, teardown, probe, pause, and unpause;
   - egress and the SSH CA install;
   - standing-session state volumes;
   - the SSH bootstrap that pushes secrets and starts `cove-master`.

   The first and only implementation is Colima. It runs **bare-metal on the Mac**, next to Colima.
2. **The manager dials Jam.** It makes one long-lived outbound gRPC stream to Jam's existing cove-facing TLS port, the same way a studio does. Jam never connects out to a manager, so no firewall or port-forwarding is needed in either direction.
3. **Multiple managers per Jam, with routing.**
   - Every session request may carry an **ordered list of allowed managers** and a **placement timeout**.
   - A timeout of zero means "do not wait".
   - With no list, Jam uses the first available manager.
4. **Standing sessions are pinned** to the manager that holds their state. They are never restarted on a different manager.
5. **Jam runs in a container.** That needs an image for `at-jam serve`, plus a compose service beside Postgres and Garage. Jam no longer needs docker, Colima, SSH keys, or loopback access to studios.
6. **The in-process Colima launcher stays for now**, as a fallback. It is kept long enough to run and troubleshoot the Studio Manager live, then deleted in a follow-up.

## Non-goals

- A second substrate (Fly, Firecracker, cloud VMs). The protocol must not *preclude* one. See the constraints below.
- Placement heuristics: cost, environment, load balancing. Cost and environment will drive provisioning eventually, but the heuristics are not known yet. Whoever builds a request decides the list, and Jam honours its order.
- Per-role or per-project default manager lists. "First available" is the only default for now. Config comes later as needed.
- Moving or migrating a standing session's state between managers.
- Preserving existing standing sessions' state across the cutover. Sessions raised by today's in-process launcher are reset when a Jam stops using `local`: they start fresh on a Studio Manager. There is no adopt step (the user's decision).
- Deleting the in-process launcher. That is a follow-up once the manager is proven live.

## Anti-goals (failure even if it "works")

- Jam needing any host access to talk to studios: a docker socket, SSH keys, `host-gateway`, or loopback ports.
- A standing session silently coming up fresh on a different manager, which would lose its state.
- Docker- or Colima-shaped concepts in the Jam↔manager protocol. Examples: container names that Jam interprets, docker contexts, or `docker port` output. `Location` stays an opaque handle that only its manager interprets.
- Per-raise secrets (identity token, launch secret) in logs, or anywhere other than the TLS stream and the studio's tmpfs.
- Reaping a studio just because its manager is briefly disconnected.

## Current state (summary)

- **The seam is already there.** `jam.Launcher` has seven methods: `Raise`, `Teardown`, `Probe`, `Pause`, `Unpause`, `ApplyEgress`, and `PrepareKit`. There are also optional interfaces, detected by type assertion: `ImageTagger`, `StateKeeper`, and `KitDraftBuilder`. `internal/jam` never imports the backend. All wiring is at `cmd/at-jam/main.go:2126-2169`.
- **Colima work that bypasses the seam.**
  - `imagefacts` (`internal/jam/imagefacts`, wired at `main.go:2133`) runs `docker create` and `docker cp` against the Colima context. It backs the studio setup inspection page.
  - `at-jam colima setup-docker|check-docker` (`cmd/at-jam/colima.go`).
  - The `runtime.launcher` config block (`cmd/at-jam/config.go:592-617`): `docker-context`, `docker`, `dns`, `identity-file`, `known-hosts-dir`, `jam-host`, and `runtime-addr`.
- **SSH from Jam into studios happens only inside the launcher.**
  - At raise: wait for sshd, then `connect.LaunchCoveMaster`.
  - At teardown: a best-effort tail of the cove-master log.
  - Egress and the SSH CA go in through `docker exec`.
  - Everything after raise uses the studio's own dial-back over Attach.

## Design

### Components

| Piece | Runs where | Owns |
|---|---|---|
| **Jam** (`at-jam serve`) | container, or anywhere | the supervisor, session/instance state, minting per-raise creds, kit registry and definitions, placement, the manager registry and auth, the admin API/UI |
| **Studio Manager** (new binary; name is the builder's call, e.g. `at-studio-manager`) | bare-metal next to its substrate (Mac + Colima) | everything in `internal/jam/launcher` + `internal/backend/colima` + `imagefacts` + the `colima setup/check-docker` verbs; the studio-facing Jam address it bakes into studios (`jam-host`, `runtime-addr`) |
| **`local` manager** (transitional) | in-process in Jam, from the existing `runtime.launcher` block | the current behaviour, presented to the router as a manager named `local` |

**My call: the in-process launcher becomes a manager named `local`.** Placement, pinning, timeouts, and the admin view then behave the same for in-process and remote managers. Both can be configured at once, so they can be compared live. Deleting `local` later is a clean removal. A containerized Jam simply has no `runtime.launcher` block, so it has no `local` manager. The name `local` is reserved: an operator cannot register a remote manager under it.

### Seam shape inside Jam

The supervisor keeps depending on an interface rather than on gRPC. My call on how:

- A **router** implements today's `jam.Launcher` and optional interfaces for the supervisor. It holds a set of named managers. Each one is either a **remote adapter**, which turns calls into stream messages, or the **local** in-process launcher.
- Every `Instance` records the manager that raised it, in a new `Manager` field. The existing `Backend` field is unused and can be repurposed if the builder prefers.
- Every per-instance operation (Teardown, Probe, Pause, Unpause, ApplyEgress, log capture) routes to that instance's manager.
- `Raise` takes the placement inputs (below) and picks the manager.
- Kit-, draft-, and image-scoped operations (`PrepareKit`, `ImageTag`, `BuildKitDraft`/`HasKitDraftImage`, `ImageFacts`) are per manager. Builds happen on, and images live on, a particular manager.

Builder's call, within these constraints:
- the exact shape of the router API;
- whether `RaiseSpec` gains the placement fields directly or `Supervisor.Raise` takes them separately.

### Connection, protocol, and auth

- **Transport:** a new gRPC service, separate from `Runtime`, on the same cove-facing `:443` TLS mux. It has one bidirectional stream, for example `StudioManager.Connect(stream ManagerUp) returns (stream ManagerDown)`. Exact proto naming is the builder's call.
- **Requests and results:** Jam→manager carries requests with a correlation id. Manager→Jam carries:
  - results;
  - async events: kit build finished, draft build log chunks, and a studio that died;
  - heartbeats.
- **Hello:** on connect, the manager sends its name, version, capacity, and a **full inventory** of the studios it is running (location plus actor id) and the persisted state it holds (state owners).
- **Requests the protocol must cover** (today's interfaces as messages):

  | Group | Requests |
  |---|---|
  | `jam.Launcher` | Raise, Teardown, Probe, Pause, Unpause, ApplyEgress, PrepareKit |
  | `StateKeeper` | PurgeState, StateOwners |
  | Images and drafts | ImageTag; BuildKitDraft (log streamed back) and HasKitDraftImage; ImageFacts |
  | Teardown support | the cove-master log tail, returned as part of the Teardown result |
- **Kit definitions travel in the request.** `PrepareKit` and `BuildKitDraft` already receive the full `KitDefinition` from the supervisor; the remote adapter just carries it. If a definition or draft tree can exceed gRPC message limits, chunk it. That is the builder's call, but it must not fail on realistic kits.
- **Per-raise secrets** (`LaunchCreds`) travel in the Raise request over the TLS stream. Neither side logs them.
- **Auth (my call; the user said mTLS or a shared secret both work):** a **registered manager token**.
  - An operator mints it in Jam per manager name, with a new verb such as `at-jam manager add <name>`, plus `list` and `revoke`. The admin API does the same.
  - The token is shown once. Jam stores only its hash, as it does for studio tokens.
  - The manager presents it as bearer metadata on `Connect`. Revoking it drops the live stream.
  - mTLS can be added later without changing anything else.
- **TLS:** the manager verifies Jam's server certificate the same way `cove-master` does (a CA bundle in config).
- **One stream per name** (my call): a new authenticated connection for a name replaces the old one, which is closed.

### Placement (routing)

**Placement inputs**, carried on every session request:

- **`managers`:** an optional ordered list of manager names allowed to host the session.
- **`placement timeout`:** how long to keep looking for a manager. **Zero means "do not wait":** try only managers connected right now and fail at once if none accepts.

**Default when no list is given:** any connected manager, taken in a stable order: registration order, with `local` last. "First available" therefore means the first in that order that accepts. Using registration order is my call. Per-role and per-project default lists come later, in config.

**Default timeout when the caller omits one** (my call): zero, which matches today's behaviour. The internal loops (requisitioner, standing reconciler) already retry on their own cycles. API and CLI callers (`at-jam studio raise`, personal-session start) expose the field. When it is set, the call waits up to that long.

**The algorithm.** Walk the list in order. For each manager that is connected, send Raise. The manager either accepts and raises, or **declines** with a reason. Valid decline reasons:

- at capacity, against its configured maximum number of studios;
- cannot run this kit or feature, for example Sysbox or `docker: true` is not available;
- an unrecoverable error on that manager.

On a decline, move to the next manager. If the whole list declines or is disconnected, wait for a manager to connect or report free capacity, then retry. Stop when the timeout runs out. The failure then reads "no manager could host it", with each manager's reason.

**`ErrKitNotReady` is not a decline.** A manager that needs to build the kit first is still the preferred host. Jam runs `PrepareKit` on *that* manager and retries the raise there, using today's prepare/defer flow. Preference order wins over build latency (my call). The placement timeout covers finding a manager that accepts. It does not cover a build in progress.

**Where it applies.** Every raise path uses placement: the requisitioner, personal sessions, `at-jam studio raise`, standing sessions, and draft studios. Draft studios are naturally constrained to the manager that built the draft image. That is my call: a draft image exists only on one manager.

### Pinning (standing sessions)

- A standing session is pinned to **the manager holding its state**.
  - Jam records the pin when the session is first raised.
  - On reconnect, Jam cross-checks the pin against the `StateOwners` that manager reports.
- Re-raising a pinned session goes **only** to its pinned manager.
  - If that manager is disconnected or declines, the raise waits up to the placement timeout and then fails.
  - The session is never raised anywhere else.
- A request whose `managers` list excludes the pinned manager **fails immediately**, with an error naming the pin (my call).
- **A reset** (`ResetStanding`: tear down and purge state) **clears the pin**. The next raise places the session fresh.
- **A pin to a manager that is no longer configured** (in practice `local`, once a Jam runs without `runtime.launcher`) is treated as a reset. The pin is cleared, the session is placed fresh, and its old state is not carried over. This follows from the user's "no carry-over at cutover" decision. Log it loudly.
- The pin is not part of the request. It is Jam's record.
- Other session kinds are not pinned. Pinning keys on persisted state, which today only standing sessions have.
- The user accepts that strict pinning may cause issues. They will be addressed with experience. Do not add a "move" escape hatch now.

### Disconnects and reconciliation

- **Probe while disconnected:** a Probe for a studio on a disconnected manager returns `LivenessUnknown`, the existing transient result. Unknown never reaps, so studios are not torn down because their manager went quiet.
- **Studios keep running** while the manager is away. They are containers on the Mac, and their Attach stream to Jam is independent of the manager.
- **Commands that need the manager while it is away:**
  - Teardown, Pause, Unpause, and ApplyEgress fail with a "manager unavailable" error.
  - Teardown specifically must be **retried when the manager reconnects**, so a torn-down studio does not linger. Whether that is a durable pending-teardown mark or the existing reconciler re-driving it is the builder's call.
- **On Hello, Jam reconciles** the manager's inventory against the instances it believes are on that manager:
  - Jam's live instances that are missing from the inventory are marked Lost, as today.
  - Inventory studios that Jam doesn't know are **reported, not destroyed**: logged and shown in the admin view. Jam's `Reconcile` (`supervisor.go:1512`) has no stray sweep today, and adding a destructive one is out of scope (my call).
- **Studios and state are labeled with the raising manager's name** (my call), alongside the existing `harbor.cove` and Jam-address labels. A manager's inventory and `StateOwners` cover only its own labels. `local` and a remote manager can then share one Colima daemon during the fallback period without seeing each other's studios or state.
- **A manager restart does not touch running studios.** The manager rebuilds its inventory from the substrate, which for Colima means docker labels (`harbor.cove`).

### What moves where (config and verbs)

**Manager config.** Today's `runtime.launcher` fields move to the manager's own config file:
- `docker-context`, `docker`, `dns`, `identity-file`, and `known-hosts-dir`;
- `jam-host` and `runtime-addr`: the address studios dial Jam on. This is the Jam address *as seen from studios on that host*;
- the kit-image fingerprint inputs (asm, launcher public key).

It also gains new fields:
- its name and token;
- Jam's URL and CA;
- its capacity (maximum studios).

**Jam config.** Jam's config gains whatever the manager registry needs; manager tokens live in the store. `runtime.launcher` remains, solely to configure `local`, until the follow-up removes it.

**Verbs.** `at-jam colima setup-docker|check-docker` moves to the manager binary. During the fallback period the `at-jam` verbs may stay, since `local` still needs them (builder's call).

**Admin visibility.** The admin API and UI list managers: name, connected or not, last seen, version, capacity, studio count, and which manager each studio is on. Revoking a token is available there too. Add metrics alongside the existing ones (`deploy/monitoring`).

### Jam in a container

- **An image for `at-jam serve`.** The builder must audit what `serve` shells out to once the launcher is gone (for example `git`, for git-backed kits and config) and include only that.
- **A compose service** for Jam beside Postgres and Garage, publishing the cove-facing TLS port. Coves and managers on the Mac reach it at the host's published port. The manager's `jam-host`/`runtime-addr` point there, so the existing `--add-host <jam-host>:host-gateway` mapping still works for studios.
- **Docs** for running it. `dev/` is the dev instance. Whether a separate deploy compose is added is the builder's call.

### Operations not tied to a running studio

These are:
- admin draft test builds;
- the image-facts setup inspection;
- `PrepareImage` and standing-upgrade prepares;
- `CurrentImage` staleness.

My calls on how they map to managers:
- Admin draft builds and image-facts inspection take an optional manager name in the admin API/UI. It defaults to the first available manager. Draft build records are keyed by (kit, manager).
- Prepares for a standing upgrade go to the session's pinned manager. Other prepares go to the manager placement would pick.
- Staleness is computed per studio, against its own manager's `ImageTag`. The per-role "current image" view shows one row per connected manager.

### Deployment assumptions

- **One Jam process per store** (my call). The requisitioner is already single-instance (`docs/usage/jam/requisitioner.md`). Managers connect to that one process, and the router's registry is in memory.
- **Jam and manager versions must match** (the user agreed, for now). Jam rejects a Hello whose version differs, with a clear error. The manager embeds `cove-master`, which then speaks Attach to Jam, so lockstep avoids a compatibility matrix on both protocols. Upgrading means upgrading both.

### Constraints for the protocol (future substrates)

- `Location` is opaque to Jam.
- The manager owns its build cache.
- Kit image tags and fingerprints are per manager: `ImageTag` answers are scoped to a manager. Staleness compares a studio's tag against *its own* manager's current tag.
- Nothing in the protocol assumes a local docker daemon, a docker socket, or SSH.

## Phasing (suggested; the builder may reorder)

1. **Router and `local` manager.** Instances gain `Manager`, and placement inputs exist. Behaviour does not change with only `local` configured.
2. **Protocol, auth, and manager registry in Jam**, plus the remote adapter. Test it hermetically against a fake manager over an in-memory gRPC connection (bufconn).
3. **The Studio Manager binary**, wrapping the existing launcher, Colima backend, and imagefacts.
4. **Placement, pinning, and disconnect/reconcile** across multiple managers.
5. **Jam container image and compose; docs.** Docs to update: `docs/usage/jam/serve.md` (launcher block), `docs/usage/jam/coves.md`, and the `docs/OVERVIEW.md` architecture section. Add a new usage doc for the manager, routed from `docs/usage/INDEX.md`.
6. **Follow-up, not this project:** after live troubleshooting, delete `local`, `runtime.launcher`, and the `at-jam colima` verbs.

## Testing expectations

- The existing launcher and Colima tests keep passing unchanged. The code moves, not the behaviour.
- Router and placement need hermetic tests:
  - order and preference;
  - declines;
  - timeout zero versus non-zero;
  - an excluded pinned manager;
  - disconnect giving `Unknown` with no reap;
  - reconnect reconciliation;
  - a teardown retried after reconnect.
- Protocol tests run against a fake manager. They cover auth, revoke, and replacing a connection.
- One manual live run: a containerized Jam plus a bare-metal manager on a Mac, raising, tearing down, pausing, building a kit, and running a draft build. That run is what gates deleting `local`.

## Decided by the user

- Rename the launcher to **Studio Manager**. It is a separate service.
- The Colima manager runs **bare-metal on the Mac**.
- **The manager dials into Jam**, like a studio. mTLS or a shared secret both work.
- **Do the routing now.** Every session request carries an **ordered list of allowed managers**.
- **The requester supplies a placement timeout**, and some value means "do not wait".
- With no list, default to the **first available manager**. Add config later.
- **Never restart a standing session on a different manager.** Address the resulting issues with experience.
- **No state carry-over at cutover.** Existing standing sessions are reset and start fresh on the new manager.
- **Jam and manager versions must match**, for now.
- **Keep the in-process launcher** long enough to run and troubleshoot live, then drop it.

## Calls made in this design (check these)

- The in-process launcher becomes a router-visible manager named `local`, sorted last for "first available".
- Auth is a per-name manager token, stored hashed, revocable, one live stream per name.
- The default placement timeout when omitted is zero ("do not wait").
- "First available" means connected and accepting, in registration order.
- A cold kit on a preferred manager builds there. Order beats build latency, and the build is outside the placement timeout.
- A request list that excludes a pinned manager is an immediate error.
- A reset clears the pin.
- Draft studios go to the manager holding the draft image.
- On disconnect, Probe returns Unknown with no reap. Teardown is retried on reconnect. Strays found on reconnect are reported, not destroyed.
- Studios and state carry a manager-name label, so `local` and a remote manager can share one daemon.
- Admin draft builds and image facts take an optional manager, defaulting to first available. Staleness is per manager.
- One Jam process per store.
- A new gRPC service on the existing `:443` mux, rather than extending Attach.

## Left to the builder

- Binary name, proto names, and router API shape.
- Chunking of large kit definitions and logs.
- How a pending teardown is made durable.
- Whether the `at-jam colima` verbs stay during the fallback period.
- The exact contents of the Jam container image.
- The deploy compose beyond `dev/`.
