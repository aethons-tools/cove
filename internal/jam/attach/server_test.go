package attach

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/attach/attachpb"
)

// aliveLauncher is a no-backend launcher for the supervisor under test.
type aliveLauncher struct{}

func (aliveLauncher) Raise(context.Context, jam.RaiseSpec, jam.LaunchCreds) (string, error) {
	return "fake", nil
}
func (aliveLauncher) Teardown(context.Context, jam.Instance) error { return nil }
func (aliveLauncher) Probe(context.Context, jam.Instance) (jam.Liveness, error) {
	return jam.LivenessAlive, nil
}
func (aliveLauncher) Pause(context.Context, jam.Instance) error   { return nil }
func (aliveLauncher) Unpause(context.Context, jam.Instance) error { return nil }
func (aliveLauncher) ApplyEgress(context.Context, jam.Instance, *jam.EgressPolicy) error {
	return nil
}
func (aliveLauncher) PrepareKit(context.Context, jam.KitDefinition) (jam.KitStatus, error) {
	return jam.KitStatus{State: jam.KitReady}, nil
}

// harness raises one instance and starts an in-memory Attach server. Returns the
// store, supervisor, server, a dial func, and the raised actor's token + launch secret.
func harness(t *testing.T) (jam.Store, *jam.Supervisor, *Server, func() *grpc.ClientConn, string, string) {
	t.Helper()
	store := jam.NewMemStore()
	store.PutRole("default", jam.Role{Name: "guest", Scope: jam.Scope{Destinations: []string{"anthropic"}, TTL: time.Hour}})
	sup := jam.NewSupervisor(store, aliveLauncher{}, "holder-test", time.Minute, 30*time.Second, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	_, tok, secret, err := sup.Raise(context.Background(), jam.RaiseSpec{ActorID: "w1", Role: "guest"})
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(store, sup, slog.New(slog.NewTextHandler(io.Discard, nil)))
	sup.SetControlSink(srv)

	lis := bufconn.Listen(1 << 20)
	// WaitForHandlers: Stop (a t.Cleanup, run before the TempDir removal) must
	// wait for Attach handlers to return. A handler may still be persisting a
	// heartbeat to the in-memory test store when the test body ends; without the wait,
	// TempDir's RemoveAll races that write and fails "directory not empty".
	gs := grpc.NewServer(grpc.WaitForHandlers(true))
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
		return ok && inst.Activity == jam.ActivityWaiting
	}) {
		inst, _ := store.GetInstance("w1")
		t.Fatalf("activity never became waiting: %+v", inst)
	}
}

