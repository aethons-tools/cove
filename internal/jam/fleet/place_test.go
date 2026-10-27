package fleet

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
)

func placeRouter(t *testing.T, ms ...Manager) *Router {
	t.Helper()
	r := New(nil)
	for _, m := range ms {
		if err := r.Add(m); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func spec(timeout time.Duration, names ...string) jam.RaiseSpec {
	return jam.RaiseSpec{Role: "r", Placement: jam.Placement{Managers: names, Timeout: timeout}}
}

func TestRaiseOnFirstAvailableInOrder(t *testing.T) {
	a, b := newFake("a"), newFake("b")
	a.avail.Store(false)
	r := placeRouter(t, a, b)
	p, err := r.RaiseOn(context.Background(), spec(0, "a", "b"), jam.LaunchCreds{})
	if err != nil || p.Manager != "b" || p.Location != "b-loc" {
		t.Fatalf("got %+v, %v", p, err)
	}
}

func TestRaiseOnDeclineMovesToNext(t *testing.T) {
	a, b := newFake("a"), newFake("b")
	a.raiseErr = errors.New("at capacity")
	r := placeRouter(t, a, b)
	p, err := r.RaiseOn(context.Background(), spec(0, "a", "b"), jam.LaunchCreds{})
	if err != nil || p.Manager != "b" {
		t.Fatalf("got %+v, %v", p, err)
	}
}

func TestRaiseOnNoListUsesNamesOrder(t *testing.T) {
	r := placeRouter(t, newFake("x"), newFake("a"))
	p, err := r.RaiseOn(context.Background(), spec(0), jam.LaunchCreds{})
	if err != nil || p.Manager != "x" {
		t.Fatalf("got %+v, %v", p, err)
	}
}

func TestRaiseOnZeroTimeoutFailsAtOnce(t *testing.T) {
	a, b := newFake("a"), newFake("b")
	a.avail.Store(false)
	b.avail.Store(false)
	r := placeRouter(t, a, b)
	start := time.Now()
	_, err := r.RaiseOn(context.Background(), spec(0, "a", "b"), jam.LaunchCreds{})
	var noMgr *jam.NoManagerError
	if !errors.As(err, &noMgr) {
		t.Fatalf("err = %v", err)
	}
	if want := []string{"a: unavailable", "b: unavailable"}; !slices.Equal(noMgr.Reasons, want) {
		t.Fatalf("reasons = %v", noMgr.Reasons)
	}
	if time.Since(start) > 50*time.Millisecond {
		t.Fatal("slow")
	}
}

func TestRaiseOnWaitsForKick(t *testing.T) {
	a := newFake("a")
	a.avail.Store(false)
	r := placeRouter(t, a)
	go func() {
		time.Sleep(20 * time.Millisecond)
		a.avail.Store(true)
		r.Kick()
	}()
	start := time.Now()
	p, err := r.RaiseOn(context.Background(), spec(5*time.Second, "a"), jam.LaunchCreds{})
	if err != nil || p.Manager != "a" {
		t.Fatalf("got %+v, %v", p, err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("did not wake on kick")
	}
}

func TestRaiseOnTimeoutExpires(t *testing.T) {
	a := newFake("a")
	a.avail.Store(false)
	r := placeRouter(t, a)
	r.poll = 10 * time.Millisecond
	_, err := r.RaiseOn(context.Background(), spec(30*time.Millisecond, "a"), jam.LaunchCreds{})
	var noMgr *jam.NoManagerError
	if !errors.As(err, &noMgr) {
		t.Fatalf("err = %v", err)
	}
}

func TestRaiseOnCtxCancelReturnsPromptly(t *testing.T) {
	a := newFake("a")
	a.avail.Store(false)
	r := placeRouter(t, a)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	start := time.Now()
	_, err := r.RaiseOn(ctx, spec(10*time.Second, "a"), jam.LaunchCreds{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 200*time.Millisecond {
		t.Fatal("slow")
	}
}

func TestRaiseOnKitNotReadyNamesManager(t *testing.T) {
	a, b := newFake("a"), newFake("b")
	a.raiseErr = jam.ErrKitNotReady
	r := placeRouter(t, a, b)
	_, err := r.RaiseOn(context.Background(), spec(0, "a", "b"), jam.LaunchCreds{})
	var k *jam.KitNotReadyError
	if !errors.As(err, &k) || k.Manager != "a" {
		t.Fatalf("err = %v", err)
	}
	if len(b.get(&b.raised)) != 0 {
		t.Fatal("b was called")
	}
}

func TestRaiseOnUnknownNameFailsFast(t *testing.T) {
	r := placeRouter(t, newFake("a"))
	start := time.Now()
	_, err := r.RaiseOn(context.Background(), spec(10*time.Second, "nope"), jam.LaunchCreds{})
	if !errors.Is(err, jam.ErrUnknownManager) || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 50*time.Millisecond {
		t.Fatal("slow")
	}
}

func TestPrepareKitOnRoutesToNamed(t *testing.T) {
	a, b := newFake("a"), newFake("b")
	r := placeRouter(t, a, b)
	if _, err := r.PrepareKitOn(context.Background(), "b", jam.KitDefinition{}); err != nil {
		t.Fatal(err)
	}
	if len(a.get(&a.prepared)) != 0 || len(b.get(&b.prepared)) != 1 {
		t.Fatal("wrong routing")
	}
	if _, err := r.PrepareKitOn(context.Background(), "zz", jam.KitDefinition{}); !errors.Is(err, jam.ErrUnknownManager) {
		t.Fatalf("err = %v", err)
	}
	b.avail.Store(false)
	if _, err := r.PrepareKitOn(context.Background(), "b", jam.KitDefinition{}); !errors.Is(err, jam.ErrManagerUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

// testCtx is cancelled when the test ends, so parked raises don't outlive it.
func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return ctx
}

// waitFor fails the test if cond is not true within 2s.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestRaiseOnInterruptedDoesNotFallThrough(t *testing.T) {
	a, b := newFake("a"), newFake("b")
	a.raiseErr = fmt.Errorf("x: %w", jam.ErrRaiseInterrupted)
	r := placeRouter(t, a, b)
	start := time.Now()
	_, err := r.RaiseOn(testCtx(t), spec(5*time.Second, "a", "b"), jam.LaunchCreds{})
	if !errors.Is(err, jam.ErrRaiseInterrupted) || time.Since(start) > 100*time.Millisecond {
		t.Fatalf("err %v after %v", err, time.Since(start))
	}
	if n := a.raiseCalls.Load(); n != 1 {
		t.Fatalf("a saw %d raises, want 1", n)
	}
	if n := b.raiseCalls.Load(); n != 0 {
		t.Fatalf("b saw %d raises", n)
	}
}

func TestKickWakesAllWaiters(t *testing.T) {
	a := newFake("a")
	a.avail.Store(false)
	r := placeRouter(t, a)
	r.poll = time.Hour
	ctx := testCtx(t)
	var wg sync.WaitGroup
	errs := make(chan error, 3)
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := r.RaiseOn(ctx, spec(5*time.Second, "a"), jam.LaunchCreds{})
			errs <- err
		}()
	}
	waitFor(t, "three parked waiters", func() bool { return r.waiters.Load() >= 3 })
	a.avail.Store(true)
	r.Kick()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("not all waiters woke")
	}
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestDeclineBackoff(t *testing.T) {
	a := newFake("a")
	a.raiseErr = errors.New("full")
	r := placeRouter(t, a)
	r.poll = 10 * time.Millisecond
	_, err := r.RaiseOn(testCtx(t), spec(200*time.Millisecond, "a"), jam.LaunchCreds{})
	var nm *jam.NoManagerError
	if !errors.As(err, &nm) {
		t.Fatalf("err %v", err)
	}
	if n := a.raiseCalls.Load(); n > 6 || n < 2 {
		t.Fatalf("calls = %d, want 2..6", n)
	}
	// Whether the final round skipped or tried, the real decline text survives.
	if !strings.Contains(err.Error(), "full") {
		t.Fatalf("decline reason lost: %v", err)
	}

	// A Kick makes the manager eligible at once (poll is 1h, so any retry is
	// Kick-caused).
	b := newFake("b")
	b.raiseErr = errors.New("full")
	r2 := placeRouter(t, b)
	r2.poll = time.Hour
	ctx := testCtx(t)
	go func() { _, _ = r2.RaiseOn(ctx, spec(5*time.Second, "b"), jam.LaunchCreds{}) }()
	waitFor(t, "first decline and park", func() bool { return b.raiseCalls.Load() >= 1 && r2.waiters.Load() >= 1 })
	before := b.raiseCalls.Load()
	r2.Kick()
	waitFor(t, "retry after Kick", func() bool { return b.raiseCalls.Load() > before })
}

func TestUnavailableNotBackedOff(t *testing.T) {
	a := newFake("a")
	a.avail.Store(false)
	r := placeRouter(t, a)
	r.poll = 10 * time.Millisecond
	ctx := testCtx(t)
	type res struct {
		p   jam.Placed
		err error
	}
	out := make(chan res, 1)
	go func() {
		p, err := r.RaiseOn(ctx, spec(5*time.Second, "a"), jam.LaunchCreds{})
		out <- res{p, err}
	}()
	// Unavailable for ~100ms: with no backoff it is polled every ~10ms
	// (about 10 checks); a doubling backoff would give about 4.
	time.Sleep(100 * time.Millisecond)
	if n := a.availCalls.Load(); n < 6 {
		t.Fatalf("Available() checked %d times in 100ms, want >= 6", n)
	}
	a.avail.Store(true) // no Kick
	select {
	case got := <-out:
		if got.err != nil || got.p.Manager != "a" {
			t.Fatalf("got %+v, %v", got.p, got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("not placed after the manager became available")
	}
}

// cancelingManager cancels the caller's ctx during Raise, as a caller giving
// up mid-call would, and fails with the ctx's error.
type cancelingManager struct {
	*fakeManager
	cancel context.CancelFunc
}

func (m cancelingManager) Raise(ctx context.Context, _ jam.RaiseSpec, _ jam.LaunchCreds) (string, error) {
	m.raiseCalls.Add(1)
	m.cancel()
	<-ctx.Done()
	return "", fmt.Errorf("raise: %w", ctx.Err())
}

// A raise cut short by the caller's ctx is not a decline: no other manager is
// tried, and the ctx's error is returned.
func TestRaiseOnCtxCancelledDuringRaiseStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := cancelingManager{fakeManager: newFake("a"), cancel: cancel}
	b := newFake("b")
	r := placeRouter(t, a, b)
	_, err := r.RaiseOn(ctx, spec(5*time.Second, "a", "b"), jam.LaunchCreds{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if n := b.raiseCalls.Load(); n != 0 {
		t.Fatalf("b saw %d raises after the caller gave up", n)
	}
}
