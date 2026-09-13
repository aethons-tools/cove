# harbor Attach stream (harbor-side gRPC server) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A harbor-side bidirectional gRPC **Attach** stream — a cove holds it open to stream status up (Running/Waiting/Blocked/Done + heartbeats) and receive lifecycle control down (Wake/Teardown/TierChanged) — wired to the COV-149 supervisor, hermetically tested with bufconn. Server only; real in-cove client and `:443` mux deferred.

**Architecture:** grpc is isolated in a new sub-package `internal/harbor/attach` (generated `attachpb` + the Attach server, which implements both `attachpb.RuntimeServer` and a new grpc-free `harbor.ControlSink`). The core `internal/harbor` stays grpc-free, gaining only the `ControlSink` interface, a `Supervisor.sink`/`SetControlSink`/`Heartbeat`, and an `Instance.LaunchSecretHash`. Cove dials harbor; two-factor auth = identity token (→Actor) + per-instance launch secret (→Instance).

**Tech Stack:** Go; `google.golang.org/grpc` v1.83.2 + `google.golang.org/protobuf` v1.36.12 (runtime, cached); `buf` v1.45.0 + `protoc-gen-go`/`-go-grpc` for codegen (committed output).

**Spec:** `docs/superpowers/specs/2026-09-13-harbor-attach-stream.md` (COV-153). **Foundation:** `docs/superpowers/specs/2026-09-12-harbor-cove-supervisor.md` (COV-149).

## Global Constraints

- **Boundary gates (must hold at the end):** `go list -deps ./internal/harbor | grep -i grpc` is **empty** (grpc only in `internal/harbor/attach`); `go list -deps ./cmd/at-cove | grep -iE 'oidc|grpc'` is **empty**.
- **Generated code is committed.** `internal/harbor/attach/attachpb/*.pb.go` is produced by `buf generate` and committed. CI/normal builds never run codegen — only the runtime modules (pinned in go.mod).
- **Codegen toolchain** (only when regenerating the proto): `GOPROXY=https://proxy.golang.org GOSUMDB=off GOTOOLCHAIN=local go install google.golang.org/protobuf/cmd/protoc-gen-go@latest google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest github.com/bufbuild/buf/cmd/buf@v1.45.0`, then `PATH="$PATH:$(go env GOPATH)/bin" buf generate` from `internal/harbor/attach`. Fetching deps uses `GOPROXY=https://proxy.golang.org` (the sandbox allow-lists `storage.googleapis.com`); `GOTOOLCHAIN=local` avoids a blocked toolchain auto-download.
- **Secrets never logged.** The identity token and launch secret are returned once via the admin API; never print/log either (supervisor, attach server, admin, CLI).
- **Tests hermetic** — bufconn in-process listener, temp-dir store, injected clock; no real port, no network. `go test -race` is unavailable in the sandbox — argue concurrency by construction + functional tests.
- **Append, don't overwrite** existing tests.

## File Structure

- **Create** `internal/harbor/attach/proto/attach.proto`, `internal/harbor/attach/buf.yaml`, `internal/harbor/attach/buf.gen.yaml`.
- **Create (generated, committed)** `internal/harbor/attach/attachpb/attach.pb.go`, `internal/harbor/attach/attachpb/attach_grpc.pb.go`.
- **Create** `internal/harbor/attach/attachpb/doc.go` (tiny, hand-written — package doc + a symbol-presence test target), `internal/harbor/attach/attachpb/attachpb_test.go`.
- **Modify** `go.mod` / `go.sum` (grpc + protobuf).
- **Modify** `internal/harbor/instance.go` (`LaunchSecretHash`), `internal/harbor/supervisor.go` (`ControlSink`, `sink`, `SetControlSink`, `Heartbeat`, `Raise` signature, `Teardown` nudge), `internal/harbor/supervisor_test.go` (callers + new tests), `internal/harbor/admin.go` (`CoveRaiseResult.LaunchSecret` + handler).
- **Create** `internal/harbor/attach/server.go`, `internal/harbor/attach/server_test.go`.
- **Modify** `cmd/at-harbor/config.go` (`Runtime.Listen`), `cmd/at-harbor/config_test.go`, `cmd/at-harbor/main.go` (`cmdServe` grpc startup).
- **Modify** `docs/usage/harbor/coves.md`, `docs/usage/harbor/serve.md`, `docs/DEVELOPMENT.md`, and add a `buf-gen` recipe to the `justfile`.

---

## Task 1: Proto + generated code + runtime deps

