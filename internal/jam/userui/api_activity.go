package userui

import (
	"cmp"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
)

// The session view's activity (conversations-sessions.adoc#session-view):
// every message the session authored or was delivered, in every
// conversation, with the turns of thinking between them.

// Page sizes for GET api/sessions/{id}/activity, as read's.
const (
	activityDefault = 50
	activityMax     = 500
	// maxFolds caps the turns one page folds in.
	maxFolds = 500
)

// activityQuery is GET api/sessions/{id}/activity's paging, as read's:
// anchor start, end (the default), or id; dir forward (the default) or
// backward, from an id; and limit.
type activityQuery struct {
	anchor, id, dir string
	limit           int
}

// parseActivityQuery reads r's paging, or has answered 400.
func parseActivityQuery(w http.ResponseWriter, r *http.Request) (activityQuery, bool) {
	q := r.URL.Query()
	aq := activityQuery{anchor: q.Get("anchor"), id: q.Get("id"), dir: q.Get("dir"), limit: activityDefault}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			writeAPIError(w, http.StatusBadRequest, "bad_request", "limit: not a positive number")
			return aq, false
		}
		aq.limit = min(n, activityMax)
	}
	switch {
	case aq.anchor != "" && aq.anchor != "end" && aq.anchor != "start" && aq.anchor != "id":
		writeAPIError(w, http.StatusBadRequest, "bad_request", "anchor: start, end, or id")
	case aq.anchor == "id" && aq.id == "":
		writeAPIError(w, http.StatusBadRequest, "bad_request", "anchor=id needs id")
	case aq.dir != "" && aq.dir != "forward" && aq.dir != "backward":
		writeAPIError(w, http.StatusBadRequest, "bad_request", "dir: forward or backward")
	default:
		return aq, true
	}
	return aq, false
}

func (h *handler) apiSessionActivity(w http.ResponseWriter, r *http.Request) {
	aq, ok := parseActivityQuery(w, r)
	if !ok {
		return
	}
	p, ok := jam.ParticipantFrom(r)
	if !ok {
		writeAPIError(w, http.StatusForbidden, "forbidden", "sign in as a project member")
		return
	}
	inst, ok := h.viewedSession(r, p, r.PathValue("id"))
	if !ok || h.d.Log == nil || h.d.Intercom == nil {
		noSession(w)
		return
	}
	self := ident.ID(inst.ActorID)
	var msgs []intercom.Squawk
	switch aq.anchor {
	case "", "end":
		msgs = h.d.Log.ActivityBefore(self, 0, aq.limit)
	case "start":
		msgs = h.d.Log.ActivitySince(self, 0, aq.limit)
	case "id":
		seq, found := h.d.Log.SeqOf(aq.id)
		if !found {
			writeAPIError(w, http.StatusBadRequest, "bad_request", "id: no such message")
			return
		}
		if aq.dir == "backward" {
			msgs = h.d.Log.ActivityBefore(self, seq, aq.limit)
		} else {
			msgs = h.d.Log.ActivitySince(self, seq, aq.limit)
		}
	}
	a := SessionActivity{Messages: []ActivityRow{}, Turns: []TurnFold{}, Limited: []ConversationRef{}, Ended: inst.Phase == jam.PhaseGone,
		Label: h.d.Intercom.PartyOf(self).Label}
	a.Color, _ = h.d.Store.SessionColor(inst.ActorID)
	if !a.Ended {
		a.SendTo = "session:" + inst.ActorID
	}
	colors := map[ident.ID]string{}
	convs := map[ident.ID]ConversationRef{}
	var order []ident.ID
	for _, m := range msgs {
		c, seen := convs[m.Channel]
		if !seen {
			cp := h.d.Intercom.ChannelParty(m.Channel)
			c = ConversationRef{ID: string(cp.ID), Kind: cp.Kind, Label: cp.Label}
			convs[m.Channel] = c
			order = append(order, m.Channel)
		}
		a.Messages = append(a.Messages, ActivityRow{MessageRow: shownRow(p, h.d, m, colors), ID: m.ID, Conversation: c, Out: m.From == self})
	}
	// The page's thinking: from its first message (or the beginning) to the
	// next page's first (or now).
	var from, until time.Time
	if len(msgs) > 0 {
		first, last := msgs[0], msgs[len(msgs)-1]
		a.PageFirst, a.PageLast = first.ID, last.ID
		a.Earlier = len(h.d.Log.ActivityBefore(self, first.Seq, 1)) > 0
		if a.Earlier {
			from = first.At
		}
		if next := h.d.Log.ActivitySince(self, last.Seq, 1); len(next) > 0 {
			a.Later, until = true, next[0].At
		}
	}
	if len(msgs) > 0 || aq.anchor != "id" {
		a.Turns = h.turnFolds(inst.ActorID, from, until)
	}
	if h.d.AgentLimit != nil {
		for _, ch := range order {
			if h.d.AgentLimit.Tripped(ch) {
				a.Limited = append(a.Limited, convs[ch])
			}
		}
	}
	writeJSON(w, http.StatusOK, a)
}

// turnFolds are actor's turns that start in [from, until) (a zero bound:
// none), by start, at most maxFolds.
func (h *handler) turnFolds(actor string, from, until time.Time) []TurnFold {
	out := []TurnFold{}
	if h.d.Events == nil {
		return out
	}
	streams, err := h.d.Events.Streams(actor)
	if err != nil {
		h.lg.Error("session activity: listing streams failed", "session", actor, "error", err.Error())
		return out
	}
	for _, s := range streams {
		turns, err := h.d.Events.Turns(actor, s.StreamID)
		if err != nil {
			h.lg.Error("session activity: listing turns failed", "session", actor, "error", err.Error())
			continue
		}
		for _, t := range turns {
			if !from.IsZero() && t.Start.Before(from) || !until.IsZero() && !t.Start.Before(until) {
				continue
			}
			out = append(out, TurnFold{Stream: s.StreamID, Turn: t.Turn, Start: t.Start.UnixMilli(), End: t.End.UnixMilli(),
				After: t.FirstSeq - 1, Before: t.LastSeq + 1, Events: t.Events})
		}
	}
	slices.SortStableFunc(out, func(a, b TurnFold) int { return cmp.Compare(a.Start, b.Start) })
	if len(out) > maxFolds {
		out = out[:maxFolds]
	}
	return out
}
