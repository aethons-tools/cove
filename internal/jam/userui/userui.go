// Package userui serves the user area under /app/user: the Jam app (web/app,
// embedded via uiassets) and the JSON API and byte/stream routes it reads
// over the person's channels (jam.UserChannels on the channel log), with a
// read-only History of the legacy log. It has its own gate (no loopback
// trust); identity is per-request via jam.ParticipantFrom.
package userui

import (
	"context"
	"sort"
	"strings"

	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/collab"
	"github.com/aethons-tools/cove/internal/jam/sessionevents"
)

// Deps are what the user area reads: the store, the intercom (who may see and
// post where), the channel log, and the frozen legacy log (its History; nil
// = none).
type Deps struct {
	Store    jam.Store
	Intercom *jam.Intercom
	Log      intercom.Store
	Legacy   jam.LogReader
	// Attachments serves uploads and blobs (nil: attachments off).
	Attachments *jam.Attachments
	// Boards serves whiteboards (nil: off).
	Boards *jam.Boards
	// Ops runs the session header's actions (nil: none offered); Totals
	// derives its cost and counts from session events (nil: none shown).
	Ops    SessionOps
	Totals *sessionevents.TotalsCache
	// PreviewURL maps a session's exposed port name to the URL a
	// collaborator opens (nil: previews off).
	PreviewURL func(actorID, name string) (string, error)
	// Frames mints frame URLs for the pane's framed views (nil: framing off;
	// Studio and View here are hidden).
	Frames interface {
		FrameURL(p collab.Principal, display, actorID, name string, right collab.Right) (string, error)
	}
	// Events and Hub serve the Thinking tab (nil Events: not recorded).
	Events sessionevents.Store
	Hub    *sessionevents.Hub
	// Send posts the viewer's messages (POST api/messages): the same
	// *jam.ParticipantSendHandler as /app/user/send (nil: 503).
	Send Sender
	// Personal starts the viewer's personal sessions (New session; nil: not
	// offered).
	Personal PersonalStarter
}

// PersonalStarter starts a personal session owned by owner:
// jam.StartPersonalSession with the runtime's supervisor and allocator.
type PersonalStarter interface {
	StartPersonal(ctx context.Context, owner jam.Member, by string, b jam.PersonalSessionBody) (jam.PersonalSessionResult, error)
}

// Sender posts a participant's message: *jam.ParticipantSendHandler.
type Sender interface {
	Send(p jam.Participant, req jam.SendRequest) (intercom.Squawk, error)
}

// legacyPrefix marks a History channel id (the legacy projection's ids).
const legacyPrefix = "legacy:"

// bucketHistory groups the legacy log's conversations, read-only.
const bucketHistory jam.AttentionBucket = "history"

var bucketTitle = map[jam.AttentionBucket]string{
	jam.BucketNeedsYou: "Needs you",
	jam.BucketActive:   "Sessions",
	jam.BucketChannels: "Channels",
	bucketHistory:      "History (before the upgrade)",
}

var bucketOrder = []jam.AttentionBucket{jam.BucketNeedsYou, jam.BucketActive, jam.BucketChannels}

// legacyNames is how the legacy log named the participant in each of their
// projects: as their user name (the name the roster view gave them). Never a
// pre-registry alias: the legacy projection is not project-scoped, so a name
// a migration clash moved to someone else would show that person's history.
func legacyNames(p jam.Participant) map[string]string {
	names := map[string]string{}
	if p.Name == "" {
		return names
	}
	for _, proj := range p.Projects {
		names[proj] = p.Name
	}
	return names
}

// legacyChannels is the participant's History: their conversations in the
// legacy log, projected as before the channel log (per project, as their
// roster name), ids prefixed legacyPrefix, all read.
func legacyChannels(p jam.Participant, d Deps) []jam.ChannelView {
	if d.Legacy == nil {
		return nil
	}
	var all []jam.ChannelView
	seen := map[string]bool{}
	instances := d.Store.ListInstances()
	names := legacyNames(p)
	for _, proj := range p.Projects {
		name, ok := names[proj]
		if !ok {
			continue
		}
		roster := jam.Roster{}
		if pr, ok := d.Store.GetProject(proj); ok {
			for _, ch := range d.Store.ListChannels(pr.ID, jam.SourceRoom) {
				roster.Channels = append(roster.Channels, jam.RosterChannel{Name: ch.Key})
			}
		}
		target := intercom.Target{Kind: "human", Ref: name}
		var insts []jam.Instance
		for _, i := range instances {
			if jam.SameProject(d.Store, i.Project, proj) {
				i.Project = proj // the legacy projection labels by name
				insts = append(insts, i)
			}
		}
		for _, ch := range jam.ProjectChannels(target, d.Legacy, roster, insts, nil) {
			if seen[ch.ID] {
				continue
			}
			seen[ch.ID] = true
			ch.ID, ch.Unread, ch.Phase, ch.NeedsYou = legacyPrefix+ch.ID, 0, "", false
			all = append(all, ch)
		}
	}
	return all
}

// groupRail groups channels into the attention-ordered rail (Needs you →
// Active → Channels); within a group, most-recent first, then id. Empty groups
// are dropped. selectedID marks the open channel.
func groupRail(chs []jam.ChannelView, selectedID string) []RailGroup {
	byBucket := map[jam.AttentionBucket][]ChannelRow{}
	for _, ch := range chs {
		if ch.ID == selectedID {
			// The open conversation is being read: the app commits its
			// cursor only after it loads (POST api/conversations/{c}/read), so the stored
			// count would otherwise linger in the rail and app badge.
			ch.Unread = 0
		}
		b := ch.Bucket()
		if strings.HasPrefix(ch.ID, legacyPrefix) {
			b = bucketHistory
		}
		byBucket[b] = append(byBucket[b], ChannelRow{
			ID:          ch.ID,
			Label:       ch.Label,
			Project:     ch.Project,
			Kind:        ch.Kind,
			NeedsYou:    ch.NeedsYou,
			Phase:       ch.Phase,
			Unread:      ch.Unread,
			OpenAsks:    ch.OpenAsks,
			LastSeq:     ch.LastSeq,
			Selected:    ch.ID == selectedID,
			Mission:     ch.Mission,
			cardSession: ch.CardSession,
		})
	}
	var groups []RailGroup
	for _, b := range append(bucketOrder, bucketHistory) {
		rows := byBucket[b]
		if len(rows) == 0 {
			continue
		}
		sort.SliceStable(rows, func(i, j int) bool {
			if rows[i].LastSeq != rows[j].LastSeq {
				return rows[i].LastSeq > rows[j].LastSeq
			}
			return rows[i].ID < rows[j].ID
		})
		groups = append(groups, RailGroup{Bucket: b, Title: bucketTitle[b], Rows: rows})
	}
	return groups
}

// Rail builds the participant's rail: their channels, grouped by attention,
// then their History, with selectedID marked.
func Rail(p jam.Participant, d Deps, selectedID string) []RailGroup {
	chs := jam.UserChannels(d.Store, d.Intercom, d.Log, p.UserID)
	return groupRail(append(chs, legacyChannels(p, d)...), selectedID)
}

// unreadTotal is the rail's total unread (History rows are always read), the
// same sum as jam.UnreadTotal.
func unreadTotal(groups []RailGroup) int {
	n := 0
	for _, g := range groups {
		for _, r := range g.Rows {
			n += r.Unread
		}
	}
	return n
}