**Files:** create `internal/harbor/attach/proto/attach.proto`, `buf.yaml`, `buf.gen.yaml`, the committed `attachpb/*.pb.go`, `attachpb/doc.go`, `attachpb/attachpb_test.go`; modify `go.mod`/`go.sum`, `justfile`.

**Interfaces — Produces:** the `attachpb` package with `RuntimeServer`/`UnimplementedRuntimeServer`/`RegisterRuntimeServer`, `Runtime_AttachServer`/`Runtime_AttachClient`, `NewRuntimeClient`, `StatusUp`(+`StatusUp_Status`/`StatusUp_Heartbeat`), `Activity_{RUNNING,WAITING,BLOCKED,DONE}`, `Heartbeat`, `ControlDown`(+`ControlDown_Wake`/`_Teardown`/`_Tier`/`_Rotate`), `Wake`/`Teardown`/`TierChanged`/`RotateToken`.

- [ ] **Step 1: Write the proto** — `internal/harbor/attach/proto/attach.proto`:

```proto
syntax = "proto3";
package harbor.attach.v1;
option go_package = "github.com/aethons-tools/cove/internal/harbor/attach/attachpb";

service Runtime {
  rpc Attach(stream StatusUp) returns (stream ControlDown);
}

message StatusUp {
  oneof msg {
    Activity  status    = 1;
    Heartbeat heartbeat = 2;
  }
}
enum Activity {
  ACTIVITY_UNSPECIFIED = 0;
  RUNNING = 1;
  WAITING = 2;
  BLOCKED = 3;
  DONE    = 4;
}
message Heartbeat {}

message ControlDown {
  oneof msg {
    Wake        wake     = 1;
    Teardown    teardown = 2;
    TierChanged tier     = 3;
    RotateToken rotate   = 4; // reserved
  }
}
message Wake {}
message Teardown    { string reason = 1; }
message TierChanged { int32 tier = 1; }
message RotateToken { string token = 1; }
```

- [ ] **Step 2: buf config** — `internal/harbor/attach/buf.yaml`:

```yaml
version: v2
modules:
  - path: proto
```
and `internal/harbor/attach/buf.gen.yaml`:
```yaml
version: v2
plugins:
  - local: protoc-gen-go
    out: .
    opt: paths=source_relative
  - local: protoc-gen-go-grpc
    out: .
    opt: paths=source_relative
```
(With `go_package` set to the full import path and `out: .`, buf writes `attachpb/attach.pb.go` and `attachpb/attach_grpc.pb.go` relative to the `attach` dir.)

- [ ] **Step 3: Install the toolchain and generate**

```bash
export GOPROXY=https://proxy.golang.org GOSUMDB=off GOTOOLCHAIN=local
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
go install github.com/bufbuild/buf/cmd/buf@v1.45.0
cd internal/harbor/attach && PATH="$PATH:$(go env GOPATH)/bin" buf generate && cd -
```
Expected: `internal/harbor/attach/attachpb/attach.pb.go` and `attach_grpc.pb.go` exist. If `buf generate` writes to an unexpected path, adjust `out`/`go_package` so the files land in `internal/harbor/attach/attachpb/`.

- [ ] **Step 4: Add the runtime deps + a doc file**

Create `internal/harbor/attach/attachpb/doc.go`:
```go
// Package attachpb holds the generated gRPC types for the managed-cove Attach
// stream (harbor.attach.v1). Generated by `buf generate` from ../proto/attach.proto;
// do not edit by hand. Regenerate per docs/DEVELOPMENT.md.
package attachpb
```
Then wire the module deps:
```bash
export GOPROXY=https://proxy.golang.org GOSUMDB=off GOTOOLCHAIN=local GOFLAGS=-mod=mod
go get google.golang.org/grpc@v1.83.2 google.golang.org/protobuf@v1.36.12
go mod tidy
```

- [ ] **Step 5: Write a presence test** — `internal/harbor/attach/attachpb/attachpb_test.go`:

```go
package attachpb

import "testing"

// Guards that the generated surface the server depends on exists and has the
// expected shape (a cheap canary against a bad/partial regen).
func TestGeneratedSurface(t *testing.T) {
	// Activity enum values map to the proto.
	if Activity_RUNNING == Activity_DONE {
		t.Fatal("activity enum collapsed")
	}
	// StatusUp oneof wrappers.
	_ = &StatusUp{Msg: &StatusUp_Status{Status: Activity_WAITING}}
	_ = &StatusUp{Msg: &StatusUp_Heartbeat{Heartbeat: &Heartbeat{}}}
	// ControlDown oneof wrappers, including the reserved RotateToken.
	_ = &ControlDown{Msg: &ControlDown_Teardown{Teardown: &Teardown{Reason: "x"}}}
	_ = &ControlDown{Msg: &ControlDown_Wake{Wake: &Wake{}}}
	_ = &ControlDown{Msg: &ControlDown_Rotate{Rotate: &RotateToken{}}}
	// Server registration symbol exists.
	var _ = RegisterRuntimeServer
	var _ RuntimeServer = (*UnimplementedRuntimeServer)(nil)
}
```

