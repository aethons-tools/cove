package jam

import (
	"sort"
	"strings"

	"github.com/aethons-tools/cove/internal/intercom"
)

// This file is the hermetic core of the participant intercom UI (COV-196
// slice 1): a PURE projection of the squawk Log + roster + live Instance state
// into the channels a participant is a member of, an active-recipients
// directory for the New Message picker, and the attention grouping. It has no
// HTTP, auth, or VM dependency; separate slices consume it. The per-(participant,
// channel) unread cursor it reads is persisted by the Store (see CommitUnread).

// ChannelKind classifies a projected conversation.
//
//   - DM     — a private channel between two participants: {participant, session}
//     (a session ref, "actor:<id>") or {participant, human} ("human:<name>").
//   - Studio — a studio's own conversation, keyed by its Instance.Unit
//     ("channel:<unit>"); the studio's session is a member. This is the agent's
//     default target (its send defaults to its own ticket).
//   - Named  — a roster-declared group channel ("channel:<name>").
//
// A session ref ("actor:<id>", a DM to a specific running agent) is deliberately
// distinct from a studio ref ("channel:<unit>", the studio channel): the Log
// to[] carries both kinds, and this is how the read-model tells them apart.
type ChannelKind string

const (
	ChannelDM     ChannelKind = "dm"
	ChannelStudio ChannelKind = "studio"
	ChannelNamed  ChannelKind = "named"
)

// AttentionBucket is the default rail grouping: waiting → active → channels.
type AttentionBucket string

const (
	BucketWaiting  AttentionBucket = "waiting"  // a studio waiting/idled on a human
	BucketActive   AttentionBucket = "active"   // a live studio/session, not waiting
	BucketChannels AttentionBucket = "channels" // everything else (named channels, human DMs)
)

// Channel is one projected conversation a participant is a member of, tagged so
// a caller can group by attention (Bucket) OR by Project from the same data.
// Phase/Waiting derive from the backing session's live Instance the same way
// /ui/coves reads it; Unread/LastSeq derive from the Log and the caller's
// cursor.
type ChannelView struct {
	ID      string // stable, deterministic channel id
	Kind    ChannelKind
	Label   string // display label from the viewer's perspective
	Project string // the studio/session's project, else the squawk's project
	Phase   string // backing session Instance.Phase; "" when no session backs it
	Waiting bool   // backing session is waiting/idled (the attention signal)
	Unread  int    // messages on this channel with Seq > cursor, not authored by the viewer
	LastSeq int64  // highest append Seq seen on this channel
	// Sessions are the actor ids of the sessions taking part (sent or were
	// addressed here, or back it), sorted.
	Sessions []string
}

// Bucket places the channel in the attention-ordered rail. Waiting wins; a
// live/raising/idled session-backed channel is Active; everything else is a
// plain Channel.
func (c ChannelView) Bucket() AttentionBucket {
	switch {
	case c.Waiting:
		return BucketWaiting
	case activePhase(c.Phase) && (c.Kind == ChannelStudio || c.Kind == ChannelDM):
		return BucketActive
	default:
		return BucketChannels
	}
}

// StudioChannelID derives a studio channel's id from its Instance.Unit.
func StudioChannelID(unit string) string { return "studio:" + unit }

// NamedChannelID derives a named channel's id from its roster name.
func NamedChannelID(name string) string { return "named:" + name }

// DMChannelID derives a DM channel's id deterministically from its two
// endpoints, independent of direction (the two Target strings, sorted).
func DMChannelID(a, b intercom.Target) string {
	x, y := a.String(), b.String()
	if x > y {
		x, y = y, x
	}
	return "dm:" + x + "|" + y
}

// LogReader is the read side of the squawk Log the projection needs: a
// seq-cursored bounded read (ListSince(0, 0) walks the whole Log in append
// order). *intercom.Log and *intercompg.Store both satisfy it; tests use a
// slice-backed fake.
type LogReader interface {
	ListSince(afterSeq int64, limit int) []intercom.Squawk
}

