// Package wakeon is Jam's resident wake-on engine: it watches Waiting managed
// coves and wakes them (over the Attach ControlSink) when an external-origin
// reply lands in the message Log addressed to them (and, with SetRunningWake,
// Running ones too, so a reply reaches an agent holding a live episode open), or tears them down past a
// max-wait (personal sessions excepted — they wait on their owner, and instead
// climb the idle ladder: nag the owner, optionally reclaim; the owner answers a
// nag with "keep" or "release", which Jam acts on without waking). With
// SetTurnEnd it also enforces turn end: a session that asked to end is torn
// down once Waiting and never woken again, an armed idle deadline wakes the
// session or tears it down per its role, and due alarms fire for a Holding or
// Waiting session (held while it runs). Wired from cmd/at-jam; not
// imported by internal/jam core.
package wakeon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
)

type Registry interface{ ListInstances() []jam.Instance }
type Waker interface {
	Wake(actorID string, reasons ...jam.WakeReason)
}
type Reaper interface {
	Teardown(ctx context.Context, actorID string) error
}

// Inbox is the read side of the intercom the engine uses to detect replies:
// a session's inbox after a seq (jam.SessionInbox — its deliveries, and the
// legacy squawks addressed to it before the cutover). May be nil (no log →
// no reply-waking; teardown and pause still run).
type Inbox interface {
	Since(p ident.ID, afterSeq int64, limit int) []intercom.Squawk
}

// ChannelHistory reads a channel's recent squawks (intercom.Store's
// ChannelBefore: ascending, the last limit before a seq) — what the
// agent-to-agent loop breaker counts.
type ChannelHistory interface {
	ChannelBefore(ch ident.ID, beforeSeq int64, limit int) []intercom.Squawk
}

