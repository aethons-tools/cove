// Package wakeon is harbor's resident wake-on engine: it watches Waiting managed
// coves and wakes them (over the Attach ControlSink) when a reply lands on their
// ticket, or tears them down past a max-wait. Wired from cmd/at-harbor; not
// imported by internal/harbor core.
package wakeon

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/aethons-tools/cove/internal/harbor"
)

type Registry interface{ ListInstances() []harbor.Instance }
type Cursors interface {
	SetWaitCursor(actorID, cursor string) error
}
type Waker interface{ Wake(actorID string) }
type Reaper interface {
	Teardown(ctx context.Context, actorID string) error
}
type Commenter interface {
	IssueByIdentifier(ctx context.Context, identifier string) (string, error)
	Comments(ctx context.Context, issueID string) ([]harbor.Comment, error)
}

// Idler pauses/unpauses a Live cove going through its warm-idle window (B2).
// Backed by the supervisor's Idle/Resume — never the Launcher/backend
// directly (see internal/harbor.Supervisor.Idle/Resume).
type Idler interface {
	Idle(ctx context.Context, actorID string) error
	Resume(ctx context.Context, actorID string) error
}

type Config struct{ PollInterval, MaxWait, WarmTimeout time.Duration }

const (
	defaultPollInterval = 15 * time.Second
	defaultMaxWait      = 30 * time.Minute
	defaultWarmTimeout  = 60 * time.Second
)

type Engine struct {
	reg   Registry
	cur   Cursors
	wake  Waker
	reap  Reaper
	idler Idler
	cmt   Commenter
	cfg   Config
	now   func() time.Time
	log   *slog.Logger
}

func New(reg Registry, cur Cursors, wake Waker, reap Reaper, idler Idler, cmt Commenter, cfg Config, log *slog.Logger) *Engine {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = defaultPollInterval
	}
	if cfg.MaxWait <= 0 {
		cfg.MaxWait = defaultMaxWait
	}
	if cfg.WarmTimeout <= 0 {
		cfg.WarmTimeout = defaultWarmTimeout
	}
	if log == nil {
		log = slog.New(slog.NewTextHandler(discard{}, nil))
	}
	return &Engine{reg, cur, wake, reap, idler, cmt, cfg, time.Now, log}
}

func (e *Engine) Run(ctx context.Context) {
	e.tick(ctx)
	tk := time.NewTicker(e.cfg.PollInterval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			e.tick(ctx)
		}
	}
}

func (e *Engine) tick(ctx context.Context) {
	for _, inst := range e.reg.ListInstances() {
		if inst.Activity != harbor.ActivityWaiting {
			continue
		}
		if !inst.WaitingSince.IsZero() && e.now().Sub(inst.WaitingSince) > e.cfg.MaxWait {
			if err := e.reap.Teardown(ctx, inst.ActorID); err != nil {
				e.log.Warn("wakeon: teardown (max-wait) failed", "actor", inst.ActorID, "error", err.Error())
			}
			continue
		}
		issueID, err := e.cmt.IssueByIdentifier(ctx, inst.Unit)
		if err != nil {
			e.log.Warn("wakeon: resolve ticket failed", "actor", inst.ActorID, "error", err.Error())
			continue
		}
		comments, err := e.cmt.Comments(ctx, issueID)
		if err != nil {
			e.log.Warn("wakeon: read comments failed", "actor", inst.ActorID, "error", err.Error())
			continue
		}
		n := len(comments)
		if inst.WaitCursor == "" { // baseline
			if err := e.cur.SetWaitCursor(inst.ActorID, strconv.Itoa(n)); err != nil {
				e.log.Warn("wakeon: set cursor failed", "actor", inst.ActorID, "error", err.Error())
			}
			continue
		}
		base, _ := strconv.Atoi(inst.WaitCursor)
		if n > base { // a reply arrived
			if inst.Phase == harbor.PhaseIdled {
				if err := e.idler.Resume(ctx, inst.ActorID); err != nil {
					e.log.Warn("wakeon: resume failed", "actor", inst.ActorID, "error", err.Error())
				}
				// Wake is sent on a later tick, once it's Live+Waiting and the stream has reconnected.
				continue
			}
			e.log.Info("wakeon: reply detected, waking", "actor", inst.ActorID)
			e.wake.Wake(inst.ActorID)
			continue
		}
		// no reply
		if inst.Phase != harbor.PhaseIdled && e.now().Sub(inst.WaitingSince) > e.cfg.WarmTimeout {
			if err := e.idler.Idle(ctx, inst.ActorID); err != nil {
				e.log.Warn("wakeon: idle (pause) failed", "actor", inst.ActorID, "error", err.Error())
			}
		}
	}
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
