// Package attach — tunnel.go: the Jam side of studio tunnels (studio
// collaboration, docs/usage/jam/collaboration.md). A human connection asks
// Open for a studio port; the broker pushes OpenTunnel down the studio's
// Attach stream and waits (DialBack) for the studio to dial a Tunnel stream
// back and claim the id, then splices the two. Ids are single-use and bound
// to the studio asked.
package attach

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/aethons-tools/cove/internal/jam/attach/attachpb"
)

var (
	ErrStudioNotConnected = errors.New("studio not connected")
	ErrDialBackTimeout    = errors.New("studio did not open the tunnel in time (is its cove-master current?)")
	ErrTooManyTunnels     = errors.New("too many open tunnels for this studio")
	errUnknownTunnel      = errors.New("unknown or already-claimed tunnel id")
)

type TunnelOptions struct {
	DialBack     time.Duration
	MaxPerStudio int
	Idle         time.Duration
	Log          *slog.Logger
}

type TunnelStream interface {
	Send(*attachpb.TunnelFrame) error
	Recv() (*attachpb.TunnelFrame, error)
	Context() context.Context
}

type pending struct {
	actorID string
	ready   chan *handoff // buffered(1): Claim sends after taking the entry
}

// handoff is a claimed stream passed from Claim to Open. done is closed when
// the tunnel ends on the Jam side (or Open abandons it), releasing Claim.
type handoff struct {
	stream TunnelStream
	done   chan struct{}
	once   sync.Once
}

func (h *handoff) finish() { h.once.Do(func() { close(h.done) }) }

type TunnelBroker struct {
	push func(actorID, id string, port int32) bool
	opt  TunnelOptions

	mu      sync.Mutex
	pending map[string]*pending
	live    map[string]map[*tunnelConn]struct{} // actorID → open human ends
}