- [ ] **Step 6: Build + test**

Run: `go build ./... && go test ./internal/harbor/attach/attachpb/`
Expected: PASS.

- [ ] **Step 7: Add a regen recipe to `justfile`** — a `buf-gen` recipe documenting the commands from Step 3 (so regen is one command). Keep it consistent with existing recipe style.

- [ ] **Step 8: Commit**

```bash
git add internal/harbor/attach go.mod go.sum justfile
git commit -m "harbor/attach: Attach proto + generated gRPC code + runtime deps (COV-153)"
```

---

## Task 2: Core harbor changes (ControlSink, Heartbeat, launch secret)

**Files:** `internal/harbor/instance.go`, `internal/harbor/supervisor.go`, `internal/harbor/supervisor_test.go`, `internal/harbor/admin.go`. Stays grpc-free.

**Interfaces:**
- Produces: `ControlSink` interface; `Supervisor.SetControlSink`; `Supervisor.Heartbeat(actorID) error`; `Instance.LaunchSecretHash`; `Raise(ctx, RaiseSpec) (Instance, token, launchSecret string, err error)`; `CoveRaiseResult.LaunchSecret`.
- Consumes: COV-149 `Supervisor`, `Enroll`, `MintToken`/`HashToken`, `Instance`, admin cove handler.

- [ ] **Step 1: Write the failing tests** (append to `internal/harbor/supervisor_test.go`):

```go
// fakeSink records ControlSink calls for assertions.
type fakeSink struct {
	teardowns []string
	wakes     []string
}
func (f *fakeSink) RequestTeardown(id string) { f.teardowns = append(f.teardowns, id) }
func (f *fakeSink) Wake(id string)            { f.wakes = append(f.wakes, id) }

func TestRaiseMintsLaunchSecret(t *testing.T) {
	sup, store, _ := supTestKit(t, &fakeLauncher{liveness: LivenessAlive})
	inst, tok, secret, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest"})
	if err != nil {
		t.Fatal(err)
	}
	if tok == "" || secret == "" {
		t.Fatalf("expected token AND launch secret, got tok=%q secret=%q", tok, secret)
	}
	// The hash is stored, never the plaintext.
	if inst.LaunchSecretHash != HashToken(secret) {
		t.Fatalf("launch secret hash not stored correctly")
	}
	if inst.LaunchSecretHash == secret {
		t.Fatal("stored the plaintext launch secret")
	}
	got, _ := store.GetInstance("w1")
	if got.LaunchSecretHash != HashToken(secret) {
		t.Fatal("persisted instance missing launch secret hash")
	}
}

func TestHeartbeatRenewsWithoutChangingActivity(t *testing.T) {
	sup, store, clk := supTestKit(t, &fakeLauncher{liveness: LivenessAlive})
	sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest"})
	sup.Report(context.Background(), "w1", ActivityWaiting)
	*clk = clk.Add(10 * time.Second) // 1010
	if err := sup.Heartbeat("w1"); err != nil {
		t.Fatal(err)
	}
	got, _ := store.GetInstance("w1")
	if got.Activity != ActivityWaiting {
		t.Fatalf("heartbeat changed activity: %s", got.Activity)
	}
	if !got.Lease.Expiry.Equal(time.Unix(1070, 0).UTC()) { // 1010 + 60
		t.Fatalf("heartbeat did not renew lease: %+v", got.Lease)
	}
	if !got.LastSeen.Equal(time.Unix(1010, 0).UTC()) {
		t.Fatalf("heartbeat did not bump LastSeen: %v", got.LastSeen)
	}
}

func TestHeartbeatErrorsForAbsentInstance(t *testing.T) {
	sup, _, _ := supTestKit(t, &fakeLauncher{})
	if err := sup.Heartbeat("ghost"); err == nil {
		t.Fatal("expected error for absent instance")
	}
}

func TestTeardownNudgesSink(t *testing.T) {
	sup, _, _ := supTestKit(t, &fakeLauncher{liveness: LivenessAlive})
	sink := &fakeSink{}
	sup.SetControlSink(sink)
	sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest"})
	if err := sup.Teardown(context.Background(), "w1"); err != nil {
		t.Fatal(err)
	}
	if len(sink.teardowns) != 1 || sink.teardowns[0] != "w1" {
		t.Fatalf("teardown did not nudge the sink: %+v", sink.teardowns)
	}
}
```

