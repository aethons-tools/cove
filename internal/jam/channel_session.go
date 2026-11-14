package jam

import (
	"github.com/aethons-tools/cove/internal/ident"
)

// ChannelSession is the live session that channel ch is the conversation
// of: a work log's current worker, or the one session of a dm (ok=false:
// none, a dm of two sessions, a group, or a room).
func ChannelSession(st Store, ch ident.ID) (Instance, bool) {
	c, ok := st.GetChannel(ch)
	if !ok {
		return Instance{}, false
	}
	live := func(id ident.ID) (Instance, bool) {
		inst, ok := st.GetInstance(string(id))
		return inst, ok && inst.Phase != PhaseGone
	}
	switch c.Kind {
	case SourceWork:
		if c.Worker != "" {
			return live(c.Worker)
		}
	case SourceDM:
		var sessions []ident.ID
		for _, m := range st.ChannelMembers(c.ID) {
			if isSessionID(m.ParticipantID) {
				sessions = append(sessions, m.ParticipantID)
			}
		}
		if len(sessions) == 1 {
			return live(sessions[0])
		}
	}
	return Instance{}, false
}

// SessionConversation is session inst's conversation for person viewer,
// without creating one: its unit's work log, else the viewer's dm with it
// (ok=false: neither exists).
func (ic *Intercom) SessionConversation(inst Instance, viewer ident.ID) (Channel, bool) {
	if ch, ok := ic.TicketChannelOf(inst); ok {
		return ch, true
	}
	p, ok := ic.store.GetProject(orDefaultProject(inst.Project))
	if !ok {
		return Channel{}, false
	}
	return ic.existingChat(p, []ident.ID{viewer, ident.ID(inst.ActorID)})
}
