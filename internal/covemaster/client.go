package covemaster

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/aethons-tools/cove/internal/jam/attach/attachpb"
)

const (
	defaultHeartbeat = 10 * time.Second
	initialBackoff   = 500 * time.Millisecond
	maxBackoff       = 15 * time.Second

	defaultEventBufferEvents = 10000
	defaultEventBufferBytes  = 64 << 20
	defaultFlushTimeout      = 5 * time.Second
	redactedMarker           = "«redacted»" // same marker as internal/logging.Scrub
)

type Client struct {
	cfg Config
	log *slog.Logger

	mu        sync.Mutex
	latest    Activity
	hasLatest bool
	activity  chan Activity   // coalesced (buffer 1)
	connector string          // latest applied-connector fingerprint (guarded by mu)
	connCh    chan string     // coalesced (buffer 1)
	gates     chan GateResult // gate results awaiting the stream (buffer gateBuffer; overflow dropped)
	events    *eventBuf
}

func New(cfg Config, log *slog.Logger) *Client {
	if cfg.Heartbeat <= 0 {
		cfg.Heartbeat = defaultHeartbeat
	}
	if cfg.EventBufferEvents <= 0 {
		cfg.EventBufferEvents = defaultEventBufferEvents
	}
	if cfg.EventBufferBytes <= 0 {
		cfg.EventBufferBytes = defaultEventBufferBytes
	}
	if cfg.FlushTimeout <= 0 {
		cfg.FlushTimeout = defaultFlushTimeout
	}
	return &Client{cfg: cfg, log: newLogger(log), activity: make(chan Activity, 1), connCh: make(chan string, 1), gates: make(chan GateResult, gateBuffer),
		events: newEventBuf(newStreamID(), cfg.EventBufferEvents, cfg.EventBufferBytes)}
}

// newStreamID returns 16 random bytes as 32 lowercase hex chars.
func newStreamID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return hex.EncodeToString(b[:])
}

// StreamID identifies this client's event stream to Jam.
func (c *Client) StreamID() string { return c.events.streamID }

// Event implements Handle: redact this cove's own secrets, then buffer.
func (c *Client) Event(turn uint32, raw []byte, truncatedBytes uint64) {
	c.events.add(turn, c.redact(raw), truncatedBytes, time.Now())
}

// redact replaces exact occurrences of the cove's identity token and launch
// secret — the only secrets a Jam cove holds. The marker has no `"` or `\`, so
// a valid JSON line stays valid.
func (c *Client) redact(raw []byte) []byte {
	for _, s := range []string{c.cfg.Token, c.cfg.LaunchSecret} {
		if s != "" {
			raw = bytes.ReplaceAll(raw, []byte(s), []byte(redactedMarker))
		}
	}
	return raw
}

// ConnectorApplied implements Handle: remember the latest fingerprint (for
// re-send on reconnect) and coalesce it onto connCh for the live session.
func (c *Client) ConnectorApplied(fp string) {
	c.mu.Lock()
	c.connector = fp
	c.mu.Unlock()
	select {
	case c.connCh <- fp:
	default:
		select { // drop the stale pending value, then retry once
		case <-c.connCh:
		default:
		}
		select {
		case c.connCh <- fp:
		default:
		}
	}
}

// GateResult implements Handle: it queues a gate's result for the stream,
// never blocking. A result that finds the queue full, or is lost with a
// dropped stream, is not retried: Jam resolves a gate with no result as
// failed after its grace period.
func (c *Client) GateResult(g GateResult) {
	select {
	case c.gates <- g:
	default:
		c.log.Warn("gate result dropped: queue full", "run", g.RunID)
	}
}

// Report implements Handle. It records the latest activity (for re-send on
// reconnect) and coalesces onto a buffer-1 channel (newest wins) for live
// delivery to the active session.
func (c *Client) Report(a Activity) {
	c.mu.Lock()
	c.latest, c.hasLatest = a, true
	c.mu.Unlock()
	for {
		select {
		case c.activity <- a:
			return
		default:
			select { // drop the stale pending value, then retry
			case <-c.activity:
			default:
			}
		}
	}
}

type outcomeKind int

