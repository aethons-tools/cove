// Package client is the manager side of the Jam to Studio Manager protocol:
// Run connects a Launcher to Jam and serves Jam's requests over one stream.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/managers/managerpb"
	"github.com/aethons-tools/cove/internal/jam/managers/wire"
)

// Config configures Run.
type Config struct {
	Name, Token, Version string
	Capacity             int
	Launcher             jam.Launcher                                           // required; StateKeeper, ImageTagger, KitDraftBuilder asserted per request
	Images               jam.ImageFactsReader                                   // nil: image-facts requests fail
	Inventory            func(ctx context.Context) ([]jam.ManagedStudio, error) // nil: none
	HeartbeatEvery       time.Duration                                          // 0: wire.HeartbeatEvery
	DeadAfter            time.Duration                                          // 0: wire.DeadAfter
	Log                  *slog.Logger                                           // nil: discard

	// OnWelcome, if set, is called synchronously with Jam's Welcome, before
	// any request is served.
	OnWelcome func(w *managerpb.Welcome)

	maxMessage int // test override for wire.MaxMessageBytes
}

// Run opens one Connect stream on cc, sends Hello and waits for Welcome, then
// serves Jam's requests until the stream ends. It returns the stream's error
// (a gRPC status from Jam when refused), or ctx.Err() after ctx ends. It
// returns without waiting for in-flight requests: their contexts are
// cancelled, and any reply they send afterwards is dropped.
func Run(ctx context.Context, cc grpc.ClientConnInterface, cfg Config) error {
	if cfg.Launcher == nil {
		return errors.New("client: Launcher is required")
	}
	if cfg.HeartbeatEvery <= 0 {
		cfg.HeartbeatEvery = wire.HeartbeatEvery
	}
	if cfg.DeadAfter <= 0 {
		cfg.DeadAfter = wire.DeadAfter
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	hello, err := buildHello(ctx, cfg)
	if err != nil {
		return err
	}

	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sctx = metadata.AppendToOutgoingContext(sctx, "authorization", "Bearer "+cfg.Token)
	stream, err := managerpb.NewStudioManagerClient(cc).Connect(sctx)
	if err != nil {
		return orCtx(ctx, err)
	}
	c := &conn{cfg: cfg, stream: stream, ctx: sctx, cancels: map[uint64]context.CancelFunc{}}
	if err := c.sendUp(&managerpb.ManagerUp{Msg: &managerpb.ManagerUp_Hello{Hello: hello}}); err != nil {
		return orCtx(ctx, c.recvErr(err))
	}

	// Dead-man timer: reset on every message received.
	var fatalMu sync.Mutex
	var fatal error
	setFatal := func(e error) {
		fatalMu.Lock()
		if fatal == nil {
			fatal = e
		}
		fatalMu.Unlock()
		cancel()
	}
	dead := time.AfterFunc(cfg.DeadAfter, func() { setFatal(errors.New("heartbeat timeout")) })
	defer dead.Stop()

	// On return: cancel unblocks the heartbeat and any in-flight Send (gRPC
	// aborts a Send once the stream's context ends); wait for the heartbeat;
	// then close the conn under sendMu so no Send can start after Run has
	// returned. Request goroutines are not waited for: a launcher call may
	// ignore ctx for minutes (a kit build), and Run must return promptly so
	// the manager can reconnect. Their late replies fail with errClosed and
	// are dropped.
	var hb sync.WaitGroup
	defer func() {
		cancel()
		hb.Wait()
		c.close()
	}()

	hb.Add(1)
	go func() {
		defer hb.Done()
		t := time.NewTicker(cfg.HeartbeatEvery)
		defer t.Stop()
		for {
			select {
			case <-sctx.Done():
				return
			case <-t.C:
				if err := c.sendUp(&managerpb.ManagerUp{Msg: &managerpb.ManagerUp_Heartbeat{Heartbeat: &managerpb.Heartbeat{}}}); err != nil {
					return
				}
			}
		}
	}()

	asm := wire.Assembler{Max: cfg.maxMessage}
	welcomed := false
	for {
		m, rerr := stream.Recv()
		if rerr != nil {
			fatalMu.Lock()
			f := fatal
			fatalMu.Unlock()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if f != nil {
				return f
			}
			if rerr == io.EOF {
				return nil
			}
			return rerr
		}
		dead.Reset(cfg.DeadAfter)
		if !welcomed {
			if m.GetWelcome() == nil {
				return errors.New("client: expected Welcome from Jam")
			}
			if cfg.OnWelcome != nil {
				cfg.OnWelcome(m.GetWelcome())
			}
			welcomed = true
			continue
		}
		f := m.GetFrame()
		if f == nil {
			continue // heartbeat
		}
		if f.Op == wire.OpCancel {
			c.cancel(f.Id)
			continue
		}
		data, done, aerr := asm.Add(f)
		if aerr != nil {
			// Fatal at once: a buffered later frame of the dropped id must
			// never be served as a truncated request.
			return aerr
		}
		if !done {
			continue
		}
		rctx, rcancel := context.WithCancel(sctx)
		c.mu.Lock()
		c.cancels[f.Id] = rcancel
		c.mu.Unlock()
		go func(id uint64, op string) {
			defer func() {
				c.mu.Lock()
				delete(c.cancels, id)
				c.mu.Unlock()
				rcancel()
			}()
			c.serve(rctx, id, op, data)
		}(f.Id, f.Op)
	}
}

func orCtx(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func buildHello(ctx context.Context, cfg Config) (*managerpb.Hello, error) {
	h := &managerpb.Hello{Name: cfg.Name, Version: cfg.Version, Capacity: int32(cfg.Capacity)}
	if cfg.Inventory != nil {
		studios, err := cfg.Inventory(ctx)
		if err != nil {
			return nil, fmt.Errorf("client: inventory: %w", err)
		}
		for _, s := range studios {
			h.Studios = append(h.Studios, &managerpb.Studio{ActorId: s.ActorID, Location: s.Location})
		}
	}
	if sk, ok := cfg.Launcher.(jam.StateKeeper); ok {
		owners, err := sk.StateOwners(ctx)
		if err != nil {
			return nil, fmt.Errorf("client: state owners: %w", err)
		}
		h.StateOwners = owners
	}
	return h, nil
}

type conn struct {
	cfg    Config
	stream grpc.BidiStreamingClient[managerpb.ManagerUp, managerpb.ManagerDown]
	ctx    context.Context

	sendMu  sync.Mutex // serializes all sends; guards closed
	closed  bool       // set once Run returns: sends fail with errClosed
	mu      sync.Mutex
	cancels map[uint64]context.CancelFunc
}

func (c *conn) recvErr(err error) error {
	// A failed Send reports io.EOF; the real status comes from Recv.
	if errors.Is(err, io.EOF) {
		if _, rerr := c.stream.Recv(); rerr != nil && rerr != io.EOF {
			return rerr
		}
	}
	return err
}

// errClosed is returned by a send after Run has returned.
var errClosed = errors.New("client: stream closed")

// close makes every later send fail with errClosed. It waits for an in-flight
// send, so the stream's context must already be cancelled.
func (c *conn) close() {
	c.sendMu.Lock()
	c.closed = true
	c.sendMu.Unlock()
}

func (c *conn) sendUp(m *managerpb.ManagerUp) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	if c.closed {
		return errClosed
	}
	return c.stream.Send(m)
}

// sendFrames sends frames under one lock hold so they are never interleaved.
func (c *conn) sendFrames(frames []*managerpb.Frame) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	if c.closed {
		return errClosed
	}
	for _, f := range frames {
		if err := c.stream.Send(&managerpb.ManagerUp{Msg: &managerpb.ManagerUp_Frame{Frame: f}}); err != nil {
			return err
		}
	}
	return nil
}