func TestAttachRejectsBadAuth(t *testing.T) {
	_, _, _, dial, tok, secret := harness(t)

	t.Run("no metadata", func(t *testing.T) {
		cc := dial()
		defer cc.Close()
		stream, err := attachpb.NewRuntimeClient(cc).Attach(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := stream.Recv(); status.Code(err) != codes.Unauthenticated {
			t.Fatalf("expected Unauthenticated, got %v", err)
		}
	})

	t.Run("wrong token", func(t *testing.T) {
		cc := dial()
		defer cc.Close()
		stream, err := attachpb.NewRuntimeClient(cc).Attach(authCtx("not-the-token", secret))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := stream.Recv(); status.Code(err) != codes.Unauthenticated {
			t.Fatalf("expected Unauthenticated, got %v", err)
		}
	})

	t.Run("wrong launch secret", func(t *testing.T) {
		cc := dial()
		defer cc.Close()
		stream, err := attachpb.NewRuntimeClient(cc).Attach(authCtx(tok, "not-the-secret"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := stream.Recv(); status.Code(err) != codes.Unauthenticated {
			t.Fatalf("expected Unauthenticated, got %v", err)
		}
	})
}

func TestAttachHeartbeatRenews(t *testing.T) {
	store, _, _, dial, tok, secret := harness(t)
	cc := dial()
	defer cc.Close()

	before, ok := store.GetInstance("w1")
	if !ok {
		t.Fatal("instance w1 not found")
	}

	stream, err := attachpb.NewRuntimeClient(cc).Attach(authCtx(tok, secret))
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&attachpb.StatusUp{Msg: &attachpb.StatusUp_Heartbeat{Heartbeat: &attachpb.Heartbeat{}}}); err != nil {
		t.Fatal(err)
	}

	if !eventually(func() bool {
		inst, ok := store.GetInstance("w1")
		return ok && inst.Lease.Expiry.After(before.Lease.Expiry)
	}) {
		t.Fatal("lease was never renewed")
	}
	inst, _ := store.GetInstance("w1")
	if inst.Activity != jam.ActivityRunning {
		t.Fatalf("expected activity unchanged (running), got %v", inst.Activity)
	}
}

func TestControlDownDelivery(t *testing.T) {
	_, _, srv, dial, tok, secret := harness(t)
	cc := dial()
	defer cc.Close()

	stream, err := attachpb.NewRuntimeClient(cc).Attach(authCtx(tok, secret))
	if err != nil {
		t.Fatal(err)
	}
	// Send something first so the server has processed the auth and registered
	// the stream before we request teardown.
	if err := stream.Send(&attachpb.StatusUp{Msg: &attachpb.StatusUp_Heartbeat{Heartbeat: &attachpb.Heartbeat{}}}); err != nil {
		t.Fatal(err)
	}
	if !eventually(func() bool { return srv.connected("w1") }) {
		t.Fatal("stream never registered")
	}

	srv.RequestTeardown("w1")

	msg, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv failed: %v", err)
	}
	if _, ok := msg.GetMsg().(*attachpb.ControlDown_Teardown); !ok {
		t.Fatalf("expected ControlDown_Teardown, got %T", msg.GetMsg())
	}
}

func TestWakeDelivery(t *testing.T) {
	_, _, srv, dial, tok, secret := harness(t)
	cc := dial()
	defer cc.Close()

	stream, err := attachpb.NewRuntimeClient(cc).Attach(authCtx(tok, secret))
	if err != nil {
		t.Fatal(err)
	}
	// Send something first so the server has processed the auth and registered
	// the stream before we request the wake.
	if err := stream.Send(&attachpb.StatusUp{Msg: &attachpb.StatusUp_Heartbeat{Heartbeat: &attachpb.Heartbeat{}}}); err != nil {
		t.Fatal(err)
	}
	if !eventually(func() bool { return srv.connected("w1") }) {
		t.Fatal("stream never registered")
	}

	srv.Wake("w1", jam.WakeReason{Kind: jam.WakeSquawk})

	msg, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv failed: %v", err)
	}
	wk, ok := msg.GetMsg().(*attachpb.ControlDown_Wake)
	if !ok {
		t.Fatalf("expected ControlDown_Wake, got %T", msg.GetMsg())
	}
	if rs := wk.Wake.GetReasons(); len(rs) != 1 || rs[0].GetKind() != "squawk" {
		t.Fatalf("wake reasons = %v, want [squawk]", rs)
	}
}

func TestFromPBActivityHolding(t *testing.T) {
	got, ok := fromPBActivity(attachpb.Activity_HOLDING)
	if !ok || got != jam.ActivityHolding {
		t.Fatalf("fromPBActivity(HOLDING) = %q, %v; want holding, true", got, ok)
	}
}

