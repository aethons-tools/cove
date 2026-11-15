package userui

import (
	"cmp"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/collab"
)

// The rail's sections, Me, Work, Agents, and History, and the per-user saved
// order of Me and Agents (docs/modules/ROOT/pages/usage/jam/inbox-rail.adoc).

// railSections fills r's sections for viewer p from their channels (chs:
// jam.UserChannels) and History (legacy), with selectedID the open
// conversation.
func (h *handler) railSections(req *http.Request, p jam.Participant, chs, legacy []jam.ChannelView, selectedID string) RailResponse {
	r := RailResponse{Me: []ChannelRow{}, Work: []WorkRow{}, Agents: []AgentItem{}, History: []ChannelRow{}}
	followed := map[string]int{} // the work logs p follows: their unread
	for _, ch := range h.meChannels(p, chs) {
		row := channelRow(ch, selectedID)
		r.Me = append(r.Me, row)
		if ch.Source == jam.SourceWork {
			followed[ch.ID] = row.Unread
		}
	}
	h.railSessions(req, p, r.Me)
	h.attachCards(req, p, r.Me)
	orderRows(r.Me, h.d.Store.RailOrder(p.UserID, jam.RailMe))
	for _, ch := range legacy {
		r.History = append(r.History, channelRow(ch, selectedID))
	}
	sortNewest(r.History)
	r.Work = h.workRows(p, followed)
	r.Agents = h.agentItems(req, p)
	r.UnreadTotal = unreadTotal(r.Me, r.History)
	return r
}

// meChannels keeps the channels of chs that are in p's Me: the live
// conversations p is a member of, where a dm or a group has a person or an
// active session besides p (so a dm with a session leaves when it ends).
func (h *handler) meChannels(p jam.Participant, chs []jam.ChannelView) []jam.ChannelView {
	member := map[string]bool{}
	for _, id := range h.d.Store.ChannelsOf(p.UserID) {
		member[string(id)] = true
	}
	var out []jam.ChannelView
	for _, ch := range chs {
		c, ok := h.d.Store.GetChannel(ident.ID(ch.ID))
		if !ok || !member[ch.ID] || c.Status != jam.StatusLive || c.Kind == jam.SourceSession {
			continue
		}
		if jam.IsChat(c.Kind) && !h.hasOthers(c.ID, p.UserID) {
			continue
		}
		out = append(out, ch)
	}
	return out
}

// hasOthers reports whether conversation ch has a member besides viewer
// that is a person or an active session.
func (h *handler) hasOthers(ch, viewer ident.ID) bool {
	for _, m := range h.d.Store.ChannelMembers(ch) {
		if m.ParticipantID == viewer {
			continue
		}
		inst, isSession := h.d.Store.GetInstance(string(m.ParticipantID))
		if isSession && jam.InstanceActive(inst) || !isSession && m.ParticipantID.Kind() != ident.Session {
			return true
		}
	}
	return false
}

// workRows is every unit of work in p's projects, that p may see, with a
// current worker, by key; followed is the logs p follows, with their unread.
func (h *handler) workRows(p jam.Participant, followed map[string]int) []WorkRow {
	rows := []WorkRow{}
	for _, name := range p.Projects {
		proj, ok := h.d.Store.GetProject(name)
		if !ok {
			continue
		}
		for _, ch := range h.d.Store.ListChannels(proj.ID, jam.SourceWork) {
			if ch.Worker == "" || h.d.Intercom == nil || !h.d.Intercom.CanSee(p.UserID, ch) {
				continue
			}
			inst, ok := h.d.Store.GetInstance(string(ch.Worker))
			if !ok {
				continue
			}
			card, ok := h.card(inst)
			if !ok {
				continue // ending or gone: no current worker
			}
			row := WorkRow{Key: workKey(ch), Title: inst.UnitTitle, Log: string(ch.ID),
				Worker: WorkWorker{ID: inst.ActorID, Label: card.Name, State: card.State}}
			if n, ok := followed[string(ch.ID)]; ok {
				row.Unread = &n
			}
			rows = append(rows, row)
		}
	}
	slices.SortStableFunc(rows, func(a, b WorkRow) int { return cmp.Or(cmp.Compare(a.Key, b.Key), cmp.Compare(a.Log, b.Log)) })
	return rows
}