func (c *conn) cancel(id uint64) {
	c.mu.Lock()
	cf := c.cancels[id]
	c.mu.Unlock()
	if cf != nil {
		cf()
	}
}

func (c *conn) reply(id uint64, body any, err error) {
	data, eerr := wire.EncodeResult(body, err)
	if eerr != nil {
		data, _ = wire.EncodeResult(nil, eerr)
	}
	if serr := c.sendFrames(wire.Split(id, wire.OpResult, data)); serr != nil {
		c.cfg.Log.Debug("manager: send result failed", "id", id, "err", serr)
	}
}

// logWriter sends each Write as OpLog frames of at most MaxFrameData.
type logWriter struct {
	c  *conn
	id uint64
}

func (w logWriter) Write(p []byte) (int, error) {
	for off := 0; off < len(p); off += wire.MaxFrameData {
		end := min(off+wire.MaxFrameData, len(p))
		chunk := append([]byte(nil), p[off:end]...)
		if err := w.c.sendFrames([]*managerpb.Frame{{Id: w.id, Op: wire.OpLog, Data: chunk}}); err != nil {
			return off, err
		}
	}
	return len(p), nil
}

func unsupported(op string) error { return fmt.Errorf("manager does not support %s", op) }

func decodeReq(data []byte, v any) error {
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("bad request: %w", err)
	}
	return nil
}