// TestReconnectReplaces verifies a reconnect supersedes the prior stream's
// control-delivery channel: register() swaps in a fresh channel and closes the
// old one, so a subsequent ControlDown reaches only the NEW stream. (The old
// stream's underlying RPC keeps running server-side — its recv loop still reads
// whatever the old client sends — but its send path is dead, so it never again
// receives a pushed ControlDown. Only the client hanging up, or the server
// process exiting, ends the old RPC itself.)
func TestReconnectReplaces(t *testing.T) {
	store, _, srv, dial, tok, secret := harness(t)

	cc1 := dial()
	defer cc1.Close()
	stream1, err := attachpb.NewRuntimeClient(cc1).Attach(authCtx(tok, secret))
	if err != nil {
		t.Fatal(err)
	}
	if err := stream1.Send(&attachpb.StatusUp{Msg: &attachpb.StatusUp_Heartbeat{Heartbeat: &attachpb.Heartbeat{}}}); err != nil {
		t.Fatal(err)
	}
	if !eventually(func() bool { return srv.connected("w1") }) {
		t.Fatal("first stream never registered")
	}

	// Snapshot the lease before the second Attach connects. Attach() calls
	// register() (the channel swap) and then unconditionally renews the lease
	// via sup.Heartbeat — strictly before its recv loop even starts — so once
	// the lease has visibly moved past this snapshot, the map is guaranteed to
	// point at stream2's channel, not stream1's. (Just checking srv.connected()
	// again is not a safe signal here: it was already true from stream1 and
	// would race ahead of stream2's own registration.)
	before, ok := store.GetInstance("w1")
	if !ok {
		t.Fatal("instance w1 not found")
	}

	cc2 := dial()
	defer cc2.Close()
	stream2, err := attachpb.NewRuntimeClient(cc2).Attach(authCtx(tok, secret))
	if err != nil {
		t.Fatal(err)
	}
	if !eventually(func() bool {
		inst, ok := store.GetInstance("w1")
		return ok && inst.Lease.Expiry.After(before.Lease.Expiry)
	}) {
		t.Fatal("second stream never registered")
	}

	srv.RequestTeardown("w1")

	// The new stream gets the pushed control message...
	msg, err := stream2.Recv()
	if err != nil {
		t.Fatalf("stream2.Recv failed: %v", err)
	}
	if _, ok := msg.GetMsg().(*attachpb.ControlDown_Teardown); !ok {
		t.Fatalf("expected ControlDown_Teardown on new stream, got %T", msg.GetMsg())
	}

	// ...while the superseded old stream's channel was closed at reconnect time,
	// so it never sees the push: its Recv has nothing pending within a short window.
	recv1 := make(chan error, 1)
	go func() {
		_, err := stream1.Recv()
		recv1 <- err
	}()
	select {
	case err := <-recv1:
		t.Fatalf("superseded stream1 unexpectedly received a message/error: %v", err)
	case <-time.After(200 * time.Millisecond):
		// expected: nothing delivered to the old stream.
	}
}

func TestDoneTearsDown(t *testing.T) {
	store, _, _, dial, tok, secret := harness(t)
	cc := dial()
	defer cc.Close()

	stream, err := attachpb.NewRuntimeClient(cc).Attach(authCtx(tok, secret))
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&attachpb.StatusUp{Msg: &attachpb.StatusUp_Status{Status: attachpb.Activity_DONE}}); err != nil {
		t.Fatal(err)
	}

	if !eventually(func() bool {
		_, ok := store.GetInstance("w1")
		return !ok
	}) {
		t.Fatal("instance w1 was never torn down")
	}
}

func TestStreamDropDeregisters(t *testing.T) {
	_, _, srv, dial, tok, secret := harness(t)
	cc := dial()

	stream, err := attachpb.NewRuntimeClient(cc).Attach(authCtx(tok, secret))
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&attachpb.StatusUp{Msg: &attachpb.StatusUp_Heartbeat{Heartbeat: &attachpb.Heartbeat{}}}); err != nil {
		t.Fatal(err)
	}
	if !eventually(func() bool { return srv.connected("w1") }) {
		t.Fatal("stream never registered")
	}

	cc.Close()

	if !eventually(func() bool { return !srv.connected("w1") }) {
		t.Fatal("server never deregistered dropped stream")
	}

	// Should not panic even though there's no live stream.
	srv.RequestTeardown("w1")
}

func TestAttachRecordsConnector(t *testing.T) {
	store, _, _, dial, tok, secret := harness(t)
	cc := dial()
	defer cc.Close()
	stream, err := attachpb.NewRuntimeClient(cc).Attach(authCtx(tok, secret))
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&attachpb.StatusUp{Msg: &attachpb.StatusUp_Connector{Connector: &attachpb.ConnectorApplied{Fingerprint: "fp-1"}}}); err != nil {
		t.Fatal(err)
	}
	if !eventually(func() bool { i, ok := store.GetInstance("w1"); return ok && i.Connector == "fp-1" }) {
		i, _ := store.GetInstance("w1")
		t.Fatalf("connector not recorded: %+v", i)
	}
}

