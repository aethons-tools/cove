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
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/aethons-tools/cove/internal/jam/attach/attachpb"
)

var (
	ErrStudioNotConnected = errors.New("studio not connected")
	ErrDialBackTimeout    = errors.New("studio did not open the tunnel in time (is its cove-master current?)")
	ErrTooManyTunnels     = errors.New("too many open tunnels for this studio")
	// ErrTooManyPreviewTunnels: previews hold their share of the studio's
	// tunnels (the rest are kept for SSH). It is an ErrTooManyTunnels.
	ErrTooManyPreviewTunnels = fmt.Errorf("%w: previews hold their share (the rest are kept for SSH)", ErrTooManyTunnels)
	// ErrTunnelsClosed: the studio is pausing or stopping (CloseAll ran) and
	// takes no tunnels until it is resumed or raised again (Reopen).
	ErrTunnelsClosed = errors.New("studio is pausing or stopping; its tunnels are closed")
	errUnknownTunnel = errors.New("unknown or already-claimed tunnel id")
)

// SSHReservedTunnels is how many of a studio's tunnels previews may never
// take, so SSH still connects while previews are busy.
const SSHReservedTunnels = 4

type TunnelOptions struct {
	DialBack     time.Duration
	MaxPerStudio int // all tunnels, SSH and preview (default 16)
	// MaxPreviewPerStudio caps OpenPreview's tunnels within MaxPerStudio
	// (default MaxPerStudio - SSHReservedTunnels, at least 1).
	MaxPreviewPerStudio int
	Idle                time.Duration
}

type TunnelStream interface {
	Send(*attachpb.TunnelFrame) error
	Recv() (*attachpb.TunnelFrame, error)
	Context() context.Context
}

type pending struct {
	actorID   string
	preview   bool          // an OpenPreview slot
	ready     chan *handoff // buffered(1): Claim sends after taking the entry
	cancel    chan struct{} // closed by CloseAll after it took the entry
	cancelled bool          // guarded by TunnelBroker.mu
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
	slots   map[string]int                      // actorID → reserved tunnels (pending + live)
	pslots  map[string]int                      // actorID → those of slots that are previews'
	closed  map[string]bool                     // actorID → CloseAll ran; refuse until Reopen
}

func NewTunnelBroker(push func(actorID, id string, port int32) bool, opt TunnelOptions) *TunnelBroker {
	if opt.DialBack <= 0 {
		opt.DialBack = 10 * time.Second
	}
	if opt.MaxPerStudio <= 0 {
		opt.MaxPerStudio = 16
	}
	if opt.MaxPreviewPerStudio <= 0 {
		opt.MaxPreviewPerStudio = max(opt.MaxPerStudio-SSHReservedTunnels, 1)
	}
	opt.MaxPreviewPerStudio = min(opt.MaxPreviewPerStudio, opt.MaxPerStudio)
	if opt.Idle <= 0 {
		opt.Idle = 30 * time.Minute
	}
	return &TunnelBroker{push: push, opt: opt, pending: map[string]*pending{}, live: map[string]map[*tunnelConn]struct{}{}, slots: map[string]int{}, pslots: map[string]int{}, closed: map[string]bool{}}
}

func newID() string { b := make([]byte, 16); _, _ = rand.Read(b); return hex.EncodeToString(b) }

// Open returns the human-side end of a tunnel to actorID's 127.0.0.1:port.
func (b *TunnelBroker) Open(ctx context.Context, actorID string, port int32) (net.Conn, error) {
	return b.open(ctx, actorID, port, false)
}

// OpenPreview is Open for a preview connection: it also counts against the
// studio's preview cap (MaxPreviewPerStudio), so previews never take the
// tunnels kept for SSH; past it, ErrTooManyPreviewTunnels. It waits for the
// dial-back until ctx's deadline when that is later than DialBack.
func (b *TunnelBroker) OpenPreview(ctx context.Context, actorID string, port int32) (net.Conn, error) {
	return b.open(ctx, actorID, port, true)
}

func (b *TunnelBroker) open(ctx context.Context, actorID string, port int32, preview bool) (net.Conn, error) {
	b.mu.Lock()
	if b.closed[actorID] {
		b.mu.Unlock()
		return nil, ErrTunnelsClosed
	}
	if preview && b.pslots[actorID] >= b.opt.MaxPreviewPerStudio {
		b.mu.Unlock()
		return nil, ErrTooManyPreviewTunnels
	}
	if b.slots[actorID] >= b.opt.MaxPerStudio {
		b.mu.Unlock()
		return nil, ErrTooManyTunnels
	}
	b.slots[actorID]++
	if preview {
		b.pslots[actorID]++
	}
	id := newID()
	p := &pending{actorID: actorID, preview: preview, ready: make(chan *handoff, 1), cancel: make(chan struct{})}
	b.pending[id] = p
	b.mu.Unlock()

	if !b.push(actorID, id, port) {
		b.mu.Lock()
		delete(b.pending, id)
		b.releaseLocked(actorID, preview)
		b.mu.Unlock()
		return nil, ErrStudioNotConnected
	}
	t := time.NewTimer(b.dialBackWait(ctx, preview))
	defer t.Stop()
	select {
	case h := <-p.ready:
		return b.splice(actorID, preview, h)
	case <-p.cancel:
		b.mu.Lock()
		b.releaseLocked(actorID, preview)
		b.mu.Unlock()
		return nil, ErrTunnelsClosed
	case <-t.C:
		return nil, b.abandon(id, p, ErrDialBackTimeout)
	case <-ctx.Done():
		return nil, b.abandon(id, p, ctx.Err())
	}
}

