package jam

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"time"
)

// Recovery: a session whose studio dies — found Lost, or its agent exits
// without asking to end — is not ended. It goes PhaseRecovering: its studio
// is torn down and its identity revoked, but its instance stays (so the
// Requisitioner still sees its ticket worked and its allocator slot stays
// held), its channels and ticket are untouched, and its state volumes are
// kept. The reconcile loop re-raises it under the same id, onto the same
// manager when it is still there, where it resumes its conversation. A
// re-raise that keeps failing backs off and, after RecoveryMaxAttempts or
// RecoveryMaxAge, gives up: a final teardown, its unfinished ticket blocked.
const (
	RecoveryBackoffInitial = 30 * time.Second
	RecoveryBackoffMax     = 30 * time.Minute
	RecoveryMaxAttempts    = 5
	RecoveryMaxAge         = 2 * time.Hour
)

// recoverable reports whether inst's dying studio is recovered rather than
// ended: raised since recovery existed, and not asked to end.
func recoverable(inst Instance) bool {
	return inst.Recoverable && inst.EndRequested == nil
}

// recoverableHere is recoverable, and inst's manager still configured: a
// studio lost with its manager (removed, or force-revoked) took its state
// with it, so the session is ended instead.
func (s *Supervisor) recoverableHere(inst Instance) bool {
	if !recoverable(inst) {
		return false
	}
	pl, ok := s.launcher.(PlacingLauncher)
	return !ok || pl.HasManager(cmp.Or(inst.Manager, LocalManager))
}

// recoveryBackoff is the delay before the next re-raise after attempts
// failures (30s doubling, capped at 30m).
func recoveryBackoff(attempts int) time.Duration {
	if attempts <= 0 {
		return RecoveryBackoffInitial
	}
	return min(RecoveryBackoffInitial<<min(attempts, 16), RecoveryBackoffMax)
}

// markRecovering tears inst's dead studio down, keeping the session: its
// instance goes PhaseRecovering with its first re-raise due after
// RecoveryBackoffInitial. Its state, channels, ticket and slot are left as
// they are. A launcher failure leaves it as it was, for the next pass.
func (s *Supervisor) markRecovering(ctx context.Context, inst Instance, reason string) error {
	if s.sink != nil {
		s.sink.RequestTeardown(inst.ActorID)
	}
	if s.tunnels != nil {
		s.tunnels.CloseAll(inst.ActorID)
	}
	if err := s.launcher.Teardown(ctx, inst); err != nil {
		return err
	}
	if err := s.revokeActor(inst.ActorID); err != nil {
		return err
	}
	if s.tunnels != nil {
		s.tunnels.Reopen(inst.ActorID)
	}
	now := s.now()
	s.patchInstance(inst.ActorID, func(i *Instance) {
		i.Phase, i.Activity, i.Lease = PhaseRecovering, "", Lease{}
		i.Exposures, i.KeptProcesses = nil, 0
		i.Recovery = &Recovery{Since: now, Next: now.Add(RecoveryBackoffInitial), Reason: reason}
	})
	s.notifyExposures(inst.ActorID)
	if s.log != nil {
		s.log.Warn("session's studio died; recovering it", "id", inst.ActorID, "reason", reason)
	}
	return nil
}

// recoverDue re-raises a recovering inst once its attempt is due, recording a
// failure's backoff, or gives up past the limits.
func (s *Supervisor) recoverDue(ctx context.Context, inst Instance, now time.Time) {
	rec := cmp.Or(inst.Recovery, &Recovery{Since: now, Next: now})
	if now.Before(rec.Next) {
		return
	}
	if err := s.reraise(ctx, inst); err != nil {
		attempts := rec.Attempts + 1
		if s.log != nil {
			s.log.Warn("recovery: re-raise failed", "id", inst.ActorID, "attempt", attempts, "err", err.Error())
		}
		if attempts >= RecoveryMaxAttempts || now.Sub(rec.Since) >= RecoveryMaxAge {
			s.giveUpRecovery(ctx, inst)
			return
		}
		s.patchInstance(inst.ActorID, func(i *Instance) {
			i.Recovery = &Recovery{Since: rec.Since, Next: now.Add(recoveryBackoff(attempts)), Attempts: attempts, Reason: rec.Reason}
		})
	}
}

// reraise raises inst again under its id, with the spec it was raised with,
// on its manager while that is still configured, then restores what the
// session had: its inbox cursor, focus, ticket report, alarms and escalation
// category.
func (s *Supervisor) reraise(ctx context.Context, inst Instance) error {
	spec := RaiseSpec{
		ActorID: inst.ActorID, Project: inst.Project, Role: inst.Role, Unit: inst.Unit,
		Mission: inst.Mission, Prompt: inst.RaisePrompt,
		Owner: inst.Owner, OwnerID: inst.OwnerID, Name: inst.Name, SessionKind: inst.SessionKind,
	}
	if pl, ok := s.launcher.(PlacingLauncher); ok && inst.Manager != "" && pl.HasManager(inst.Manager) {
		spec.Placement = Placement{Managers: []string{inst.Manager}}
	}
	if _, _, _, err := s.Raise(ctx, spec); err != nil {
		return err
	}
	s.patchInstance(inst.ActorID, func(i *Instance) {
		i.CommitSeq, i.CommitCursor = inst.CommitSeq, inst.CommitCursor
		i.Focus, i.Report, i.Alarms, i.EscalationCategory = inst.Focus, inst.Report, inst.Alarms, inst.EscalationCategory
	})
	if s.log != nil {
		s.log.Info("recovery: session re-raised", "id", inst.ActorID)
	}
	return nil
}

// giveUpRecovery ends a session whose re-raises keep failing: a final
// teardown, its unfinished ticket marked blocked, its slot released.
func (s *Supervisor) giveUpRecovery(ctx context.Context, inst Instance) {
	s.blockUnfinished(ctx, inst, "re-raise gave up")
	s.patchInstance(inst.ActorID, func(i *Instance) { i.Phase = PhaseTerminating })
	if err := s.Teardown(ctx, inst.ActorID); err != nil && s.log != nil {
		s.log.Warn("recovery: teardown after giving up failed", "id", inst.ActorID, "err", err.Error())
	}
	if s.log != nil {
		s.log.Warn("recovery: gave up re-raising the session; ended it", "id", inst.ActorID)
	}
}

// ErrNotRestartable is a Restart of a session raised before recovery existed:
// ending it is the only way to replace its studio.
var ErrNotRestartable = errors.New("session was raised before restart existed; end it instead")

// Restart restarts a session's studio, keeping the session: a standing one is
// torn down for its reconciler to re-raise (its state kept); any other is
// recovered at once — its studio torn down and re-raised on the next
// reconcile pass, resuming its conversation.
func (s *Supervisor) Restart(ctx context.Context, actorID string) error {
	inst, ok := s.store.GetInstance(actorID)
	if !ok || inst.Phase == PhaseGone {
		return fmt.Errorf("no session %q", actorID)
	}
	if inst.SessionKind == SessionKindStanding {
		return s.Teardown(ctx, actorID)
	}
	if inst.Phase == PhaseRecovering {
		s.patchInstance(actorID, func(i *Instance) {
			if i.Recovery != nil {
				i.Recovery.Next = s.now()
			}
		})
		return nil
	}
	if !recoverable(inst) {
		return ErrNotRestartable
	}
	if err := s.markRecovering(ctx, inst, "restarted"); err != nil {
		return err
	}
	now := s.now()
	s.patchInstance(actorID, func(i *Instance) {
		if i.Recovery != nil {
			i.Recovery.Next = now
		}
	})
	return nil
}
