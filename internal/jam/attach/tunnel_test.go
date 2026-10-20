// internal/jam/attach/tunnel_test.go
package attach

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/attach/attachpb"
)

// fakeStream is an in-memory TunnelStream: frames Sent by the broker land in
// toStudio; frames the "studio" writes go into fromStudio.
type fakeStream struct {
	ctx        context.Context
	cancel     context.CancelFunc
	toStudio   chan *attachpb.TunnelFrame
	fromStudio chan *attachpb.TunnelFrame
	returned   atomic.Bool  // set once the Claim (gRPC handler) has returned
	violations atomic.Int32 // stream ops started after return
}

func newFakeStream() *fakeStream {
	ctx, cancel := context.WithCancel(context.Background())
	return &fakeStream{ctx: ctx, cancel: cancel, toStudio: make(chan *attachpb.TunnelFrame, 16), fromStudio: make(chan *attachpb.TunnelFrame, 16)}
}
func (f *fakeStream) Send(fr *attachpb.TunnelFrame) error {
	if f.returned.Load() {
		f.violations.Add(1)
	}
	f.toStudio <- fr
	return nil
}
func (f *fakeStream) Recv() (*attachpb.TunnelFrame, error) {
	if f.returned.Load() {
		f.violations.Add(1)
	}
	select {
	case fr := <-f.fromStudio:
		return fr, nil
	case <-f.ctx.Done():
		return nil, io.EOF
	}
}
func (f *fakeStream) Context() context.Context { return f.ctx }

type pushRec struct {
	mu  sync.Mutex
	ids []string
	ok  bool
}

func (p *pushRec) push(actor, id string, port int32) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ids = append(p.ids, actor+"/"+id)
	return p.ok
}
func (p *pushRec) last() string { p.mu.Lock(); defer p.mu.Unlock(); return p.ids[len(p.ids)-1] }

func openFrame(id string) *attachpb.TunnelFrame {
	return &attachpb.TunnelFrame{Msg: &attachpb.TunnelFrame_Open{Open: &attachpb.TunnelOpen{Id: id}}}
}
func dataFrame(b string) *attachpb.TunnelFrame {
	return &attachpb.TunnelFrame{Msg: &attachpb.TunnelFrame_Data{Data: []byte(b)}}
}

func TestBrokerSplicesBothWays(t *testing.T) {
	p := &pushRec{ok: true}
	b := NewTunnelBroker(p.push, TunnelOptions{DialBack: time.Second})
	var human net.Conn
	errc := make(chan error, 1)
	go func() { var err error; human, err = b.Open(context.Background(), "ses_a", 2222); errc <- err }()
	waitFor(t, func() bool { p.mu.Lock(); defer p.mu.Unlock(); return len(p.ids) == 1 })
	id := p.last()[len("ses_a/"):]
	s := newFakeStream()
	s.fromStudio <- openFrame(id)
	go func() { _ = b.Claim("ses_a", s) }()
	if err := <-errc; err != nil {
		t.Fatalf("Open: %v", err)
	}
	s.fromStudio <- dataFrame("SSH-2.0-OpenSSH\r\n")
	buf := make([]byte, 64)
	n, _ := human.Read(buf)
	if string(buf[:n]) != "SSH-2.0-OpenSSH\r\n" {
		t.Fatalf("human read %q", buf[:n])
	}
	_, _ = human.Write([]byte("client hello"))
	got := <-s.toStudio
	if string(got.GetData()) != "client hello" {
		t.Fatalf("studio got %v", got)
	}
	_ = human.Close()
	if fr := <-s.toStudio; fr.GetClose() == nil {
		t.Fatalf("want close frame after human closes, got %v", fr)
	}
}

func TestBrokerDialBackTimeout(t *testing.T) {
	b := NewTunnelBroker((&pushRec{ok: true}).push, TunnelOptions{DialBack: 50 * time.Millisecond})
	_, err := b.Open(context.Background(), "ses_a", 2222)
	if !errors.Is(err, ErrDialBackTimeout) {
		t.Fatalf("err = %v, want ErrDialBackTimeout", err)
	}
	b2 := NewTunnelBroker((&pushRec{ok: false}).push, TunnelOptions{DialBack: time.Second})
	if _, err := b2.Open(context.Background(), "ses_a", 2222); !errors.Is(err, ErrStudioNotConnected) {
		t.Fatalf("err = %v, want ErrStudioNotConnected", err)
	}
}

