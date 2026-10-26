// Package managers is Jam's side of the Studio Manager protocol: one conn per
// accepted manager stream, and the Remote adapter the router drives.
package managers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/managers/managerpb"
	"github.com/aethons-tools/cove/internal/jam/managers/wire"
)

// errInterrupted marks a failure that happened after the request may have
// reached the manager: its outcome is unknown.
var errInterrupted = errors.New("connection lost with the call in flight")

// errClosed is the cause recorded when a conn is closed without a reason.
var errClosed = errors.New("manager stream closed")

type result struct {
	data []byte
	err  error
}

type call struct {
	done chan result // buffered 1; written once by whoever removes the call from pending
	mu   sync.Mutex  // serializes log writes against the call finishing
	log  io.Writer
	over bool
}

// conn is one accepted stream. Calls are owned by their conn: ids are per
// conn, so a result on a replaced stream can never resolve a call on its
// successor.
type conn struct {
	send   func(*managerpb.ManagerDown) error
	sendMu sync.Mutex

	mu      sync.Mutex
	pending map[uint64]*call
	nextID  uint64
	asm     wire.Assembler
	closed  bool
	cause   error
	done    chan struct{}
}

func newConn(send func(*managerpb.ManagerDown) error) *conn {
	return &conn{send: send, pending: map[uint64]*call{}, done: make(chan struct{})}
}

func frameMsg(f *managerpb.Frame) *managerpb.ManagerDown {
	return &managerpb.ManagerDown{Msg: &managerpb.ManagerDown_Frame{Frame: f}}
}

// sendFrames writes frames under one lock hold so they are never interleaved.
func (c *conn) sendFrames(frames []*managerpb.Frame) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	for _, f := range frames {
		if err := c.send(frameMsg(f)); err != nil {
			return err
		}
	}
	return nil
}

func (c *conn) unavailable() error {
	return fmt.Errorf("%w: %w", jam.ErrManagerUnavailable, c.cause)
}

// call sends op/req as frames, awaits the OpResult and decodes it into resp
// (nil = ignore body). log (may be nil) receives OpLog chunks for this call.
// ctx done: sends OpCancel, returns ctx.Err(). conn closed: returns an error
// wrapping jam.ErrManagerUnavailable; when the request may already have been
// sent it also wraps errInterrupted.
func (c *conn) call(ctx context.Context, op string, req, resp any, log io.Writer) error {
	if req == nil {
		req = wire.Empty{}
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("managers: encode %s: %w", op, err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	cl := &call{done: make(chan result, 1), log: log}
	c.mu.Lock()
	if c.closed {
		err := c.unavailable()
		c.mu.Unlock()
		return err
	}
	c.nextID++
	id := c.nextID
	c.pending[id] = cl
	c.mu.Unlock()

	if err := c.sendFrames(wire.Split(id, op, payload)); err != nil {
		c.finish(id, result{err: fmt.Errorf("%w: %w: send %s: %w", jam.ErrManagerUnavailable, errInterrupted, op, err)})
	}

	select {
	case r := <-cl.done:
		if r.err != nil {
			return r.err
		}
		return wire.DecodeResult(r.data, resp)
	case <-ctx.Done():
		if c.remove(id) != nil {
			cl.end()
			_ = c.sendFrames([]*managerpb.Frame{{Id: id, Op: wire.OpCancel}})
			return ctx.Err()
		}
		// Lost the race: the call was finished; honour its outcome.
		r := <-cl.done
		if r.err != nil {
			return r.err
		}
		return wire.DecodeResult(r.data, resp)
	}
}

// end stops further log writes to the call's writer.
func (cl *call) end() {
	cl.mu.Lock()
	cl.over = true
	cl.mu.Unlock()
}

// remove takes id out of pending; nil if it was already gone.
func (c *conn) remove(id uint64) *call {
	c.mu.Lock()
	defer c.mu.Unlock()
	cl := c.pending[id]
	delete(c.pending, id)
	return cl
}

// finish resolves id with r, if still pending.
func (c *conn) finish(id uint64, r result) {
	if cl := c.remove(id); cl != nil {
		cl.end()
		cl.done <- r
	}
}

// deliver handles one frame from the manager. Unknown ids are dropped. An
// error is fatal for the stream: the caller ends it.
func (c *conn) deliver(f *managerpb.Frame) error {
	switch f.Op {
	case wire.OpLog:
		c.mu.Lock()
		cl := c.pending[f.Id]
		c.mu.Unlock()
		if cl == nil {
			return nil
		}
		cl.mu.Lock()
		defer cl.mu.Unlock()
		if cl.over || cl.log == nil {
			return nil
		}
		_, _ = cl.log.Write(f.Data) // a failing log sink must not fail the call
		return nil
	case wire.OpResult:
		c.mu.Lock()
		data, done, err := c.asm.Add(f)
		c.mu.Unlock()
		if err != nil {
			return err
		}
		if done {
			c.finish(f.Id, result{data: data})
		}
		return nil
	}
	return nil // not an op the manager sends
}

// close fails every pending call; later calls fail as unavailable. err is the
// cause (nil: the stream simply ended). Idempotent.
func (c *conn) close(err error) {
	if err == nil {
		err = errClosed
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.cause = err
	pend := c.pending
	c.pending = map[uint64]*call{}
	close(c.done)
	failure := fmt.Errorf("%w: %w: %w", jam.ErrManagerUnavailable, errInterrupted, err)
	c.mu.Unlock()
	for _, cl := range pend {
		cl.end()
		cl.done <- result{err: failure}
	}
}
