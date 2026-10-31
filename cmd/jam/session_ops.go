package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
)

// sessionCommander sends a session-control press over the session's Attach
// stream (attach.Server); false is no live stream.
type sessionCommander interface {
	Interrupt(actorID, by string) bool
	Compact(actorID, by, instructions string) bool
	Clear(actorID, by string) bool
	Connected(actorID string) bool
}

// resumer unpauses an idled session (the supervisor).
type resumer interface {
	Resume(ctx context.Context, actorID string) error
}

// statusLineSetter sets a session's mission and focus (the supervisor).
type statusLineSetter interface {
	SetMission(actorID, mission string) error
	SetFocus(actorID, focus string) error
}

// errNoStream is a command with no live Attach stream to carry it.
var errNoStream = errors.New("no live stream")

const (
	defaultConnectWait = 30 * time.Second
	defaultConnectPoll = 250 * time.Millisecond
)

// sessionOps are the /app/user session header's actions over the supervisor
// (userui.SessionOps): teardown (release / restart / end), a standing
// session's upgrade, and the session-control commands.
type sessionOps struct {
	store jam.Store
	sup   *jam.Supervisor
	res   resumer
	cmd   sessionCommander
	// lines (mission and focus) are reset on Clear: Clear means a new task,
	// so the old lines would be stale.
	lines statusLineSetter
	// log reports a failed mission or focus reset (nil: slog.Default).
	log *slog.Logger
	// connectWait bounds waiting for a resumed session's stream (default 30s),
	// polled every connectPoll (default 250ms).
	connectWait, connectPoll time.Duration
}

func (o sessionOps) Teardown(ctx context.Context, actorID string) error {
	return o.sup.Teardown(ctx, actorID)
}

func (o sessionOps) Upgrade(inst jam.Instance) (string, error) {
	res, err := jam.UpgradeStanding(o.store, o.sup, jam.ProjectName(o.store, inst.Project), inst.Role, inst.Name, false)
	if err != nil {
		return "", err
	}
	if res.State != "" {
		return res.State, nil
	}
	return res.Reason, nil
}

func (o sessionOps) UpgradeState(inst jam.Instance) string {
	return o.sup.StandingUpgradeState(jam.ProjectName(o.store, inst.Project), inst.Role, inst.Name)
}

// Command sends c, pressed by by, to inst, first resuming a paused session
// and waiting for its stream to reconnect.
func (o sessionOps) Command(ctx context.Context, inst jam.Instance, c jam.SessionCommand, by string) error {
	if inst.Phase == jam.PhaseIdled {
		if err := o.res.Resume(ctx, inst.ActorID); err != nil {
			return fmt.Errorf("resume %s: %w", inst.ActorID, err)
		}
		if err := o.awaitStream(ctx, inst.ActorID); err != nil {
			return fmt.Errorf("%s %s: %w", c, inst.ActorID, err)
		}
	}
	var sent bool
	switch c {
	case jam.CmdInterrupt:
		sent = o.cmd.Interrupt(inst.ActorID, by)
	case jam.CmdCompact:
		sent = o.cmd.Compact(inst.ActorID, by, jam.CompactInstructions)
	case jam.CmdClear:
		sent = o.cmd.Clear(inst.ActorID, by)
	default:
		return fmt.Errorf("unknown session command %q", c)
	}
	if !sent {
		return fmt.Errorf("%s %s: %w", c, inst.ActorID, errNoStream)
	}
	if c == jam.CmdClear && o.lines != nil {
		// The clear went out; a failed reset only leaves a stale line, so it
		// must not fail the command.
		l := o.log
		if l == nil {
			l = slog.Default()
		}
		if err := o.lines.SetMission(inst.ActorID, ""); err != nil {
			l.Warn("clear: reset mission", "actor", inst.ActorID, "err", err.Error())
		}
		if err := o.lines.SetFocus(inst.ActorID, ""); err != nil {
			l.Warn("clear: reset focus", "actor", inst.ActorID, "err", err.Error())
		}
	}
	return nil
}

// awaitStream polls until actorID's stream is connected, connectWait passes
// or ctx ends.
func (o sessionOps) awaitStream(ctx context.Context, actorID string) error {
	wait, poll := o.connectWait, o.connectPoll
	if wait <= 0 {
		wait = defaultConnectWait
	}
	if poll <= 0 {
		poll = defaultConnectPoll
	}
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	tick := time.NewTicker(poll)
	defer tick.Stop()
	for !o.cmd.Connected(actorID) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errNoStream
		case <-tick.C:
		}
	}
	return nil
}
