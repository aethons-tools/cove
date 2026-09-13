# cove-master: in-cove Attach client (COV-155)

**Status:** design approved, pre-plan
**Issue:** COV-155 (slice 3 of the cove-management tree)
**Foundation:** COV-153 (Attach gRPC server), COV-149 (supervisor). **Design history:** `docs/superpowers/specs/2026-09-10-harbor-design.md`; `docs/superpowers/specs/2026-09-13-harbor-attach-stream.md`.

## Summary

Build the **in-cove Attach client** — the cove side of the COV-153 stream — as the first limb of **cove-master**, the cove's future primary process. A cove runs `cove-master`; it dials harbor's runtime listener, authenticates with its identity token + per-instance launch secret, holds the bidi Attach stream, reports **activity up**, and reacts to **control down**. This slice ships the client library + the `cove-master` binary with a **stub workload**; the real agent wrapper, and the sandbox restructure (cove-master as image entrypoint in its own non-root account, collapsing the SSH/systemd boot), are deferred — but the client is shaped so they drop in cleanly.

This is deliberately **not** bolted onto the SSH-driven bracket: cove-master needs only its environment (harbor address + credentials) to run. No host orchestration, no `at-cove` import.

## Package layout & boundary

- **`internal/covemaster`** (new) — the client library. Imports **only** `attachpb` + grpc (+ stdlib). It defines its **own** `Activity`/`Control` types and maps them to the wire enum, so it never imports `internal/harbor` (staying lean and server-free).
- **`cmd/cove-master`** (new, `package main`) — reads env, builds the client, runs it with a workload. Imports `internal/covemaster` + `attachpb`.
- `at-cove` neither imports nor (this slice) embeds cove-master. **Gate:** `go list -deps ./cmd/at-cove | grep -iE 'oidc|grpc'` stays empty.

## The client API + the Workload seam

```go
package covemaster

type Activity int
const ( Running Activity = iota; Waiting; Blocked; Done )

type ControlKind int
const ( Wake ControlKind = iota; Teardown )   // TierChanged is NOT surfaced (see below)
type Control struct { Kind ControlKind }

// Handle is what the client hands the workload to report activity.
type Handle interface { Report(Activity) }

// Workload is what cove-master supervises (the agent, in a later slice).
type Workload interface {
    // Run does the work, reporting activity via h, until it completes or ctx is
    // cancelled. Returning nil → the client reports Done and exits. A non-nil
    // error is logged; the client still reports Done and exits (the unit is over).
    Run(ctx context.Context, h Handle) error
    // Control delivers a decoded control message. Teardown ALSO cancels Run's ctx;
    // Control(Teardown) is the cooperative hook (checkpoint/flush) before unwind.
    Control(c Control)
}

type Config struct {
    Addr         string        // AT_HARBOR_RUNTIME_ADDR
    Token        string        // AT_HARBOR_IDENTITY_TOKEN
    LaunchSecret string        // AT_HARBOR_LAUNCH_SECRET
    Heartbeat    time.Duration // default 10s
    // dial options injected for tests (bufconn); production uses a plaintext TCP
    // dial this slice (:443/TLS mux is deferred).
    DialOptions  []grpc.DialOption
}

func New(cfg Config, log *slog.Logger) *Client
// Run connects and supervises w until w.Run returns (→Done), a Teardown arrives,
// or ctx is cancelled. It reconnects with backoff across transient stream drops.
func (c *Client) Run(ctx context.Context, w Workload) error
```

### Lifecycle

