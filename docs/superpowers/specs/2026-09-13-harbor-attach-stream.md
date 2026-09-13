# harbor: managed-cove Attach stream — harbor-side gRPC server (COV-153)

**Status:** design approved, pre-plan
**Issue:** COV-153 (slice 2 of the cove-management tree; foundation = COV-149)
**Design history:** `docs/superpowers/specs/2026-09-10-harbor-design.md` (the runtime/conductor, the two clocks); `docs/superpowers/specs/2026-09-12-harbor-cove-supervisor.md` (the supervisor spine this builds on).

## Summary

Build the **harbor-side Attach stream**: a bidirectional gRPC stream a managed cove holds open to its supervisor, carrying **status up** (Running/Waiting/Blocked/Done + heartbeats) and **lifecycle control down** (Wake/Teardown/TierChanged). This is the live transport for the `Phase`/`Activity` model COV-149 shipped.

**Harbor-side server only.** The real in-cove client and `:443` multiplexing are deferred. The server is exercised hermetically with an in-process **bufconn** client — no real cove, no network, the placeholder launcher from COV-149 still stands in.

The core `internal/harbor` stays **grpc-free**: grpc lives only in a new sub-package `internal/harbor/attach` (+ the `at-harbor` binary).

## Connection direction & trust (decided)

**The cove dials harbor** (not harbor→cove). Rationale: reuses the existing sealed `:443` egress path the broker already uses, keeps the cove with **no inbound listener** (smaller attack surface; fits the locked-down sandbox), and gives **passive liveness** — a dropped stream means the cove is gone, no polling. Reversing the direction was considered and rejected: it doesn't increase trust (mutual auth is direction-independent) and it would force the cove to accept inbound, breaking the "cove only dials out" model and the suspend/NAT cases.

Auth is **mutual and two-factor**:

1. **Identity token** (gRPC metadata `authorization: Bearer <token>`) → `HashToken` → `Store.Lookup` → authenticates the **Actor** (reuses the broker's exact path).
2. **Per-instance launch secret** (metadata `x-harbor-launch-secret: <secret>`) → hashed and compared to `Instance.LaunchSecretHash` → binds the stream to **this raised instance**. A leaked identity token **alone** cannot attach — defense in depth. The secret is minted at `Raise`, its hash stored on the `Instance`, and the plaintext returned once (the launcher injects it into the cove in a later slice; the bufconn test presents it directly).

A `RotateToken` control variant is **reserved** in the proto (forward-compat for a later fast-rotation slice) but no rotation mechanics are built here.

## Proto

`internal/harbor/attach/proto/attach.proto`, generated into `internal/harbor/attach/attachpb` (committed):

```proto
syntax = "proto3";
package harbor.attach.v1;
option go_package = "github.com/aethons-tools/cove/internal/harbor/attach/attachpb";

service Runtime {
  // The cove opens one long-lived Attach stream: it sends StatusUp, harbor sends ControlDown.
  rpc Attach(stream StatusUp) returns (stream ControlDown);
}

message StatusUp {
  oneof msg {
    Activity  status    = 1;  // a reported activity change
    Heartbeat heartbeat = 2;  // liveness keepalive (renews the lease)
  }
}
enum Activity {            // mirrors harbor.Activity (only meaningful while Phase==Live)
  ACTIVITY_UNSPECIFIED = 0;
  RUNNING = 1;
  WAITING = 2;
  BLOCKED = 3;
  DONE    = 4;            // triggers teardown
}
message Heartbeat {}

message ControlDown {
  oneof msg {
    Wake        wake        = 1;  // resume / answer-arrived (reserved — no internal trigger yet)
    Teardown    teardown    = 2;  // cooperative "wrap up and exit"
    TierChanged tier        = 3;  // escalation-tier change (reserved)
    RotateToken rotate      = 4;  // RESERVED for a later token-rotation slice
  }
}
message Wake {}
message Teardown    { string reason = 1; }
message TierChanged { int32 tier = 1; }
message RotateToken { string token = 1; }  // reserved; not issued this slice
```

## Package layout & boundary

- **`internal/harbor`** (core) — stays grpc-free and kit-free. Gains only: a `ControlSink` interface, a `Supervisor.sink` field + `SetControlSink`, a `Heartbeat` method, an `Instance.LaunchSecretHash` field, and `Raise` minting/returning the launch secret.
- **`internal/harbor/attach`** (new) — the generated `attachpb` code + the Attach gRPC **Server**, which implements both `attachpb.RuntimeServer` and `harbor.ControlSink`. **grpc lives only here** (+ the binary).
- **`cmd/at-harbor`** — wires the server when `runtime.listen` is set.

Boundary gates (CI-checkable):
- `go list -deps ./internal/harbor | grep -i grpc` → **empty** (grpc only in `internal/harbor/attach`).
- `go list -deps ./cmd/at-cove | grep -iE 'oidc|grpc'` → **empty** (client deferred; at-cove unaffected).

## Core changes (`internal/harbor`)

```go
// ControlSink is how the supervisor pushes lifecycle control to a connected cove.
// Implemented by the Attach server; nil when no stream server is running (slice-1
// behavior). All methods are best-effort and non-blocking — no connected stream
// for the actor is a silent no-op.
type ControlSink interface {
    RequestTeardown(actorID string) // enqueue a ControlDown Teardown
    Wake(actorID string)            // enqueue a ControlDown Wake (reserved use)
}
```

- `Instance` gains `LaunchSecretHash string json:"launch_secret_hash,omitempty"` (additive; v5 store unchanged otherwise — a new optional field round-trips).
- `Supervisor` gains `sink ControlSink` (unexported) + `func (s *Supervisor) SetControlSink(ControlSink)` (set after construction, resolving the supervisor↔server cycle).
- `Raise` now mints a launch secret (`MintToken`), stores `HashToken(secret)` on the Instance, and returns the plaintext. Signature becomes:
  `Raise(ctx, RaiseSpec) (Instance, token string, launchSecret string, err error)`. Rollback paths unchanged.
- `Teardown` best-effort nudges the cove before its authoritative actions: `if s.sink != nil { s.sink.RequestTeardown(actorID) }`, then launcher.Teardown + revoke + RemoveInstance (unchanged order otherwise).
- `func (s *Supervisor) Heartbeat(actorID string) error` — renews the lease (`Holder=self`, `Expiry=now+ttl`) + `LastSeen`, without touching `Activity`/`Phase`; errors if the instance is absent or `Gone`.
- Admin `CoveRaiseResult` gains `LaunchSecret string json:"launch_secret"` (returned once, like the token; never logged). The CLI prints neither secret.

## The Attach server (`internal/harbor/attach`)

```go
type Server struct {
    attachpb.UnimplementedRuntimeServer
    store harbor.Store
    sup   *harbor.Supervisor
    log   *slog.Logger
    mu    sync.Mutex
    conns map[string]chan *attachpb.ControlDown // actorID → send channel
}
func NewServer(store harbor.Store, sup *harbor.Supervisor, log *slog.Logger) *Server
```

**`Attach(stream)`:**
1. **Authenticate** from incoming metadata: bearer token → `harbor.HashToken` → `store.Lookup` → Actor (else `codes.Unauthenticated`). Require a live `Instance` (`store.GetInstance(actor.ID)`), and `harbor.HashToken(launchSecret) == inst.LaunchSecretHash` (else `Unauthenticated`). Missing metadata → `Unauthenticated`.
2. **Register** a fresh send channel under `conns[actorID]`, **replacing** (and closing) any existing one — a reconnect supersedes the old stream (the lease-steal analog).
3. Renew the lease immediately: `sup.Heartbeat(actorID)` (the cove is talking to us now).
4. **recv loop:** `StatusUp.Status` → map `attachpb.Activity`→`harbor.Activity` → `sup.Report(actorID, activity)` (renews lease; DONE → teardown). `StatusUp.Heartbeat` → `sup.Heartbeat(actorID)`.
5. **send loop:** range the channel → `stream.Send(controlDown)`.
6. On stream ctx done / recv error / EOF: deregister `conns[actorID]` (only if still the current channel). No proactive teardown — the lease stops being renewed and the reconciler's `Probe` decides, with the TTL as the reconnect grace window.

**As `harbor.ControlSink`:** `RequestTeardown`/`Wake` look up `conns[actorID]` and **non-blocking** send the corresponding `ControlDown` (drop if the buffer is full or absent — best-effort).

Activity mapping lives in one helper (`fromPBActivity`), the inverse of the COV-149 `Activity` constants.

## serve wiring (`cmd/at-harbor`)

`serveConfig.Runtime` gains `listen string yaml:"listen"` (alongside `lease-ttl`/`reconcile-interval`). When `runtime.listen` is set, `cmdServe`:
1. builds the supervisor (as today),
2. constructs `attach.NewServer(store, sup, log)`, calls `sup.SetControlSink(srv)`,
3. `gs := grpc.NewServer()`, `attachpb.RegisterRuntimeServer(gs, srv)`, and serves on a listener at `runtime.listen` in a goroutine.

`:443` multiplexing with the broker is deferred — for now the stream server binds its own address (e.g. `127.0.0.1:9090`). When `runtime.listen` is empty, no gRPC server starts (slice-1 behavior; the sink stays nil).

## Testing (hermetic, bufconn)

All in `internal/harbor/attach` using `google.golang.org/grpc/test/bufconn` — an in-memory listener, no real port:

- **auth success:** raise an instance (get token + launch secret), dial via bufconn, open Attach with both metadata values → stream opens; send `Activity(WAITING)` → store shows `Activity=waiting` + lease renewed.
- **auth failures:** missing/garbage token → `Unauthenticated`; wrong launch secret → `Unauthenticated`; valid token but no live Instance → `Unauthenticated`.
- **heartbeat:** `Heartbeat` → lease renewed + `LastSeen` bumped, `Activity`/`Phase` unchanged.
- **control delivery:** `srv.RequestTeardown(actorID)` → the client receives a `ControlDown{Teardown}`.
- **reconnect replaces:** a second Attach for the same actor supersedes the first (old stream ends / its channel closed).
- **DONE → teardown:** send `Activity(DONE)` → the instance is torn down (gone from the store), actor revoked.
- **stream drop → deregister:** close the client → the server deregisters; a subsequent `RequestTeardown` is a no-op (no panic, no send on a closed channel).

## Toolchain (verified)

No `protoc` needed. Codegen uses `buf`'s built-in compiler + the Go plugins, all installed via the module proxy:

```
GOPROXY=https://proxy.golang.org GOSUMDB=off GOTOOLCHAIN=local \
  go install google.golang.org/protobuf/cmd/protoc-gen-go@latest \
             google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest \
             github.com/bufbuild/buf/cmd/buf@v1.45.0
PATH="$PATH:$(go env GOPATH)/bin" buf generate   # from internal/harbor/attach
```

Egress: the sandbox allow-list now includes `storage.googleapis.com` (the proxy's zip host). **The generated `.pb.go` is committed**, so CI and normal builds need no codegen tool — only the runtime modules (`google.golang.org/grpc`, `google.golang.org/protobuf`), which go.mod pins and CI fetches normally. A `just`/`buf.gen.yaml` target + a DEVELOPMENT.md note document regen. Verified end-to-end in this sandbox (trivial proto → compiling `.pb.go`).

## Deferred to later slices

- **Real in-cove client** — the generalized at-switchboard runtime, in a `cmd/at-switchboard`-only package so grpc stays out of `at-cove` (which imports `internal/atswitchboard`).
- **`:443` multiplexing** with the broker (route gRPC vs broker HTTP by ALPN/content-type/path); squid CONNECT-tunnels it.
- **Token-rotation mechanics** (the reserved `RotateToken`).
- **Wake/TierChanged triggers** — the comms/escalation slice (COV-145) wires what pushes them; reserved here.
- **Real backend `Launcher`** (COV-151 `LivenessUnknown` contract), then **COV-146** dispatcher trigger.

## Docs

Update `docs/usage/harbor/coves.md` (the stream: how a cove attaches, the `runtime.listen` config, auth = token + launch secret, that the real client is a later slice) and `serve.md`'s `runtime:` block (add `listen`). Add a `docs/DEVELOPMENT.md` note on the gRPC codegen recipe (buf via `go install`, `GOPROXY=https://proxy.golang.org`, committed generated code).