func TestRunGateDelivery(t *testing.T) {
	_, _, srv, dial, tok, secret := harness(t)
	cc := dial()
	defer cc.Close()
	stream, err := attachpb.NewRuntimeClient(cc).Attach(authCtx(tok, secret))
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&attachpb.StatusUp{Msg: &attachpb.StatusUp_Heartbeat{Heartbeat: &attachpb.Heartbeat{}}}); err != nil {
		t.Fatal(err)
	}
	if !eventually(func() bool { return srv.connected("w1") }) {
		t.Fatal("stream never registered")
	}
	srv.RunGate("w1", "r1", "ci", "gh run view --exit-status", time.Minute)
	msg, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	g := msg.GetGate()
	if g == nil || g.GetRunId() != "r1" || g.GetAlarm() != "ci" || g.GetCommand() != "gh run view --exit-status" || g.GetTimeoutS() != 60 {
		t.Fatalf("got %+v", msg)
	}
}

func TestGateResultResolves(t *testing.T) {
	store, sup, srv, dial, tok, secret := harness(t)
	if _, err := sup.SetAlarm("w1", "ci", "@every 1m", "CI changed", "true"); err != nil {
		t.Fatal(err)
	}
	if err := sup.Report(context.Background(), "w1", jam.ActivityWaiting); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := sup.StartGate("w1", "ci", "r1", time.Now().Add(2*time.Minute)); !ok || err != nil {
		t.Fatalf("start gate: %v %v", ok, err)
	}
	cc := dial()
	defer cc.Close()
	stream, err := attachpb.NewRuntimeClient(cc).Attach(authCtx(tok, secret))
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&attachpb.StatusUp{Msg: &attachpb.StatusUp_Gate{Gate: &attachpb.GateResult{RunId: "r1", Exit: 0, Output: []byte("ok")}}}); err != nil {
		t.Fatal(err)
	}
	_ = srv
	if !eventually(func() bool {
		inst, _ := store.GetInstance("w1")
		return len(inst.Alarms) == 1 && inst.Alarms[0].FireKind == jam.WakeAlarm && inst.Alarms[0].FireDetail == "ok"
	}) {
		inst, _ := store.GetInstance("w1")
		t.Fatalf("alarm not resolved: %+v", inst.Alarms)
	}
}

// Jam re-sanitizes and re-caps a cove's gate output: NUL would make the
// Postgres store reject the instance, and a modified client could send MBs.
func TestGateResultSanitized(t *testing.T) {
	store, sup, _, dial, tok, secret := harness(t)
	if _, err := sup.SetAlarm("w1", "ci", "@every 1m", "", "true"); err != nil {
		t.Fatal(err)
	}
	_ = sup.Report(context.Background(), "w1", jam.ActivityWaiting)
	if _, ok, _ := sup.StartGate("w1", "ci", "r1", time.Now().Add(2*time.Minute)); !ok {
		t.Fatal("gate not started")
	}
	cc := dial()
	defer cc.Close()
	stream, err := attachpb.NewRuntimeClient(cc).Attach(authCtx(tok, secret))
	if err != nil {
		t.Fatal(err)
	}
	big := append([]byte("a\x00b"), bytes.Repeat([]byte("x"), 100000)...)
	if err := stream.Send(&attachpb.StatusUp{Msg: &attachpb.StatusUp_Gate{Gate: &attachpb.GateResult{RunId: "r1", Exit: 0, Output: big}}}); err != nil {
		t.Fatal(err)
	}
	if !eventually(func() bool { inst, _ := store.GetInstance("w1"); return inst.Alarms[0].FireKind == jam.WakeAlarm }) {
		t.Fatal("not resolved")
	}
	inst, _ := store.GetInstance("w1")
	d := inst.Alarms[0].FireDetail
	if strings.ContainsRune(d, 0) || len(d) > 4096+64 || !strings.HasSuffix(d, "[output truncated]") {
		t.Fatalf("detail len=%d nul=%v suffix=%v", len(d), strings.ContainsRune(d, 0), strings.HasSuffix(d, "[output truncated]"))
	}
}

func TestServerConnected(t *testing.T) {
	_, _, srv, _, _, _ := harness(t)
	if srv.Connected("w1") {
		t.Fatal("connected with no stream")
	}
}
