package covemaster

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/aethons-tools/cove/internal/harbor/attach/attachpb"
)

const (
	defaultHeartbeat = 10 * time.Second
	initialBackoff   = 500 * time.Millisecond
	maxBackoff       = 15 * time.Second
)

type Client struct {
	cfg Config
	log *slog.Logger

	mu        sync.Mutex
	latest    Activity
	hasLatest bool
	activity  chan Activity // coalesced (buffer 1)
}

func New(cfg Config, log *slog.Logger) *Client {
	if cfg.Heartbeat <= 0 {
		cfg.Heartbeat = defaultHeartbeat
	}
	return &Client{cfg: cfg, log: newLogger(log), activity: make(chan Activity, 1)}
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

	md := metadata.Pairs("authorization", "Bearer "+c.cfg.Token, "x-harbor-launch-secret", c.cfg.LaunchSecret)
	stream, err := attachpb.NewRuntimeClient(cc).Attach(metadata.NewOutgoingContext(ctx, md))
	if err != nil {
		return classify(ctx, err, nil)
	}

	// recv goroutine → control/recvErr.
	controlCh := make(chan *attachpb.ControlDown, 8)
	recvErr := make(chan error, 1)
	go func() {
		for {
			cd, err := stream.Recv()
			if err != nil {
				recvErr <- err
				return
			}
			select {
			case controlCh <- cd:
			default:
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
		case <-hb.C:
			if err := stream.Send(heartbeatMsg()); err != nil {
				return classify(ctx, err, recvErr)
			}
		case <-doneCh:
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
			switch cd.GetMsg().(type) {
			case *attachpb.ControlDown_Teardown:
				w.Control(Control{Kind: Teardown})
				return outcome{kind: stopTeardown}
			case *attachpb.ControlDown_Wake:
				w.Control(Control{Kind: Wake})
			default:
				c.log.Info("ignoring unsupported control message") // TierChanged/RotateToken (reserved)
			}
		case err := <-recvErr:
			return classify(ctx, err, nil)
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
