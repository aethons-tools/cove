# cove-master in-cove Attach client — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** An in-cove Attach **client** (`internal/covemaster`) + a `cove-master` binary that dials harbor's runtime listener, authenticates (identity token + launch secret), streams activity up and reacts to control down (reconnecting across drops), driving an injected `Workload` — shipped this slice with a stub workload.

**Architecture:** grpc-isolated client library depending only on `attachpb` + grpc (never `internal/harbor`); a `Workload` seam with two self-termination paths (Done, Teardown); `cmd/cove-master` wires env → client → stub workload. The real agent wrapper + the image/entrypoint/account restructure are deferred.

**Tech Stack:** Go; `google.golang.org/grpc` + `attachpb` (from COV-153, already in go.mod).

**Spec:** `docs/superpowers/specs/2026-09-13-cove-master-client.md` (COV-155).

## Global Constraints

- **Boundary:** `go list -deps ./cmd/at-cove | grep -iE 'oidc|grpc'` stays **empty**. `internal/covemaster` imports only `attachpb` + grpc + stdlib — **never** `internal/harbor` (tests may import it).
- **No SSH / no host orchestration:** the client needs only `AT_HARBOR_RUNTIME_ADDR`, `AT_HARBOR_IDENTITY_TOKEN`, `AT_HARBOR_LAUNCH_SECRET` from its environment.
- **Auth failure is fatal** (no retry); transient stream drops **reconnect with backoff**, re-authenticating each time.
- **Secrets never logged** — the token and launch secret appear only in gRPC metadata, never in a log line.
- Tests hermetic (bufconn); `go test -race` unavailable — argue concurrency by construction.
- Append, don't overwrite tests.

## File Structure

- **Create** `internal/covemaster/covemaster.go` (types + `New` + `toPBActivity`), `internal/covemaster/client.go` (`Run` + `session`), `internal/covemaster/client_test.go` (bufconn round-trip).
- **Create** `cmd/cove-master/main.go` (env + stub workload + signals), `cmd/cove-master/main_test.go`.
- **Create** `internal/covemaster/integration_test.go` (`//go:build integration`).
- **Modify** `docs/usage/harbor/coves.md`.

---

## Task 1: The client library (`internal/covemaster`)

**Files:** create `internal/covemaster/covemaster.go`, `internal/covemaster/client.go`, `internal/covemaster/client_test.go`.

**Interfaces — Produces:** `Activity` (Running/Waiting/Blocked/Done), `ControlKind` (Wake/Teardown), `Control`, `Handle` (`Report(Activity)`), `Workload` (`Run(ctx,Handle) error`, `Control(Control)`), `Config`, `Client`, `New(Config,*slog.Logger) *Client`, `(*Client) Run(ctx, Workload) error`, `(*Client) Report(Activity)`.

- [ ] **Step 1: Write the failing test** — `internal/covemaster/client_test.go`:

```go
package covemaster

import (
	"context"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/aethons-tools/cove/internal/harbor"
	"github.com/aethons-tools/cove/internal/harbor/attach"
	"github.com/aethons-tools/cove/internal/harbor/attach/attachpb"
)

type aliveLauncher struct{}
func (aliveLauncher) Raise(context.Context, harbor.RaiseSpec) (string, error)         { return "fake", nil }
func (aliveLauncher) Teardown(context.Context, harbor.Instance) error                 { return nil }
func (aliveLauncher) Probe(context.Context, harbor.Instance) (harbor.Liveness, error) { return harbor.LivenessAlive, nil }

// serverHarness raises one instance and starts an in-memory attach server.
// Returns the store, the attach server, a DialOption that reaches it, and the
// raised actor's token + launch secret.
func serverHarness(t *testing.T) (harbor.Store, *attach.Server, grpc.DialOption, string, string) {
	t.Helper()
	store, err := harbor.NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err != nil { t.Fatal(err) }
	store.PutRole("default", harbor.Role{Name: "guest", Scope: harbor.Scope{Destinations: []string{"anthropic"}, TTL: time.Hour}})
	sup := harbor.NewSupervisor(store, aliveLauncher{}, "holder-test", time.Minute, 30*time.Second, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	_, tok, secret, err := sup.Raise(context.Background(), harbor.RaiseSpec{ActorID: "w1", Role: "guest"})
	if err != nil { t.Fatal(err) }
	srv := attach.NewServer(store, sup, slog.New(slog.NewTextHandler(io.Discard, nil)))
	sup.SetControlSink(srv)

	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer()
	attachpb.RegisterRuntimeServer(gs, srv)
	go gs.Serve(lis)
	t.Cleanup(gs.Stop)
	dialOpt := grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) { return lis.DialContext(context.Background()) })
	return store, srv, dialOpt, tok, secret
}

func eventually(cond func() bool) bool {
	for i := 0; i < 200; i++ { if cond() { return true }; time.Sleep(10 * time.Millisecond) }
	return cond()
}

// blockWorkload reports one activity then blocks until ctx is cancelled.
type blockWorkload struct { first Activity; controls chan Control }
func (b *blockWorkload) Run(ctx context.Context, h Handle) error { h.Report(b.first); <-ctx.Done(); return ctx.Err() }
func (b *blockWorkload) Control(c Control)                        { select { case b.controls <- c: default: } }

func TestClientReportsActivity(t *testing.T) {
	store, _, dial, tok, secret := serverHarness(t)
	c := New(Config{Addr: "bufnet", Token: tok, LaunchSecret: secret, Heartbeat: 20 * time.Millisecond,
		DialOptions: []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials()), dial}}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx, &blockWorkload{first: Waiting, controls: make(chan Control, 4)}) }()
	if !eventually(func() bool { inst, ok := store.GetInstance("w1"); return ok && inst.Activity == harbor.ActivityWaiting }) {
		inst, _ := store.GetInstance("w1"); t.Fatalf("activity never reached waiting: %+v", inst)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}
}

func TestClientTeardownFromServer(t *testing.T) {
	_, srv, dial, tok, secret := serverHarness(t)
	w := &blockWorkload{first: Running, controls: make(chan Control, 4)}
	c := New(Config{Addr: "bufnet", Token: tok, LaunchSecret: secret, Heartbeat: 50 * time.Millisecond,
		DialOptions: []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials()), dial}}, nil)
	done := make(chan error, 1)
	go func() { done <- c.Run(context.Background(), w) }()
	// wait until attached, then push teardown.
	time.Sleep(100 * time.Millisecond)
	srv.RequestTeardown("w1")
	select {
	case c := <-w.controls:
		if c.Kind != Teardown { t.Fatalf("got control %v, want Teardown", c.Kind) }
	case <-time.After(2 * time.Second):
		t.Fatal("workload never got Teardown control")
	}
	select {
	case err := <-done:
		if err != nil { t.Fatalf("Run returned error on teardown: %v", err) }
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after teardown")
	}
}

// doneWorkload finishes immediately → client reports Done.
type doneWorkload struct{}
func (doneWorkload) Run(context.Context, Handle) error { return nil }
func (doneWorkload) Control(Control)                   {}

func TestClientDoneTearsDownInstance(t *testing.T) {
	store, _, dial, tok, secret := serverHarness(t)
	c := New(Config{Addr: "bufnet", Token: tok, LaunchSecret: secret, Heartbeat: 50 * time.Millisecond,
		DialOptions: []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials()), dial}}, nil)
	if err := c.Run(context.Background(), doneWorkload{}); err != nil { t.Fatalf("Run: %v", err) }
	if !eventually(func() bool { _, ok := store.GetInstance("w1"); return !ok }) {
		t.Fatal("instance not torn down after Done")
	}
}

func TestClientAuthFailureIsFatal(t *testing.T) {
	_, _, dial, tok, _ := serverHarness(t)
	c := New(Config{Addr: "bufnet", Token: tok, LaunchSecret: "WRONG-SECRET", Heartbeat: time.Second,
		DialOptions: []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials()), dial}}, nil)
	done := make(chan error, 1)
	go func() { done <- c.Run(context.Background(), &blockWorkload{first: Running, controls: make(chan Control, 4)}) }()
	select {
	case err := <-done:
		if err == nil { t.Fatal("expected fatal auth error, got nil (did it retry forever?)") }
	case <-time.After(2 * time.Second):
		t.Fatal("auth failure did not fail fast (infinite retry?)")
	}
}

func TestClientHeartbeatRenewsLease(t *testing.T) {
	store, _, dial, tok, secret := serverHarness(t)
	c := New(Config{Addr: "bufnet", Token: tok, LaunchSecret: secret, Heartbeat: 20 * time.Millisecond,
		DialOptions: []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials()), dial}}, nil)
	ctx, cancel := context.WithCancel(context.Background()); defer cancel()
	go c.Run(ctx, &blockWorkload{first: Running, controls: make(chan Control, 4)})
	var first time.Time
	eventually(func() bool { inst, ok := store.GetInstance("w1"); if ok { first = inst.Lease.Expiry }; return ok })
	if !eventually(func() bool { inst, ok := store.GetInstance("w1"); return ok && inst.Lease.Expiry.After(first) }) {
		t.Fatal("heartbeat did not renew the lease over time")
	}
}
```