// BreakerNotifier posts the loop breaker's notice into a channel, as from
// (the session that wasn't woken), with id (a duplicate id is no error: the
// notice is posted once). jam.Intercom.BreakerNotice.
type BreakerNotifier interface {
	BreakerNotice(ch, from ident.ID, id, body string) error
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

// Ender tells a session's owner that it ended itself (the `end` tool).
// Implemented in cmd/at-jam over the intercom log.
type Ender interface {
	NotifyEnded(ctx context.Context, inst jam.Instance, reason string) error
}

// AlarmFirer marks a cove's due alarms fired and returns its alarms
// (Supervisor.FireAlarms). Fired alarms stay pending — their Wake re-sent each
// tick — until the cove next runs and Report retires them.
type AlarmFirer interface {
	FireAlarms(actorID string, now time.Time) ([]jam.Alarm, error)
}

// GateState records and resolves alarm gate runs (Supervisor.StartGate /
// ResolveGate).
type GateState interface {
	StartGate(actorID, name, runID string, now time.Time) (jam.Alarm, bool, error)
	ResolveGate(actorID, runID string, o jam.GateOutcome) error
}

// GateRunner asks a cove to run a gate (attach.Server). RunGate is sent only
// while Connected: a request to a cove with no stream would be dropped.
type GateRunner interface {
	Connected(actorID string) bool
	RunGate(actorID, runID, alarm, command string, timeout time.Duration)
}

// TicketCloser marks a ticket session's ticket blocked when Jam ends the
// session without a final report (implemented in cmd/at-jam; a no-op for a
// ticketless or terminally-reported session).
type TicketCloser interface {
	BlockUnfinished(ctx context.Context, inst jam.Instance, reason string) error
}

// ReportRecorder stamps a session's ticket report (Supervisor.SetReport).
type ReportRecorder interface {
	SetReport(actorID string, r jam.TicketReport) error
}

// blockTimeout bounds the tracker call made before a teardown, so a stalled
// tracker can't freeze the wake-on tick.
const blockTimeout = 15 * time.Second

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

	// Turn-end enforcement (SetTurnEnd); off until set.
	turnEnd bool
	ender   Ender      // nil = no end notices
	alarms  AlarmFirer // nil = no alarms

	tickets TicketCloser   // nil = no ticket updates on teardown
	reports ReportRecorder // records the blocked report so it is made once

	// Alarm gates (SetGates); off while either is nil.
	gates      GateState
	gateRunner GateRunner

	// Sessions waking sessions (SetSessionWakes); off while history is nil.
	history      ChannelHistory
	breaker      BreakerNotifier // nil = no notice
	sessionLimit int
	// tripped caches each session delivery's verdict by seq (it can't change:
	// every earlier seq is settled), so a suppressed delivery is checked
	// against the log once, not every tick; cleared when it grows large.
	trippedMu sync.Mutex
	tripped   map[int64]bool
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

// SetSessionWakes lets another session's post wake a waiting session — with
// a loop breaker: once a channel has had more than limit session posts in a
// row since a person or account last posted there, session posts in it no
// longer wake, and breaker posts one notice there (a person's reply resets
// it). Call before Run.
func (e *Engine) SetSessionWakes(history ChannelHistory, breaker BreakerNotifier, limit int) {
	e.history, e.breaker, e.sessionLimit = history, breaker, limit
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

// SetTurnEnd turns on turn-end enforcement: end(reason) and the idle
// deadline. roles supplies each role's on-idle action (nil keeps any set by
// SetIdleLadder); ender (may be nil) notifies an owner that a session ended
// itself; alarms (may be nil) fires due alarms. The idle deadline and fired
// alarms are retired by the supervisor when the cove next runs, so their Wake
// is re-sent every tick until it is answered. Call before Run.
// SetGates turns on alarm gates: a due gated alarm's gate is run in the cove
// (resuming a paused one first) and its verdict decides whether it fires; a
// run with no result within GateTimeout+GateGrace fails. Call before Run.
func (e *Engine) SetGates(state GateState, runner GateRunner) { e.gates, e.gateRunner = state, runner }

// SetTickets has every teardown wake-on performs (end, idle timeout,
// wait-max) first mark an unfinished ticket blocked, and record that as the
// session's report (so a teardown retried next tick doesn't mark it again).
// Call before Run.
func (e *Engine) SetTickets(t TicketCloser, reports ReportRecorder) {
	e.tickets, e.reports = t, reports
}

// blockUnfinished marks inst's ticket blocked before Jam ends it (best-effort
// and time-bounded: a failure is logged and the teardown goes ahead).
func (e *Engine) blockUnfinished(ctx context.Context, inst jam.Instance, reason string) {
	if e.tickets == nil || inst.Unit == "" || (inst.Report != nil && inst.Report.Terminal()) {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, blockTimeout)
	defer cancel()
	if err := e.tickets.BlockUnfinished(ctx, inst, reason); err != nil {
		e.log.Warn("wakeon: mark ticket blocked failed; tearing down anyway", "actor", inst.ActorID, "ticket", inst.Unit, "error", err.Error())
		return
	}
	if e.reports != nil {
		r := jam.TicketReport{State: jam.ReportBlocked, Summary: "session ended without a final report: " + reason, At: e.now()}
		if err := e.reports.SetReport(inst.ActorID, r); err != nil {
			e.log.Warn("wakeon: record blocked report failed", "actor", inst.ActorID, "error", err.Error())
		}
	}
}

func (e *Engine) SetTurnEnd(roles RoleLookup, ender Ender, alarms AlarmFirer) {
	if roles != nil {
		e.roles = roles
	}
	e.turnEnd, e.ender, e.alarms = true, ender, alarms
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
		if e.turnEnd && inst.EndRequested != nil {
			// Asked to end: never woken again; torn down once its turn is over.
			if inst.Activity == jam.ActivityWaiting {
				e.endSession(ctx, inst)
			}
			continue
		}
		if inst.Activity == jam.ActivityRunning || inst.Activity == jam.ActivityHolding {
			// holding (turn over, background tasks running) is woken like running
			// and, like running, never paused or reaped here.
			// Alarms are held while it runs; a Holding cove's turn is over.
			var alarms []jam.WakeReason
			if inst.Activity == jam.ActivityHolding {
				e.runGates(ctx, inst)
				alarms = e.alarmReasons(inst)
			}
			if !e.wakeRunning(inst, alarms) && inst.Activity == jam.ActivityHolding && e.idleDue(inst) && e.idleAction(inst) == jam.OnIdleWake {
				// A teardown action waits for Waiting: never end a cove mid-hold.
				e.log.Info("wakeon: idle timeout, waking", "actor", inst.ActorID)
				e.wake.Wake(inst.ActorID, jam.WakeReason{Kind: jam.WakeIdle})
			}
			continue
		}
		if inst.Activity != jam.ActivityWaiting {
			continue
		}
		// A resident session (personal or standing) waits for as long as it
		// takes: it is never reaped for waiting (it is still idled and woken
		// below). A personal one ends when its owner releases it, a standing one
		// when an operator dismisses it. With an idle deadline armed, the role's
		// on-idle action decides instead, and an alarm is itself a wake
		// condition; wait-max is the backstop when neither is set.
		if !jam.IsResident(inst.SessionKind) && !e.idleArmed(inst) && !(e.alarms != nil && len(inst.Alarms) > 0) &&
			!inst.WaitingSince.IsZero() && e.now().Sub(inst.WaitingSince) > e.cfg.MaxWait {
			e.blockUnfinished(ctx, inst, "wait-max")
			if err := e.reap.Teardown(ctx, inst.ActorID); err != nil {
				e.log.Warn("wakeon: teardown (max-wait) failed", "actor", inst.ActorID, "error", err.Error())
			}
			continue
		}
		if e.runGates(ctx, inst) {
			continue // resumed to run a gate; it runs on a later tick
		}
		// One Wake carries every reason: a pending reply and each fired alarm.
		reasons := e.alarmReasons(inst)
		if rs := e.replies(inst); len(rs) > 0 {
			// A personal session's owner may answer a nag with "keep" or
			// "release" instead of waking it (see command).
			if inst.SessionKind == jam.SessionKindPersonal && e.command(ctx, inst, rs) {
				continue
			}
			reasons = append([]jam.WakeReason{{Kind: jam.WakeSquawk}}, reasons...)
		}
		if len(reasons) > 0 {
			if inst.Phase == jam.PhaseIdled {
				if err := e.idler.Resume(ctx, inst.ActorID); err != nil {
					e.log.Warn("wakeon: resume failed", "actor", inst.ActorID, "error", err.Error())
				}
				// Wake is sent on a later tick, once it's Live+Waiting and the stream has reconnected.
				continue
			}
			e.log.Info("wakeon: waking", "actor", inst.ActorID, "reasons", len(reasons))
			e.wake.Wake(inst.ActorID, reasons...)
			continue
		}
		if e.idleDue(inst) {
			e.fireIdle(ctx, inst)
			continue
		}
		// no reply. The idle ladder is personal-only: a standing session has no
		// owner to nag.
		if inst.SessionKind == jam.SessionKindPersonal && e.idleLadder(ctx, inst) {
			continue // reclaimed
		}
		if inst.Phase != jam.PhaseIdled && e.now().Sub(inst.WaitingSince) > e.cfg.WarmTimeout && !gateInFlight(inst) {
			if err := e.idler.Idle(ctx, inst.ActorID); err != nil {
				e.log.Warn("wakeon: idle (pause) failed", "actor", inst.ActorID, "error", err.Error())
			}
		}
	}
}

// wakeRunning Wakes a Live, Running or Holding cove that has replies past its baseline and
// advances the baseline past them (see SetRunningWake), reporting whether it
// woke it; extra reasons (a Holding cove's fired alarms) ride along, or wake
// it alone. A failed advance is logged; the next tick then wakes again, which
// the cove coalesces.
func (e *Engine) wakeRunning(inst jam.Instance, extra []jam.WakeReason) bool {
	if e.cursor == nil || inst.Phase != jam.PhaseLive {
		return false
	}
	rs := e.replies(inst)
	if len(rs) == 0 {
		if len(extra) == 0 {
			return false
		}
		e.log.Info("wakeon: alarm for a holding cove, waking", "actor", inst.ActorID)
		e.wake.Wake(inst.ActorID, extra...)
		return true
	}
	last := inst.WaitSeq
	for _, m := range rs {
		last = max(last, m.Seq)
	}
	e.log.Info("wakeon: reply to a running cove, waking", "actor", inst.ActorID)
	e.wake.Wake(inst.ActorID, append([]jam.WakeReason{{Kind: jam.WakeSquawk}}, extra...)...)
	if err := e.cursor.SetWaitSeq(inst.ActorID, last); err != nil {
		e.log.Warn("wakeon: advance wait baseline failed", "actor", inst.ActorID, "error", err.Error())
	}
	return true
}

// runGates starts the gates of inst's due gated alarms, and fails any run
// that got no result within GateTimeout+GateGrace. A paused cove is resumed
// first, and its gates run on a later tick: it reports whether it resumed.
func (e *Engine) runGates(ctx context.Context, inst jam.Instance) bool {
	if e.gates == nil || e.gateRunner == nil {
		return false
	}
	now := e.now()
	for _, a := range inst.Alarms {
		if a.Gate == "" || a.NextAt.IsZero() || a.NextAt.After(now) {
			continue
		}
		if r := a.GateRun; r != nil {
			if !now.Before(r.StartedAt.Add(jam.GateTimeout + jam.GateGrace)) {
				e.log.Warn("wakeon: gate got no result; failing it", "actor", inst.ActorID, "alarm", a.Name, "run", r.RunID)
				if err := e.gates.ResolveGate(inst.ActorID, r.RunID, jam.GateOutcome{At: now, NoResult: true}); err != nil {
					e.log.Warn("wakeon: resolve gate failed", "actor", inst.ActorID, "error", err.Error())
				}
			}
			continue
		}
		if inst.Phase == jam.PhaseIdled {
			if err := e.idler.Resume(ctx, inst.ActorID); err != nil {
				e.log.Warn("wakeon: resume for gate failed", "actor", inst.ActorID, "error", err.Error())
			}
			return true
		}
		if !e.gateRunner.Connected(inst.ActorID) {
			continue // the stream is (re)connecting: run it on a later tick
		}
		runID := newRunID()
		if _, ok, err := e.gates.StartGate(inst.ActorID, a.Name, runID, now); err != nil {
			e.log.Warn("wakeon: start gate failed", "actor", inst.ActorID, "alarm", a.Name, "error", err.Error())
		} else if ok {
			e.log.Info("wakeon: running gate", "actor", inst.ActorID, "alarm", a.Name, "run", runID)
			e.gateRunner.RunGate(inst.ActorID, runID, a.Name, a.Gate, jam.GateTimeout)
		}
	}
	return false
}

// gateInFlight reports whether any of inst's alarms has a gate running: the
// cove must not be paused under it.
func gateInFlight(inst jam.Instance) bool {
	for _, a := range inst.Alarms {
		if a.GateRun != nil {
			return true
		}
	}
	return false
}

// newRunID is a random gate-run id.
func newRunID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// alarmReasons fires inst's due alarms and returns a reason for every alarm
// fired and not yet answered (re-sent until the cove runs).
func (e *Engine) alarmReasons(inst jam.Instance) []jam.WakeReason {
	if e.alarms == nil {
		return nil
	}
	alarms, now := inst.Alarms, e.now()
	for _, a := range alarms {
		if !a.NextAt.IsZero() && !a.NextAt.After(now) {
			fired, err := e.alarms.FireAlarms(inst.ActorID, now)
			if err != nil {
				e.log.Warn("wakeon: fire alarms failed", "actor", inst.ActorID, "error", err.Error())
				break
			}
			alarms = fired
			break
		}
	}
	var out []jam.WakeReason
	for _, a := range alarms {
		if !a.FiredAt.IsZero() {
			kind := a.FireKind
			if kind == "" {
				kind = jam.WakeAlarm
			}
			out = append(out, jam.WakeReason{Kind: kind, Alarm: a.Name, Note: a.Note, Detail: a.FireDetail})
		}
	}
	return out
}

// idleArmed reports whether inst has an idle deadline this engine enforces.
func (e *Engine) idleArmed(inst jam.Instance) bool {
	return e.turnEnd && !inst.IdleDeadline.IsZero()
}

// idleDue reports whether inst's armed idle deadline has passed.
func (e *Engine) idleDue(inst jam.Instance) bool {
	return e.idleArmed(inst) && !e.now().Before(inst.IdleDeadline)
}

// idleAction is the role's on-idle action for inst (wake when unknown).
func (e *Engine) idleAction(inst jam.Instance) string {
	if e.roles != nil {
		if r, ok := e.roles.GetRole(inst.Project, inst.Role); ok {
			return r.TurnEnd.Action()
		}
	}
	return jam.OnIdleWake
}

// fireIdle applies a Waiting cove's on-idle action once its deadline passed:
// teardown ends it; wake resumes a paused cove first (woken on a later tick,
// once it is Live again) and otherwise wakes it with the idle reason — again
// every tick until the cove runs (a Wake to a cove with no stream is dropped).
func (e *Engine) fireIdle(ctx context.Context, inst jam.Instance) {
	if e.idleAction(inst) == jam.OnIdleTeardown {
		e.log.Info("wakeon: idle timeout, tearing down", "actor", inst.ActorID)
		e.blockUnfinished(ctx, inst, "idle timeout")
		if err := e.reap.Teardown(ctx, inst.ActorID); err != nil {
			e.log.Warn("wakeon: teardown (idle) failed", "actor", inst.ActorID, "error", err.Error())
		}
		return
	}
	if inst.Phase == jam.PhaseIdled {
		if err := e.idler.Resume(ctx, inst.ActorID); err != nil {
			e.log.Warn("wakeon: resume failed", "actor", inst.ActorID, "error", err.Error())
		}
		return
	}
	e.log.Info("wakeon: idle timeout, waking", "actor", inst.ActorID)
	e.wake.Wake(inst.ActorID, jam.WakeReason{Kind: jam.WakeIdle})
}

// endSession tears down a Waiting session that asked to end, then tells its
// owner (best-effort). A failed teardown is retried next tick.
func (e *Engine) endSession(ctx context.Context, inst jam.Instance) {
	e.log.Info("wakeon: session ended itself", "actor", inst.ActorID, "reason", inst.EndRequested.Reason)
	e.blockUnfinished(ctx, inst, inst.EndRequested.Reason)
	if err := e.reap.Teardown(ctx, inst.ActorID); err != nil {
		e.log.Warn("wakeon: teardown (end) failed; retrying next tick", "actor", inst.ActorID, "error", err.Error())
		return
	}
	if e.ender != nil {
		if err := e.ender.NotifyEnded(ctx, inst, inst.EndRequested.Reason); err != nil {
			e.log.Warn("wakeon: end notice failed", "actor", inst.ActorID, "error", err.Error())
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
	for _, m := range e.inbox.Since(ident.ID(inst.ActorID), inst.WaitSeq, 0) {
		// A person's or an account's post wakes the session. Another
		// session's wakes it only with session wakes on, and not once its
		// channel's loop breaker has tripped (two sessions would otherwise
		// wake each other turn after turn) — it waits in the inbox for the
		// next read. A legacy squawk from another session never wakes.
		if m.From == ident.ID(inst.ActorID) || strings.HasPrefix(string(m.From), "actor:") {
			continue
		}
		if isSession(m.From) && (e.history == nil || jam.IsNotice(m.ID) && !jam.IsCallInNotice(m.ID) || e.loopTripped(inst, m)) {
			continue // a session's notice (a nag, the breaker's) is for people
		}
		out = append(out, m)
	}
	return out
}

// breakerScan bounds how far back the breaker looks for a person's last post
// (for its notice's id).
const breakerScan = 500

// loopTripped reports whether m (a session's post) is past its channel's
// loop breaker: the last sessionLimit+1 squawks there, m included, are all
// from sessions. When it is, the breaker's notice is posted (once per run,
// keyed by the person's last post in the channel).
func (e *Engine) loopTripped(inst jam.Instance, m intercom.Squawk) bool {
	if m.Channel == "" {
		return false
	}
	e.trippedMu.Lock()
	verdict, known := e.tripped[m.Seq]
	e.trippedMu.Unlock()
	if known {
		return verdict
	}
	verdict = e.checkLoop(inst, m)
	e.trippedMu.Lock()
	if e.tripped == nil || len(e.tripped) >= trippedCacheMax {
		e.tripped = map[int64]bool{}
	}
	e.tripped[m.Seq] = verdict
	e.trippedMu.Unlock()
	return verdict
}

// trippedCacheMax bounds the breaker's verdict cache.
const trippedCacheMax = 10000

// checkLoop decides loopTripped from the log, posting the notice on a trip.
func (e *Engine) checkLoop(inst jam.Instance, m intercom.Squawk) bool {
	window := e.history.ChannelBefore(m.Channel, m.Seq+1, e.sessionLimit+1)
	if len(window) <= e.sessionLimit {
		return false
	}
	for _, w := range window {
		if !isSession(w.From) {
			return false
		}
	}
	if e.breaker != nil {
		var since int64
		for _, w := range e.history.ChannelBefore(m.Channel, window[0].Seq, breakerScan) {
			if !isSession(w.From) {
				since = w.Seq
			}
		}
		id := fmt.Sprintf("breaker:%s:%d", m.Channel, since)
		body := fmt.Sprintf("Jam paused agent-to-agent wakes here after %d messages between sessions in a row. Reply here to resume.", e.sessionLimit)
		if err := e.breaker.BreakerNotice(m.Channel, ident.ID(inst.ActorID), id, body); err != nil {
			e.log.Warn("wakeon: loop breaker notice failed", "channel", string(m.Channel), "error", err.Error())
		}
	}
	return true
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
	if e.nags == nil || (inst.OwnerID == "" && inst.Owner == "") {
		return false // ladder off: there are no nags to answer
	}
	// The owner: their user id, or — for a reply from before the cutover —
	// the legacy log's human:<name>.
	isOwner := func(from ident.ID) bool {
		return (inst.OwnerID != "" && from == inst.OwnerID) || (inst.Owner != "" && from == ident.ID("human:"+inst.Owner))
	}
	var release, other bool
	var lastKeep int64
	for _, m := range rs {
		word := commandWord(m.Body)
		if word == "" || !jam.IsNagReply(m.ReplyTo, inst.ActorID) {
			other = true
			continue
		}
		if !isOwner(m.From) {
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

// isSession reports whether a participant is a session: a ses_ id, or a
// grandfathered one (the only participant ids that aren't registry ids). A
// legacy squawk's "kind:ref" author is not one.
func isSession(id ident.ID) bool {
	if strings.Contains(string(id), ":") {
		return false
	}
	_, err := ident.Parse(string(id))
	return err != nil || id.Kind() == ident.Session
}