1. **Connect + auth:** dial `Addr`; open `Runtime.Attach` with metadata `authorization: Bearer <token>` + `x-harbor-launch-secret: <secret>`. On `Unauthenticated`, fail fast (no retry — bad credentials won't fix themselves).
2. **Workload:** start `w.Run(runCtx, handle)` once, in a goroutine. `handle.Report(a)` enqueues a `StatusUp{Status}`. When `w.Run` returns, the client reports `StatusUp{Done}` and stops (exit 0).
3. **Heartbeat:** an auto-ticker sends `StatusUp{Heartbeat}` every `Heartbeat` interval to renew the lease, independent of activity.
4. **Control (recv):** decode each `ControlDown`:
   - `Teardown` → call `w.Control(Teardown)` **and** cancel `runCtx` (so `w.Run` unwinds); then stop.
   - `Wake` → `w.Control(Wake)`.
   - `TierChanged` → **log and ignore** (not surfaced to the seam this slice; re-added when escalation/COV-145 needs it).
   - `RotateToken` → log and ignore (reserved; rotation mechanics are a later slice).
5. **Reconnect:** a stream drop (non-auth error / EOF) while the workload is still running → reconnect with capped exponential backoff, re-authenticating each attempt. The workload keeps running across reconnects (its `runCtx` is only cancelled by Teardown or `w.Run` completing). Harbor supersedes the prior server-side stream (COV-154).

Activity reported while disconnected is coalesced to "latest" and sent on reconnect (a small `latest Activity` held under a mutex), so a transient drop never loses the current state.

## The stub workload (this slice)

`cmd/cove-master` ships a `stubWorkload`: `Run` reports `Running` once then blocks on `ctx.Done()`, returning `ctx.Err()`; `Control` logs Wake/Teardown. This exercises the whole client end-to-end (connect → Running → heartbeats → Teardown→exit) without a real agent. The real agent wrapper replaces it next slice.

`cmd/cove-master` `main`: read the three env vars (error out clearly if `ADDR`/`TOKEN`/`SECRET` missing), build `Config`, `New(...).Run(ctx, stubWorkload{})` with ctx cancelled on SIGINT/SIGTERM.

## Wire mapping

`toPBActivity(Activity) attachpb.Activity` (Running→RUNNING, …, Done→DONE) — the inverse of harbor's `fromPBActivity`. Kept in the client; the single source of the enum correspondence is the proto.

## Tests (hermetic)

In `internal/covemaster` (the round-trip test imports `internal/harbor` + `internal/harbor/attach` to stand up a real server — test-only, so the library stays server-free):

- **Harness:** temp-dir `FileStore` + a guest role + `Supervisor` with an alive fake launcher; raise an instance (get token + launch secret); `attach.NewServer` + `grpc.NewServer` on a **bufconn** listener; inject a bufconn `DialOption` into the client `Config`.
- **auth + status up:** run the client with a workload that reports `Waiting`; `eventually` the supervisor's store shows `Activity=waiting` and the lease renewed.
- **auth failure:** a bad launch secret → `Client.Run` returns an `Unauthenticated` error promptly (no infinite retry).
- **control down → workload:** `srv.RequestTeardown(actorID)` → the workload's `Control(Teardown)` fires AND `w.Run`'s ctx is cancelled AND `Client.Run` returns.
- **Done self-termination:** a workload whose `Run` returns nil → the supervisor sees `Activity=done` → instance torn down; `Client.Run` returns.
- **heartbeat:** with a short `Heartbeat` interval and a workload that never changes activity, the lease keeps being renewed (store `LastSeen`/`Lease.Expiry` advance across ticks).
- **reconnect re-auths:** stop+restart the server (or drop the stream) while the workload runs → the client reconnects and status flows again. (If a clean server-restart is awkward under bufconn, assert reconnect via a dial-attempt counter in an injected dialer.)
- **stub workload unit test:** `Run` returns `ctx.Err()` on cancel; `Control` doesn't panic.
- **integration (`//go:build integration`):** the client against a real `attach.Server` on a localhost TCP listener — exercises the real dial path.

## Deferred (the sandbox restructure — NOT this slice)

- The real **agent wrapper** implementing `Workload` (claude lifecycle → Activity; delivering Wake/Teardown to the running agent).
- cove-master as the image **entrypoint** in its own **non-root account**; collapsing the systemd + sshd boot ritual; how the binary is **delivered into the image** (bake vs the at-switchboard-style embed).
- `:443`/TLS **multiplexing** (this slice dials plaintext TCP to `runtime.listen`).
- **Token rotation** (`RotateToken`) and **escalation tiers** (`TierChanged`) — re-surface in the seam when their slices land.
- Server-side **transport supersede** of a superseded stream (COV-154).

## Docs

Add a short "cove-master (the in-cove client)" note to `docs/usage/harbor/coves.md` — what cove-master is (the cove's client of the Attach stream), the three env vars it needs, and that this slice ships the client with a stub workload (the real agent wrapper + entrypoint/account restructure are later). Keep the harbor design as the rationale source.