> **Implementer note:** each test builds `Config` inline with `grpc.WithTransportCredentials(insecure.NewCredentials())` + the bufconn dial option; `Addr` is `"bufnet"` (the bufconn dialer ignores it).

- [ ] **Step 2: Run to verify failure** — `go test ./internal/covemaster/ -run TestClient -v` → compile error (covemaster API undefined).

- [ ] **Step 3: Create `internal/covemaster/covemaster.go`**

```go
// Package covemaster is the in-cove client of harbor's Attach stream — the first
// limb of cove-master, the cove's primary process. It dials harbor's runtime
// listener, authenticates with the cove's identity token + per-instance launch
// secret, reports activity up and reacts to control down, reconnecting across
// transient drops. It depends only on the generated attachpb types + grpc, never
// on internal/harbor.
package covemaster

import (
	"io"
	"log/slog"
	"time"

	"google.golang.org/grpc"

	"github.com/aethons-tools/cove/internal/harbor/attach/attachpb"
)

// Activity is the cove's self-reported status (maps to attachpb.Activity).
type Activity int

const (
	Running Activity = iota
	Waiting
	Blocked
	Done
)

// ControlKind is a control message the workload reacts to. (TierChanged and
// RotateToken are decoded off the wire but not surfaced here this slice.)
type ControlKind int

const (
	Wake ControlKind = iota
	Teardown
)

type Control struct{ Kind ControlKind }

// Handle lets the workload report activity to the client.
type Handle interface{ Report(Activity) }

// Workload is what cove-master supervises (the agent, in a later slice).
type Workload interface {
	// Run does the work, reporting activity via h, until it completes (return
	// nil → the client reports Done and exits) or ctx is cancelled (a Teardown,
	// or the parent shutting down). A non-nil error is logged; the unit is still
	// considered over.
	Run(ctx context.Context, h Handle) error
	// Control delivers a decoded control message. Teardown also cancels Run's
	// ctx; Control(Teardown) is the cooperative hook before the unwind.
	Control(c Control)
}

// Config is the client's connection + behavior configuration.
type Config struct {
	Addr         string
	Token        string
	LaunchSecret string
	Heartbeat    time.Duration    // default 10s
	DialOptions  []grpc.DialOption // injected (bufconn in tests); must include transport creds
}

func toPBActivity(a Activity) attachpb.Activity {
	switch a {
	case Running:
		return attachpb.Activity_RUNNING
	case Waiting:
		return attachpb.Activity_WAITING
	case Blocked:
		return attachpb.Activity_BLOCKED
	case Done:
		return attachpb.Activity_DONE
	}
	return attachpb.Activity_ACTIVITY_UNSPECIFIED
}

func statusMsg(a Activity) *attachpb.StatusUp {
	return &attachpb.StatusUp{Msg: &attachpb.StatusUp_Status{Status: toPBActivity(a)}}
}
func heartbeatMsg() *attachpb.StatusUp {
	return &attachpb.StatusUp{Msg: &attachpb.StatusUp_Heartbeat{Heartbeat: &attachpb.Heartbeat{}}}
}

func newLogger(log *slog.Logger) *slog.Logger {
	if log != nil {
		return log
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
```
(Add `"context"` to the imports — it's used by `Workload`.)

- [ ] **Step 4: Create `internal/covemaster/client.go`**

```go
package covemaster

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/aethons-tools/cove/internal/harbor/attach/attachpb"
)

const (
	defaultHeartbeat = 10 * time.Second
	initialBackoff   = 500 * time.Millisecond
	maxBackoff       = 15 * time.Second
)

type Client struct {
	cfg Config
	log *slog.Logger

	mu        sync.Mutex
	latest    Activity
	hasLatest bool
	activity  chan Activity // coalesced (buffer 1)
}

func New(cfg Config, log *slog.Logger) *Client {
	if cfg.Heartbeat <= 0 {
		cfg.Heartbeat = defaultHeartbeat
	}
	return &Client{cfg: cfg, log: newLogger(log), activity: make(chan Activity, 1)}
}

// Report implements Handle. It records the latest activity (for re-send on
// reconnect) and coalesces onto a buffer-1 channel (newest wins) for live
// delivery to the active session.
func (c *Client) Report(a Activity) {
	c.mu.Lock()
	c.latest, c.hasLatest = a, true
	c.mu.Unlock()
	for {
		select {
		case c.activity <- a:
			return
		default:
			select { // drop the stale pending value, then retry
			case <-c.activity:
			default:
			}
		}
	}
}

type outcomeKind int

const (
	retry        outcomeKind = iota // transient; reconnect with backoff
	fatal                           // auth failure; stop with error
	stopDone                        // workload finished; reported Done
	stopTeardown                    // server asked to tear down
	stopParent                      // parent ctx cancelled
)

type outcome struct {
	kind outcomeKind
	err  error
}

// Run connects and supervises w until it finishes (Done), a Teardown arrives,
// the parent ctx is cancelled, or a fatal (auth) error occurs. Transient drops
// reconnect with backoff; the workload runs exactly once across reconnects.
func (c *Client) Run(ctx context.Context, w Workload) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	doneCh := make(chan struct{})
	var runErr error
	go func() {
		runErr = w.Run(runCtx, c)
		close(doneCh)
	}()

	backoff := initialBackoff
	for {
		oc := c.session(runCtx, w, doneCh)
		switch oc.kind {
		case stopDone:
			if runErr != nil && !errors.Is(runErr, context.Canceled) {
				c.log.Warn("workload ended with error", "err", runErr.Error())
			}
			return nil
		case stopTeardown:
			cancel()   // unwind the workload
			<-doneCh   // wait for it to return
			return nil
		case stopParent:
			<-doneCh
			return ctx.Err()
		case fatal:
			cancel()
			<-doneCh
			return oc.err
		case retry:
			select {
			case <-runCtx.Done():
				<-doneCh
				return ctx.Err()
			case <-doneCh:
				// workload finished while we were disconnected — reconnect once
				// more to report Done (next session hits the doneCh case).
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, maxBackoff)
		}
	}
}

// session runs one connection: dial, auth, then pump activity/heartbeat up and
// control down until the session ends. It returns the outcome that drives Run's
// reconnect/stop decision. The workload is NOT started here (Run owns it).
func (c *Client) session(ctx context.Context, w Workload, doneCh <-chan struct{}) outcome {
	opts := append([]grpc.DialOption{}, c.cfg.DialOptions...)
	cc, err := grpc.NewClient(c.cfg.Addr, opts...)
	if err != nil {
		return outcome{kind: retry, err: err}
	}
	defer cc.Close()

	md := metadata.Pairs("authorization", "Bearer "+c.cfg.Token, "x-harbor-launch-secret", c.cfg.LaunchSecret)
	stream, err := attachpb.NewRuntimeClient(cc).Attach(metadata.NewOutgoingContext(ctx, md))
	if err != nil {
		return classify(ctx, err, nil)
	}

	// recv goroutine → control/recvErr.
	controlCh := make(chan *attachpb.ControlDown, 8)
	recvErr := make(chan error, 1)
	go func() {
		for {
			cd, err := stream.Recv()
			if err != nil {
				recvErr <- err
				return
			}
			select {
			case controlCh <- cd:
			default:
			}
		}
	}()

	// Re-send the latest activity on (re)connect so a drop never loses state.
	c.mu.Lock()
	latest, has := c.latest, c.hasLatest
	c.mu.Unlock()
	if has {
		if err := stream.Send(statusMsg(latest)); err != nil {
			return classify(ctx, err, recvErr)
		}
	}

	hb := time.NewTicker(c.cfg.Heartbeat)
	defer hb.Stop()
	for {
		select {
		case <-ctx.Done():
			return outcome{kind: stopParent}
		case a := <-c.activity:
			if err := stream.Send(statusMsg(a)); err != nil {
				return classify(ctx, err, recvErr)
			}
		case <-hb.C:
			if err := stream.Send(heartbeatMsg()); err != nil {
				return classify(ctx, err, recvErr)
			}
		case <-doneCh:
			if err := stream.Send(statusMsg(Done)); err != nil {
				return classify(ctx, err, recvErr)
			}
			return outcome{kind: stopDone}
		case cd := <-controlCh:
			switch cd.GetMsg().(type) {
			case *attachpb.ControlDown_Teardown:
				w.Control(Control{Kind: Teardown})
				return outcome{kind: stopTeardown}
			case *attachpb.ControlDown_Wake:
				w.Control(Control{Kind: Wake})
			default:
				c.log.Info("ignoring unsupported control message") // TierChanged/RotateToken (reserved)
			}
		case err := <-recvErr:
			return classify(ctx, err, nil)
		}
	}
}

// classify turns a stream error into an outcome. A gRPC Send error is not itself
// authoritative (the real status comes from Recv), so when recvErr is provided we
// wait briefly for it. Unauthenticated → fatal (never retry); everything else →
// retry. A cancelled parent ctx → stopParent.
func classify(ctx context.Context, sendErr error, recvErr <-chan error) outcome {
	err := sendErr
	if recvErr != nil {
		select {
		case e := <-recvErr:
			err = e
		case <-ctx.Done():
			return outcome{kind: stopParent}
		case <-time.After(2 * time.Second):
		}
	}
	if ctx.Err() != nil {
		return outcome{kind: stopParent}
	}
	if status.Code(err) == codes.Unauthenticated {
		return outcome{kind: fatal, err: err}
	}
	return outcome{kind: retry, err: err}
}
```

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/covemaster/ -run TestClient -v`
Expected: PASS (all five).

- [ ] **Step 6: Boundary + gofmt**

```bash
go build ./... && gofmt -l internal/covemaster/
go list -deps ./cmd/at-cove | grep -iE 'oidc|grpc' || echo CLEAN   # expect CLEAN
```

- [ ] **Step 7: Commit**

```bash
git add internal/covemaster/covemaster.go internal/covemaster/client.go internal/covemaster/client_test.go
git commit -m "covemaster: in-cove Attach client — connect/auth/stream/reconnect + Workload seam (COV-155)"
```

---

## Task 2: The `cove-master` binary

**Files:** create `cmd/cove-master/main.go`, `cmd/cove-master/main_test.go`.

**Interfaces:** Consumes `internal/covemaster`. Produces the binary: env → `covemaster.Config` → `Client.Run(ctx, stubWorkload{})`, ctx cancelled on SIGINT/SIGTERM.

- [ ] **Step 1: Write the failing test** — `cmd/cove-master/main_test.go`:

```go
package main

import (
	"context"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/covemaster"
)

func TestBuildConfigRequiresEnv(t *testing.T) {
	env := map[string]string{}
	get := func(k string) string { return env[k] }
	if _, err := buildConfig(get); err == nil {
		t.Fatal("expected error when AT_HARBOR_RUNTIME_ADDR is missing")
	}
	env["AT_HARBOR_RUNTIME_ADDR"] = "harbor:9090"
	if _, err := buildConfig(get); err == nil {
		t.Fatal("expected error when AT_HARBOR_IDENTITY_TOKEN is missing")
	}
	env["AT_HARBOR_IDENTITY_TOKEN"] = "tok"
	if _, err := buildConfig(get); err == nil {
		t.Fatal("expected error when AT_HARBOR_LAUNCH_SECRET is missing")
	}
	env["AT_HARBOR_LAUNCH_SECRET"] = "sec"
	cfg, err := buildConfig(get)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != "harbor:9090" || cfg.Token != "tok" || cfg.LaunchSecret != "sec" {
		t.Fatalf("config = %+v", cfg)
	}
	if len(cfg.DialOptions) == 0 {
		t.Fatal("expected transport credentials in DialOptions")
	}
}

func TestStubWorkloadRunReturnsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	w := stubWorkload{}
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx, noopHandle{}) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stub Run did not return on ctx cancel")
	}
	w.Control(covemaster.Control{Kind: covemaster.Teardown}) // must not panic
}

