// Package meui serves the participant intercom inbox under /me — a two-pane,
// server-rendered (html/template + htmx) web UI over the channel read-model
// (COV-198), the participant plane (COV-199), and the send endpoint (COV-200).
// It mirrors internal/jam/adminui's proven mechanism but has its own two-pane
// chrome and its own gate (no loopback trust); identity is per-request via
// jam.ParticipantFrom.
package meui

import (
	"sort"

	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
)

// Store is the slice of jam.Store the inbox reads.
type Store interface {
	GetRoster(project string) (jam.Roster, bool)
	ListInstances() []jam.Instance
	UnreadCursors(participant string) map[string]int64
	CommitUnread(participant, channel string, seq int64) error
}

// ChannelRow is one channel in the left rail, from the viewer's perspective.
type ChannelRow struct {
	ID       string
	Label    string
	Project  string
	Kind     jam.ChannelKind
	Waiting  bool
	Phase    string
	Unread   int
	LastSeq  int64
	Selected bool
}

// RailGroup is one attention section of the rail (Waiting on you / Active /
// Channels), non-empty.
type RailGroup struct {
	Bucket jam.AttentionBucket
	Title  string
	Rows   []ChannelRow
}

var bucketTitle = map[jam.AttentionBucket]string{
	jam.BucketWaiting:  "Waiting on you",
	jam.BucketActive:   "Active",
	jam.BucketChannels: "Channels",
}

var bucketOrder = []jam.AttentionBucket{jam.BucketWaiting, jam.BucketActive, jam.BucketChannels}

// channelsFor returns the participant's channels across all their projects,
// de-duplicated by channel id. A participant is a global person; per project we
// resolve their roster identity (name may differ per project) and project the
// channels they're a member of, matching the send path's per-project resolution.
func channelsFor(p jam.Participant, store Store, log jam.LogReader) []jam.ChannelView {
	if log == nil {
		return nil // no intercom Log configured → empty inbox
	}
	var all []jam.ChannelView
	seen := map[string]bool{}
	instances := store.ListInstances()
	for _, proj := range p.Projects {
		roster, ok := store.GetRoster(proj)
		if !ok {
			continue
		}
		self, ok := roster.HumanByIdentity(p.Issuer, p.Subject)
		if !ok || self.Name == "" {
			continue // not bound (or unnamed) in this project
		}
		target := intercom.Target{Kind: "human", Ref: self.Name}
		var insts []jam.Instance
		for _, i := range instances {
			if i.Project == proj {
				insts = append(insts, i)
			}
		}
		cursors := store.UnreadCursors(target.String())
		for _, ch := range jam.ProjectChannels(target, log, roster, insts, cursors) {
			if seen[ch.ID] {
				continue
			}
			seen[ch.ID] = true
			all = append(all, ch)
		}
	}
	return all
}

// groupRail groups channels into the attention-ordered rail (Waiting on you →
// Active → Channels); within a group, most-recent first, then id. Empty groups
// are dropped. selectedID marks the open channel. Pure — the UI's own logic,
// separate from the read-model projection (jam.ProjectChannels).
func groupRail(chs []jam.ChannelView, selectedID string) []RailGroup {
	byBucket := map[jam.AttentionBucket][]ChannelRow{}
	for _, ch := range chs {
		b := ch.Bucket()
		byBucket[b] = append(byBucket[b], ChannelRow{
			ID:       ch.ID,
			Label:    ch.Label,
			Project:  ch.Project,
			Kind:     ch.Kind,
			Waiting:  ch.Waiting,
			Phase:    ch.Phase,
			Unread:   ch.Unread,
			LastSeq:  ch.LastSeq,
			Selected: ch.ID == selectedID,
		})
	}
	var groups []RailGroup
	for _, b := range bucketOrder {
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

// Rail builds the participant's rail: their channels across all projects,
// grouped by attention, with selectedID marked.
func Rail(p jam.Participant, store Store, log jam.LogReader, selectedID string) []RailGroup {
	return groupRail(channelsFor(p, store, log), selectedID)
}
