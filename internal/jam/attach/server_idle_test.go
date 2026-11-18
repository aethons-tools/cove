package attach

import (
	"context"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/attach/attachpb"
)

// idleHarness is harness with the guest role's idle timeout set to timeout.
func idleHarness(t *testing.T, timeout time.Duration) (jam.Store, *jam.Supervisor, *Server, attachpb.Runtime_AttachClient) {
	t.Helper()
	store, sup, srv, dial, tok, secret := harness(t)
	role, _ := store.GetRole("default", "guest")
	role.TurnEnd.IdleTimeout = timeout
	if err := store.PutRole("default", role); err != nil {
		t.Fatal(err)
	}
	cc := dial()
	t.Cleanup(func() { cc.Close() })
	stream, err := attachpb.NewRuntimeClient(cc).Attach(boundedCtx(t, authCtx(tok, secret)))
	if err != nil {
		t.Fatal(err)
	}
	return store, sup, srv, stream
}

// boundedCtx bounds a test stream, so a push that never comes fails the test
// instead of hanging it.
func boundedCtx(t *testing.T, ctx context.Context) context.Context {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// recvUntil receives control messages until want accepts one and returns the
// messages before it.
func recvUntil(t *testing.T, stream attachpb.Runtime_AttachClient, want func(*attachpb.ControlDown) bool) []*attachpb.ControlDown {
	t.Helper()
	var before []*attachpb.ControlDown
	for {
		cd, err := stream.Recv()
		if err != nil {
			t.Fatal(err)
		}
		if want(cd) {
			return before
		}
		before = append(before, cd)
	}
}

func recvIdleTimeout(t *testing.T, stream attachpb.Runtime_AttachClient) uint64 {
	t.Helper()
	var ms uint64
	recvUntil(t, stream, func(cd *attachpb.ControlDown) bool {
		ms = cd.GetIdleTimeout().GetMs()
		return cd.GetIdleTimeout() != nil
	})
	return ms
}

func recvIdleDeadline(t *testing.T, stream attachpb.Runtime_AttachClient) int64 {
	t.Helper()
	var at int64
	recvUntil(t, stream, func(cd *attachpb.ControlDown) bool {
		at = cd.GetIdleDeadline().GetAt()
		return cd.GetIdleDeadline() != nil
	})
	return at
}

func sendActivity(t *testing.T, store jam.Store, stream attachpb.Runtime_AttachClient, a attachpb.Activity, want jam.Activity) {
	t.Helper()
	if err := stream.Send(&attachpb.StatusUp{Msg: &attachpb.StatusUp_Status{Status: a}}); err != nil {
		t.Fatal(err)
	}
	if !eventually(func() bool { inst, _ := store.GetInstance("w1"); return inst.Activity == want }) {
		t.Fatalf("activity never became %s", want)
	}
}

func TestAttachStartsWithIdleTimeout(t *testing.T) {
	_, _, _, stream := idleHarness(t, 30*time.Minute)
	cd, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if got := cd.GetIdleTimeout(); got == nil || got.GetMs() != 30*60*1000 {
		t.Fatalf("first control = %v, want IdleTimeout 30m", cd)
	}
}

// A stream that attaches while a deadline is armed is told it after its seed.
func TestAttachSendsArmedDeadline(t *testing.T) {
	store, sup, _, dial, tok, secret := harness(t)
	role, _ := store.GetRole("default", "guest")
	role.TurnEnd.IdleTimeout = time.Minute
	_ = store.PutRole("default", role)
	if err := sup.Report(context.Background(), "w1", jam.ActivityWaiting); err != nil {
		t.Fatal(err)
	}
	inst, _ := store.GetInstance("w1")
	cc := dial()
	defer cc.Close()
	stream, err := attachpb.NewRuntimeClient(cc).Attach(boundedCtx(t, authCtx(tok, secret)))
	if err != nil {
		t.Fatal(err)
	}
	var seq []string
	recvUntil(t, stream, func(cd *attachpb.ControlDown) bool {
		switch {
		case cd.GetIdleTimeout() != nil:
			seq = append(seq, "timeout")
		case cd.GetTunnelPorts() != nil:
			seq = append(seq, "ports")
		case cd.GetIdleDeadline() != nil:
			seq = append(seq, "deadline")
			if got := cd.GetIdleDeadline().GetAt(); got != inst.IdleDeadline.UnixMilli() {
				t.Fatalf("deadline %d, want %d", got, inst.IdleDeadline.UnixMilli())
			}
			return true
		}
		return false
	})
	if len(seq) != 3 || seq[0] != "timeout" || seq[1] != "ports" {
		t.Fatalf("seed = %v, want timeout, ports, deadline", seq)
	}
}

func TestIdleTimeoutPushedOnRoleChange(t *testing.T) {
	store, sup, _, stream := idleHarness(t, time.Hour)
	if got := recvIdleTimeout(t, stream); got != uint64(time.Hour.Milliseconds()) {
		t.Fatalf("seed %d", got)
	}
	role, _ := store.GetRole("default", "guest")
	role.TurnEnd.IdleTimeout = 10 * time.Minute
	_ = store.PutRole("default", role)
	if err := sup.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := recvIdleTimeout(t, stream); got != uint64((10 * time.Minute).Milliseconds()) {
		t.Fatalf("pushed %d, want 10m", got)
	}
}

func TestIdlePushOverrideAndTurns(t *testing.T) {
	store, sup, srv, stream := idleHarness(t, time.Hour)
	recvIdleTimeout(t, stream) // seed
	// An override set during the turn.
	if err := sup.SetIdleOverride("w1", jam.IdleOverride{Duration: 5 * time.Minute, Scope: jam.IdleScopeNext}); err != nil {
		t.Fatal(err)
	}
	if got := recvIdleTimeout(t, stream); got != uint64((5 * time.Minute).Milliseconds()) {
		t.Fatalf("override pushed %d, want 5m", got)
	}
	// The turn end arms the deadline from it and uses it up.
	sendActivity(t, store, stream, attachpb.Activity_WAITING, jam.ActivityWaiting)
	inst, _ := store.GetInstance("w1")
	if got := recvIdleDeadline(t, stream); got != inst.IdleDeadline.UnixMilli() || got == 0 {
		t.Fatalf("deadline pushed %d, want %d", got, inst.IdleDeadline.UnixMilli())
	}
	// The turn start pushes nothing: the next message is the sentinel.
	sendActivity(t, store, stream, attachpb.Activity_RUNNING, jam.ActivityRunning)
	srv.RequestTeardown("w1")
	for _, cd := range recvUntil(t, stream, func(cd *attachpb.ControlDown) bool { return cd.GetTeardown() != nil }) {
		if cd.GetIdleDeadline() != nil {
			t.Fatalf("turn start pushed %v", cd)
		}
	}
}

func TestIdleTimeoutRepeatNotPushed(t *testing.T) {
	_, _, srv, stream := idleHarness(t, time.Hour)
	recvIdleTimeout(t, stream) // seed
	if !eventually(func() bool { return srv.Connected("w1") }) {
		t.Fatal("stream never registered")
	}
	srv.IdleTimeoutChanged("w1", time.Hour)
	srv.IdleTimeoutChanged("w1", 2*time.Hour)
	if got := recvIdleTimeout(t, stream); got != uint64((2 * time.Hour).Milliseconds()) {
		t.Fatalf("pushed %d, want only the change to 2h", got)
	}
}

func TestIdleDeadlineDisarmPushesZero(t *testing.T) {
	_, _, srv, stream := idleHarness(t, time.Hour)
	recvIdleTimeout(t, stream)
	if !eventually(func() bool { return srv.Connected("w1") }) {
		t.Fatal("stream never registered")
	}
	srv.IdleDeadlineChanged("w1", time.UnixMilli(5000))
	if got := recvIdleDeadline(t, stream); got != 5000 {
		t.Fatalf("armed %d", got)
	}
	srv.IdleDeadlineChanged("w1", time.Time{})
	if got := recvIdleDeadline(t, stream); got != 0 {
		t.Fatalf("disarmed %d, want 0", got)
	}
}
