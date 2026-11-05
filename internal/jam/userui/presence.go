package userui

import (
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/sessionevents"
)

// Presence is a source of derived session statuses — *sessionevents.Presence.
// Status is a session's latest status from its event stream; Inits counts its
// system/init events; Subagents is how many subagents it is running now;
// Subscribe returns a coalescing channel signalled when any of them changes.
type Presence interface {
	Status(actorID string) (sessionevents.Status, bool)
	Inits(actorID string) int
	Subagents(actorID string) int
	Subscribe() (<-chan struct{}, func())
}

// WithPresence shows each session in a conversation with its live status (the
// strip under the messages) and pushes a payload-free `presence` event on
// /app/user/events when one changes. Only the status and a tool name ever reach the
// page — never event content. Without it, a live session reads "working".
func WithPresence(p Presence) Option {
	return func(h *handler) { h.presence = p }
}

// sessionRows builds the strip for a conversation's session actors, in order.
// A session with no Instance, or one that is ending or gone, is left out.
func sessionRows(actors []string, instances []jam.Instance, pr Presence, kits jam.KitActivityFunc) []SessionRow {
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
		prep, _ := preparingKit(inst, kits)
		row, show := sessionRow(inst, st, known, prep)
		if show {
			rows = append(rows, row)
		}
	}
	return rows
}

// preparingKit is the prepare-kit activity holding up inst's raise: inst
// is raising or recovering and a connected manager (its own first) is
// building its kit (jam.FindKitActivity). Nil without a lookup.
func preparingKit(inst jam.Instance, kits jam.KitActivityFunc) (*jam.ManagerActivity, bool) {
	if kits == nil || inst.Kit.ID == "" || (inst.Phase != jam.PhaseRaising && inst.Phase != jam.PhaseRecovering) {
		return nil, false
	}
	a, ok := kits(inst.Kit, inst.Manager)
	if !ok {
		return nil, false
	}
	return &a, true
}

// sessionRow derives one session's line. The Instance's lifecycle wins (a
// session raising, paused, idle, asking for a person, or blocked says so whatever its last
// event was); a running session shows its latest event-derived status. prep,
// when set, is the kit build holding up a raising or recovering session.
func sessionRow(inst jam.Instance, st sessionevents.Status, known bool, prep *jam.ManagerActivity) (SessionRow, bool) {
	label := inst.Name // as the New message picker labels sessions
	if label == "" {
		label = inst.ActorID
	}
	row := func(text, class string) (SessionRow, bool) {
		return SessionRow{Label: label, Text: text, Class: class}, true
	}
	if jam.NeedsPerson(inst) && inst.Activity != jam.ActivityBlocked {
		return row("needs you", "wait") // asked, even once paused
	}
	if prep != nil && (inst.Phase == jam.PhaseRaising || inst.Phase == jam.PhaseRecovering) {
		text := "is preparing its kit image"
		if p := prep.Progress(); p != "" {
			text += " — " + p
		}
		return row(text, "busy")
	}
	switch inst.Phase {
	case jam.PhaseRaising:
		return row("is starting", "busy")
	case jam.PhaseRecovering:
		return row("is recovering", "busy")
	case jam.PhaseIdled:
		return row("is paused", "dim")
	case jam.PhaseLive:
	default:
		return SessionRow{}, false // terminating, lost, gone
	}
	switch inst.Activity {
	case jam.ActivityWaiting:
		return row("is idle", "dim") // asking was handled above
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