Also UPDATE existing `sup.Raise(...)` callers in this file to the new 4-value signature: every `inst, tok, err := sup.Raise(...)` → `inst, tok, _, err := sup.Raise(...)`, and every bare `sup.Raise(...)` stays (return values ignored). Grep `sup.Raise(` to find them all; do not change their assertions otherwise.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/harbor/ -run 'TestRaiseMints|TestHeartbeat|TestTeardownNudges' -v`
Expected: compile error (signature / ControlSink / Heartbeat undefined) → after stubs, FAIL.

- [ ] **Step 3: `instance.go`** — add the field to `Instance`:

```go
	Lease    Lease     `json:"lease"`
	LaunchSecretHash string `json:"launch_secret_hash,omitempty"` // hash of the per-instance launch secret (COV-153)
	RaisedAt time.Time `json:"raised_at"`
```

- [ ] **Step 4: `supervisor.go`** — add `ControlSink`, the `sink` field + setter, `Heartbeat`, the `Raise` signature + launch-secret mint, and the `Teardown` nudge.

Add the interface + field:
```go
// ControlSink pushes lifecycle control to a connected cove (implemented by the
// Attach server). Best-effort and non-blocking; no connected stream is a no-op.
// nil when no stream server runs (slice-1 behavior).
type ControlSink interface {
	RequestTeardown(actorID string)
	Wake(actorID string)
}
```
In `Supervisor` add `sink ControlSink` (after `log`). Add:
```go
// SetControlSink installs the control sink after construction (resolving the
// supervisor↔Attach-server cycle). nil-safe throughout.
func (s *Supervisor) SetControlSink(sink ControlSink) { s.sink = sink }
```
Change `Raise` to mint + store + return the launch secret (mint it BEFORE `Launcher.Raise`, so a mint failure rolls back only the enrollment):
```go
func (s *Supervisor) Raise(ctx context.Context, spec RaiseSpec) (Instance, string, string, error) {
	if spec.ActorID == "" {
		return Instance{}, "", "", fmt.Errorf("actor id is required")
	}
	tok, err := Enroll(s.store, spec.ActorID, spec.Project, spec.Role, nil, s.now())
	if err != nil {
		return Instance{}, "", "", err
	}
	secret, err := MintToken()
	if err != nil {
		_ = s.store.RemoveActor(spec.ActorID)
		return Instance{}, "", "", err
	}
	loc, err := s.launcher.Raise(ctx, spec)
	if err != nil {
		_ = s.store.RemoveActor(spec.ActorID)
		return Instance{}, "", "", fmt.Errorf("raise: %w", err)
	}
	now := s.now()
	inst := Instance{
		ActorID: spec.ActorID, Project: orDefaultProject(spec.Project), Role: spec.Role, Unit: spec.Unit,
		Location: loc, Phase: PhaseLive, Activity: ActivityRunning,
		Lease:            Lease{Holder: s.holder, Expiry: now.Add(s.ttl)},
		LaunchSecretHash: HashToken(secret),
		RaisedAt:         now, LastSeen: now,
	}
	if err := s.store.PutInstance(inst); err != nil {
		_ = s.launcher.Teardown(ctx, inst)
		_ = s.store.RemoveActor(spec.ActorID)
		return Instance{}, "", "", err
	}
	if s.log != nil {
		s.log.Info("cove raised", "id", spec.ActorID, "project", inst.Project, "role", spec.Role, "phase", string(inst.Phase))
	}
	return inst, tok, secret, nil
}
```
Add `Heartbeat`:
```go
// Heartbeat renews the lease + LastSeen for a connected cove WITHOUT changing
// Activity or Phase (the stream keepalive path). Errors if the instance is
// absent or gone.
func (s *Supervisor) Heartbeat(actorID string) error {
	inst, ok := s.store.GetInstance(actorID)
	if !ok {
		return fmt.Errorf("no instance for actor %q", actorID)
	}
	if inst.Phase == PhaseGone {
		return fmt.Errorf("instance %q is gone", actorID)
	}
	now := s.now()
	inst.LastSeen = now
	inst.Lease = Lease{Holder: s.holder, Expiry: now.Add(s.ttl)}
	return s.store.PutInstance(inst)
}
```
In `Teardown`, add the best-effort nudge right after the idempotent early-return (before marking Terminating):
```go
	inst, ok := s.store.GetInstance(actorID)
	if !ok {
		return nil
	}
	if s.sink != nil {
		s.sink.RequestTeardown(actorID) // best-effort cooperative nudge
	}
