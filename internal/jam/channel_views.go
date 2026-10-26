package jam

import (
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
)

// The /app/user read-model on the channel log (intercom slice 2b): a person's
// channels are the ones they are a member of or have deliveries in, that they
// may see; unread is their deliveries above their read cursor. The synthetic
// projection in channels.go now only serves the legacy log (the History).

// viewKind maps a channel kind onto the rail's kinds: a chat is a
// conversation with its people and sessions (DM), a ticket or a session's
// own channel is a studio's conversation, a room a named channel.
func viewKind(k SourceKind) ChannelKind {
	switch k {
	case SourceTicket, SourceSession:
		return ChannelStudio
	case SourceRoom:
		return ChannelNamed
	}
	return ChannelDM
}

// UserChannels lists the channels user u takes part in: members of, or with
// deliveries to them, that they may see — sorted for the attention rail.
func UserChannels(store Store, ic *Intercom, lg intercom.Store, u ident.ID) []ChannelView {
	if lg == nil || u == "" {
		return nil
	}
	ids := map[ident.ID]bool{}
	for _, ch := range store.ChannelsOf(u) {
		ids[ch] = true
	}
	for _, st := range lg.InboxChannels(u) {
		ids[st.Channel] = true
	}
	unread := unreadByChannel(store, lg, u)
	// Every open ask, read once for the whole rail (not per channel).
	open := map[ident.ID][]intercom.Ask{}
	for _, a := range lg.ListAsks(intercom.AskFilter{State: intercom.AskOpen}) {
		open[a.Channel] = append(open[a.Channel], a)
	}
	var out []ChannelView
	for id := range ids {
		if v, ok := userChannel(store, ic, lg, u, id, open[id]); ok {
			v.Unread = unread[id]
			out = append(out, v)
		}
	}
	sortChannels(out)
	return out
}

// unreadByChannel counts user u's deliveries above their read cursor, per
// channel (seen or not: callers keep only the channels u may see).
func unreadByChannel(store Store, lg intercom.Store, u ident.ID) map[ident.ID]int {
	reads := store.ChannelReads(u)
	unread := map[ident.ID]int{}
	for _, m := range lg.InboxSince(u, 0, 0) {
		if m.Seq > reads[m.Channel] {
			unread[m.Channel]++
		}
	}
	return unread
}

// UnreadTotal is user u's unread across the channels they may see: the sum
// of UserChannels' per-channel Unread (the app badge, a push's count).
func UnreadTotal(store Store, ic *Intercom, lg intercom.Store, u ident.ID) int {
	if lg == nil || u == "" {
		return 0
	}
	total := 0
	for id, n := range unreadByChannel(store, lg, u) {
		if ch, ok := store.GetChannel(id); ok && ic.CanSee(u, ch) {
			total += n
		}
	}
	return total
}

// UserChannel is one channel as user u sees it (ok=false: gone, or theirs
// to see no longer). Unread is left zero.
func UserChannel(store Store, ic *Intercom, lg intercom.Store, u, id ident.ID) (ChannelView, bool) {
	return userChannel(store, ic, lg, u, id, ic.OpenAsksIn(id))
}

// userChannel is channel id as u sees it; openAsks are its open asks.
func userChannel(store Store, ic *Intercom, lg intercom.Store, u, id ident.ID, openAsks []intercom.Ask) (ChannelView, bool) {
	ch, ok := store.GetChannel(id)
	if !ok || !ic.CanSee(u, ch) {
		return ChannelView{}, false
	}
	v := ChannelView{ID: string(ch.ID), Kind: viewKind(ch.Kind), Label: ch.Label}
	if e, ok := store.Resolve(ch.ProjectID); ok {
		v.Project = e.Name
	}
	if last := lg.ChannelBefore(ch.ID, 0, 1); len(last) == 1 {
		v.LastSeq = last[0].Seq
	}
	var others []string
	// The mission shown is the channel's own session's (SourceSession), the
	// latest-raised active session's (SourceTicket), or a chat's only active
	// session's; rooms show none.
	var active int
	var latest time.Time
	var chatMission string
	for _, m := range store.ChannelMembers(ch.ID) {
		if m.ParticipantID == u {
			continue
		}
		if isSessionID(m.ParticipantID) {
			v.Sessions = append(v.Sessions, string(m.ParticipantID))
			if inst, ok := store.GetInstance(string(m.ParticipantID)); ok && instanceActive(inst) {
				if v.Phase == "" || NeedsPerson(inst) {
					v.Phase, v.NeedsYou = string(inst.Phase), v.NeedsYou || NeedsPerson(inst)
				}
				active++
				switch ch.Kind {
				case SourceSession:
					if inst.ActorID == ch.Key {
						v.Mission = inst.Mission
					}
				case SourceTicket:
					if active == 1 || inst.RaisedAt.After(latest) {
						v.Mission, latest = inst.Mission, inst.RaisedAt
					}
				case SourceChat:
					chatMission = inst.Mission
				}
			}
		}
		if ch.Kind == SourceChat {
			others = append(others, ic.PartyOf(m.ParticipantID).Label)
		}
	}
	for _, a := range openAsks {
		v.OpenAsks++
		if !v.NeedsYou && ic.MayAnswer(u, a) {
			v.NeedsYou = true
		}
	}
	if ch.Kind == SourceChat && active == 1 {
		v.Mission = chatMission
	}
	if ch.Kind == SourceChat && len(others) > 0 {
		v.Label = strings.Join(others, ", ")
	}
	return v, true
}
