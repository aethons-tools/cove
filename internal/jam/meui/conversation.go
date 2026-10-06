package meui

import (
	"html/template"
	"strings"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/squawkrender"
)

// MessageRow is one squawk in the conversation pane, from the viewer's side.
type MessageRow struct {
	From string
	Body template.HTML // rendered per the squawk's content type (squawkrender)
	Raw  string        // the body as sent, for the Raw view (escaped by the template)
	// Plain marks a text/plain squawk, shown literally with its whitespace.
	Plain bool
	At    string
	Mine  bool
	Seq   int64
}

// Conversation is the right-pane view of one open channel.
type Conversation struct {
	ChannelID   string
	Label       string
	Kind        jam.ChannelKind
	Project     string
	Phase       string
	Waiting     bool
	SendTo      string // what the composer POSTs to /me/send ("" = read-only History)
	Messages    []MessageRow
	HasMessages bool
	LastSeq     int64
	// SessionIDs are the sessions taking part; Sessions is their presence
	// strip (filled by the handler, which holds the Presence source).
	SessionIDs []string
	Sessions   []SessionRow
}

func messageRow(from string, m intercom.Squawk, mine bool) MessageRow {
	return MessageRow{
		From:  from,
		Body:  squawkrender.Body(m.ContentType, m.Body),
		Raw:   m.Body,
		Plain: squawkrender.IsPlain(m.ContentType),
		At:    m.At.Format("15:04"),
		Mine:  mine,
		Seq:   m.Seq,
	}
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
		ChannelID: channelID, Label: v.Label, Kind: v.Kind, Project: v.Project, Phase: v.Phase, Waiting: v.Waiting,
		SendTo: channelID, LastSeq: v.LastSeq, SessionIDs: v.Sessions,
	}
	for _, m := range d.Log.ChannelSince(ident.ID(channelID), 0, 0) {
		conv.Messages = append(conv.Messages, messageRow(d.Intercom.PartyOf(m.From).Label, m, m.From == p.UserID))
	}
	conv.HasMessages = len(conv.Messages) > 0
	return conv, true
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
	for _, proj := range p.Projects {
		if roster, ok := d.Store.GetRoster(proj); ok {
			if h, ok := roster.HumanByIdentity(p.Issuer, p.Subject); ok && h.Name != "" {
				mine[intercom.Target{Kind: "human", Ref: h.Name}.String()] = true
			}
		}
	}
	conv := Conversation{ChannelID: channelID, Label: meta.Label, Kind: meta.Kind, Project: meta.Project, LastSeq: meta.LastSeq}
	for _, m := range jam.ChannelSquawks(strings.TrimPrefix(channelID, legacyPrefix), d.Legacy, d.Store.ListInstances()) {
		row := messageRow(m.From.Ref, intercom.Squawk{Seq: m.Seq, Body: m.Body, At: m.At, ContentType: m.ContentType}, mine[m.From.String()])
		conv.Messages = append(conv.Messages, row)
	}
	conv.HasMessages = len(conv.Messages) > 0
	return conv, true
}

// NewMessageOption is one recipient offered in the New Message picker.
type NewMessageOption struct {
	To      string // what to POST to /me/send: user:<id>, session:<id>, or a channel id
	Label   string
	Kind    string
	Project string
	Waiting bool
}

// newMessageOptions lists who the participant can start a conversation
// with, across their projects: its other members and live sessions (a chat),
// the live sessions' tickets, and its rooms.
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
			if inst.Project != name || (inst.Phase != jam.PhaseLive && inst.Phase != jam.PhaseRaising && inst.Phase != jam.PhaseIdled) {
				continue
			}
			waiting := inst.Activity == jam.ActivityWaiting || inst.Phase == jam.PhaseIdled
			label := d.Intercom.PartyOf(ident.ID(inst.ActorID)).Label
			add(NewMessageOption{To: "session:" + inst.ActorID, Label: label, Kind: "session", Project: name, Waiting: waiting})
			if inst.Unit != "" {
				if ch, ok := d.Intercom.TicketChannelOf(inst); ok {
					add(NewMessageOption{To: string(ch.ID), Label: ch.Label, Kind: "ticket", Project: name, Waiting: waiting})
				}
			}
		}
		for _, ch := range d.Store.ListChannels(proj.ID, jam.SourceRoom) {
			add(NewMessageOption{To: string(ch.ID), Label: ch.Label, Kind: "room", Project: name})
		}
	}
	return out
}