```

- [ ] **Step 5: `admin.go`** — carry the launch secret in the raise result.

Add to `CoveRaiseResult`:
```go
	Token    string `json:"token"`
	LaunchSecret string `json:"launch_secret"`
	Phase    string `json:"phase"`
```
Update the `POST /admin/coves` handler's `sup.Raise` call:
```go
		inst, tok, secret, err := sup.Raise(r.Context(), RaiseSpec{ID... })
		...
		writeJSON(w, http.StatusCreated, CoveRaiseResult{ID: b.ID, Token: tok, LaunchSecret: secret, Phase: string(inst.Phase), Location: inst.Location})
```
(The raise log line stays id/project/role only — never the token or secret.)

- [ ] **Step 6: Run tests**

Run: `go test ./internal/harbor/...`
Expected: PASS (new tests + all prior). Fix any remaining `sup.Raise` call sites the compiler flags.

- [ ] **Step 7: Verify the boundary still holds**

Run: `go list -deps ./internal/harbor | grep -i grpc || echo CLEAN`
Expected: `CLEAN`.

- [ ] **Step 8: Commit**

```bash
git add internal/harbor/instance.go internal/harbor/supervisor.go internal/harbor/supervisor_test.go internal/harbor/admin.go
git commit -m "harbor: ControlSink + Heartbeat + per-instance launch secret (COV-153)"
```

---

## Task 3: The Attach gRPC server

**Files:** create `internal/harbor/attach/server.go`, `internal/harbor/attach/server_test.go`.

**Interfaces:**
- Consumes: `attachpb` (Task 1), `harbor.{Store,Supervisor,HashToken,Activity*,ControlSink}` (Tasks 1-2).
- Produces: `attach.Server` implementing `attachpb.RuntimeServer` + `harbor.ControlSink`; `NewServer(store, sup, log) *Server`.

- [ ] **Step 1: Write the failing tests** — `internal/harbor/attach/server_test.go`:

```go
package attach

import (
	"context"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/aethons-tools/cove/internal/harbor"
	"github.com/aethons-tools/cove/internal/harbor/attach/attachpb"
)

// aliveLauncher is a no-backend launcher for the supervisor under test.
type aliveLauncher struct{}
func (aliveLauncher) Raise(context.Context, harbor.RaiseSpec) (string, error)      { return "fake", nil }
func (aliveLauncher) Teardown(context.Context, harbor.Instance) error              { return nil }
func (aliveLauncher) Probe(context.Context, harbor.Instance) (harbor.Liveness, error) { return harbor.LivenessAlive, nil }

// harness raises one instance and starts an in-memory Attach server. Returns the
// store, supervisor, server, a dial func, and the raised actor's token + launch secret.
func harness(t *testing.T) (harbor.Store, *harbor.Supervisor, *Server, func() *grpc.ClientConn, string, string) {
	t.Helper()
	store, err := harbor.NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err != nil {
		t.Fatal(err)
	}
	store.PutRole("default", harbor.Role{Name: "guest", Scope: harbor.Scope{Destinations: []string{"anthropic"}, TTL: time.Hour}})
	sup := harbor.NewSupervisor(store, aliveLauncher{}, "holder-test", time.Minute, 30*time.Second, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	_, tok, secret, err := sup.Raise(context.Background(), harbor.RaiseSpec{ActorID: "w1", Role: "guest"})
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(store, sup, slog.New(slog.NewTextHandler(io.Discard, nil)))
	sup.SetControlSink(srv)

	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer()
	attachpb.RegisterRuntimeServer(gs, srv)
	go gs.Serve(lis)
	t.Cleanup(gs.Stop)

	dial := func() *grpc.ClientConn {
		cc, err := grpc.NewClient("passthrough:///bufnet",
			grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) { return lis.DialContext(context.Background()) }),
			grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			t.Fatal(err)
		}
		return cc
	}
	return store, sup, srv, dial, tok, secret
}

func authCtx(token, secret string) context.Context {
	return metadata.NewOutgoingContext(context.Background(), metadata.Pairs(
		"authorization", "Bearer "+token, "x-harbor-launch-secret", secret))
}