const (
	retry        outcomeKind = iota // transient; reconnect with backoff
	fatal                           // auth failure; stop with error
	stopDone                        // workload finished; reported Done
	stopTeardown                    // server asked to tear down
	stopParent                      // parent ctx cancelled
)

type outcome struct {
	kind outcomeKind
	err  error
}

// Run connects and supervises w until it finishes (Done), a Teardown arrives,
// the parent ctx is cancelled, or a fatal (auth) error occurs. Transient drops
// reconnect with backoff; the workload runs exactly once across reconnects.
func (c *Client) Run(ctx context.Context, w Workload) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	doneCh := make(chan struct{})
	var runErr error
	go func() {
		runErr = w.Run(runCtx, c)
		close(doneCh)
	}()

	backoff := initialBackoff
	for {
		oc := c.session(runCtx, w, doneCh)
		switch oc.kind {
		case stopDone:
			if runErr != nil && !errors.Is(runErr, context.Canceled) {
				c.log.Warn("workload ended with error", "err", runErr.Error())
			}
			return nil
		case stopTeardown:
			cancel() // unwind the workload
			<-doneCh // wait for it to return
			return nil
		case stopParent:
			<-doneCh
			return ctx.Err()
		case fatal:
			cancel()
			<-doneCh
			return oc.err
		case retry:
			select {
			case <-runCtx.Done():
				<-doneCh
				return ctx.Err()
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, maxBackoff)
		}
	}
}

// session runs one connection: dial, auth, then pump activity/heartbeat up and
// control down until the session ends. It returns the outcome that drives Run's
// reconnect/stop decision. The workload is NOT started here (Run owns it).
func (c *Client) session(ctx context.Context, w Workload, doneCh <-chan struct{}) outcome {
	opts := append([]grpc.DialOption{}, c.cfg.DialOptions...)
	// Use the "passthrough" resolver so the target is handed to the dialer
	// (bufconn in tests, real DNS/IP in production) exactly as configured,
	// instead of grpc.NewClient's default DNS-resolver scheme — which would
	// otherwise attempt real name resolution even when a custom
	// grpc.WithContextDialer is supplied, egress-locked sandboxes and the
	// "bufnet" test target included.
	cc, err := grpc.NewClient("passthrough:///"+c.cfg.Addr, opts...)
	if err != nil {
		return outcome{kind: retry, err: err}
	}
	defer cc.Close()

	// x-harbor-launch-secret keeps its pre-rename name: it is the wire key
	// every Jam server reads (docs/usage/jam/renamed-from-harbor.md).
	md := metadata.Pairs("authorization", "Bearer "+c.cfg.Token, "x-harbor-launch-secret", c.cfg.LaunchSecret)
	stream, err := attachpb.NewRuntimeClient(cc).Attach(metadata.NewOutgoingContext(ctx, md))
	if err != nil {
		return classify(ctx, err, nil)
	}

	// recv goroutine → control/recvErr.
	// Acks are applied right here (eventBuf is mutex-safe) so a flood of them
	// can never crowd Teardown/Wake out of controlCh; only non-ack control
	// messages are forwarded, and those are never dropped.
	controlCh := make(chan *attachpb.ControlDown, 8)
	recvErr := make(chan error, 1)
	sessionDone := make(chan struct{})
	defer close(sessionDone)
	go func() {
		for {
			cd, err := stream.Recv()
			if err != nil {
				recvErr <- err
				return
			}
			if a, ok := cd.GetMsg().(*attachpb.ControlDown_Ack); ok {
				if a.Ack.GetStreamId() == c.events.streamID {
					c.events.ack(a.Ack.GetSeq())
				}
				continue
			}
			select {
			case controlCh <- cd:
			case <-sessionDone:
				return
			}
		}
	}()

	// Re-send the latest activity on (re)connect so a drop never loses state.
	c.mu.Lock()
	latest, has := c.latest, c.hasLatest
	c.mu.Unlock()
	if has {
		if err := stream.Send(statusMsg(latest)); err != nil {
			return classify(ctx, err, recvErr)
		}
	}

	c.mu.Lock()
	conn := c.connector
	c.mu.Unlock()
	if conn != "" {
		if err := stream.Send(connectorMsg(conn)); err != nil {
			return classify(ctx, err, recvErr)
		}
	}

	// Replay every unacked event (a reconnect resends exactly the unacked
	// tail), then keep sending as new events arrive.
	sent := c.events.ackedSeq()
	sendEvents := func() error {
		for _, ev := range c.events.since(sent) {
			if err := stream.Send(eventMsg(ev)); err != nil {
				return err
			}
			sent = ev.Seq
		}
		return nil
	}
	if err := sendEvents(); err != nil {
		return classify(ctx, err, recvErr)
	}

	hb := time.NewTicker(c.cfg.Heartbeat)
	defer hb.Stop()
	for {
		select {
		case <-ctx.Done():
			return outcome{kind: stopParent}
		case a := <-c.activity:
			if err := stream.Send(statusMsg(a)); err != nil {
				return classify(ctx, err, recvErr)
			}
		case fp := <-c.connCh:
			if err := stream.Send(connectorMsg(fp)); err != nil {
				return classify(ctx, err, recvErr)
			}
		case g := <-c.gates:
			if err := stream.Send(gateResultMsg(g)); err != nil {
				return classify(ctx, err, recvErr)
			}
		case <-c.events.notify:
			if err := sendEvents(); err != nil {
				return classify(ctx, err, recvErr)
			}
		case <-hb.C:
			if err := stream.Send(heartbeatMsg()); err != nil {
				return classify(ctx, err, recvErr)
			}
		case <-doneCh:
			if err := sendEvents(); err != nil {
				return classify(ctx, err, recvErr)
			}
			c.awaitFinalAck(ctx, sent, recvErr)
			if err := stream.Send(statusMsg(Done)); err != nil {
				return classify(ctx, err, recvErr)
			}
			// Half-close and wait briefly for the server to end the RPC. This
			// guarantees the Done status is actually flushed to (and processed
			// by) the server before we tear down the connection — a bare Send
			// only queues the frame locally; an immediate cc.Close() could race
			// the write and drop it.
			_ = stream.CloseSend()
			select {
			case <-recvErr:
			case <-time.After(2 * time.Second):
			}
			return outcome{kind: stopDone}
		case cd := <-controlCh:
			ctl, ok := controlFromPB(cd)
			if !ok {
				c.log.Info("ignoring unsupported control message") // TierChanged/RotateToken (reserved)
				continue
			}
			w.Control(ctl)
			if ctl.Kind == Teardown {
				return outcome{kind: stopTeardown}
			}
		case err := <-recvErr:
			return classify(ctx, err, nil)
		}
	}
}