type noopHandle struct{}
func (noopHandle) Report(covemaster.Activity) {}
```

- [ ] **Step 2: Run to verify failure** — `go test ./cmd/cove-master/ -v` → compile error.

- [ ] **Step 3: Create `cmd/cove-master/main.go`**

```go
// Command cove-master is the in-cove primary process (first limb): it connects to
// harbor's Attach stream and supervises the cove's workload. This slice ships a
// stub workload; the real agent wrapper — and cove-master becoming the image
// entrypoint in its own non-root account — are later slices. It reads its
// configuration from the environment (no SSH, no host orchestration):
//
//	AT_HARBOR_RUNTIME_ADDR   harbor's runtime (Attach) listener, host:port
//	AT_HARBOR_IDENTITY_TOKEN the cove's identity token
//	AT_HARBOR_LAUNCH_SECRET  the per-instance launch secret
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/aethons-tools/cove/internal/covemaster"
)

func buildConfig(getenv func(string) string) (covemaster.Config, error) {
	addr := getenv("AT_HARBOR_RUNTIME_ADDR")
	token := getenv("AT_HARBOR_IDENTITY_TOKEN")
	secret := getenv("AT_HARBOR_LAUNCH_SECRET")
	switch {
	case addr == "":
		return covemaster.Config{}, fmt.Errorf("AT_HARBOR_RUNTIME_ADDR is required")
	case token == "":
		return covemaster.Config{}, fmt.Errorf("AT_HARBOR_IDENTITY_TOKEN is required")
	case secret == "":
		return covemaster.Config{}, fmt.Errorf("AT_HARBOR_LAUNCH_SECRET is required")
	}
	return covemaster.Config{
		Addr: addr, Token: token, LaunchSecret: secret,
		// Plaintext TCP this slice; :443/TLS mux is deferred.
		DialOptions: []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())},
	}, nil
}