// ProjectChannels returns, for participant, the channels they are a member of
// (member = has SENT OR RECEIVED on the channel), each tagged from the roster,
// the live Instance snapshot, and the caller's per-channel unread cursors
// (channel id → last-seen Seq; nil = everything unread). It is pure: no I/O
// beyond the LogReader, deterministic ordering (attention bucket, then most
// recent, then id).
func ProjectChannels(participant intercom.Target, log LogReader, roster Roster, instances []Instance, cursors map[string]int64) []ChannelView {
	byUnit := map[string]Instance{}
	byActor := map[string]Instance{}
	for _, i := range instances {
		if i.Unit != "" {
			byUnit[i.Unit] = i
		}
		byActor[i.ActorID] = i
	}

	type acc struct {
		ch      ChannelView
		members map[string]bool
	}
	accs := map[string]*acc{}
	self := participant.String()

	ensure := func(id string, kind ChannelKind, project, sessionActor, label string) *acc {
		a := accs[id]
		if a != nil {
			return a
		}
		a = &acc{ch: ChannelView{ID: id, Kind: kind, Project: project, Label: label}, members: map[string]bool{}}
		if sessionActor != "" {
			a.members[actorRef(sessionActor)] = true
			if inst, ok := byActor[sessionActor]; ok {
				a.ch.Phase = string(inst.Phase)
				a.ch.Waiting = isWaiting(inst)
			}
		}
		accs[id] = a
		return a
	}

	for _, m := range log.ListSince(0, 0) {
		for _, t := range m.To {
			var a *acc
			switch t.Kind {
			case "channel":
				if inst, ok := byUnit[t.Ref]; ok {
					a = ensure(StudioChannelID(t.Ref), ChannelStudio, inst.Project, inst.ActorID, t.Ref)
				} else {
					a = ensure(NamedChannelID(t.Ref), ChannelNamed, m.Project, "", t.Ref)
				}
			case "actor", "human":
				session := dmSession(m.From, t)
				project := m.Project
				if inst, ok := byActor[session]; ok && inst.Project != "" {
					project = inst.Project
				}
				a = ensure(DMChannelID(m.From, t), ChannelDM, project, session, t.Ref)
				a.members[t.String()] = true
			default:
				continue
			}
			a.members[m.From.String()] = true
			if m.Seq > a.ch.LastSeq {
				a.ch.LastSeq = m.Seq
			}
			if m.Seq > cursors[a.ch.ID] && m.From != participant {
				a.ch.Unread++
			}
		}
	}

	var out []ChannelView
	for _, a := range accs {
		if !a.members[self] {
			continue
		}
		if a.ch.Kind == ChannelDM {
			a.ch.Label = dmLabel(a.members, self)
		}
		for m := range a.members {
			if kind, ref, ok := strings.Cut(m, ":"); ok && kind == "actor" {
				a.ch.Sessions = append(a.ch.Sessions, ref)
			}
		}
		sort.Strings(a.ch.Sessions)
		out = append(out, a.ch)
	}
	sortChannels(out)
	return out
}

// dmSession returns the actor (session) endpoint of a DM, if either endpoint is
// an actor; "" for a human↔human DM.
func dmSession(from, to intercom.Target) string {
	if to.Kind == "actor" {
		return to.Ref
	}
	if from.Kind == "actor" {
		return from.Ref
	}
	return ""
}

// dmLabel returns the member of a DM that is not the viewer (the ref), so the
// channel reads from the viewer's perspective.
func dmLabel(members map[string]bool, self string) string {
	for m := range members {
		if m == self {
			continue
		}
		// members are Target strings ("kind:ref"); the label is the ref.
		if _, ref, ok := strings.Cut(m, ":"); ok {
			return ref
		}
	}
	return ""
}

