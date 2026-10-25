// internal/jam/attach/tunnel_test.go
package attach

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
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

// openPending starts an Open on actor and returns its result channel and the
// tunnel id the studio was told to claim.
func openPending(t *testing.T, b *TunnelBroker, p *pushRec, actor string) (<-chan net.Conn, <-chan error, string) {
	t.Helper()
	p.mu.Lock()
	before := len(p.ids)
	p.mu.Unlock()
	conns, errs := make(chan net.Conn, 1), make(chan error, 1)
	go func() {
		c, err := b.Open(context.Background(), actor, 2222)
		conns <- c
		errs <- err
	}()
	waitFor(t, func() bool { p.mu.Lock(); defer p.mu.Unlock(); return len(p.ids) > before })
	return conns, errs, p.last()[len(actor)+1:]
}

// Live counts a studio's tunnels in use: pending opens and spliced ones.
func TestBrokerLiveCountsPendingAndLive(t *testing.T) {
	p := &pushRec{ok: true}
	b := NewTunnelBroker(p.push, TunnelOptions{DialBack: time.Second})
	if n := b.Live("ses_a"); n != 0 {
		t.Fatalf("Live before any open = %d", n)
	}
	conns, _, id := openPending(t, b, p, "ses_a")
	if n := b.Live("ses_a"); n != 1 {
		t.Fatalf("Live with a pending open = %d, want 1", n)
	}
	s := newFakeStream()
	s.fromStudio <- openFrame(id)
	go func() { _ = b.Claim("ses_a", s) }()
	c := <-conns
	if n := b.Live("ses_a"); n != 1 || b.Live("ses_b") != 0 {
		t.Fatalf("Live with a spliced tunnel = %d (ses_b %d), want 1 (0)", n, b.Live("ses_b"))
	}
	_ = c.Close()
	waitFor(t, func() bool { return b.Live("ses_a") == 0 })
}

