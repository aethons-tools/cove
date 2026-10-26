package fleet

import (
	"context"
	"errors"
	"slices"
	"strings"
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
	<-r.wake // drop the kick from Add
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
