// Package wakeon is Jam's resident wake-on engine: it watches Waiting managed
// coves and wakes them (over the Attach ControlSink) when an external-origin
// reply lands in the message Log addressed to them (and, with SetRunningWake,
// Running ones too, so a reply reaches an agent holding a live episode open), or tears them down past a
// max-wait (personal sessions excepted — they wait on their owner, and instead
// climb the idle ladder: nag the owner, optionally reclaim; the owner answers a
// nag with "keep" or "release", which Jam acts on without waking). Wired from
// cmd/at-jam; not imported by internal/jam core.
package wakeon

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
)

type Registry interface{ ListInstances() []jam.Instance }
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
// directly (see internal/jam.Supervisor.Idle/Resume).
type Idler interface {
	Idle(ctx context.Context, actorID string) error
	Resume(ctx context.Context, actorID string) error
}

// RoleLookup reads a Role's idle settings (RoleAllocation.PersonalIdle). Satisfied
// by jam.Store. A missing role gets the defaults.
type RoleLookup interface {
	GetRole(project, name string) (jam.Role, bool)
}

// NagRecorder persists the idle ladder's state on a personal session: that an
// owner was nagged (Supervisor.RecordNag), and that the owner replied "keep" —
// restart the idle period past the reply without waking (Supervisor.KeepWaiting).
type NagRecorder interface {
	RecordNag(actorID string, at time.Time) error
	KeepWaiting(actorID string, afterSeq int64, at time.Time) error
}

// Nagger tells a personal session's owner their session is idle, or that it was
// reclaimed, and confirms their "keep"/"release" reply to a nag. Implemented in
// cmd/at-jam over the intercom log.
type Nagger interface {
	Nag(ctx context.Context, inst jam.Instance, idle time.Duration) error
	NotifyReclaimed(ctx context.Context, inst jam.Instance, idle time.Duration) error
	NotifyKept(ctx context.Context, inst jam.Instance, next time.Duration) error
	NotifyReleased(ctx context.Context, inst jam.Instance) error
}

