package userui

import (
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/sessionevents"
)

// The session status card (docs/superpowers/specs/2026-10-30-session-status-card-design.md):
// one compact summary of a session, the rail row of a session channel and the
// extension's Session Status pane.

// cardOf is inst's status card from its latest event-derived status (st,
// known), its init count, and its totals. Like sessionRow, the Instance's
// lifecycle wins over the event stream. ok=false: the session is ending or
// gone, and has no card.
func cardOf(inst jam.Instance, st sessionevents.Status, known bool, inits int, t sessionevents.Totals) (SessionCard, bool) {
	c := SessionCard{Session: inst.ActorID, Name: inst.Name, Mission: inst.Mission, Focus: inst.Focus, Inits: inits,
		Counts: CardCounts{Asks: inst.OpenAsks, Alarms: len(inst.Alarms), Previews: len(inst.Exposures), Processes: inst.KeptProcesses},
		// Context is the main thread's, as the header's ctx figure.
		Context: CardContext{Used: t.Context, Window: t.ContextWindow}}
	if c.Name == "" {
		c.Name = inst.ActorID
	}
	set := func(s CardState, pill string) (SessionCard, bool) {
		c.State, c.Pill = s, pill
		return c, true
	}
	switch inst.Phase {
	case jam.PhaseRaising:
		return set(CardThinking, "starting")
	case jam.PhaseIdled:
		if jam.NeedsPerson(inst) && inst.Activity != jam.ActivityBlocked {
			return set(CardSleeping, "needs you")
		}
		return set(CardSleeping, "paused")
	case jam.PhaseLive:
	default:
		return SessionCard{}, false // terminating, lost, gone
	}
	if jam.NeedsPerson(inst) && inst.Activity != jam.ActivityBlocked {
		return set(CardIdle, "needs you")
	}
	switch inst.Activity {
	case jam.ActivityBlocked:
		return set(CardIdle, "blocked")
	case jam.ActivityHolding:
		return set(CardWaiting, "waiting")
	case jam.ActivityWaiting:
		if inst.KeptProcesses > 0 || len(inst.Alarms) > 0 {
			return set(CardWaiting, "waiting")
		}
		return set(CardIdle, "idle")
	case jam.ActivityDone:
		return set(CardIdle, "done")
	}
	if known && st.State == sessionevents.StatusRunning {
		c.Tool = st.Tool
		return set(CardWorking, "working")
	}
	return set(CardThinking, "thinking")
}

// card is inst's status card, from the handler's presence (status, inits,
// subagents) and totals.
func (h *handler) card(inst jam.Instance) (SessionCard, bool) {
	var st sessionevents.Status
	var known bool
	var inits, subagents int
	if h.presence != nil {
		st, known = h.presence.Status(inst.ActorID)
		inits = h.presence.Inits(inst.ActorID)
		subagents = h.presence.Subagents(inst.ActorID)
	}
	var t sessionevents.Totals
	if h.d.Totals != nil {
		t, _ = h.d.Totals.Of(inst.ActorID) // no totals yet: no context bar
	}
	c, ok := cardOf(inst, st, known, inits, t)
	if ok {
		c.Counts.Subagents = subagents
	}
	return c, ok
}

// attachCards gives each rail row backed by a session (ChannelRow.cardSession)
// that session's card.
func (h *handler) attachCards(groups []RailGroup) {
	for gi := range groups {
		for ri := range groups[gi].Rows {
			r := &groups[gi].Rows[ri]
			if r.cardSession == "" {
				continue
			}
			if inst, ok := h.d.Store.GetInstance(r.cardSession); ok {
				if c, ok := h.card(inst); ok {
					r.Card = &c
				}
			}
		}
	}
}
