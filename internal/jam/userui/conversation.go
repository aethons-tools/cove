package userui

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/squawkrender"
)

// fileChips are m's attachments; files-only reports whether m's text is
// just their names (the fallback body), which then isn't shown again.
func fileChips(m intercom.Squawk) (chips []FileChip, filesOnly bool) {
	var names []string
	for _, p := range m.Parts {
		if p.Ref == "" {
			continue
		}
		img := p.Type == "image/png" || p.Type == "image/jpeg" || p.Type == "image/gif" || p.Type == "image/webp"
		chips = append(chips, FileChip{ID: p.Ref, Name: p.Name, Size: humanSize(p.Size), Image: img})
		names = append(names, p.Name)
	}
	return chips, len(chips) > 0 && m.Body == strings.Join(names, ", ")
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

func messageRow(from string, m intercom.Squawk, mine bool) MessageRow {
	return MessageRow{
		From:  from,
		Body:  squawkrender.Body(m.ContentType, m.Body),
		Raw:   m.Body,
		Plain: squawkrender.IsPlain(m.ContentType),
		At:    m.At.Format("15:04"),
		Time:  m.At,
		Mine:  mine,
		Seq:   m.Seq,
	}
}

// shownRow is m as viewer p sees it: its sender's color (colors caches
// them per sender), and its ask, board, and file cards.
func shownRow(p jam.Participant, d Deps, m intercom.Squawk, colors map[ident.ID]string) MessageRow {
	row := messageRow(d.Intercom.PartyOf(m.From).Label, m, m.From == p.UserID)
	c, ok := colors[m.From]
	if !ok {
		c, _ = d.Store.SessionColor(string(m.From)) // a person has none
		colors[m.From] = c
	}
	row.Color = c
	decorateAsk(&row, p, d, m)
	if card := boardCard(d, m); card != nil {
		row.Board = card
	}
	switch {
	case row.Board != nil:
		row.Card = "board"
	case row.Ask != nil:
		row.Card = "ask"
	}
	if chips, filesOnly := fileChips(m); len(chips) > 0 {
		row.Files = chips
		if filesOnly {
			row.Body, row.Raw = "", ""
		}
	}
	return row
}

// conversation builds the open-channel view for channelID, or ok=false if the
// participant may not see it. A History id opens the legacy conversation,
// read-only.
func conversation(p jam.Participant, d Deps, channelID string) (Conversation, bool) {
	if strings.HasPrefix(channelID, legacyPrefix) {
		return legacyConversation(p, d, channelID)
	}
	if d.Log == nil {
		return Conversation{}, false
	}
	v, ok := jam.UserChannel(d.Store, d.Intercom, d.Log, p.UserID, ident.ID(channelID))
	if !ok {
		return Conversation{}, false
	}
	conv := Conversation{
		ChannelID: channelID, Label: v.Label, Kind: viewKind(v), Project: v.Project, Phase: v.Phase, NeedsYou: v.NeedsYou,
		SendTo: channelID, LastSeq: v.LastSeq, SessionIDs: v.Sessions,
	}
	if ch, ok := d.Store.GetChannel(ident.ID(channelID)); ok {
		// A finished conversation reads as before, with no composer.
		if conv.Finished = jam.Finished(d.Store, p.UserID, ch); conv.Finished != "" {
			conv.SendTo = ""
		}
	}
	colors := map[ident.ID]string{}
	for _, m := range d.Log.ChannelSince(ident.ID(channelID), 0, 0) {
		conv.Messages = append(conv.Messages, shownRow(p, d, m, colors))
	}
	conv.OpenAsks = openAskCards(p, d, ident.ID(channelID))
	membershipControls(&conv, p, d)
	return conv, true
}

// membershipControls fills the conversation's Join/Leave/Call in controls:
// a dm or a group has none (its members are fixed). Rooms and work logs
// take Join and Leave; Call in lists the project's people and the agents the
// viewer may reach. A room also lists its members.
func membershipControls(conv *Conversation, p jam.Participant, d Deps) {
	ch, ok := d.Store.GetChannel(ident.ID(conv.ChannelID))
	if !ok || ch.Status != jam.StatusLive || jam.IsChat(ch.Kind) {
		return
	}
	in := map[ident.ID]bool{}
	for _, m := range d.Store.ChannelMembers(ch.ID) {
		if !m.Left {
			in[m.ParticipantID] = true
		}
	}
	if ch.Kind == jam.SourceRoom {
		conv.Members = roomMembers(d, ch, in)
	}
	if !in[p.UserID] {
		conv.CanJoin = true
		return
	}
	conv.CanLeave = true
	for _, uid := range d.Store.ListMembers(ch.ProjectID) {
		if u, ok := d.Store.GetUser(uid); ok && u.Status == jam.StatusLive && !in[uid] {
			conv.CallIn = append(conv.CallIn, NewMessageOption{To: "user:" + string(uid), Label: u.Name, Kind: "person"})
		}
	}
	for _, inst := range d.Store.ListInstances() {
		if inst.Phase == jam.PhaseGone || in[ident.ID(inst.ActorID)] {
			continue
		}
		if !d.Intercom.MayReach(p.UserID, inst) {
			continue // someone else's personal session isn't theirs to call in
		}
		if pr, ok := d.Store.GetProject(inst.Project); ok && pr.ID == ch.ProjectID {
			conv.CallIn = append(conv.CallIn, NewMessageOption{To: "session:" + inst.ActorID, Label: d.Intercom.PartyOf(ident.ID(inst.ActorID)).Label, Kind: "agent"})
		}
	}
}

// roomMembers is room ch's member list, people then agents, each by label:
// a person while they are a live user, a session while it lives.
func roomMembers(d Deps, ch jam.Channel, in map[ident.ID]bool) []MemberRow {
	out := []MemberRow{}
	for id := range in {
		switch {
		case id.Kind() == ident.User:
			if u, ok := d.Store.GetUser(id); ok && u.Status == jam.StatusLive {
				out = append(out, MemberRow{ID: string(id), Label: u.Name, Kind: "person"})
			}
		default:
			if inst, ok := d.Store.GetInstance(string(id)); ok && inst.Phase != jam.PhaseGone {
				out = append(out, MemberRow{ID: string(id), Label: d.Intercom.PartyOf(id).Label, Kind: "agent"})
			}
		}
	}
	slices.SortFunc(out, func(a, b MemberRow) int {
		return cmp.Or(cmp.Compare(b.Kind, a.Kind), cmp.Compare(a.Label, b.Label), cmp.Compare(a.ID, b.ID)) // person before agent
	})
	return out
}

// legacyConversation is a History conversation: the legacy projection's
// messages, read-only.
func legacyConversation(p jam.Participant, d Deps, channelID string) (Conversation, bool) {
	var meta *jam.ChannelView
	for _, ch := range legacyChannels(p, d) {
		if ch.ID == channelID {
			c := ch
			meta = &c
			break
		}
	}
	if meta == nil {
		return Conversation{}, false
	}
	mine := map[string]bool{}
	for _, name := range legacyNames(p) {
		mine[intercom.Target{Kind: "human", Ref: name}.String()] = true
	}
	conv := Conversation{ChannelID: channelID, Label: meta.Label, Kind: viewKind(*meta), Project: meta.Project, LastSeq: meta.LastSeq}
	for _, m := range jam.ChannelSquawks(strings.TrimPrefix(channelID, legacyPrefix), d.Legacy, d.Store.ListInstances()) {
		row := messageRow(m.From.Ref, intercom.Squawk{Seq: m.Seq, Body: m.Body, At: m.At, ContentType: m.ContentType}, mine[m.From.String()])
		conv.Messages = append(conv.Messages, row)
	}
	return conv, true
}

// newMessageOptions lists who the participant can start a conversation
// with, across their projects: its other members (a dm, or several for a
// group), the live agents they may reach (in a dm), its units of work (their
// log, and the dm with the worker: workerTo), and its rooms.
func newMessageOptions(p jam.Participant, d Deps) []NewMessageOption {
	seen := map[string]bool{}
	var out []NewMessageOption
	add := func(o NewMessageOption) {
		if !seen[o.To] {
			seen[o.To] = true
			out = append(out, o)
		}
	}
	instances := d.Store.ListInstances()
	for _, name := range p.Projects {
		proj, ok := d.Store.GetProject(name)
		if !ok {
			continue
		}
		for _, uid := range d.Store.ListMembers(proj.ID) {
			if uid == p.UserID {
				continue
			}
			if u, ok := d.Store.GetUser(uid); ok && u.Status == jam.StatusLive {
				add(NewMessageOption{To: "user:" + string(uid), Label: u.Name, Kind: "person", Project: name})
			}
		}
		for _, inst := range instances {
			if !jam.SameProject(d.Store, inst.Project, name) || (inst.Phase != jam.PhaseLive && inst.Phase != jam.PhaseRaising && inst.Phase != jam.PhaseIdled) {
				continue
			}
			if d.Intercom.MayReach(p.UserID, inst) {
				add(NewMessageOption{To: "session:" + inst.ActorID, Label: d.Intercom.PartyOf(ident.ID(inst.ActorID)).Label, Kind: "agent", Project: name})
			}
		}
		for _, ch := range d.Store.ListChannels(proj.ID, jam.SourceWork) {
			add(NewMessageOption{To: string(ch.ID), Worker: workerTo(p, d, ch), Label: ch.Label, Kind: "work", Project: name})
		}
		for _, ch := range d.Store.ListChannels(proj.ID, jam.SourceRoom) {
			add(NewMessageOption{To: string(ch.ID), Label: ch.Label, Kind: "room", Project: name})
		}
	}
	return out
}

// workerTo is the worker:<key> target of work log ch for p: the dm with the
// live session working on it, when p may reach it ("" = none, or a log on a
// connection other than the tracker, which worker: can't name).
func workerTo(p jam.Participant, d Deps, ch jam.Channel) string {
	tracker, ok := d.Intercom.Tracker()
	conn, key, _ := strings.Cut(ch.Key, "/")
	if !ok || conn != string(tracker) || ch.Worker == "" {
		return ""
	}
	inst, ok := d.Store.GetInstance(string(ch.Worker))
	if !ok || !d.Intercom.MayReach(p.UserID, inst) {
		return ""
	}
	return "worker:" + key
}
