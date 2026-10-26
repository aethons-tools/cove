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
	a := newFake("a")
	r := placeRouter(t, Local(newFake("x")), a)
	p, err := r.RaiseOn(context.Background(), spec(0), jam.LaunchCreds{})
	if err != nil || p.Manager != "a" {
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

func TestRaiseOnInterruptedDoesNotFallThrough(t *testing.T) {
	a, b := newFake("a"), newFake("b")
	a.raiseErr = fmt.Errorf("x: %w", jam.ErrRaiseInterrupted)
	r := placeRouter(t, a, b)
	start := time.Now()
	_, err := r.RaiseOn(context.Background(), spec(5*time.Second, "a", "b"), jam.LaunchCreds{})
	if !errors.Is(err, jam.ErrRaiseInterrupted) || time.Since(start) > 100*time.Millisecond {
		t.Fatalf("err %v after %v", err, time.Since(start))
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
	var wg sync.WaitGroup
	errs := make(chan error, 3)
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := r.RaiseOn(context.Background(), spec(5*time.Second, "a"), jam.LaunchCreds{})
			errs <- err
		}()
	}
	// wait until all three are parked: each has taken the wake channel.
	deadline := time.Now().Add(2 * time.Second)
	for r.waiters.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
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
	_, err := r.RaiseOn(context.Background(), spec(200*time.Millisecond, "a"), jam.LaunchCreds{})
	var nm *jam.NoManagerError
	if !errors.As(err, &nm) {
		t.Fatalf("err %v", err)
	}
	if n := a.raiseCalls.Load(); n > 6 || n < 2 {
		t.Fatalf("calls = %d, want 2..6", n)
	}

	// A Kick makes the manager eligible at once.
	b := newFake("b")
	b.raiseErr = errors.New("full")
	r2 := placeRouter(t, b)
	r2.poll = time.Hour
	go func() { _, _ = r2.RaiseOn(context.Background(), spec(5*time.Second, "b"), jam.LaunchCreds{}) }()
	deadline := time.Now().Add(2 * time.Second)
	for (b.raiseCalls.Load() < 1 || r2.waiters.Load() < 1) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	before := b.raiseCalls.Load()
	r2.Kick()
	deadline = time.Now().Add(5 * time.Millisecond)
	for b.raiseCalls.Load() == before && time.Now().Before(deadline) {
		time.Sleep(100 * time.Microsecond)
	}
	if b.raiseCalls.Load() == before {
		t.Fatal("no retry within 5ms of Kick")
	}
}

func TestUnavailableNotBackedOff(t *testing.T) {
	a := newFake("a")
	a.avail.Store(false)
	r := placeRouter(t, a)
	r.poll = 10 * time.Millisecond
	time.AfterFunc(30*time.Millisecond, func() { a.avail.Store(true) }) // no Kick
	start := time.Now()
	p, err := r.RaiseOn(context.Background(), spec(5*time.Second, "a"), jam.LaunchCreds{})
	if err != nil || p.Manager != "a" {
		t.Fatalf("got %+v, %v", p, err)
	}
	if d := time.Since(start); d > 60*time.Millisecond {
		t.Fatalf("took %v", d)
	}
}