// workKey is a work log's issue key: its channel key without the
// connection id ("<connection id>/<issue key>").
func workKey(ch jam.Channel) string {
	if _, key, ok := strings.Cut(ch.Key, "/"); ok {
		return key
	}
	return ch.Key
}

// agentItems is every active session whose view p may open, in p's saved
// order, then by start time.
func (h *handler) agentItems(req *http.Request, p jam.Participant) []AgentItem {
	now, cp := time.Now(), collabPrincipal(req, p)
	insts := h.d.Store.ListInstances()
	slices.SortStableFunc(insts, func(a, b jam.Instance) int {
		return cmp.Or(a.RaisedAt.Compare(b.RaisedAt), cmp.Compare(a.ActorID, b.ActorID))
	})
	items := []AgentItem{}
	for _, inst := range insts {
		if !jam.InstanceActive(inst) || !mayOpen(req, p, inst) {
			continue
		}
		card, ok := h.card(inst)
		if !ok {
			continue
		}
		_, ssh := collab.Decide(h.d.Store, now, cp, inst, collab.SSH)
		items = append(items, AgentItem{Session: inst.ActorID, SSH: ssh, Card: card})
	}
	orderBy(items, h.d.Store.RailOrder(p.UserID, jam.RailAgents), func(a AgentItem) string { return a.Session })
	return items
}

// sortNewest orders rows newest message first, then by id.
func sortNewest(rows []ChannelRow) {
	slices.SortStableFunc(rows, func(a, b ChannelRow) int {
		return cmp.Or(cmp.Compare(b.LastSeq, a.LastSeq), cmp.Compare(a.ID, b.ID))
	})
}

// orderRows puts Me's rows in the saved order, the rest after them newest
// message first.
func orderRows(rows []ChannelRow, saved []string) {
	sortNewest(rows)
	orderBy(rows, saved, func(r ChannelRow) string { return r.ID })
}

// orderBy stably moves the items whose id is in saved to the front, in
// saved's order; the rest keep their order after them.
func orderBy[T any](items []T, saved []string, id func(T) string) {
	pos := map[string]int{}
	for i, s := range saved {
		if _, dup := pos[s]; !dup {
			pos[s] = i
		}
	}
	rank := func(t T) int {
		if i, ok := pos[id(t)]; ok {
			return i
		}
		return len(saved)
	}
	slices.SortStableFunc(items, func(a, b T) int { return cmp.Compare(rank(a), rank(b)) })
}

// apiRailOrder saves the viewer's order of a rail section (PUT
// api/rail/order): only ids now in the section are kept, so ones that left
// it are dropped.
func (h *handler) apiRailOrder(w http.ResponseWriter, r *http.Request) {
	var req RailOrderRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	p, ok := jam.ParticipantFrom(r)
	if !ok {
		writeAPIError(w, http.StatusForbidden, "forbidden", "sign in as a project member")
		return
	}
	section := jam.RailSection(req.Section)
	if !jam.ValidRailSection(section) {
		writeAPIError(w, http.StatusBadRequest, "bad_request", `section is "me" or "agents"`)
		return
	}
	in := map[string]bool{}
	if section == jam.RailMe {
		for _, ch := range h.meChannels(p, jam.UserChannels(h.d.Store, h.d.Intercom, h.d.Log, p.UserID)) {
			in[ch.ID] = true
		}
	} else {
		for _, a := range h.agentItems(r, p) {
			in[a.Session] = true
		}
	}
	ids := []string{}
	for _, id := range req.IDs {
		if in[id] {
			ids = append(ids, id)
			in[id] = false // once
		}
	}
	if err := h.d.Store.PutRailOrder(p.UserID, section, ids); err != nil {
		h.lg.Error("userui rail order save failed", "error", err.Error())
		writeAPIError(w, http.StatusInternalServerError, "internal", "couldn't save the order")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
