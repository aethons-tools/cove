// Package wakeon is harbor's resident wake-on engine: it watches Waiting managed
// coves and wakes them (over the Attach ControlSink) when an external-origin
// reply lands in the message Log addressed to them, or tears them down past a
// max-wait. Wired from cmd/at-harbor; not imported by internal/harbor core.
package wakeon

import (
	"context"
	"log/slog"
	"time"

	"github.com/aethons-tools/cove/internal/harbor"
	"github.com/aethons-tools/cove/internal/msglog"
)

type Registry interface{ ListInstances() []harbor.Instance }
type Waker interface{ Wake(actorID string) }
type Reaper interface {
	Teardown(ctx context.Context, actorID string) error
}

// Inbox is the read side of the message Log the engine uses to detect
// replies. Satisfied by *msglog.Log; may be nil (message-log unconfigured →
// no reply-waking, teardown/pause still run).
type Inbox interface {
	ReadInboxSince(t msglog.Target, afterSeq int64, limit int) []msglog.Message
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
	wake  Waker
	reap  Reaper
	idler Idler
	inbox Inbox
	cfg   Config
	now   func() time.Time
	log   *slog.Logger
}

func New(reg Registry, wake Waker, reap Reaper, idler Idler, inbox Inbox, cfg Config, log *slog.Logger) *Engine {
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
	return &Engine{reg, wake, reap, idler, inbox, cfg, time.Now, log}
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
		if e.replied(inst) {
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

// replied reports whether an external-origin inbound message addressed to
// the cove arrived after its WaitSeq position (the log tail's append-order
// Seq, baselined when it entered Waiting). Seq is append order, not a lexical
// id compare — so this fires correctly regardless of which id-namespaced
// source (Linear vs. Discord ingress, etc.) produced the reply's id (COV-184:
// a lexical-id compare could wrongly treat a later reply as "before" the
// baseline when the two ids come from different, non-interleaved namespaces).
// A nil inbox (message-log unconfigured) always reports false — reply-waking
// is off, but the max-wait teardown and warm-timeout Idle above still run.
func (e *Engine) replied(inst harbor.Instance) bool {
	if e.inbox == nil {
		return false
	}
	for _, m := range e.inbox.ReadInboxSince(msglog.Target{Kind: "actor", Ref: inst.ActorID}, inst.WaitSeq, 0) {
		if msglog.Classify(m.From) == msglog.External {
			return true
		}
	}
	return false
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
