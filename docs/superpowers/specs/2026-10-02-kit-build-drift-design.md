# jam: rebuild kits when Jam changes, and keep running studios' connector current

**Status:** design approved in conversation (2026-10-02); written spec awaiting review
**Scope:** Jam-managed studios (the launcher's `cove-kit:*` images and cove-master's agent turns).
**Problem:** two inputs that shape a studio are not captured by the studio kit's version:

1. **Jam-side build inputs.** A kit image is tagged `cove-kit:<BuildDigest>`, and
   `BuildDigest` hashes only the kit's `base`/`egress`/`build-args`. The build also
   bakes in resources compiled into `at-jam` (the sealed hardening layer, the
   at-task / at-switchboard / cove-master binaries, the blessed base pin) and two
   launcher settings (`JamHost` → the infra egress list; the launcher public key →
   `authorized_keys`). `PrepareKit` short-circuits on an existing tag, so none of
   these ever reach a cached kit. (Observed: session telemetry not arriving because
   cached images carry an old cove-master.)
2. **The connector.** A studio's destination env + git routing is computed once at
   raise, sourced into cove-master's process env, and inherited by every agent
   turn. A destination `--env`/`--git` edit, or a grant change on the role, never
   reaches a running studio.

**Out of scope (ticketed separately):** resolving a tag-form `base.image` to a
digest before hashing; garbage-collecting superseded `cove-kit:*` images; flagging
running studios whose *image* is stale (they pick up a new image on their next raise).

## Part 1 — assembly fingerprint in the image tag

### Decisions

- **The launcher owns it.** Only the launcher knows `JamHost` and its key; the
  registry's `KitRef` (`ID`, `Version`, `Digest`) is unchanged, and so are
  `BuildDigest`, the admin UI's digest display, and the kit-prepare protocol.
- **Hash bytes, not a version string.** A Jam release that does not touch the
  payload must not rebuild every kit; a dev build with changed binaries must.
- **Computed once** at `launcher.New`, so the Raise hot path is unchanged.

### Components

- `assemble.PayloadDigest() string` — sha256 over, in a fixed order with
  length-prefixed framing: every file of the embedded hardening FS (path + bytes,
  walked in lexical order), the embedded at-task / at-switchboard / cove-master
  binaries for each arch (absent embed → empty, matching the placeholder the build
  writes), `basedigest.DefaultRef()`, and `basedigest.BlessedRefs()`. Lives in
  `assemble` because it owns what `AssembleContext` stages; a test pins that every
  staged input is covered.
- Launcher: `asmDigest = sha256(PayloadDigest ‖ JamHost ‖ PublicKey)` stored on
  the `Launcher`. `imageTag` becomes a method:
  `cove-kit:<kitDigest[:32]>-<asmDigest[:32]>` (Docker caps a tag at 128 chars;
  128 bits each is ample). The inventory, `PrepareKit` and `Raise` all go through
  it, so they agree by construction.
- `PrepareKit`'s log line gains `asm` (short) so an operator can see why a kit
  rebuilt.

### Behavior

- After a Jam upgrade that changes the payload (or a `JamHost`/key change), the
  next raise of each kit misses the inventory → `ErrKitNotReady` → `PrepareKit`
  builds → retry. This is the existing lazy-prepare path; nothing new.
- Running studios are untouched until their next raise.
- Old tags are left in the daemon (GC is out of scope).

## Part 2 — connector pull per agent turn (B-pull)

### Decisions

- **Pull, not push.** Before each `claude` spawn (first turn, resume, wake),
  cove-master fetches `GET /connector` — the endpoint host-side clients already
  use — with its identity token as the bearer, and applies the result to *that*
  spawn. One source of truth; grant changes are covered for free; no new control
  message, ack, or retry loop.
- **Never mid-turn.** A turn already running keeps the env it started with.
- **Fail toward last-known.** A fetch failure logs a warning and the turn spawns
  with the last connector applied (initially the raise-time one). The connector is
  client-side routing only — the broker enforces scope — so stale is wrong, never
  insecure.
- **The token never travels in the connector.** `/connector` returns templates;
  cove-master expands `{token}` in memory from `AT_JAM_IDENTITY_TOKEN`
  (`snippet.Connector.Expand`), exactly as host-side clients do.

### Raise-time handoff (launcher → cove-master env)

`connect.LaunchCoveMaster` additionally exports:

- `AT_JAM_CONNECTOR` — the raise-time connector as JSON (templates, no token).
  It seeds cove-master's "last applied" connector, so cove-master knows which env
  keys it owns (to remove keys a later connector drops) and has a fallback if the
  first fetch fails.
- `AT_JAM_BASE_URL` — `https://<JamHost>`, the base for `/connector` and for
  template expansion. Both are in the `AT_JAM_*` namespace destinations are
  already forbidden to set, so no connector can collide with them.

