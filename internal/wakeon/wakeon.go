// Package wakeon is harbor's resident wake-on engine: it watches Waiting managed
// coves and wakes them (over the Attach ControlSink) when an external-origin
// reply lands in the message Log addressed to them, or tears them down past a
// max-wait (personal sessions excepted — they wait on their owner, and instead
// climb the idle ladder: nag the owner, optionally reclaim). Wired from
// cmd/at-harbor; not imported by internal/harbor core.
package wakeon

import (
	"context"
	"log/slog"
	"time"

	"github.com/aethons-tools/cove/internal/harbor"
	"github.com/aethons-tools/cove/internal/intercom"
)

type Registry interface{ ListInstances() []harbor.Instance }
type Waker interface{ Wake(actorID string) }
type Reaper interface {
	Teardown(ctx context.Context, actorID string) error
}

// Inbox is the read side of the message Log the engine uses to detect
// replies. Satisfied by *intercom.Log; may be nil (squawk log unconfigured →
// no reply-waking, teardown/pause still run).
type Inbox interface {
	ReadInboxSince(t intercom.Target, afterSeq int64, limit int) []intercom.Squawk
}

// Idler pauses/unpauses a Live cove going through its warm-idle window (B2).
// Backed by the supervisor's Idle/Resume — never the Launcher/backend
// directly (see internal/harbor.Supervisor.Idle/Resume).
type Idler interface {
	Idle(ctx context.Context, actorID string) error
	Resume(ctx context.Context, actorID string) error
}

// RoleLookup reads a Role's idle settings (RoleAllocation.PersonalIdle). Satisfied
// by harbor.Store. A missing role gets the defaults.
type RoleLookup interface {
	GetRole(project, name string) (harbor.Role, bool)
}

// NagRecorder persists that an owner was nagged (Supervisor.RecordNag).
type NagRecorder interface {
	RecordNag(actorID string, at time.Time) error
}

// Nagger tells a personal session's owner their session is idle, or that it was
// reclaimed. Implemented in cmd/at-harbor over the intercom log.
type Nagger interface {
	Nag(ctx context.Context, inst harbor.Instance, idle time.Duration) error
	NotifyReclaimed(ctx context.Context, inst harbor.Instance, idle time.Duration) error
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

	// The personal-session idle ladder (SetIdleLadder); off while roles or
	// nags is nil.
	roles  RoleLookup
	nags   NagRecorder
	nagger Nagger // nil = no nags or notices (reclaim still runs)
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
	return &Engine{reg: reg, wake: wake, reap: reap, idler: idler, inbox: inbox, cfg: cfg, now: time.Now, log: log}
}

// SetIdleLadder turns on the personal-session idle ladder: roles supplies each
// role's idle settings, nags records sent nags, and nagger delivers them (nil =
// no intercom: no nags or reclaim notices, but a configured reclaim still
// happens). Call before Run.
func (e *Engine) SetIdleLadder(roles RoleLookup, nags NagRecorder, nagger Nagger) {
	e.roles, e.nags, e.nagger = roles, nags, nagger
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
		// A resident session (personal or standing) waits for as long as it
		// takes: it is never reaped for waiting (it is still idled and woken
		// below). A personal one ends when its owner releases it, a standing one
		// when an operator dismisses it.
		if !harbor.IsResident(inst.SessionKind) &&
			!inst.WaitingSince.IsZero() && e.now().Sub(inst.WaitingSince) > e.cfg.MaxWait {
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
		// no reply. The idle ladder is personal-only: a standing session has no
		// owner to nag.
		if inst.SessionKind == harbor.SessionKindPersonal && e.idleLadder(ctx, inst) {
			continue // reclaimed
		}
		if inst.Phase != harbor.PhaseIdled && e.now().Sub(inst.WaitingSince) > e.cfg.WarmTimeout {
			if err := e.idler.Idle(ctx, inst.ActorID); err != nil {
				e.log.Warn("wakeon: idle (pause) failed", "actor", inst.ActorID, "error", err.Error())
			}
		}
	}
}

// idleLadder runs one step of a Waiting personal session's idle ladder: once it
// has waited on its owner for idle-after, nag the owner every nag-every; once
// reclaim-after (if set) passes, tell the owner and tear it down. Reports
// whether the session was reclaimed. A failed nag is logged and retried on the
// next tick; a failed reclaim notice defers the reclaim to the next tick, so a
// session is never reclaimed without telling its owner (unless there is no
// nagger at all).
func (e *Engine) idleLadder(ctx context.Context, inst harbor.Instance) bool {
	if e.roles == nil || e.nags == nil || inst.WaitingSince.IsZero() {
		return false
	}
	role, _ := e.roles.GetRole(inst.Project, inst.Role) // missing role → defaults
	idleAfter, nagEvery, reclaimAfter := role.Allocation.PersonalIdle()
	now := e.now()
	idle := now.Sub(inst.WaitingSince)
	if reclaimAfter > 0 && idle >= reclaimAfter {
		if e.nagger != nil {
			if err := e.nagger.NotifyReclaimed(ctx, inst, idle); err != nil {
				e.log.Warn("wakeon: reclaim notice failed; reclaim deferred", "actor", inst.ActorID, "error", err.Error())
				return false
			}
		}
		e.log.Info("wakeon: reclaiming idle personal session", "actor", inst.ActorID, "owner", inst.Owner, "idle", idle.String())
		if err := e.reap.Teardown(ctx, inst.ActorID); err != nil {
			e.log.Warn("wakeon: teardown (reclaim) failed", "actor", inst.ActorID, "error", err.Error())
		}
		return true
	}
	if e.nagger == nil || idle < idleAfter {
		return false
	}
	if !inst.LastNagAt.IsZero() && now.Sub(inst.LastNagAt) < nagEvery {
		return false
	}
	if err := e.nagger.Nag(ctx, inst, idle); err != nil {
		e.log.Warn("wakeon: idle nag failed; retrying next tick", "actor", inst.ActorID, "error", err.Error())
		return false
	}
	if err := e.nags.RecordNag(inst.ActorID, now); err != nil {
		e.log.Warn("wakeon: record nag failed", "actor", inst.ActorID, "error", err.Error())
	}
	return false
}

// replied reports whether an external-origin inbound message addressed to
// the cove arrived after its WaitSeq position (the log tail's append-order
// Seq, baselined when it entered Waiting). Seq is append order, not a lexical
// id compare — so this fires correctly regardless of which id-namespaced
// source (Linear vs. Discord ingress, etc.) produced the reply's id (COV-184:
// a lexical-id compare could wrongly treat a later reply as "before" the
// baseline when the two ids come from different, non-interleaved namespaces).
// A nil inbox (squawk log unconfigured) always reports false — reply-waking
// is off, but the max-wait teardown and warm-timeout Idle above still run.
func (e *Engine) replied(inst harbor.Instance) bool {
	if e.inbox == nil {
		return false
	}
	for _, m := range e.inbox.ReadInboxSince(intercom.Target{Kind: "actor", Ref: inst.ActorID}, inst.WaitSeq, 0) {
		if intercom.Classify(m.From) == intercom.External {
			return true
		}
	}
	return false
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
