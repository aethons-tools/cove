// Package meui serves the participant intercom inbox under /me — a two-pane,
// server-rendered (html/template + htmx) web UI over the person's channels
// (intercom slice 2b: jam.UserChannels on the channel log), with a read-only
// History of the legacy log. It mirrors internal/jam/adminui's mechanism but
// has its own two-pane chrome and its own gate (no loopback trust); identity
// is per-request via jam.ParticipantFrom.
package meui

import (
	"sort"
	"strings"

	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
)

// Deps are what the inbox reads: the store, the intercom (who may see and
// post where), the channel log, and the frozen legacy log (its History; nil
// = none).
type Deps struct {
	Store    jam.Store
	Intercom *jam.Intercom
	Log      intercom.Store
	Legacy   jam.LogReader
}

// legacyPrefix marks a History channel id (the legacy projection's ids).
const legacyPrefix = "legacy:"

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
// Channels / History), non-empty.
type RailGroup struct {
	Bucket jam.AttentionBucket
	Title  string
	Rows   []ChannelRow
}

// bucketHistory groups the legacy log's conversations, read-only.
const bucketHistory jam.AttentionBucket = "history"

var bucketTitle = map[jam.AttentionBucket]string{
	jam.BucketWaiting:  "Waiting on you",
	jam.BucketActive:   "Active",
	jam.BucketChannels: "Channels",
	bucketHistory:      "History (before the upgrade)",
}

var bucketOrder = []jam.AttentionBucket{jam.BucketWaiting, jam.BucketActive, jam.BucketChannels}

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
			if i.Project == proj {
				insts = append(insts, i)
			}
		}
		for _, ch := range jam.ProjectChannels(target, d.Legacy, roster, insts, nil) {
			if seen[ch.ID] {
				continue
			}
			seen[ch.ID] = true
			ch.ID, ch.Unread, ch.Phase, ch.Waiting = legacyPrefix+ch.ID, 0, "", false
			all = append(all, ch)
		}
	}
	return all
}

// groupRail groups channels into the attention-ordered rail (Waiting on you →
// Active → Channels); within a group, most-recent first, then id. Empty groups
// are dropped. selectedID marks the open channel.
func groupRail(chs []jam.ChannelView, selectedID string) []RailGroup {
	byBucket := map[jam.AttentionBucket][]ChannelRow{}
	for _, ch := range chs {
		b := ch.Bucket()
		if strings.HasPrefix(ch.ID, legacyPrefix) {
			b = bucketHistory
		}
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