func NewTunnelBroker(push func(actorID, id string, port int32) bool, opt TunnelOptions) *TunnelBroker {
	if opt.DialBack <= 0 {
		opt.DialBack = 10 * time.Second
	}
	if opt.MaxPerStudio <= 0 {
		opt.MaxPerStudio = 16
	}
	if opt.Idle <= 0 {
		opt.Idle = 30 * time.Minute
	}
	if opt.Log == nil {
		opt.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &TunnelBroker{push: push, opt: opt, pending: map[string]*pending{}, live: map[string]map[*tunnelConn]struct{}{}}
}

func newID() string { b := make([]byte, 16); _, _ = rand.Read(b); return hex.EncodeToString(b) }

// Open returns the human-side end of a tunnel to actorID's 127.0.0.1:port.
func (b *TunnelBroker) Open(ctx context.Context, actorID string, port int32) (net.Conn, error) {
	b.mu.Lock()
	if len(b.live[actorID]) >= b.opt.MaxPerStudio {
		b.mu.Unlock()
		return nil, ErrTooManyTunnels
	}
	id := newID()
	p := &pending{actorID: actorID, ready: make(chan *handoff, 1)}
	b.pending[id] = p
	b.mu.Unlock()

	if !b.push(actorID, id, port) {
		b.mu.Lock()
		delete(b.pending, id)
		b.mu.Unlock()
		return nil, ErrStudioNotConnected
	}
	t := time.NewTimer(b.opt.DialBack)
	defer t.Stop()
	select {
	case h := <-p.ready:
		return b.splice(actorID, h), nil
	case <-t.C:
		return nil, b.abandon(id, p, ErrDialBackTimeout)
	case <-ctx.Done():
		return nil, b.abandon(id, p, ctx.Err())
	}
}

// abandon gives up on a pending open. If a Claim already took the entry, the
// handed-over stream is closed so the studio's Tunnel RPC does not leak.
func (b *TunnelBroker) abandon(id string, p *pending, cause error) error {
	b.mu.Lock()
	_, stillPending := b.pending[id]
	delete(b.pending, id)
	b.mu.Unlock()
	if !stillPending {
		h := <-p.ready // Claim took the entry and is about to send (or has sent)
		_ = h.stream.Send(closeFrame("open abandoned"))
		h.finish()
	}
	return cause
}

func closeFrame(reason string) *attachpb.TunnelFrame {
	return &attachpb.TunnelFrame{Msg: &attachpb.TunnelFrame_Close{Close: &attachpb.TunnelClose{Reason: reason}}}
}

// Claim is the Tunnel RPC body after authentication: the first frame must be
// open{id} for a pending id bound to actorID. It blocks until the tunnel ends
// on the Jam side (human closed, idle timeout, CloseAll) or the stream ends.
func (b *TunnelBroker) Claim(actorID string, s TunnelStream) error {
	fr, err := s.Recv()
	if err != nil {
		return err
	}
	open := fr.GetOpen()
	if open == nil {
		return errors.New("first tunnel frame must be open")
	}
	b.mu.Lock()
	p, ok := b.pending[open.GetId()]
	if ok && p.actorID == actorID {
		delete(b.pending, open.GetId())
	}
	b.mu.Unlock()
	if !ok || p.actorID != actorID {
		return errUnknownTunnel
	}
	h := &handoff{stream: s, done: make(chan struct{})}
	p.ready <- h
	select {
	case <-h.done:
	case <-s.Context().Done():
	}
	return nil
}

func (b *TunnelBroker) splice(actorID string, h *handoff) net.Conn {
	s := h.stream
	human, inner := net.Pipe()
	tc := &tunnelConn{Conn: human}
	// gRPC streams do not allow concurrent Sends.
	var sendMu sync.Mutex
	send := func(fr *attachpb.TunnelFrame) error {
		sendMu.Lock()
		defer sendMu.Unlock()
		return s.Send(fr)
	}
	var once sync.Once
	var idle *time.Timer
	done := func(reason string) {
		once.Do(func() {
			_ = send(closeFrame(reason))
			_ = inner.Close()
			idle.Stop()
			b.mu.Lock()
			if m := b.live[actorID]; m != nil {
				delete(m, tc)
				if len(m) == 0 {
					delete(b.live, actorID)
				}
			}
			b.mu.Unlock()
			h.finish()
		})
	}
	tc.closeAll = func() { done("closed by Jam") }
	idle = time.AfterFunc(b.opt.Idle, func() { done("idle timeout") })
	touch := func() { idle.Reset(b.opt.Idle) }

	b.mu.Lock()
	if b.live[actorID] == nil {
		b.live[actorID] = map[*tunnelConn]struct{}{}
	}
	b.live[actorID][tc] = struct{}{}
	b.mu.Unlock()

	go func() { // studio → human
		for {
			fr, err := s.Recv()
			if err != nil || fr.GetClose() != nil {
				done("studio closed")
				return
			}
			touch()
			if _, err := inner.Write(fr.GetData()); err != nil {
				done("human closed")
				return
			}
		}
	}()
	go func() { // human → studio
		buf := make([]byte, 32<<10)
		for {
			n, err := inner.Read(buf)
			if n > 0 {
				touch()
				if serr := send(&attachpb.TunnelFrame{Msg: &attachpb.TunnelFrame_Data{Data: append([]byte(nil), buf[:n]...)}}); serr != nil {
					done("studio gone")
					return
				}
			}
			if err != nil {
				done("human closed")
				return
			}
		}
	}()
	return tc
}

// CloseAll closes every live tunnel to actorID (teardown, idle, revocation).
func (b *TunnelBroker) CloseAll(actorID string) {
	b.mu.Lock()
	var all []*tunnelConn
	for tc := range b.live[actorID] {
		all = append(all, tc)
	}
	b.mu.Unlock()
	for _, tc := range all {
		tc.closeAll()
	}
}

type tunnelConn struct {
	net.Conn
	closeAll func()
}