// eventually polls cond up to ~1s (10ms steps) — for the server's async recv loop.
func eventually(cond func() bool) bool {
	for i := 0; i < 100; i++ {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}

func TestAttachAuthAndStatus(t *testing.T) {
	store, _, _, dial, tok, secret := harness(t)
	cc := dial()
	defer cc.Close()
	stream, err := attachpb.NewRuntimeClient(cc).Attach(authCtx(tok, secret))
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&attachpb.StatusUp{Msg: &attachpb.StatusUp_Status{Status: attachpb.Activity_WAITING}}); err != nil {
		t.Fatal(err)
	}
	// The server's Report runs in its recv loop — poll the store.
	if !eventually(func() bool {
		inst, ok := store.GetInstance("w1")
		return ok && inst.Activity == harbor.ActivityWaiting
	}) {
		inst, _ := store.GetInstance("w1")
		t.Fatalf("activity never became waiting: %+v", inst)
	}
}
```

Full set of tests to implement (all via the harness + bufconn; each binds the leading `store`/`srv` it needs from `harness`):
- `TestAttachAuthAndStatus` — valid token+secret; send `Activity(WAITING)`; `eventually` the store shows `Activity==waiting` and the lease renewed.
- `TestAttachRejectsBadAuth` — three sub-cases, each expects `codes.Unauthenticated` on the first `Recv`/`Send`: (a) no metadata, (b) wrong token, (c) wrong launch secret. (Auth errors surface when the stream is first used.)
- `TestAttachHeartbeatRenews` — send `Heartbeat`; `eventually` lease renewed, `Activity` unchanged.
- `TestControlDownDelivery` — `srv.RequestTeardown("w1")`; the client `Recv()` returns a `*attachpb.ControlDown_Teardown`.
- `TestReconnectReplaces` — open a second Attach for `w1`; the first stream's `Recv` ends (io.EOF / error) — the old connection is superseded.
- `TestDoneTearsDown` — send `Activity(DONE)`; `eventually` `store.GetInstance("w1")` is gone.
- `TestStreamDropDeregisters` — open, then `cc.Close()`; `eventually` `srv.connected("w1") == false` (add a tiny test-only exported helper `func (s *Server) connected(id string) bool`), and a subsequent `srv.RequestTeardown("w1")` does not panic.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/harbor/attach/ -run TestAttach -v`
Expected: compile failure (NewServer/Server undefined).

- [ ] **Step 3: Implement `internal/harbor/attach/server.go`**

