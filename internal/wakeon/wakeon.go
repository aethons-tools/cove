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

type Config struct{ PollInterval, MaxWait time.Duration }

const (
	defaultPollInterval = 15 * time.Second
	defaultMaxWait      = 30 * time.Minute
)

type Engine struct {
	reg  Registry
	cur  Cursors
	wake Waker
	reap Reaper
	cmt  Commenter
	cfg  Config
	now  func() time.Time
	log  *slog.Logger
}

func New(reg Registry, cur Cursors, wake Waker, reap Reaper, cmt Commenter, cfg Config, log *slog.Logger) *Engine {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = defaultPollInterval
	}
	if cfg.MaxWait <= 0 {
		cfg.MaxWait = defaultMaxWait
	}
	if log == nil {
		log = slog.New(slog.NewTextHandler(discard{}, nil))
	}
	return &Engine{reg, cur, wake, reap, cmt, cfg, time.Now, log}
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
		if n > base {
			e.log.Info("wakeon: reply detected, waking", "actor", inst.ActorID)
			e.wake.Wake(inst.ActorID)
		}
	}
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
