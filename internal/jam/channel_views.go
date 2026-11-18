package jam

import (
	"slices"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
)

// The /app/user read-model on the channel log (intercom slice 2b): a person's
// channels are the ones they are a member of or have deliveries in, that they
// may see; unread is their deliveries above their read cursor. The synthetic
// projection in channels.go now only serves the legacy log (the History).

// viewKind maps a channel kind onto the rail's kinds: a dm or a group is a
// conversation with its people and sessions (DM), a work log (or an archived
// session channel an older Jam made) is a studio's conversation, a room a
// named channel.
func viewKind(k SourceKind) ChannelKind {
	switch k {
	case SourceWork, SourceSession:
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

// UnreadTotal is user u's unread across their Me (InMe) that they may see:
// the sum of the rail's Me unread (the app badge, a push's count; the legacy
// History is always read).
func UnreadTotal(store Store, ic *Intercom, lg intercom.Store, u ident.ID) int {
	if lg == nil || u == "" {
		return 0
	}
	total := 0
	for id, n := range unreadByChannel(store, lg, u) {
		if ch, ok := store.GetChannel(id); ok && ic.CanSee(u, ch) && InMe(store, u, ch) {
			total += n
		}
	}
	return total
}

// InMe reports whether ch is in user u's Me, the rail's section of their
// live conversations (docs/modules/ROOT/pages/usage/jam/rail-api.adoc): u
// is a member of it and it hasn't finished (Finished), so a dm with a
// session leaves when the session ends, and a work log when its unit is done.
func InMe(store Store, u ident.ID, ch Channel) bool {
	if ch.Kind == SourceSession {
		return false
	}
	member := slices.ContainsFunc(store.ChannelMembers(ch.ID), func(m ChannelMember) bool { return m.ParticipantID == u })
	return member && Finished(store, u, ch) == ""
}

// Why a conversation finished (rail-api.adoc#lookup).
const (
	FinishedEnded    = "ended"    // a dm or group whose sessions ended, with no other person
	FinishedArchived = "archived" // an archived channel
	FinishedDone     = "done"     // the log of a unit that is done
)

// Finished says why ch is a finished conversation for user u, "" while it
// is live: it was archived; it is a dm or a group with no person or active
// session besides u; or it is the log of a unit whose state is done.
func Finished(store Store, u ident.ID, ch Channel) string {
	switch {
	case ch.Status != StatusLive:
		return FinishedArchived
	case IsChat(ch.Kind):
		for _, m := range store.ChannelMembers(ch.ID) {
			if m.ParticipantID == u {
				continue
			}
			inst, isSession := store.GetInstance(string(m.ParticipantID))
			if isSession && InstanceActive(inst) || !isSession && !isSessionID(m.ParticipantID) {
				return ""
			}
		}
		return FinishedEnded
	case ch.Kind == SourceWork:
		if unit, ok := store.WorkUnit(ch.ID); ok && unit.State == ReportDone {
			return FinishedDone
		}
	}
	return ""
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
	v := ChannelView{ID: string(ch.ID), Kind: viewKind(ch.Kind), Source: ch.Kind, Label: ch.Label}
	if e, ok := store.Resolve(ch.ProjectID); ok {
		v.Project = e.Name
	}
	if last := lg.ChannelBefore(ch.ID, 0, 1); len(last) == 1 {
		v.LastSeq = last[0].Seq
	}
	var others []string
	// The mission shown is the latest-raised active session's (SourceWork), or
	// a dm's or group's only active session's; rooms show none. A dm's active
	// agent stands for the row's status card (CardSession); work logs,
	// groups, and rooms have none: they show their name.
	var active int
	var latest time.Time
	var chatMission, chatSession string
	for _, m := range store.ChannelMembers(ch.ID) {
		if m.ParticipantID == u {
			continue
		}
		if isSessionID(m.ParticipantID) {
			v.Sessions = append(v.Sessions, string(m.ParticipantID))
			if inst, ok := store.GetInstance(string(m.ParticipantID)); ok && InstanceActive(inst) {
				if v.Phase == "" || NeedsPerson(inst) {
					v.Phase, v.NeedsYou = string(inst.Phase), v.NeedsYou || NeedsPerson(inst)
				}
				active++
				switch ch.Kind {
				case SourceWork:
					if active == 1 || inst.RaisedAt.After(latest) {
						v.Mission, latest = inst.Mission, inst.RaisedAt
					}
				case SourceDM, SourceGroup:
					chatMission, chatSession = inst.Mission, inst.ActorID
				}
			}
		}
		if IsChat(ch.Kind) {
			others = append(others, ic.PartyOf(m.ParticipantID).Label)
		}
	}
	for _, a := range openAsks {
		v.OpenAsks++
		if !v.NeedsYou && ic.MayAnswer(u, a) {
			v.NeedsYou = true
		}
	}
	if IsChat(ch.Kind) && active == 1 {
		v.Mission = chatMission
	}
	if ch.Kind == SourceDM && active == 1 {
		v.CardSession = chatSession
	}
	if IsChat(ch.Kind) && len(others) > 0 {
		v.Label = strings.Join(others, ", ")
	}
	return v, true
}