```go
// Package attach is the harbor-side managed-cove Attach gRPC stream: a cove dials
// in and holds one bidirectional stream — StatusUp from the cove (activity +
// heartbeats), ControlDown from harbor (lifecycle control). grpc is isolated to
// this package; internal/harbor stays grpc-free.
package attach

import (
	"context"
	"log/slog"
	"strings"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/aethons-tools/cove/internal/harbor"
	"github.com/aethons-tools/cove/internal/harbor/attach/attachpb"
)

const (
	mdAuthorization = "authorization"
	mdLaunchSecret  = "x-harbor-launch-secret"
	sendBuffer      = 8
)

type Server struct {
	attachpb.UnimplementedRuntimeServer
	store harbor.Store
	sup   *harbor.Supervisor
	log   *slog.Logger
	mu    sync.Mutex
	conns map[string]chan *attachpb.ControlDown
}

func NewServer(store harbor.Store, sup *harbor.Supervisor, log *slog.Logger) *Server {
	return &Server{store: store, sup: sup, log: log, conns: map[string]chan *attachpb.ControlDown{}}
}

// authenticate resolves the actorID from stream metadata: bearer identity token
// (→ Actor) AND the per-instance launch secret (→ Instance.LaunchSecretHash).
func (s *Server) authenticate(ctx context.Context) (string, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", status.Error(codes.Unauthenticated, "missing metadata")
	}
	token, secret := bearer(md.Get(mdAuthorization)), first(md.Get(mdLaunchSecret))
	if token == "" || secret == "" {
		return "", status.Error(codes.Unauthenticated, "missing identity token or launch secret")
	}
	actor, ok := s.store.Lookup(harbor.HashToken(token))
	if !ok {
		return "", status.Error(codes.Unauthenticated, "unknown identity")
	}
	inst, ok := s.store.GetInstance(actor.ID)
	if !ok {
		return "", status.Error(codes.Unauthenticated, "no live instance")
	}
	if inst.LaunchSecretHash == "" || harbor.HashToken(secret) != inst.LaunchSecretHash {
		return "", status.Error(codes.Unauthenticated, "bad launch secret")
	}
	return actor.ID, nil
}

func (s *Server) Attach(stream attachpb.Runtime_AttachServer) error {
	actorID, err := s.authenticate(stream.Context())
	if err != nil {
		return err
	}
	ch := s.register(actorID)
	defer s.deregister(actorID, ch)
	_ = s.sup.Heartbeat(actorID) // the cove is talking to us now

	go func() { // send loop
		for {
			select {
			case <-stream.Context().Done():
				return
			case cd, ok := <-ch:
				if !ok {
					return
				}
				if err := stream.Send(cd); err != nil {
					return
				}
			}
		}
	}()

	for { // recv loop
		msg, err := stream.Recv()
		if err != nil {
			return err // EOF / client gone; defer deregisters
		}
		switch m := msg.GetMsg().(type) {
		case *attachpb.StatusUp_Status:
			if act, ok := fromPBActivity(m.Status); ok {
				_ = s.sup.Report(stream.Context(), actorID, act)
			}
		case *attachpb.StatusUp_Heartbeat:
			_ = s.sup.Heartbeat(actorID)
		}
	}
}

// register installs a fresh send channel for actorID, superseding (and closing)
// any prior one — a reconnect replaces the old stream (the lease-steal analog).
// The map always points at an OPEN channel.
func (s *Server) register(actorID string) chan *attachpb.ControlDown {
	ch := make(chan *attachpb.ControlDown, sendBuffer)
	s.mu.Lock()
	old := s.conns[actorID]
	s.conns[actorID] = ch
	s.mu.Unlock()
	if old != nil {
		close(old) // old send loop sees !ok and exits; map no longer references it
	}
	return ch
}

func (s *Server) deregister(actorID string, ch chan *attachpb.ControlDown) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conns[actorID] == ch {
		delete(s.conns, actorID)
		close(ch)
	}
}

// enqueue is the ControlSink delivery primitive: non-blocking send under the lock
// (so the channel can't be closed mid-send); drop if full or no stream.
func (s *Server) enqueue(actorID string, cd *attachpb.ControlDown) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch, ok := s.conns[actorID]
	if !ok {
		return
	}
	select {
	case ch <- cd:
	default: // buffer full — drop (best-effort)
	}
}

// RequestTeardown and Wake implement harbor.ControlSink.
func (s *Server) RequestTeardown(actorID string) {
	s.enqueue(actorID, &attachpb.ControlDown{Msg: &attachpb.ControlDown_Teardown{Teardown: &attachpb.Teardown{}}})
}
func (s *Server) Wake(actorID string) {
	s.enqueue(actorID, &attachpb.ControlDown{Msg: &attachpb.ControlDown_Wake{Wake: &attachpb.Wake{}}})
}

func fromPBActivity(a attachpb.Activity) (harbor.Activity, bool) {
	switch a {
	case attachpb.Activity_RUNNING:
		return harbor.ActivityRunning, true
	case attachpb.Activity_WAITING:
		return harbor.ActivityWaiting, true
	case attachpb.Activity_BLOCKED:
		return harbor.ActivityBlocked, true
	case attachpb.Activity_DONE:
		return harbor.ActivityDone, true
	}
	return "", false
}

func first(vals []string) string {
	if len(vals) > 0 {
		return vals[0]
	}
	return ""
}
func bearer(vals []string) string {
	v := first(vals)
	const p = "bearer "
	if len(v) >= len(p) && strings.EqualFold(v[:len(p)], p) {
		return strings.TrimSpace(v[len(p):])
	}
	return v
}
```