// Once CloseAll starts, the studio takes no new opens or claims — a pending
// open is cancelled too — until Reopen.
func TestCloseAllClosesPendingAndRefusesUntilReopen(t *testing.T) {
	p := &pushRec{ok: true}
	b := NewTunnelBroker(p.push, TunnelOptions{DialBack: 5 * time.Second})
	_, errs, id := openPending(t, b, p, "ses_a")
	b.CloseAll("ses_a")
	select {
	case err := <-errs:
		if !errors.Is(err, ErrTunnelsClosed) {
			t.Fatalf("pending open err = %v, want ErrTunnelsClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("CloseAll left a pending open waiting for its dial-back")
	}
	late := newFakeStream()
	late.fromStudio <- openFrame(id)
	if err := b.Claim("ses_a", late); err == nil {
		t.Fatal("a claim after CloseAll was accepted")
	}
	if n := b.Live("ses_a"); n != 0 {
		t.Fatalf("Live after CloseAll = %d", n)
	}
	if _, err := b.Open(context.Background(), "ses_a", 2222); !errors.Is(err, ErrTunnelsClosed) {
		t.Fatalf("open after CloseAll err = %v, want ErrTunnelsClosed", err)
	}
	// Other studios are unaffected.
	if _, err := (NewTunnelBroker((&pushRec{ok: false}).push, TunnelOptions{})).Open(context.Background(), "ses_b", 2222); !errors.Is(err, ErrStudioNotConnected) {
		t.Fatalf("ses_b open err = %v", err)
	}
	b.Reopen("ses_a")
	conns, _, id := openPending(t, b, p, "ses_a")
	s := newFakeStream()
	s.fromStudio <- openFrame(id)
	go func() { _ = b.Claim("ses_a", s) }()
	if c := <-conns; c == nil {
		t.Fatal("open after Reopen failed")
	}
}

// A claim racing CloseAll never leaves a tunnel open past it: whichever
// wins, the human end ends, the Claim returns and no slot is held.
func TestClaimRacingCloseAllDoesNotSurvive(t *testing.T) {
	for i := 0; i < 100; i++ {
		p := &pushRec{ok: true}
		b := NewTunnelBroker(p.push, TunnelOptions{DialBack: 5 * time.Second})
		conns, errs, id := openPending(t, b, p, "ses_a")
		s := newFakeStream()
		s.fromStudio <- openFrame(id)
		claimed := make(chan error, 1)
		go func() { claimed <- b.Claim("ses_a", s) }()
		go b.CloseAll("ses_a")
		c, err := <-conns, <-errs
		if err == nil {
			_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
			if _, rerr := c.Read(make([]byte, 1)); rerr == nil || errors.Is(rerr, os.ErrDeadlineExceeded) {
				t.Fatalf("iteration %d: a tunnel claimed during CloseAll survived it (read err %v)", i, rerr)
			}
		} else if !errors.Is(err, ErrTunnelsClosed) {
			t.Fatalf("iteration %d: open err = %v", i, err)
		}
		s.cancel()
		select {
		case <-claimed:
		case <-time.After(2 * time.Second):
			t.Fatalf("iteration %d: Claim did not return", i)
		}
		waitFor(t, func() bool { return b.Live("ses_a") == 0 })
	}
}

// CloseIfUnused closes a studio's tunnels only when none is in use, in one
// step, so no open can slip in between the check and the close.
func TestCloseIfUnused(t *testing.T) {
	p := &pushRec{ok: true}
	b := NewTunnelBroker(p.push, TunnelOptions{DialBack: 5 * time.Second})
	_, errs, _ := openPending(t, b, p, "ses_a")
	if b.CloseIfUnused("ses_a") {
		t.Fatal("closed a studio with a tunnel in use")
	}
	b.CloseAll("ses_a")
	<-errs
	b.Reopen("ses_a")
	if !b.CloseIfUnused("ses_a") {
		t.Fatal("did not close an unused studio")
	}
	if _, err := b.Open(context.Background(), "ses_a", 2222); !errors.Is(err, ErrTunnelsClosed) {
		t.Fatalf("open after CloseIfUnused err = %v, want ErrTunnelsClosed", err)
	}
}

// Previews may hold at most 12 of a studio's 16 tunnels, so SSH always has
// 4: with previews at their cap a preview open is ErrTooManyPreviewTunnels
// (an ErrTooManyTunnels) while SSH still opens, up to the studio's total.
func TestPreviewTunnelsLeaveRoomForSSH(t *testing.T) {
	p := &pushRec{ok: true}
	b := NewTunnelBroker(p.push, TunnelOptions{DialBack: 10 * time.Second})
	t.Cleanup(func() { b.CloseAll("ses_a") })
	pushed := func() int { p.mu.Lock(); defer p.mu.Unlock(); return len(p.ids) }
	previews := make(chan error, 16)
	for i := 0; i < 12; i++ {
		go func() { _, err := b.OpenPreview(context.Background(), "ses_a", 5173); previews <- err }()
	}
	waitFor(t, func() bool { return pushed() == 12 })
	if _, err := b.OpenPreview(context.Background(), "ses_a", 5173); !errors.Is(err, ErrTooManyPreviewTunnels) || !errors.Is(err, ErrTooManyTunnels) {
		t.Fatalf("13th preview open: %v, want ErrTooManyPreviewTunnels", err)
	}
	for i := 0; i < 4; i++ {
		go func() { _, _ = b.Open(context.Background(), "ses_a", 2222) }()
	}
	waitFor(t, func() bool { return pushed() == 16 })
	if _, err := b.Open(context.Background(), "ses_a", 2222); !errors.Is(err, ErrTooManyTunnels) || errors.Is(err, ErrTooManyPreviewTunnels) {
		t.Fatalf("17th open: %v, want ErrTooManyTunnels", err)
	}
	if n := b.Live("ses_a"); n != 16 {
		t.Fatalf("Live = %d, want 16", n)
	}
	// A released preview slot is a preview slot again.
	b.CloseAll("ses_a")
	for i := 0; i < 12; i++ {
		if err := <-previews; !errors.Is(err, ErrTunnelsClosed) {
			t.Fatalf("pending preview: %v", err)
		}
	}
	waitFor(t, func() bool { return b.Live("ses_a") == 0 })
	b.Reopen("ses_a")
	go func() { _, _ = b.OpenPreview(context.Background(), "ses_a", 5173) }()
	waitFor(t, func() bool { return pushed() == 17 })
}

// A smaller studio total keeps the same SSH reserve (and previews at least 1).
func TestPreviewTunnelCapDefaults(t *testing.T) {
	for total, want := range map[int]int{16: 12, 8: 4, 4: 1, 2: 1, 1: 1} {
		b := NewTunnelBroker(func(string, string, int32) bool { return true }, TunnelOptions{MaxPerStudio: total})
		if b.opt.MaxPreviewPerStudio != want {
			t.Errorf("MaxPerStudio %d: preview cap %d, want %d", total, b.opt.MaxPreviewPerStudio, want)
		}
	}
}

// OpenPreview waits for the dial-back until its ctx deadline when that is
// later than DialBack: the studio may first start the built-in IDE.
func TestOpenPreviewWaitsUntilCtxDeadline(t *testing.T) {
	p := &pushRec{ok: true}
	b := NewTunnelBroker(p.push, TunnelOptions{DialBack: 50 * time.Millisecond})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	type result struct {
		c   net.Conn
		err error
	}
	res := make(chan result, 1)
	go func() { c, err := b.OpenPreview(ctx, "ses_a", 13337); res <- result{c, err} }()
	waitFor(t, func() bool { p.mu.Lock(); defer p.mu.Unlock(); return len(p.ids) == 1 })
	time.Sleep(300 * time.Millisecond) // well past DialBack
	s := newFakeStream()
	s.fromStudio <- openFrame(p.last()[len("ses_a/"):])
	go func() { _ = b.Claim("ses_a", s) }()
	r := <-res
	if r.err != nil {
		t.Fatalf("OpenPreview: %v, want the late dial-back spliced", r.err)
	}
	_ = r.c.Close()
	// Without a deadline past DialBack it still times out at DialBack.
	start := time.Now()
	if _, err := b.OpenPreview(context.Background(), "ses_a", 13337); !errors.Is(err, ErrDialBackTimeout) {
		t.Fatalf("no deadline: %v, want ErrDialBackTimeout", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("no deadline: waited %v", d)
	}
}

// Open (SSH) keeps DialBack whatever its ctx deadline.
func TestOpenKeepsDialBackDespiteCtxDeadline(t *testing.T) {
	b := NewTunnelBroker((&pushRec{ok: true}).push, TunnelOptions{DialBack: 50 * time.Millisecond})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	start := time.Now()
	if _, err := b.Open(ctx, "ses_a", 2222); !errors.Is(err, ErrDialBackTimeout) {
		t.Fatalf("err = %v, want ErrDialBackTimeout", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("Open waited %v, want about DialBack", d)
	}
}
