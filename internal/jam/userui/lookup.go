package userui

import (
	"cmp"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
)

// The rail's lookup of finished conversations and ended sessions
// (docs/modules/ROOT/pages/usage/jam/rail-api.adoc#lookup). Access is the
// Inbox's, checked per item before matching: a conversation the viewer may
// read (jam.Intercom.CanSee), a session whose view they may open (mayOpen).
// Matching is the admin search's: a case-insensitive substring.

// lookupMax is how many of each kind a lookup returns, newest first.
const lookupMax = 50

// snippetBefore and snippetLen clip a matching message around the match.
const (
	snippetBefore = 40
	snippetLen    = 160
)

func (h *handler) apiLookup(w http.ResponseWriter, r *http.Request) {
	p, ok := jam.ParticipantFrom(r)
	if !ok {
		writeAPIError(w, http.StatusForbidden, "forbidden", "sign in as a project member")
		return
	}
	resp := LookupResponse{Conversations: []LookupConversation{}, Sessions: []LookupSession{}}
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	if q != "" && h.d.Log != nil && h.d.Intercom != nil {
		resp.Conversations = h.lookupConversations(p, q)
		resp.Sessions = h.lookupSessions(r, p, q)
	}
	writeJSON(w, http.StatusOK, resp)
}

// finishedConv is a finished conversation the viewer may read, before
// matching.
type finishedConv struct {
	ch    jam.Channel
	why   string
	at    time.Time
	title string
}

// lookupConversations is p's finished conversations matching q (lowered):
// the ones they take part in (as their rail: members, or with deliveries),
// and the work logs of their projects.
func (h *handler) lookupConversations(p jam.Participant, q string) []LookupConversation {
	ids := map[ident.ID]bool{}
	for _, id := range h.d.Store.ChannelsOf(p.UserID) {
		ids[id] = true
	}
	for _, st := range h.d.Log.InboxChannels(p.UserID) {
		ids[st.Channel] = true
	}
	for _, name := range p.Projects {
		if proj, ok := h.d.Store.GetProject(name); ok {
			for _, ch := range h.d.Store.ListChannels(proj.ID, jam.SourceWork) {
				ids[ch.ID] = true
			}
		}
	}
	var cands []finishedConv
	for id := range ids {
		ch, ok := h.d.Store.GetChannel(id)
		if !ok || !h.d.Intercom.CanSee(p.UserID, ch) {
			continue
		}
		if why := jam.Finished(h.d.Store, p.UserID, ch); why != "" {
			cands = append(cands, h.finishedConv(ch, why))
		}
	}
	slices.SortFunc(cands, func(a, b finishedConv) int {
		return cmp.Or(b.at.Compare(a.at), cmp.Compare(a.ch.ID, b.ch.ID))
	})
	out := []LookupConversation{}
	for _, c := range cands {
		if len(out) == lookupMax {
			break
		}
		v, ok := jam.UserChannel(h.d.Store, h.d.Intercom, h.d.Log, p.UserID, c.ch.ID)
		if !ok {
			continue
		}
		names := []string{v.Label, c.title}
		if c.ch.Kind == jam.SourceWork {
			names = append(names, workKey(c.ch))
		}
		var match *LookupMatch
		if !matchesAny(q, names...) {
			if match = h.newestMatch(h.d.Log.ChannelBefore(c.ch.ID, 0, 0), q); match == nil {
				continue
			}
		}
		row := channelRow(v, "")
		row.NeedsYou, row.Unread = false, 0
		out = append(out, LookupConversation{Row: row, Title: c.title, Finished: c.at, Why: c.why, Match: match})
	}
	return out
}

// finishedConv is ch, finished for why, with when it finished: a done
// unit's report, the end of a dm's or group's last session, or else (an
// archive, which keeps no time) its last message.
func (h *handler) finishedConv(ch jam.Channel, why string) finishedConv {
	c := finishedConv{ch: ch, why: why}
	if ch.Kind == jam.SourceWork {
		if u, ok := h.d.Store.WorkUnit(ch.ID); ok {
			c.title = u.Issue.Title
			if why == jam.FinishedDone {
				c.at = u.StateAt
			}
		}
	}
	if why == jam.FinishedEnded {
		for _, m := range h.d.Store.ChannelMembers(ch.ID) {
			if inst, ok := h.d.Store.GetInstance(string(m.ParticipantID)); ok && ended(inst).After(c.at) {
				c.at = ended(inst)
			}
		}
	}
	if c.at.IsZero() {
		if last := h.d.Log.ChannelBefore(ch.ID, 0, 1); len(last) == 1 {
			c.at = last[0].At
		}
	}
	return c
}

// lookupSessions is the ended sessions whose view p may open, matching q
// (lowered) on their label, mission, or activity.
func (h *handler) lookupSessions(r *http.Request, p jam.Participant, q string) []LookupSession {
	var cands []jam.Instance
	for _, inst := range h.d.Store.ListInstances() {
		if inst.Phase == jam.PhaseGone && mayOpen(r, p, inst) {
			cands = append(cands, inst)
		}
	}
	slices.SortFunc(cands, func(a, b jam.Instance) int {
		return cmp.Or(ended(b).Compare(ended(a)), cmp.Compare(a.ActorID, b.ActorID))
	})
	out := []LookupSession{}
	for _, inst := range cands {
		if len(out) == lookupMax {
			break
		}
		label := h.d.Intercom.PartyOf(ident.ID(inst.ActorID)).Label
		var match *LookupMatch
		if !matchesAny(q, label, inst.Mission) {
			// Its activity: the messages it sent or was sent. Never its
			// thinking, which is in the session events, not the log.
			if match = h.newestMatch(h.d.Log.ActivityBefore(ident.ID(inst.ActorID), 0, 0), q); match == nil {
				continue
			}
		}
		out = append(out, LookupSession{Session: inst.ActorID, Label: label, Kind: kindLabel(inst),
			Mission: inst.Mission, Ended: ended(inst), Match: match})
	}
	return out
}

// ended is when a session that ended did: when it was last seen, else when
// it was raised.
func ended(inst jam.Instance) time.Time {
	if inst.LastSeen.IsZero() {
		return inst.RaisedAt
	}
	return inst.LastSeen
}

// newestMatch is the newest of msgs (oldest first) whose text contains q
// (lowered), or nil.
func (h *handler) newestMatch(msgs []intercom.Squawk, q string) *LookupMatch {
	for i := len(msgs) - 1; i >= 0; i-- {
		if m := msgs[i]; matchesAny(q, m.Body) {
			return &LookupMatch{From: h.d.Intercom.PartyOf(m.From).Label, Text: snippet(m.Body, q)}
		}
	}
	return nil
}

// matchesAny reports whether any field contains q (lowered),
// case-insensitively.
func matchesAny(q string, fields ...string) bool {
	return slices.ContainsFunc(fields, func(f string) bool { return strings.Contains(strings.ToLower(f), q) })
}

// snippet is body clipped to snippetLen runes around q's first match (q
// lowered), with "…" where it was cut.
func snippet(body, q string) string {
	runes := []rune(body)
	if len(runes) <= snippetLen {
		return body
	}
	start := 0
	if lower := []rune(strings.ToLower(body)); len(lower) == len(runes) {
		if i := strings.Index(string(lower), q); i >= 0 {
			start = max(0, len([]rune(string(lower)[:i]))-snippetBefore)
		}
	}
	start = min(start, len(runes)-snippetLen)
	out := string(runes[start : start+snippetLen])
	if start > 0 {
		out = "…" + out
	}
	if start+snippetLen < len(runes) {
		out += "…"
	}
	return out
}
