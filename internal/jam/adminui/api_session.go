package adminui

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/aethons-tools/cove/internal/jam/sessionevents"
	"github.com/aethons-tools/cove/internal/jam/sessionview"
)

// The agent session timeline's JSON API (operator-only, behind the admin
// gate): its streams and totals, a JSON-rows SSE feed (the Thinking tab's
// row shape), older pages and one event's raw JSON.

const (
	sessionTail     = 300     // the newest events a fresh feed backfills
	sessionPageSize = 300     // a page's events
	sessionRawMax   = 1 << 20 // one event's raw JSON as served
)

// sessionEventOf is v as the timeline reads it — userui.thinkingEventOf's
// mapping (a gap shows its summary; kind other keeps no detail).
func sessionEventOf(v sessionview.EventView) SessionEvent {
	ev := SessionEvent{Stream: v.StreamID, Seq: v.Seq, Turn: v.Turn, Kind: string(v.Kind), Tool: v.Tool, ToolUseID: v.ToolUseID,
		Text: v.Narrative, Detail: v.Detail, DetailTruncated: v.DetailTruncated, IsError: v.IsError, TruncatedBytes: v.TruncatedBytes,
		CostUSD: v.CostUSD, DurationMS: v.DurationMS, ClaudeSession: v.ClaudeSession}
	if !v.At.IsZero() {
		ev.At = v.At.UnixMilli()
	}
	if v.Gap {
		ev.Text = v.Summary
	}
	if v.Kind == sessionview.KindOther {
		ev.Detail, ev.DetailTruncated = "", 0
	}
	return ev
}

func sessionTotalsOf(t sessionevents.Totals) SessionTotals {
	return SessionTotals{Turns: t.Turns, Episodes: t.Episodes, ToolCalls: t.ToolCalls, InputTokens: t.InputTokens, OutputTokens: t.OutputTokens, CostUSD: t.CostUSD}
}

func jsonLine(log *slog.Logger, v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		log.Error("ui session json failed", "error", err.Error())
		return "{}"
	}
	return string(b)
}

func apiSession(api *http.ServeMux, store sessionevents.Store, hub *sessionevents.Hub, log *slog.Logger) {
	base := apiPrefix + "agents/{id}/session"
	var totals *sessionevents.TotalsCache
	if store != nil {
		totals = sessionevents.NewTotalsCache(store)
	}
	stream := func(w http.ResponseWriter, r *http.Request) (string, bool) {
		s := r.URL.Query().Get("stream")
		if !sessionevents.ValidStreamID(s) {
			writeAPIError(w, http.StatusBadRequest, "bad_request", "stream: not a stream id")
			return "", false
		}
		return s, true
	}
	positive := func(w http.ResponseWriter, r *http.Request, name string) (uint64, bool) {
		n, err := strconv.ParseUint(r.URL.Query().Get(name), 10, 64)
		if err != nil || n == 0 {
			writeAPIError(w, http.StatusBadRequest, "bad_request", name+": not a positive number")
			return 0, false
		}
		return n, true
	}
	api.HandleFunc("GET "+base, func(w http.ResponseWriter, r *http.Request) {
		out := SessionInfo{Streams: []StreamRow{}}
		if store == nil {
			writeJSON(w, http.StatusOK, out)
			return
		}
		id := r.PathValue("id")
		out.Enabled = true
		streams, _ := store.Streams(id)
		for _, s := range streams {
			out.Streams = append(out.Streams, StreamRow{StreamID: s.StreamID, Started: s.FirstAt.Format("2006-01-02 15:04:05"), LastEvent: s.LastAt.Format("Jan 2 15:04:05"), Events: s.Events})
		}
		out.Stream = r.URL.Query().Get("stream")
		if !sessionevents.ValidStreamID(out.Stream) && len(streams) > 0 {
			out.Stream = streams[0].StreamID
		}
		if t, err := totals.Of(id); err == nil {
			out.Totals = sessionTotalsOf(t)
		}
		writeJSON(w, http.StatusOK, out)
	})
	api.HandleFunc("GET "+base+"/events", func(w http.ResponseWriter, r *http.Request) {
		if store == nil || hub == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		sessionview.Serve(w, r, sessionview.Source{Store: store, Hub: hub, ActorID: r.PathValue("id"), Tail: sessionTail,
			Event:  func(v sessionview.EventView) string { return jsonLine(log, sessionEventOf(v)) },
			Totals: func(t sessionevents.Totals) string { return jsonLine(log, sessionTotalsOf(t)) }})
	})
	api.HandleFunc("GET "+base+"/page", func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		s, ok := stream(w, r)
		if !ok {
			return
		}
		before, ok := positive(w, r, "before")
		if !ok {
			return
		}
		evs, err := store.List(sessionevents.Filter{ActorID: r.PathValue("id"), StreamID: s, BeforeSeq: before, Newest: true, Limit: sessionPageSize})
		if err != nil {
			log.Error("ui session page: listing events failed", "agent", r.PathValue("id"), "error", err.Error())
			writeAPIError(w, http.StatusInternalServerError, "internal", "listing events failed")
			return
		}
		page := SessionPage{Events: make([]SessionEvent, 0, len(evs))}
		for _, ev := range evs {
			page.Events = append(page.Events, sessionEventOf(sessionview.View(ev)))
		}
		if len(evs) > 0 && evs[0].Seq > 1 {
			page.Before = evs[0].Seq
		}
		writeJSON(w, http.StatusOK, page)
	})
	api.HandleFunc("GET "+base+"/raw", func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		s, ok := stream(w, r)
		if !ok {
			return
		}
		seq, ok := positive(w, r, "seq")
		if !ok {
			return
		}
		evs, err := store.List(sessionevents.Filter{ActorID: r.PathValue("id"), StreamID: s, AfterSeq: seq - 1, Limit: 1})
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "internal", "listing events failed")
			return
		}
		if len(evs) == 0 || evs[0].Seq != seq {
			writeAPIError(w, http.StatusNotFound, "not_found", "no such event")
			return
		}
		raw := sessionview.View(evs[0]).Raw
		out := SessionRaw{Raw: raw}
		if len(raw) > sessionRawMax {
			out.Raw, out.Truncated = raw[:sessionRawMax], len(raw)-sessionRawMax
		}
		writeJSON(w, http.StatusOK, out)
	})
}