Add the test-only helper (in `server.go`, it's tiny and generally useful):
```go
// connected reports whether a live Attach stream is registered for actorID.
func (s *Server) connected(actorID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.conns[actorID]
	return ok
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/harbor/attach/`
Expected: PASS (all bufconn tests).

- [ ] **Step 5: Confirm `Server` satisfies `harbor.ControlSink`** — add a compile-time assertion in `server.go`:
```go
var _ harbor.ControlSink = (*Server)(nil)
```

- [ ] **Step 6: Commit**

```bash
git add internal/harbor/attach/server.go internal/harbor/attach/server_test.go
git commit -m "harbor/attach: Attach gRPC server — auth, stream registry, status↔supervisor bridge (COV-153)"
```

---

## Task 4: serve wiring (`runtime.listen`)

**Files:** `cmd/at-harbor/config.go`, `cmd/at-harbor/config_test.go`, `cmd/at-harbor/main.go`.

**Interfaces:** Produces `serveConfig.Runtime.Listen`. Consumes `attach.NewServer`, `attachpb.RegisterRuntimeServer`, `grpc.NewServer`, `sup.SetControlSink`.

- [ ] **Step 1: Write the failing config test** (append to `cmd/at-harbor/config_test.go`):

```go
func TestRuntimeListenParsed(t *testing.T) {
	c, err := parseServeConfig([]byte("runtime:\n  listen: 127.0.0.1:9090\n  lease-ttl: 1m\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Runtime.Listen != "127.0.0.1:9090" {
		t.Fatalf("runtime.listen = %q", c.Runtime.Listen)
	}
	// runtime is still a known key (no unknown-key warning).
	if got := unknownServeKeys([]byte("runtime:\n  listen: :9090\n")); len(got) != 0 {
		t.Fatalf("unknown keys = %v", got)
	}
}
```

- [ ] **Step 2: Run to verify failure** — `go test ./cmd/at-harbor/ -run TestRuntimeListen -v` → FAIL (field missing).

- [ ] **Step 3: `config.go`** — add `Listen` to the `Runtime` struct:
```go
	Runtime struct {
		Listen            string `yaml:"listen"`
		LeaseTTL          string `yaml:"lease-ttl"`
		ReconcileInterval string `yaml:"reconcile-interval"`
	} `yaml:"runtime"`
```

- [ ] **Step 4: `main.go` `cmdServe`** — start the gRPC server when `runtime.listen` is set. After `sup := harbor.NewSupervisor(...)` and `go sup.Run(...)`, add:

```go
	if cfg.Runtime.Listen != "" {
		rsrv := attach.NewServer(st, sup, log)
		sup.SetControlSink(rsrv)
		gs := grpc.NewServer()
		attachpb.RegisterRuntimeServer(gs, rsrv)
		lis, err := net.Listen("tcp", cfg.Runtime.Listen)
		if err != nil {
			fmt.Fprintln(stderr, "at-harbor:", err)
			return 1
		}
		go func() {
			log.Info("harbor runtime (Attach) listening", "addr", cfg.Runtime.Listen)
			if err := gs.Serve(lis); err != nil {
				log.Error("runtime server stopped", "err", err.Error())
			}
		}()
	}
```
Add imports: `net`, `google.golang.org/grpc`, `github.com/aethons-tools/cove/internal/harbor/attach`, `github.com/aethons-tools/cove/internal/harbor/attach/attachpb`.

- [ ] **Step 5: Build + test**

Run: `go build ./... && go test ./cmd/at-harbor/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/at-harbor/config.go cmd/at-harbor/config_test.go cmd/at-harbor/main.go
git commit -m "harbor: serve wiring for the Attach runtime server (runtime.listen) (COV-153)"
```

---

## Task 5: Docs + boundary gates + final verification

**Files:** `docs/usage/harbor/coves.md`, `docs/usage/harbor/serve.md`, `docs/DEVELOPMENT.md`.

- [ ] **Step 1: `coves.md`** — add a short "The Attach stream" section: a managed cove holds a bidirectional gRPC stream to harbor; it authenticates with its identity token **and** its per-instance launch secret; it streams activity up (and heartbeats to renew its lease) and receives lifecycle control (teardown/wake) down; harbor binds it at `runtime.listen`. State plainly that **this slice is the harbor-side server** — the real in-cove client and `:443` multiplexing are later slices. Link `serve.md` for `runtime.listen`. Bump `updated:`.

- [ ] **Step 2: `serve.md`** — add `listen` to the `runtime:` block example + a config-table row (`runtime.listen` — address the Attach gRPC server binds; omit to disable; `:443` mux is a later slice). Bump `updated:`.

- [ ] **Step 3: `DEVELOPMENT.md`** — add a "Regenerating gRPC code" note: the `just buf-gen` recipe, the `GOPROXY=https://proxy.golang.org`/`GOTOOLCHAIN=local` requirement, that `storage.googleapis.com` must be allow-listed, and that generated `.pb.go` is committed (CI never regenerates).

- [ ] **Step 4: Docs audit (delta)**

Run: `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs --index OVERVIEW.md`
Confirm no NEW orphans/dangling-links/oversize/duplication vs. the baseline (the spec carries a pre-existing orphan baseline; compare the delta). All edited docs' links resolve.

- [ ] **Step 5: Boundary gates + full suite**

```bash
go build ./... && go test ./...
go list -deps ./internal/harbor | grep -i grpc || echo CLEAN   # expect CLEAN
go list -deps ./cmd/at-cove | grep -iE 'oidc|grpc' || echo CLEAN # expect CLEAN
```
Expected: tests PASS; both gates CLEAN.

- [ ] **Step 6: Commit**

```bash
git add docs/usage/harbor/coves.md docs/usage/harbor/serve.md docs/DEVELOPMENT.md
git commit -m "docs(harbor): Attach stream + gRPC codegen notes (COV-153)"
```

---

## Final steps (after all tasks)

- [ ] Full suite green (`go test ./...`), both boundary gates CLEAN.
- [ ] Dispatch the final whole-branch review (most-capable model), then `superpowers:finishing-a-development-branch`.
