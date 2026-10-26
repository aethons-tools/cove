package managers

import (
	"context"
	"testing"

	"github.com/aethons-tools/cove/internal/jam/managers/managerpb"
	"github.com/aethons-tools/cove/internal/jam/managers/wire"
)

// A result frame for an id with no call in flight is dropped, never buffered:
// a later call given that id is not corrupted by it.
func TestConnDropsResultFramesForUnknownIDs(t *testing.T) {
	s := newFakeStream()
	if err := s.c.deliver(&managerpb.Frame{Id: 1, Op: wire.OpResult, Data: []byte("junk"), More: true}); err != nil {
		t.Fatal(err)
	}
	s.c.mu.Lock()
	n := s.c.asm.Buffered()
	s.c.mu.Unlock()
	if n != 0 {
		t.Fatalf("buffered partials for unknown ids = %d, want 0", n)
	}
	errc := make(chan error, 1)
	var res wire.TagResult
	go func() { errc <- s.c.call(context.Background(), wire.OpImageTag, nil, &res, nil) }()
	if r := s.serve(t, wire.TagResult{Tag: "t"}); r.id != 1 {
		t.Fatalf("call id = %d, want 1", r.id)
	}
	if err := <-errc; err != nil || res.Tag != "t" {
		t.Fatalf("call = %+v, %v", res, err)
	}
}

// A call that ends (here: cancelled) drops its partial result.
func TestConnCancelDropsPartialResult(t *testing.T) {
	s := newFakeStream()
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- s.c.call(ctx, wire.OpImageTag, nil, nil, nil) }()
	r := s.next(t)
	if err := s.c.deliver(&managerpb.Frame{Id: r.id, Op: wire.OpResult, Data: []byte("part"), More: true}); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := <-errc; err != context.Canceled {
		t.Fatalf("err = %v", err)
	}
	s.c.mu.Lock()
	n := s.c.asm.Buffered()
	s.c.mu.Unlock()
	if n != 0 {
		t.Fatalf("buffered partials after cancel = %d, want 0", n)
	}
}