func actorRef(id string) string { return intercom.Target{Kind: "actor", Ref: id}.String() }

// sortChannels orders channels for the default attention rail: waiting first,
// then active, then plain channels; within a bucket, most-recent (LastSeq) then
// id for stability.
func sortChannels(chs []ChannelView) {
	rank := map[AttentionBucket]int{BucketWaiting: 0, BucketActive: 1, BucketChannels: 2}
	sort.Slice(chs, func(i, j int) bool {
		bi, bj := rank[chs[i].Bucket()], rank[chs[j].Bucket()]
		if bi != bj {
			return bi < bj
		}
		if chs[i].LastSeq != chs[j].LastSeq {
			return chs[i].LastSeq > chs[j].LastSeq
		}
		return chs[i].ID < chs[j].ID
	})
}

// ChannelSquawks returns, in append order, every squawk that belongs to
// channelID — using the SAME id derivation as ProjectChannels, so a channel's
// conversation is exactly the messages the rail counted. instances map a
// studio's Unit to its channel; a nil/short log yields nothing.
func ChannelSquawks(channelID string, log LogReader, instances []Instance) []intercom.Squawk {
	byUnit := map[string]Instance{}
	for _, i := range instances {
		if i.Unit != "" {
			byUnit[i.Unit] = i
		}
	}
	var out []intercom.Squawk
	for _, m := range log.ListSince(0, 0) {
		if squawkMapsToChannel(m, channelID, byUnit) {
			out = append(out, m)
		}
	}
	return out
}

// squawkMapsToChannel reports whether any recipient of m derives channelID,
// mirroring the To-target classification in ProjectChannels.
func squawkMapsToChannel(m intercom.Squawk, channelID string, byUnit map[string]Instance) bool {
	for _, t := range m.To {
		var id string
		switch t.Kind {
		case "channel":
			if _, ok := byUnit[t.Ref]; ok {
				id = StudioChannelID(t.Ref)
			} else {
				id = NamedChannelID(t.Ref)
			}
		case "actor", "human":
			id = DMChannelID(m.From, t)
		default:
			continue
		}
		if id == channelID {
			return true
		}
	}
	return false
}

// Recipient is one addressable target for the New Message picker.
type Recipient struct {
	Kind    string          // "human" | "session" | "studio" | "channel"
	Target  intercom.Target // the Log target to address it
	Label   string
	Project string
	Phase   string
	Waiting bool
}