// dialBackWait is how long an open waits for the studio to dial back:
// DialBack, or for a preview the time left to ctx's deadline when that is
// later (the studio may first start the built-in IDE, which can take 15 s).
func (b *TunnelBroker) dialBackWait(ctx context.Context, preview bool) time.Duration {
	if preview {
		if dl, ok := ctx.Deadline(); ok {
			return max(b.opt.DialBack, time.Until(dl))
		}
	}
	return b.opt.DialBack
}

func (b *TunnelBroker) releaseLocked(actorID string, preview bool) {
	if b.slots[actorID]--; b.slots[actorID] <= 0 {
		delete(b.slots, actorID)
	}
	if !preview {
		return
	}
	if b.pslots[actorID]--; b.pslots[actorID] <= 0 {
		delete(b.pslots, actorID)
	}
}

// abandon gives up on a pending open. If a Claim already took the entry, the
// handed-over stream is closed so the studio's Tunnel RPC does not leak.
func (b *TunnelBroker) abandon(id string, p *pending, cause error) error {
	b.mu.Lock()
	_, stillPending := b.pending[id]
	delete(b.pending, id)
	cancelled := p.cancelled
	b.releaseLocked(p.actorID, p.preview)
	b.mu.Unlock()
	if cancelled {
		return ErrTunnelsClosed
	}
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
	if b.closed[actorID] {
		b.mu.Unlock()
		return ErrTunnelsClosed
	}
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
		// splice tears down on the stream ending; wait for it so no stream
		// operation outlives this handler.
		<-h.done
	}
	return nil
}

// splice joins a claimed stream to a new human end. A stream claimed while
// CloseAll ran is closed instead (ErrTunnelsClosed): the check and the live
// registration share one lock, so no tunnel outlives a CloseAll.
func (b *TunnelBroker) splice(actorID string, preview bool, h *handoff) (net.Conn, error) {
	s := h.stream
	human, inner := net.Pipe()
	tc := &tunnelConn{Conn: human}
	// gRPC streams do not allow concurrent Sends, nor any use after the
	// handler (Claim) returns; closed (guarded by sendMu) fences both.
	var sendMu sync.Mutex
	closed := false
	send := func(fr *attachpb.TunnelFrame) error {
		sendMu.Lock()
		defer sendMu.Unlock()
		if closed {
			return errors.New("tunnel closed")
		}
		return s.Send(fr)
	}
	idle := time.NewTimer(b.opt.Idle)
	var once sync.Once
	done := func(reason string) {
		once.Do(func() {
			sendMu.Lock()
			if !closed {
				_ = s.Send(closeFrame(reason))
				closed = true
			}
			idle.Stop()
			sendMu.Unlock()
			_ = inner.Close()
			b.mu.Lock()
			if m := b.live[actorID]; m != nil {
				delete(m, tc)
				if len(m) == 0 {
					delete(b.live, actorID)
				}
			}
			b.releaseLocked(actorID, preview)
			b.mu.Unlock()
			h.finish()
		})
	}
	tc.closeAll = func() { done("closed by Jam") }
	touch := func() {
		sendMu.Lock()
		defer sendMu.Unlock()
		if !closed {
			idle.Reset(b.opt.Idle)
		}
	}
	go func() {
		select {
		case <-idle.C:
			done("idle timeout")
		case <-s.Context().Done():
			done("studio stream ended")
		case <-h.done:
		}
	}()
	b.mu.Lock()
	if b.closed[actorID] { // CloseAll ran after the claim
		b.mu.Unlock()
		done("closed by Jam")
		return nil, ErrTunnelsClosed
	}
	if b.live[actorID] == nil {
		b.live[actorID] = map[*tunnelConn]struct{}{}
	}
	b.live[actorID][tc] = struct{}{}
	b.mu.Unlock()

	go func() { // studio → human
		for {
			sendMu.Lock()
			stop := closed
			sendMu.Unlock()
			if stop { // never start a stream op once the handler may have returned
				return
			}
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
	return tc, nil
}

// Live reports how many tunnels actorID has in use: spliced ones and opens
// still waiting for the studio's dial-back. A studio with any is not idle.
func (b *TunnelBroker) Live(actorID string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.slots[actorID]
}

// CloseAll closes every tunnel to actorID — spliced ones and pending opens —
// and refuses new opens and claims for it until Reopen (teardown, pause).
func (b *TunnelBroker) CloseAll(actorID string) {
	b.mu.Lock()
	all := b.closeLocked(actorID)
	b.mu.Unlock()
	for _, tc := range all {
		tc.closeAll()
	}
}

// CloseIfUnused closes actorID's tunnels as CloseAll does, but only when none
// is in use (Live is 0), reporting whether it did; the check and the close are
// one step, so an open cannot slip in between (a pause).
func (b *TunnelBroker) CloseIfUnused(actorID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.slots[actorID] > 0 {
		return false
	}
	b.closeLocked(actorID)
	return true
}

// Reopen lets actorID take tunnels again after CloseAll or CloseIfUnused (a
// resume, a raise, a failed pause, or forgetting a torn-down studio).
func (b *TunnelBroker) Reopen(actorID string) {
	b.mu.Lock()
	delete(b.closed, actorID)
	b.mu.Unlock()
}

// closeLocked marks actorID closed, cancels its pending opens, and returns its
// spliced tunnels for the caller to close outside the lock.
func (b *TunnelBroker) closeLocked(actorID string) []*tunnelConn {
	b.closed[actorID] = true
	for id, p := range b.pending {
		if p.actorID == actorID {
			delete(b.pending, id)
			p.cancelled = true
			close(p.cancel) // Open releases the slot
		}
	}
	var all []*tunnelConn
	for tc := range b.live[actorID] {
		all = append(all, tc)
	}
	return all
}

type tunnelConn struct {
	net.Conn
	closeAll func()
}
