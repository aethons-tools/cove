package covemaster

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"sync/atomic"
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

func (aliveLauncher) Raise(context.Context, harbor.RaiseSpec, harbor.LaunchCreds) (string, error) {
	return "fake", nil
}
func (aliveLauncher) Teardown(context.Context, harbor.Instance) error { return nil }
func (aliveLauncher) Probe(context.Context, harbor.Instance) (harbor.Liveness, error) {
	return harbor.LivenessAlive, nil
}
func (aliveLauncher) Pause(context.Context, harbor.Instance) error   { return nil }
func (aliveLauncher) Unpause(context.Context, harbor.Instance) error { return nil }

// serverHarness raises one instance and starts an in-memory attach server.
// Returns the store, the attach server, a DialOption that reaches it, and the
// raised actor's token + launch secret.
func serverHarness(t *testing.T) (harbor.Store, *attach.Server, grpc.DialOption, string, string) {
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
	for i := 0; i < 200; i++ {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}

// blockWorkload reports one activity then blocks until ctx is cancelled.
type blockWorkload struct {
	first    Activity
	controls chan Control
}

func (b *blockWorkload) Run(ctx context.Context, h Handle) error {
	h.Report(b.first)
	<-ctx.Done()
	return ctx.Err()
}
func (b *blockWorkload) Control(c Control) {
	select {
	case b.controls <- c:
	default:
	}
}

func TestClientReportsActivity(t *testing.T) {
	store, _, dial, tok, secret := serverHarness(t)
	c := New(Config{Addr: "bufnet", Token: tok, LaunchSecret: secret, Heartbeat: 20 * time.Millisecond,
		DialOptions: []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials()), dial}}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx, &blockWorkload{first: Waiting, controls: make(chan Control, 4)}) }()
	if !eventually(func() bool { inst, ok := store.GetInstance("w1"); return ok && inst.Activity == harbor.ActivityWaiting }) {
		inst, _ := store.GetInstance("w1")
		t.Fatalf("activity never reached waiting: %+v", inst)
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
		if c.Kind != Teardown {
			t.Fatalf("got control %v, want Teardown", c.Kind)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("workload never got Teardown control")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error on teardown: %v", err)
		}
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
	if err := c.Run(context.Background(), doneWorkload{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !eventually(func() bool { _, ok := store.GetInstance("w1"); return !ok }) {
		t.Fatal("instance not torn down after Done")
	}
}

func TestClientAuthFailureIsFatal(t *testing.T) {
	_, _, dial, tok, _ := serverHarness(t)
	c := New(Config{Addr: "bufnet", Token: tok, LaunchSecret: "WRONG-SECRET", Heartbeat: time.Second,
		DialOptions: []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials()), dial}}, nil)
	done := make(chan error, 1)
	go func() {
		done <- c.Run(context.Background(), &blockWorkload{first: Running, controls: make(chan Control, 4)})
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected fatal auth error, got nil (did it retry forever?)")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("auth failure did not fail fast (infinite retry?)")
	}
}

// TestClientBacksOffWhenServerDown proves that once the workload has finished
// (doneCh closed) but the server is unreachable so session() keeps returning
// retry, the reconnect loop still honors the backoff instead of spinning in a
// tight loop on the closed doneCh (COV-155 review fix).
func TestClientBacksOffWhenServerDown(t *testing.T) {
	var dialCount int32
	failDial := grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
		atomic.AddInt32(&dialCount, 1)
		return nil, errors.New("refused")
	})
	c := New(Config{Addr: "bufnet", Token: "tok", LaunchSecret: "secret", Heartbeat: 50 * time.Millisecond,
		DialOptions: []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials()), failDial}}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx, doneWorkload{}) }()

	<-ctx.Done()
	// Give Run a moment to observe cancellation and return.
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}

	if got := atomic.LoadInt32(&dialCount); got >= 10 {
		t.Fatalf("dial count = %d, want < 10 (backoff not honored — tight reconnect loop)", got)
	} else {
		t.Logf("dial count over 400ms with server down: %d", got)
	}
}

func TestClientHeartbeatRenewsLease(t *testing.T) {
	store, _, dial, tok, secret := serverHarness(t)
	c := New(Config{Addr: "bufnet", Token: tok, LaunchSecret: secret, Heartbeat: 20 * time.Millisecond,
		DialOptions: []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials()), dial}}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx, &blockWorkload{first: Running, controls: make(chan Control, 4)})
	var first time.Time
	eventually(func() bool {
		inst, ok := store.GetInstance("w1")
		if ok {
			first = inst.Lease.Expiry
		}
		return ok
	})
	if !eventually(func() bool { inst, ok := store.GetInstance("w1"); return ok && inst.Lease.Expiry.After(first) }) {
		t.Fatal("heartbeat did not renew the lease over time")
	}
}