// ActiveRecipients lists every currently-active recipient a participant could
// start a conversation with: each roster human, each roster named channel, and
// — for every active Instance — its session (actor:<id>) and, when it holds a
// ticket, its studio channel (channel:<unit>). Deterministically ordered.
func ActiveRecipients(roster Roster, instances []Instance) []Recipient {
	var out []Recipient
	for _, h := range roster.Humans {
		out = append(out, Recipient{Kind: "human", Target: intercom.Target{Kind: "human", Ref: h.Name}, Label: h.Name})
	}
	for _, c := range roster.Channels {
		out = append(out, Recipient{Kind: "channel", Target: intercom.Target{Kind: "channel", Ref: c.Name}, Label: c.Name})
	}
	for _, i := range instances {
		if !instanceActive(i) {
			continue
		}
		waiting := isWaiting(i)
		out = append(out, Recipient{
			Kind: "session", Target: intercom.Target{Kind: "actor", Ref: i.ActorID},
			Label: sessionLabel(i), Project: i.Project, Phase: string(i.Phase), Waiting: waiting,
		})
		if i.Unit != "" {
			out = append(out, Recipient{
				Kind: "studio", Target: intercom.Target{Kind: "channel", Ref: i.Unit},
				Label: i.Unit, Project: i.Project, Phase: string(i.Phase), Waiting: waiting,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Target.String() < out[j].Target.String()
	})
	return out
}

// ResolveSendTarget maps a participant's send reference to the single Log target
// to address, within one project. ref is either a recipient target from
// ActiveRecipients — "human:<name>", "actor:<id>", or "channel:<name-or-unit>" —
// or a channel id from ProjectChannels — "studio:<unit>", "named:<name>", or
// "dm:<x>|<y>". self is the participant's own target in this project
// ("human:<name>"), used to pick the other endpoint when replying to a DM.
//
// A studio channel ("channel:<unit>" / "studio:<unit>") and a session DM
// ("actor:<id>") both resolve to the session's actor target, so an
// external-origin append wakes a waiting/idled studio exactly as a relayed reply
// does (wake-on reads the actor inbox — see internal/wakeon). Only currently
// active instances (the ActiveRecipients/ProjectChannels reachability rule) and
// roster-declared humans/named channels resolve; anything else returns
// ok=false (an unknown or inactive recipient).
func ResolveSendTarget(ref string, self intercom.Target, roster Roster, instances []Instance) (intercom.Target, bool) {
	kind, val, ok := strings.Cut(ref, ":")
	if !ok || val == "" {
		return intercom.Target{}, false
	}
	switch kind {
	case "actor": // a session DM — wakes the session
		for _, i := range instances {
			if i.ActorID == val && instanceActive(i) {
				return intercom.Target{Kind: "actor", Ref: val}, true
			}
		}
	case "studio": // a studio channel id → the studio's session actor (wakes it)
		for _, i := range instances {
			if i.Unit == val && instanceActive(i) {
				return intercom.Target{Kind: "actor", Ref: i.ActorID}, true
			}
		}
	case "named": // a named channel id
		for _, c := range roster.Channels {
			if c.Name == val {
				return intercom.Target{Kind: "channel", Ref: c.Name}, true
			}
		}
	case "channel": // a recipient target: a studio (by unit) → its session actor, else a named channel
		for _, i := range instances {
			if i.Unit == val && instanceActive(i) {
				return intercom.Target{Kind: "actor", Ref: i.ActorID}, true
			}
		}
		for _, c := range roster.Channels {
			if c.Name == val {
				return intercom.Target{Kind: "channel", Ref: c.Name}, true
			}
		}
	case "human": // a human DM
		for _, h := range roster.Humans {
			if h.Name == val {
				return intercom.Target{Kind: "human", Ref: h.Name}, true
			}
		}
	case "dm": // a DM channel id "x|y" → the endpoint that is not self, re-resolved
		a, b, ok := strings.Cut(val, "|")
		if !ok {
			return intercom.Target{}, false
		}
		var other string
		switch self.String() {
		case a:
			other = b
		case b:
			other = a
		default:
			return intercom.Target{}, false // the participant is not a member of this DM
		}
		return ResolveSendTarget(other, self, roster, instances)
	}
	return intercom.Target{}, false
}

// sessionLabel is a session's display name: its declared name when it has one,
// else its actor id.
func sessionLabel(i Instance) string {
	if i.Name != "" {
		return i.Name
	}
	return i.ActorID
}

// isWaiting reports whether a session is soliciting a human: reported Waiting,
// or paused (Idled — a reply unpauses it). Mirrors the presence signal
// /ui/coves surfaces (phase + activity).
func isWaiting(i Instance) bool {
	return i.Activity == ActivityWaiting || i.Phase == PhaseIdled
}

// instanceActive reports whether a session is currently reachable: running,
// raising, or paused. Gone/Lost/Terminating instances are not offered as
// recipients.
func instanceActive(i Instance) bool {
	switch i.Phase {
	case PhaseLive, PhaseRaising, PhaseIdled:
		return true
	}
	return false
}

// activePhase reports whether a channel's backing-session phase counts as a
// live presence for the Active bucket.
func activePhase(phase string) bool {
	switch Phase(phase) {
	case PhaseLive, PhaseRaising, PhaseIdled:
		return true
	}
	return false
}
