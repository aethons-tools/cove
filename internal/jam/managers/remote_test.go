package managers

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5/memfs"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/managers/managerpb"
	"github.com/aethons-tools/cove/internal/jam/managers/wire"
)

// req is one fully reassembled request as the manager side sees it.
type req struct {
	id     uint64
	op     string
	data   []byte
	frames int
}

// fakeStream is a conn's far end: it records what Jam sends, reassembles
// requests, and lets a test play the manager.
type fakeStream struct {
	c       *conn
	reqs    chan req
	cancels chan uint64
	mu      sync.Mutex
	asm     wire.Assembler
	frames  map[uint64]int
}

func newFakeStream() *fakeStream {
	s := &fakeStream{reqs: make(chan req, 16), cancels: make(chan uint64, 16), frames: map[uint64]int{}}
	s.c = newConn(func(m *managerpb.ManagerDown) error {
		f := m.GetFrame()
		if f == nil {
			return nil
		}
		if f.Op == wire.OpCancel {
			s.cancels <- f.Id
			return nil
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		s.frames[f.Id]++
		data, done, err := s.asm.Add(f)
		if err != nil {
			panic(err)
		}
		if done {
			s.reqs <- req{id: f.Id, op: f.Op, data: data, frames: s.frames[f.Id]}
		}
		return nil
	})
	return s
}

func (s *fakeStream) next(t *testing.T) req {
	t.Helper()
	select {
	case r := <-s.reqs:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("no request sent")
		return req{}
	}
}

func (s *fakeStream) result(t *testing.T, id uint64, body any, err error) {
	t.Helper()
	data, eerr := wire.EncodeResult(body, err)
	if eerr != nil {
		t.Fatal(eerr)
	}
	for _, f := range wire.Split(id, wire.OpResult, data) {
		if derr := s.c.deliver(f); derr != nil {
			t.Fatal(derr)
		}
	}
}

// serve answers the next request with body.
func (s *fakeStream) serve(t *testing.T, body any) req {
	r := s.next(t)
	s.result(t, r.id, body, nil)
	return r
}

func newRemote(t *testing.T) (*Remote, *fakeStream) {
	t.Helper()
	r := NewRemote("m1", slog.New(slog.DiscardHandler))
	s := newFakeStream()
	r.attach(s.c)
	return r, s
}

func TestRemoteDisconnected(t *testing.T) {
	r := NewRemote("m1", nil)
	if r.Available() {
		t.Fatal("available without a conn")
	}
	if lv, err := r.Probe(context.Background(), jam.Instance{}); lv != jam.LivenessUnknown || err != nil {
		t.Fatalf("Probe = %v, %v", lv, err)
	}
	if err := r.Teardown(context.Background(), jam.Instance{}); !errors.Is(err, jam.ErrManagerUnavailable) {
		t.Fatalf("Teardown err = %v", err)
	}
	if _, err := r.Raise(context.Background(), jam.RaiseSpec{}, jam.LaunchCreds{}); !errors.Is(err, jam.ErrManagerUnavailable) || errors.Is(err, jam.ErrRaiseInterrupted) {
		t.Fatalf("Raise err = %v", err)
	}
	if tag := r.ImageTag(jam.KitRef{ID: "k"}); tag != "" {
		t.Fatalf("ImageTag = %q", tag)
	}
}

func TestRemoteRaiseRoundTrip(t *testing.T) {
	r, s := newRemote(t)
	if !r.Available() {
		t.Fatal("not available with a conn")
	}
	go func() {
		rq := s.next(t)
		var in wire.RaiseRequest
		if err := json.Unmarshal(rq.data, &in); err != nil {
			t.Error(err)
		}
		if rq.op != wire.OpRaise || in.Spec.ActorID != "a1" || len(in.Spec.Placement.Managers) != 0 || in.Spec.Placement.Timeout != 0 {
			t.Errorf("request = %s %+v", rq.op, in.Spec)
		}
		s.result(t, rq.id, wire.RaiseResult{Location: "loc-1"}, nil)
	}()
	spec := jam.RaiseSpec{ActorID: "a1", Placement: jam.Placement{Managers: []string{"m1"}, Timeout: time.Minute}}
	loc, err := r.Raise(context.Background(), spec, jam.LaunchCreds{})
	if err != nil || loc != "loc-1" {
		t.Fatalf("Raise = %q, %v", loc, err)
	}
}

func TestRemoteRaiseInterrupted(t *testing.T) {
	r, s := newRemote(t)
	go func() {
		s.next(t) // the manager holds the request
		s.c.close(errors.New("stream dropped"))
	}()
	_, err := r.Raise(context.Background(), jam.RaiseSpec{ActorID: "a1"}, jam.LaunchCreds{})
	if !errors.Is(err, jam.ErrRaiseInterrupted) {
		t.Fatalf("err = %v, want ErrRaiseInterrupted", err)
	}
	if errors.Is(err, jam.ErrManagerUnavailable) {
		t.Fatalf("interrupted must not read as unavailable: %v", err)
	}
}

func TestRemoteCallCancel(t *testing.T) {
	r, s := newRemote(t)
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- r.Pause(ctx, jam.Instance{}) }()
	rq := s.next(t)
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	select {
	case id := <-s.cancels:
		if id != rq.id {
			t.Fatalf("cancel id = %d, want %d", id, rq.id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no OpCancel sent")
	}
	s.result(t, rq.id, wire.Empty{}, nil) // late result: dropped
}

func TestRemoteDraftLog(t *testing.T) {
	r, s := newRemote(t)
	go func() {
		rq := s.next(t)
		for _, p := range []string{"a", "b"} {
			if err := s.c.deliver(&managerpb.Frame{Id: rq.id, Op: wire.OpLog, Data: []byte(p)}); err != nil {
				t.Error(err)
			}
		}
		s.result(t, rq.id, wire.DraftBuildResult{ImageID: "img-1"}, nil)
	}()
	var buf bytes.Buffer
	id, err := r.BuildKitDraft(context.Background(), jam.KitDefinition{Ref: jam.KitRef{ID: "k"}}, "tag", &buf)
	if err != nil || id != "img-1" {
		t.Fatalf("BuildKitDraft = %q, %v", id, err)
	}
	if buf.String() != "ab" {
		t.Fatalf("log = %q", buf.String())
	}
}

func TestRemoteImageTagCached(t *testing.T) {
	r, s := newRemote(t)
	ref := jam.KitRef{ID: "k", Version: 1}
	go s.serve(t, wire.TagResult{Tag: "t1"})
	if got := r.ImageTag(ref); got != "t1" {
		t.Fatalf("tag = %q", got)
	}
	if got := r.ImageTag(ref); got != "t1" {
		t.Fatalf("cached tag = %q", got)
	}
	select {
	case extra := <-s.reqs:
		t.Fatalf("second ImageTag sent %s", extra.op)
	default:
	}
	s2 := newFakeStream()
	r.attach(s2.c)
	go s2.serve(t, wire.TagResult{Tag: "t2"})
	if got := r.ImageTag(ref); got != "t2" {
		t.Fatalf("tag after attach = %q", got)
	}
}

func TestRemoteImageTagFailureNotCached(t *testing.T) {
	r, s := newRemote(t)
	ref := jam.KitRef{ID: "k"}
	go func() {
		rq := s.next(t)
		s.result(t, rq.id, nil, errors.New("boom"))
	}()
	if got := r.ImageTag(ref); got != "" {
		t.Fatalf("tag = %q", got)
	}
	go s.serve(t, wire.TagResult{Tag: "ok"})
	if got := r.ImageTag(ref); got != "ok" {
		t.Fatalf("retry tag = %q", got)
	}
}

func TestRemoteAttachReplacesAndFailsOld(t *testing.T) {
	r, a := newRemote(t)
	errc := make(chan error, 1)
	go func() { errc <- r.Pause(context.Background(), jam.Instance{}) }()
	ra := a.next(t)

	b := newFakeStream()
	r.attach(b.c)
	err := <-errc
	if !errors.Is(err, jam.ErrManagerUnavailable) || errors.Is(err, jam.ErrRaiseInterrupted) {
		t.Fatalf("old call err = %v", err)
	}

	bErr := make(chan error, 1)
	go func() { bErr <- r.Unpause(context.Background(), jam.Instance{}) }()
	rb := b.next(t)
	if rb.id != ra.id {
		t.Fatalf("ids should be per conn: %d vs %d", rb.id, ra.id)
	}
	a.result(t, ra.id, wire.Empty{}, nil) // late result on the old conn
	select {
	case err := <-bErr:
		t.Fatalf("B's call resolved by A's result: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	b.result(t, rb.id, wire.Empty{}, nil)
	if err := <-bErr; err != nil {
		t.Fatal(err)
	}

	r.detach(a.c) // a stale detach must not clear the current conn
	if !r.Available() {
		t.Fatal("detach of an old conn cleared the current one")
	}
	r.detach(b.c)
	if r.Available() {
		t.Fatal("still available after detach")
	}
}

func TestRemoteLargeKitDefinition(t *testing.T) {
	r, s := newRemote(t)
	big := make([]byte, 5<<20)
	if _, err := rand.Read(big); err != nil {
		t.Fatal(err)
	}
	fs := memfs.New()
	f, err := fs.OpenFile("blob", os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.Write(big)
	f.Close()
	ref := jam.KitRef{ID: "k", Version: 1, Digest: "d"}
	def := jam.KitDefinition{Ref: ref, Chain: []jam.KitLink{{Ref: ref, Files: fs}}}

	go func() {
		rq := s.next(t)
		if rq.frames < 2 {
			t.Errorf("frames = %d, want more than one", rq.frames)
		}
		var in wire.KitRequest
		if err := json.Unmarshal(rq.data, &in); err != nil {
			t.Error(err)
		}
		got, err := in.Def.Definition()
		if err != nil {
			t.Error(err)
			return
		}
		rf, err := got.Chain[0].Files.Open("blob")
		if err != nil {
			t.Error(err)
			return
		}
		var back bytes.Buffer
		back.ReadFrom(rf)
		if !bytes.Equal(back.Bytes(), big) {
			t.Error("kit file differs after reassembly")
		}
		s.result(t, rq.id, jam.KitStatus{}, nil)
	}()
	if _, err := r.PrepareKit(context.Background(), def); err != nil {
		t.Fatal(err)
	}
}