// serve runs one request and sends its result. Payloads are never logged.
func (c *conn) serve(ctx context.Context, id uint64, op string, data []byte) {
	l := c.cfg.Launcher
	switch op {
	case wire.OpRaise:
		var r wire.RaiseRequest
		if err := decodeReq(data, &r); err != nil {
			c.reply(id, nil, err)
			return
		}
		loc, err := l.Raise(ctx, r.Spec, r.Creds)
		c.reply(id, wire.RaiseResult{Location: loc}, err)
	case wire.OpTeardown:
		var r wire.InstanceRequest
		if err := decodeReq(data, &r); err != nil {
			c.reply(id, nil, err)
			return
		}
		var (
			tail string
			err  error
		)
		if tc, ok := l.(wire.TeardownCapturer); ok {
			tail, err = tc.TeardownCapture(ctx, r.Instance)
		} else {
			err = l.Teardown(ctx, r.Instance)
		}
		c.reply(id, wire.TeardownResult{AgentLog: tail}, err)
	case wire.OpPause, wire.OpUnpause:
		var r wire.InstanceRequest
		if err := decodeReq(data, &r); err != nil {
			c.reply(id, nil, err)
			return
		}
		var err error
		if op == wire.OpPause {
			err = l.Pause(ctx, r.Instance)
		} else {
			err = l.Unpause(ctx, r.Instance)
		}
		c.reply(id, wire.Empty{}, err)
	case wire.OpProbe:
		var r wire.InstanceRequest
		if err := decodeReq(data, &r); err != nil {
			c.reply(id, nil, err)
			return
		}
		lv, err := l.Probe(ctx, r.Instance)
		c.reply(id, wire.ProbeResult{Liveness: lv}, err)
	case wire.OpApplyEgress:
		var r wire.EgressRequest
		if err := decodeReq(data, &r); err != nil {
			c.reply(id, nil, err)
			return
		}
		c.reply(id, wire.Empty{}, l.ApplyEgress(ctx, r.Instance, r.Policy))
	case wire.OpPrepareKit:
		var r wire.KitRequest
		if err := decodeReq(data, &r); err != nil {
			c.reply(id, nil, err)
			return
		}
		def, err := r.Def.Definition()
		if err != nil {
			c.reply(id, nil, err)
			return
		}
		st, err := l.PrepareKit(ctx, def)
		c.reply(id, st, err)
	case wire.OpPurgeState:
		sk, ok := l.(jam.StateKeeper)
		if !ok {
			c.reply(id, nil, unsupported(op))
			return
		}
		var r wire.ActorRequest
		if err := decodeReq(data, &r); err != nil {
			c.reply(id, nil, err)
			return
		}
		c.reply(id, wire.Empty{}, sk.PurgeState(ctx, r.ActorID))
	case wire.OpStateOwners:
		sk, ok := l.(jam.StateKeeper)
		if !ok {
			c.reply(id, nil, unsupported(op))
			return
		}
		ids, err := sk.StateOwners(ctx)
		c.reply(id, wire.OwnersResult{ActorIDs: ids}, err)
	case wire.OpImageTag:
		it, ok := l.(jam.ImageTagger)
		if !ok {
			c.reply(id, nil, unsupported(op))
			return
		}
		var r wire.ImageTagRequest
		if err := decodeReq(data, &r); err != nil {
			c.reply(id, nil, err)
			return
		}
		c.reply(id, wire.TagResult{Tag: it.ImageTag(r.Ref)}, nil)
	case wire.OpBuildKitDraft:
		b, ok := l.(jam.KitDraftBuilder)
		if !ok {
			c.reply(id, nil, unsupported(op))
			return
		}
		var r wire.DraftBuildRequest
		if err := decodeReq(data, &r); err != nil {
			c.reply(id, nil, err)
			return
		}
		def, err := r.Def.Definition()
		if err != nil {
			c.reply(id, nil, err)
			return
		}
		img, err := b.BuildKitDraft(ctx, def, r.Tag, logWriter{c: c, id: id})
		c.reply(id, wire.DraftBuildResult{ImageID: img}, err)
	case wire.OpHasKitDraftImage:
		b, ok := l.(jam.KitDraftBuilder)
		if !ok {
			c.reply(id, nil, unsupported(op))
			return
		}
		var r wire.DraftImageRequest
		if err := decodeReq(data, &r); err != nil {
			c.reply(id, nil, err)
			return
		}
		has, err := b.HasKitDraftImage(r.ImageID)
		c.reply(id, wire.HasResult{Has: has}, err)
	case wire.OpImageFacts:
		if c.cfg.Images == nil {
			c.reply(id, nil, unsupported(op))
			return
		}
		var r wire.ImageFactsRequest
		if err := decodeReq(data, &r); err != nil {
			c.reply(id, nil, err)
			return
		}
		f, err := c.cfg.Images.ImageFacts(ctx, r.Image)
		c.reply(id, f, err)
	default:
		c.reply(id, nil, fmt.Errorf("unknown op %s", op))
	}
}
