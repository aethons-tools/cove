package meui

import (
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/sessionevents"
)

// Presence is a source of derived session statuses — *sessionevents.Presence.
// Status is a session's latest status from its event stream; Subscribe
// returns a coalescing channel signalled when any status changes.
type Presence interface {
	Status(actorID string) (sessionevents.Status, bool)
	Subscribe() (<-chan struct{}, func())
}

// WithPresence shows each session in a conversation with its live status (the
// strip under the messages) and pushes a payload-free `presence` event on
// /me/events when one changes. Only the status and a tool name ever reach the
// page — never event content. Without it, a live session reads "working".
func WithPresence(p Presence) Option {
	return func(h *handler) { h.presence = p }
}

// SessionRow is one session's line in the presence strip: "<Label> is <Text>
// <Tool>". Class is busy (animated dots), wait (needs a human), or dim.
type SessionRow struct {
	Label, Text, Tool, Class string
}

// sessionRows builds the strip for a conversation's session actors, in order.
// A session with no Instance, or one that is ending or gone, is left out.
func sessionRows(actors []string, instances []jam.Instance, pr Presence) []SessionRow {
	byActor := map[string]jam.Instance{}
	for _, i := range instances {
		byActor[i.ActorID] = i
	}
	var rows []SessionRow
	for _, a := range actors {
		inst, ok := byActor[a]
		if !ok {
			continue
		}
		var st sessionevents.Status
		var known bool
		if pr != nil {
			st, known = pr.Status(a)
		}
		row, show := sessionRow(inst, st, known)
		if show {
			rows = append(rows, row)
		}
	}
	return rows
}

// sessionRow derives one session's line. The Instance's lifecycle wins (a
// session raising, paused, idle, asking for a person, or blocked says so whatever its last
// event was); a running session shows its latest event-derived status.
func sessionRow(inst jam.Instance, st sessionevents.Status, known bool) (SessionRow, bool) {
	label := inst.Name // as the New message picker labels sessions
	if label == "" {
		label = inst.ActorID
	}
	row := func(text, class string) (SessionRow, bool) {
		return SessionRow{Label: label, Text: text, Class: class}, true
	}
	switch inst.Phase {
	case jam.PhaseRaising:
		return row("is starting", "busy")
	case jam.PhaseIdled:
		return row("is paused", "dim")
	case jam.PhaseLive:
	default:
		return SessionRow{}, false // terminating, lost, gone
	}
	switch inst.Activity {
	case jam.ActivityWaiting:
		if jam.AskedForPerson(inst) {
			return row("needs you", "wait")
		}
		return row("is idle", "dim")
	case jam.ActivityHolding:
		return row("is working in the background", "busy")
	case jam.ActivityBlocked:
		return row("is blocked", "wait")
	case jam.ActivityDone:
		return row("is done", "dim")
	}
	if !known {
		return row("is working", "busy")
	}
	switch st.State {
	case sessionevents.StatusRunning:
		return SessionRow{Label: label, Text: "is running", Tool: st.Tool, Class: "busy"}, true
	case sessionevents.StatusWriting:
		return row("is writing", "busy")
	case sessionevents.StatusIdle:
		return row("is idle", "dim")
	default:
		return row("is thinking", "busy")
	}
}