// awaitFinalAck waits up to FlushTimeout for Jam to ack seq `sent` (the last
// event sent this session), so the audit trail is complete before Done tears
// the cove down. An old Jam never acks; that costs at most FlushTimeout. It
// does not read controlCh, so Teardown/Wake stay queued for the main loop.
func (c *Client) awaitFinalAck(ctx context.Context, sent uint64, recvErr chan error) {
	if c.events.ackedSeq() >= sent {
		return
	}
	timer := time.NewTimer(c.cfg.FlushTimeout)
	defer timer.Stop()
	for c.events.ackedSeq() < sent {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			c.log.Warn("session events: final ack not received; reporting Done anyway", "last_seq", sent, "acked", c.events.ackedSeq())
			return
		case err := <-recvErr:
			select { // stream broke: give the error back for later handling
			case recvErr <- err:
			default:
			}
			return
		case <-c.events.ackedCh:
		}
	}
}

// classify turns a stream error into an outcome. A gRPC Send error is not itself
// authoritative (the real status comes from Recv), so when recvErr is provided we
// wait briefly for it. Unauthenticated → fatal (never retry); everything else →
// retry. A cancelled parent ctx → stopParent.
func classify(ctx context.Context, sendErr error, recvErr <-chan error) outcome {
	err := sendErr
	if recvErr != nil {
		select {
		case e := <-recvErr:
			err = e
		case <-ctx.Done():
			return outcome{kind: stopParent}
		case <-time.After(2 * time.Second):
		}
	}
	if ctx.Err() != nil {
		return outcome{kind: stopParent}
	}
	if status.Code(err) == codes.Unauthenticated {
		return outcome{kind: fatal, err: err}
	}
	return outcome{kind: retry, err: err}
}