func TestBrokerRejectsForeignAndReplayedClaims(t *testing.T) {
	p := &pushRec{ok: true}
	b := NewTunnelBroker(p.push, TunnelOptions{DialBack: 300 * time.Millisecond})
	errc := make(chan error, 1)
	go func() { _, err := b.Open(context.Background(), "ses_a", 2222); errc <- err }()
	waitFor(t, func() bool { p.mu.Lock(); defer p.mu.Unlock(); return len(p.ids) == 1 })
	id := p.last()[len("ses_a/"):]
	foreign := newFakeStream()
	foreign.fromStudio <- openFrame(id)
	if err := b.Claim("ses_b", foreign); err == nil {
		t.Fatal("another studio claimed ses_a's tunnel")
	}
	ok := newFakeStream()
	ok.fromStudio <- openFrame(id)
	go func() { _ = b.Claim("ses_a", ok) }()
	if err := <-errc; err != nil {
		t.Fatalf("legit claim failed: %v", err)
	}
	replay := newFakeStream()
	replay.fromStudio <- openFrame(id)
	if err := b.Claim("ses_a", replay); err == nil {
		t.Fatal("replayed id accepted")
	}
}

func TestBrokerCapAndCloseAll(t *testing.T) {
	p := &pushRec{ok: true}
	b := NewTunnelBroker(p.push, TunnelOptions{DialBack: time.Second, MaxPerStudio: 1})
	conns := make(chan net.Conn, 1)
	go func() { c, _ := b.Open(context.Background(), "ses_a", 2222); conns <- c }()
	waitFor(t, func() bool { p.mu.Lock(); defer p.mu.Unlock(); return len(p.ids) == 1 })
	s := newFakeStream()
	s.fromStudio <- openFrame(p.last()[len("ses_a/"):])
	go func() { _ = b.Claim("ses_a", s) }()
	c := <-conns
	if _, err := b.Open(context.Background(), "ses_a", 2222); !errors.Is(err, ErrTooManyTunnels) {
		t.Fatalf("second open err = %v, want ErrTooManyTunnels", err)
	}
	b.CloseAll("ses_a")
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatal("human conn still open after CloseAll")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met")
}

// Jam closing the tunnel (CloseAll) must end the Claim RPC even though the
// studio's stream context is still live.
func TestClaimReturnsWhenJamClosesTunnel(t *testing.T) {
	p := &pushRec{ok: true}
	b := NewTunnelBroker(p.push, TunnelOptions{DialBack: time.Second})
	conns := make(chan net.Conn, 1)
	go func() { c, _ := b.Open(context.Background(), "ses_a", 2222); conns <- c }()
	waitFor(t, func() bool { p.mu.Lock(); defer p.mu.Unlock(); return len(p.ids) == 1 })
	s := newFakeStream()
	s.fromStudio <- openFrame(p.last()[len("ses_a/"):])
	claimed := make(chan error, 1)
	go func() { claimed <- b.Claim("ses_a", s) }()
	c := <-conns
	defer c.Close()
	b.CloseAll("ses_a")
	select {
	case <-claimed:
	case <-time.After(time.Second):
		t.Fatal("Claim still blocked after Jam closed the tunnel")
	}
}

// A Claim racing Open's dial-back timeout must never leave the claimed stream
// blocked: either Claim loses (unknown id) or Open closes the handed-over stream.
func TestClaimNeverLeaksWhenRacingDialBackTimeout(t *testing.T) {
	for i := 0; i < 50; i++ {
		var b *TunnelBroker
		claimed := make(chan error, 1)
		push := func(actor, id string, port int32) bool {
			s := newFakeStream()
			s.fromStudio <- openFrame(id)
			go func() {
				time.Sleep(time.Duration(i%5) * 500 * time.Microsecond)
				claimed <- b.Claim(actor, s)
			}()
			return true
		}
		b = NewTunnelBroker(push, TunnelOptions{DialBack: time.Millisecond})
		c, err := b.Open(context.Background(), "ses_a", 2222)
		if err == nil {
			_ = c.Close() // Claim won the race; closing ends it
		}
		select {
		case <-claimed:
		case <-time.After(2 * time.Second):
			t.Fatalf("iteration %d: Claim leaked (open err = %v)", i, err)
		}
	}
}

