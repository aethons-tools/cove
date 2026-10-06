package jam

import (
	"strings"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
)

// The /me read-model on the channel log (intercom slice 2b): a person's
// channels are the ones they are a member of or have deliveries in, that they
// may see; unread is their deliveries above their read cursor. The synthetic
// projection in channels.go now only serves the legacy log (the History).

// viewKind maps a channel kind onto the rail's kinds: a chat is a
// conversation with its people and sessions (DM), a ticket is a studio's
// conversation, a room a named channel.
func viewKind(k SourceKind) ChannelKind {
	switch k {
	case SourceTicket:
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
	reads := store.ChannelReads(u)
	unread := map[ident.ID]int{}
	for _, m := range lg.InboxSince(u, 0, 0) {
		if m.Seq > reads[m.Channel] {
			unread[m.Channel]++
		}
	}
	var out []ChannelView
	for id := range ids {
		if v, ok := userChannel(store, ic, lg, u, id); ok {
			v.Unread = unread[id]
			out = append(out, v)
		}
	}
	sortChannels(out)
	return out
}

// UserChannel is one channel as user u sees it (ok=false: gone, or theirs
// to see no longer). Unread is left zero.
func UserChannel(store Store, ic *Intercom, lg intercom.Store, u, id ident.ID) (ChannelView, bool) {
	return userChannel(store, ic, lg, u, id)
}

func userChannel(store Store, ic *Intercom, lg intercom.Store, u, id ident.ID) (ChannelView, bool) {
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
	for _, m := range store.ChannelMembers(ch.ID) {
		if m.ParticipantID == u {
			continue
		}
		if isSessionID(m.ParticipantID) {
			v.Sessions = append(v.Sessions, string(m.ParticipantID))
			if inst, ok := store.GetInstance(string(m.ParticipantID)); ok && instanceActive(inst) {
				if v.Phase == "" || isWaiting(inst) {
					v.Phase, v.Waiting = string(inst.Phase), v.Waiting || isWaiting(inst)
				}
			}
		}
		if ch.Kind == SourceChat {
			others = append(others, ic.PartyOf(m.ParticipantID).Label)
		}
	}
	if ch.Kind == SourceChat && len(others) > 0 {
		v.Label = strings.Join(others, ", ")
	}
	return v, true
}
