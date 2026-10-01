package meui

import (
	"html/template"

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
	SendTo      string // the ref the composer POSTs to /me/send (the channel id)
	Messages    []MessageRow
	HasMessages bool
	LastSeq     int64
}

// selfRefs returns the set of the participant's own Target strings across all
// their projects (human:<name>), used to mark a message as the viewer's own.
func selfRefs(p jam.Participant, store Store) map[string]bool {
	refs := map[string]bool{}
	for _, proj := range p.Projects {
		roster, ok := store.GetRoster(proj)
		if !ok {
			continue
		}
		if h, ok := roster.HumanByIdentity(p.Issuer, p.Subject); ok && h.Name != "" {
			refs[intercom.Target{Kind: "human", Ref: h.Name}.String()] = true
		}
	}
	return refs
}

// conversation builds the open-channel view for channelID, or ok=false if the
// participant is not a member of that channel.
func conversation(p jam.Participant, store Store, log jam.LogReader, channelID string) (Conversation, bool) {
	var meta *jam.ChannelView
	for _, ch := range channelsFor(p, store, log) {
		if ch.ID == channelID {
			c := ch
			meta = &c
			break
		}
	}
	if meta == nil {
		return Conversation{}, false
	}
	mine := selfRefs(p, store)
	conv := Conversation{
		ChannelID: channelID, Label: meta.Label, Kind: meta.Kind,
		Project: meta.Project, Phase: meta.Phase, Waiting: meta.Waiting,
		SendTo: channelID, LastSeq: meta.LastSeq,
	}
	for _, m := range jam.ChannelSquawks(channelID, log, store.ListInstances()) {
		conv.Messages = append(conv.Messages, MessageRow{
			From:  fromLabel(m.From),
			Body:  squawkrender.Body(m.ContentType, m.Body),
			Raw:   m.Body,
			Plain: squawkrender.IsPlain(m.ContentType),
			At:    m.At.Format("15:04"),
			Mine:  mine[m.From.String()],
			Seq:   m.Seq,
		})
	}
	conv.HasMessages = len(conv.Messages) > 0
	return conv, true
}

func fromLabel(t intercom.Target) string {
	if t.Ref != "" {
		return t.Ref
	}
	return t.String()
}

// selfRefForProject returns the participant's own Target string in project, or
// "" if they aren't bound (or are unnamed) there.
func selfRefForProject(p jam.Participant, store Store, project string) string {
	roster, ok := store.GetRoster(project)
	if !ok {
		return ""
	}
	h, ok := roster.HumanByIdentity(p.Issuer, p.Subject)
	if !ok || h.Name == "" {
		return ""
	}
	return intercom.Target{Kind: "human", Ref: h.Name}.String()
}

// NewMessageOption is one active recipient offered in the New Message picker.
type NewMessageOption struct {
	To      string // the Target string to POST to /me/send
	Label   string
	Kind    string
	Project string
	Waiting bool
}

// newMessageOptions lists the active recipients the participant can start a
// conversation with, across all their projects, de-duplicated by target.
func newMessageOptions(p jam.Participant, store Store) []NewMessageOption {
	seen := map[string]bool{}
	var out []NewMessageOption
	instances := store.ListInstances()
	for _, proj := range p.Projects {
		roster, ok := store.GetRoster(proj)
		if !ok {
			continue
		}
		var insts []jam.Instance
		for _, i := range instances {
			if i.Project == proj {
				insts = append(insts, i)
			}
		}
		for _, r := range jam.ActiveRecipients(roster, insts) {
			key := r.Target.String()
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, NewMessageOption{
				To: key, Label: r.Label, Kind: r.Kind, Project: r.Project, Waiting: r.Waiting,
			})
		}
	}
	return out
}