func TestOpenContextCancelDoesNotLeakClaim(t *testing.T) {
	p := &pushRec{ok: true}
	b := NewTunnelBroker(p.push, TunnelOptions{DialBack: time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { _, err := b.Open(ctx, "ses_a", 2222); errc <- err }()
	waitFor(t, func() bool { p.mu.Lock(); defer p.mu.Unlock(); return len(p.ids) == 1 })
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	s := newFakeStream()
	s.fromStudio <- openFrame(p.last()[len("ses_a/"):])
	if err := b.Claim("ses_a", s); err == nil {
		t.Fatal("claim of a cancelled open succeeded")
	}
}

// claimGuarded runs Claim and marks the stream returned afterwards, so any
// later Send/Recv is counted as a violation.
func claimGuarded(b *TunnelBroker, actor string, s *fakeStream) <-chan error {
	c := make(chan error, 1)
	go func() { err := b.Claim(actor, s); s.returned.Store(true); c <- err }()
	return c
}

func TestNoStreamOpsAfterClaimReturns(t *testing.T) {
	for _, mode := range []string{"closeAll", "streamCtx", "humanClose"} {
		t.Run(mode, func(t *testing.T) {
			p := &pushRec{ok: true}
			b := NewTunnelBroker(p.push, TunnelOptions{DialBack: time.Second})
			conns := make(chan net.Conn, 1)
			go func() { c, _ := b.Open(context.Background(), "ses_a", 2222); conns <- c }()
			waitFor(t, func() bool { p.mu.Lock(); defer p.mu.Unlock(); return len(p.ids) == 1 })
			s := newFakeStream()
			s.fromStudio <- openFrame(p.last()[len("ses_a/"):])
			claimed := claimGuarded(b, "ses_a", s)
			c := <-conns
			// keep the human→studio path busy while tearing down
			go func() {
				for i := 0; i < 50; i++ {
					if _, err := c.Write([]byte("x")); err != nil {
						return
					}
				}
			}()
			go func() {
				for range s.toStudio {
				}
			}()
			switch mode {
			case "closeAll":
				b.CloseAll("ses_a")
			case "streamCtx":
				s.cancel()
			case "humanClose":
				_ = c.Close()
			}
			select {
			case <-claimed:
			case <-time.After(2 * time.Second):
				t.Fatal("Claim did not return")
			}
			time.Sleep(50 * time.Millisecond)
			if n := s.violations.Load(); n != 0 {
				t.Fatalf("%d stream ops after Claim returned", n)
			}
		})
	}
}

func TestCapCountsPendingOpens(t *testing.T) {
	p := &pushRec{ok: true}
	b := NewTunnelBroker(p.push, TunnelOptions{DialBack: 200 * time.Millisecond, MaxPerStudio: 1})
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { _, err := b.Open(context.Background(), "ses_a", 2222); errs <- err }()
	}
	var tooMany, timeout int
	for i := 0; i < 2; i++ {
		switch err := <-errs; {
		case errors.Is(err, ErrTooManyTunnels):
			tooMany++
		case errors.Is(err, ErrDialBackTimeout):
			timeout++
		default:
			t.Fatalf("unexpected err %v", err)
		}
	}
	if tooMany != 1 || timeout != 1 {
		t.Fatalf("tooMany=%d timeout=%d, want 1/1", tooMany, timeout)
	}
	// slot released after the failed open
	if _, err := b.Open(context.Background(), "ses_a", 2222); !errors.Is(err, ErrDialBackTimeout) {
		t.Fatalf("slot leaked: %v", err)
	}
}