Older images ignore both; their cove-master never fetches (see staleness below).

### cove-master / agentrun

- `agentrun.Spawner.Spawn` gains an `env []string` parameter; `execSpawner` sets
  `cmd.Env` from it (nil keeps today's inherit-everything behavior).
- New `agentrun` collaborator `ConnectorSource` (interface; real impl wraps
  `snippet.Fetch` with an `http.Client` using `http.ProxyFromEnvironment` and the
  system trust store, short timeout). Before each spawn the Workload:
  1. fetches; on error keeps the last-applied connector and logs `warn`
     (no token, no env values — keys/fingerprint only);
  2. builds the spawn env: `os.Environ()` minus every key the last-applied *or*
     new connector sets, plus `new.Expand(base, token)`;
  3. if `GitRoute` changed, rewrites the global git config: unset the old
     `url.<base><oldRoute>.insteadOf`, set the new one (or none), keep the
     credential helper iff a route remains — via the same `git config --global`
     commands `snippet.Connector.GitConfig` renders (factored so both share it);
  4. if the fingerprint changed (or on the first turn), reports it up.
- `snippet.Fingerprint(Connector) string` — sha256 of the canonical JSON (sorted
  keys). Used by cove-master and Jam, so both compute it identically.

### Wire (attach.proto, package stays `harbor.attach.v1`)

```proto
message StatusUp {
  oneof msg {
    Activity         status    = 1;
    Heartbeat        heartbeat = 2;
    SessionEvent     event     = 3;
    ConnectorApplied connector = 4;
  }
}
message ConnectorApplied { string fingerprint = 1; }
```

`covemaster.Handle` gains `ConnectorApplied(fp string)`; the client sends it
(and re-sends the latest after a reconnect, so a dropped report is not lost).

### Jam side

- `Instance.Connector string` (`json:"connector,omitempty"`) — the fingerprint
  the studio last reported applying; "" = never reported (an older image, or not
  yet spawned).
- The Attach server records it on receipt (same patch path as Activity).
- `ConnectorStale(store, inst) bool` — `ConnectorFor(actor)` fingerprint ≠
  `inst.Connector`. Derived on read, not stored, so it can't itself drift.
  Surfaced as a `connector` column (`ok` / `stale` / `unknown`) in
  `at-jam studio list` and on the admin UI's studio views. `unknown` (never
  reported) is how an old-image studio shows up.
- No supervisor action on staleness: a current studio fixes itself at its next
  turn; an old-image studio needs a re-raise, which is the operator's call.

## Error handling

| Case | Behavior |
|---|---|
| `/connector` unreachable / 5xx at a turn | warn; spawn with last-applied; no report |
| `/connector` 401 (identity revoked/expired) | warn; spawn with last-applied (the broker will refuse the agent's calls anyway — teardown is the supervisor's job) |
| `/connector` 409 (destination conflict) | warn naming the conflict; spawn with last-applied |
| `AT_JAM_CONNECTOR` missing/malformed | start with an empty last-applied; first successful fetch fills it |
| git config rewrite fails | warn; spawn anyway (env still applied); retried next turn since the applied route is only recorded on success |
| PayloadDigest inputs unstaged (plain `go build`) | hashes the empty placeholders — deterministic, matches what the build stages |

## Testing (hermetic, TDD)

- `assemble`: `PayloadDigest` is stable across calls; a coverage test asserts
  every top-level entry `AssembleContext` stages is either hashed or is
  caller-supplied data (egress, gitlab config, key).
- launcher: tag differs when `JamHost`, key, or payload digest differs, same kit;
  tag ≤ 128 chars; inventory/Prepare/Raise use the same tag (fake ops).
- `snippet.Fingerprint`: order-insensitive, token-free.
- agentrun: per-spawn env (adds, replaces, drops keys of the previous
  connector; leaves unrelated env alone); fetch failure falls back; git rewrite
  only on route change; report only on fingerprint change — fake `ConnectorSource`,
  fake spawner, fake git runner.
- covemaster: `ConnectorApplied` on the wire and re-sent after reconnect (bufconn).
- jam: Attach server records the fingerprint; `ConnectorStale` ok/stale/unknown;
  `studio list` column.
- connect: `LaunchCoveMaster` exports `AT_JAM_CONNECTOR` (no token in it) and
  `AT_JAM_BASE_URL`.

## Docs

- `docs/usage/jam/kits.md` — the image tag includes the Jam assembly
  fingerprint; when kits rebuild.
- `docs/usage/jam/coves.md` — the per-turn connector refresh; the two new env vars
  in the cove-master env table; the `connector` column.
- `docs/usage/jam/connector.md` — "Delivery": Jam-raised studios re-fetch per turn.
- `docs/TODO.md` — base-tag pinning, image GC, image-stale flag (until ticketed).