// Cursor advances a cove's wake-on baseline (Supervisor.SetWaitSeq) past the
// replies it was just woken for while Running, so the next tick does not wake
// it again for the same reply.
type Cursor interface {
	SetWaitSeq(actorID string, seq int64) error
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

	// cursor, when set (SetRunningWake), lets the engine wake Running coves too.
	cursor Cursor
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

// SetRunningWake turns on waking Running coves: a reply to a Live, Running
// cove Wakes it at once — its agent may be holding a live episode open for a
// background task, between turns, where the Wake is written straight in (or,
// mid-turn, coalesced into one resume at turn end) — and cursor advances its
// baseline past the reply. Without it a reply landing while the cove runs only
// wakes it once it reports Waiting. Call before Run.
func (e *Engine) SetRunningWake(cursor Cursor) { e.cursor = cursor }

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
		if inst.Activity == jam.ActivityRunning {
			e.wakeRunning(inst)
			continue
		}
		if inst.Activity != jam.ActivityWaiting {
			continue
		}
		// A resident session (personal or standing) waits for as long as it
		// takes: it is never reaped for waiting (it is still idled and woken
		// below). A personal one ends when its owner releases it, a standing one
		// when an operator dismisses it.
		if !jam.IsResident(inst.SessionKind) &&
			!inst.WaitingSince.IsZero() && e.now().Sub(inst.WaitingSince) > e.cfg.MaxWait {
			if err := e.reap.Teardown(ctx, inst.ActorID); err != nil {
				e.log.Warn("wakeon: teardown (max-wait) failed", "actor", inst.ActorID, "error", err.Error())
			}
			continue
		}
		if rs := e.replies(inst); len(rs) > 0 {
			// A personal session's owner may answer a nag with "keep" or
			// "release" instead of waking it (see command).
			if inst.SessionKind == jam.SessionKindPersonal && e.command(ctx, inst, rs) {
				continue
			}
			if inst.Phase == jam.PhaseIdled {
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
		if inst.SessionKind == jam.SessionKindPersonal && e.idleLadder(ctx, inst) {
			continue // reclaimed
		}
		if inst.Phase != jam.PhaseIdled && e.now().Sub(inst.WaitingSince) > e.cfg.WarmTimeout {
			if err := e.idler.Idle(ctx, inst.ActorID); err != nil {
				e.log.Warn("wakeon: idle (pause) failed", "actor", inst.ActorID, "error", err.Error())
			}
		}
	}
}

// wakeRunning Wakes a Live, Running cove that has replies past its baseline and
// advances the baseline past them (see SetRunningWake). A failed advance is
// logged; the next tick then wakes again, which the cove coalesces.
func (e *Engine) wakeRunning(inst jam.Instance) {
	if e.cursor == nil || inst.Phase != jam.PhaseLive {
		return
	}
	rs := e.replies(inst)
	if len(rs) == 0 {
		return
	}
	last := inst.WaitSeq
	for _, m := range rs {
		last = max(last, m.Seq)
	}
	e.log.Info("wakeon: reply to a running cove, waking", "actor", inst.ActorID)
	e.wake.Wake(inst.ActorID)
	if err := e.cursor.SetWaitSeq(inst.ActorID, last); err != nil {
		e.log.Warn("wakeon: advance wait baseline failed", "actor", inst.ActorID, "error", err.Error())
	}
}

// idleLadder runs one step of a Waiting personal session's idle ladder: once it
// has waited on its owner for idle-after, nag the owner every nag-every; once
// reclaim-after (if set) passes, tell the owner and tear it down. Reports
// whether the session was reclaimed. A failed nag is logged and retried on the
// next tick; a failed reclaim notice defers the reclaim to the next tick, so a
// session is never reclaimed without telling its owner (unless there is no
// nagger at all).
func (e *Engine) idleLadder(ctx context.Context, inst jam.Instance) bool {
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

// replies returns the external-origin inbound messages addressed to the cove
// after its WaitSeq position (the log tail's append-order Seq, baselined when
// its run started and advanced past each reply it was woken for while Running). Seq is append order, not a lexical id compare — so this
// is correct regardless of which id-namespaced source (Linear vs. Discord
// ingress, etc.) produced a reply's id (COV-184: a lexical-id compare could
// wrongly treat a later reply as "before" the baseline when the two ids come
// from different, non-interleaved namespaces). A nil inbox (squawk log
// unconfigured) always returns none — reply-waking is off, but the max-wait
// teardown and warm-timeout Idle above still run.
func (e *Engine) replies(inst jam.Instance) []intercom.Squawk {
	if e.inbox == nil {
		return nil
	}
	var out []intercom.Squawk
	for _, m := range e.inbox.ReadInboxSince(intercom.Target{Kind: "actor", Ref: inst.ActorID}, inst.WaitSeq, 0) {
		if intercom.Classify(m.From) == intercom.External {
			out = append(out, m)
		}
	}
	return out
}

// Reply-to-act command words (see command).
const (
	cmdKeep    = "keep"
	cmdRelease = "release"
)

// command acts on a Waiting personal session's owner's "keep"/"release" reply
// to one of its nags, and reports whether it handled the replies (the caller
// then skips the wake and the rest of the tick for this instance).
//
// A reply is a command only when all hold: it replies to one of THIS session's
// nags (jam.IsNagReply), it is from human:<owner> — the relay attributes a
// reply to a roster human only when it was posted in that human's own discord
// inbox, never by display name — and its trimmed, lowercased body (trailing
// "." / "!" ignored) is exactly "keep" or "release". Anything else is an
// ordinary reply; a command-shaped reply from anyone but the owner is logged at
// warn and treated as ordinary. Everything fails toward waking, never teardown.
//
// Precedence across the pending replies: any "release" releases (teardown, then
// a best-effort confirmation; a failed teardown is logged and retried next tick
// because the reply is still past the baseline). Otherwise any ordinary reply
// wakes as usual (a "keep" beside it is just text the agent reads). Otherwise —
// only "keep"s — the wait baseline moves past them and the idle ladder restarts
// (KeepWaiting) before a best-effort confirmation, so a keep acts once; the
// session is not woken, and an Idled one stays paused.
func (e *Engine) command(ctx context.Context, inst jam.Instance, rs []intercom.Squawk) bool {
	if e.nags == nil || inst.Owner == "" {
		return false // ladder off: there are no nags to answer
	}
	owner := intercom.Target{Kind: "human", Ref: inst.Owner}
	var release, other bool
	var lastKeep int64
	for _, m := range rs {
		word := commandWord(m.Body)
		if word == "" || !jam.IsNagReply(m.ReplyTo, inst.ActorID) {
			other = true
			continue
		}
		if m.From != owner {
			e.log.Warn("wakeon: ignoring a nag command from someone other than the owner", "actor", inst.ActorID, "command", word)
			other = true
			continue
		}
		if word == cmdRelease {
			release = true
		} else {
			lastKeep = max(lastKeep, m.Seq)
		}
	}
	switch {
	case release:
		e.log.Info("wakeon: owner released personal session", "actor", inst.ActorID, "owner", inst.Owner, "command", cmdRelease)
		if err := e.reap.Teardown(ctx, inst.ActorID); err != nil {
			e.log.Warn("wakeon: teardown (release) failed; retrying next tick", "actor", inst.ActorID, "error", err.Error())
			return true
		}
		if e.nagger != nil {
			if err := e.nagger.NotifyReleased(ctx, inst); err != nil {
				e.log.Warn("wakeon: release confirmation failed", "actor", inst.ActorID, "error", err.Error())
			}
		}
		return true
	case other:
		return false
	}
	// only keeps
	var idleAfter time.Duration
	if e.roles != nil {
		role, _ := e.roles.GetRole(inst.Project, inst.Role) // missing role → defaults
		idleAfter, _, _ = role.Allocation.PersonalIdle()
	} else {
		idleAfter, _, _ = jam.RoleAllocation{}.PersonalIdle()
	}
	e.log.Info("wakeon: owner kept personal session", "actor", inst.ActorID, "owner", inst.Owner, "command", cmdKeep)
	if err := e.nags.KeepWaiting(inst.ActorID, lastKeep, e.now()); err != nil {
		e.log.Warn("wakeon: keep failed; retrying next tick", "actor", inst.ActorID, "error", err.Error())
		return true
	}
	if e.nagger != nil {
		if err := e.nagger.NotifyKept(ctx, inst, idleAfter); err != nil {
			e.log.Warn("wakeon: keep confirmation failed", "actor", inst.ActorID, "error", err.Error())
		}
	}
	return true
}

// commandWord returns cmdKeep or cmdRelease when body is exactly that word
// (case-insensitive, surrounding whitespace and trailing "."/"!" ignored), else "".
func commandWord(body string) string {
	w := strings.ToLower(strings.TrimSpace(strings.TrimRight(strings.TrimSpace(body), ".! ")))
	if w == cmdKeep || w == cmdRelease {
		return w
	}
	return ""
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