// stubWorkload is the placeholder supervised unit for this slice: it reports
// Running and waits for teardown. The real agent wrapper replaces it next slice.
type stubWorkload struct{ log *slog.Logger }

func (s stubWorkload) Run(ctx context.Context, h covemaster.Handle) error {
	h.Report(covemaster.Running)
	<-ctx.Done()
	return ctx.Err()
}
func (s stubWorkload) Control(c covemaster.Control) {
	if s.log != nil {
		s.log.Info("control", "kind", c.Kind)
	}
}

func run(getenv func(string) string, stderr *os.File) int {
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg, err := buildConfig(getenv)
	if err != nil {
		fmt.Fprintln(stderr, "cove-master:", err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := covemaster.New(cfg, log).Run(ctx, stubWorkload{log: log}); err != nil {
		fmt.Fprintln(stderr, "cove-master:", err)
		return 1
	}
	return 0
}

func main() { os.Exit(run(os.Getenv, os.Stderr)) }
```

- [ ] **Step 4: Run tests + build** — `go test ./cmd/cove-master/ && go build ./...`. Expected: PASS.

- [ ] **Step 5: Boundary gate** — `go list -deps ./cmd/at-cove | grep -iE 'oidc|grpc' || echo CLEAN` → CLEAN. `gofmt -l cmd/cove-master/` clean.

- [ ] **Step 6: Commit**

```bash
git add cmd/cove-master/main.go cmd/cove-master/main_test.go
git commit -m "cove-master: binary — env config + stub workload + signal handling (COV-155)"
```

---

## Task 3: Integration test + docs

**Files:** create `internal/covemaster/integration_test.go`; modify `docs/usage/harbor/coves.md`.

- [ ] **Step 1: Integration test** — `internal/covemaster/integration_test.go` (`//go:build integration`): stand up the real `attach.Server` on a **localhost TCP listener** (`net.Listen("tcp", "127.0.0.1:0")`), dial it with the client using real `grpc.WithTransportCredentials(insecure.NewCredentials())` (no bufconn dialer), and assert the same status-up round-trip as the hermetic test — exercising the real dial path. Mirror the `serverHarness` shape but bind a real port and pass the listener's `Addr().String()` as `Config.Addr`.

```go
//go:build integration

package covemaster

// TestIntegrationRealListener connects the client to a real TCP attach.Server and
// verifies activity flows end-to-end over a real grpc dial (not bufconn).
// [implement: net.Listen tcp 127.0.0.1:0; attach.Server+Supervisor+raised instance
//  as in serverHarness; New(Config{Addr: lis.Addr().String(), ..., DialOptions:
//  []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}});
//  run a blockWorkload(Waiting); eventually store.GetInstance shows waiting; cancel.]
```
Write it concretely following `client_test.go`'s harness (the test file shares the package, so `serverHarness`/`eventually`/`blockWorkload` are available — extract the server-standup into a small helper callable by both if cleaner, but do NOT duplicate the role/raise logic verbatim in a way that drifts).

- [ ] **Step 2: Verify it builds under the tag** — `go test -tags integration ./internal/covemaster/ -run TestIntegration -v`. Expected: PASS. Also confirm the default (untagged) build still passes: `go test ./internal/covemaster/`.

- [ ] **Step 3: Docs** — add a "cove-master (the in-cove client)" subsection to `docs/usage/harbor/coves.md`: cove-master is the cove's client of the Attach stream (slice 3); it needs `AT_HARBOR_RUNTIME_ADDR` + `AT_HARBOR_IDENTITY_TOKEN` + `AT_HARBOR_LAUNCH_SECRET`; this slice ships the client with a **stub workload** — the real agent wrapper and cove-master-as-entrypoint (own non-root account, collapsed boot) are later slices. Link the spec for rationale. Bump `updated:`. Keep the leaf ≤200 lines.

- [ ] **Step 4: Docs audit (delta)** — `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs --index OVERVIEW.md`; confirm no new issues from the edit.

- [ ] **Step 5: Commit**

```bash
git add internal/covemaster/integration_test.go docs/usage/harbor/coves.md
git commit -m "covemaster: integration test (real listener) + cove-master docs (COV-155)"
```

---

## Final steps (after all tasks)

- [ ] `go test ./...` green; `go test -tags integration ./internal/covemaster/` green; `gofmt -l` clean; `STRICT=1 bash scripts/lint.sh` OK.
- [ ] Boundary gate: `go list -deps ./cmd/at-cove | grep -iE 'oidc|grpc'` empty.
- [ ] Dispatch the final whole-branch review (most-capable model — scrutinize the reconnect/classify concurrency), then `superpowers:finishing-a-development-branch`.
