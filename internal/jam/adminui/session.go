package adminui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aethons-tools/cove/internal/jam/sessionevents"
)

const (
	sessionBackfillPage = 500
	sessionSubBuffer    = 256
)

func registerSession(mux *http.ServeMux, store sessionevents.Store, hub *sessionevents.Hub) {
	mux.HandleFunc("GET /ui/coves/{id}/session", func(w http.ResponseWriter, r *http.Request) {
		data := map[string]any{"Title": "Session", "ActorID": r.PathValue("id"), "Enabled": store != nil}
		if store != nil {
			streams, _ := store.Streams(r.PathValue("id"))
			selected := r.URL.Query().Get("stream")
			if !sessionevents.ValidStreamID(selected) && len(streams) > 0 {
				selected = streams[0].StreamID
			}
			data["Streams"], data["Stream"] = streams, selected
		}
		render(w, "session", data)
	})
	mux.HandleFunc("GET /ui/coves/{id}/session/events", func(w http.ResponseWriter, r *http.Request) {
		if store == nil || hub == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		serveSessionEvents(w, r, store, hub)
	})
}

type eventView struct {
	Seq            uint64
	Turn           uint32
	Label          string
	Summary        string
	Detail         string
	Raw            string
	IsError        bool
	Progress       bool // system/thinking_tokens — hidden unless toggled
	Gap            bool
	TruncatedBytes uint64
}

type totals struct {
	Turns                     uint32
	ToolCalls                 int
	InputTokens, OutputTokens int64
	CostUSD                   float64
}

func (t *totals) add(ev sessionevents.Event) {
	if ev.Turn > t.Turns {
		t.Turns = ev.Turn
	}
	if ev.Index.Type == "assistant" && ev.Index.ToolName != "" {
		t.ToolCalls++
	}
	if ev.Index.Type == "result" {
		t.InputTokens += ev.Index.InputTokens
		t.OutputTokens += ev.Index.OutputTokens
		t.CostUSD += ev.Index.CostUSD
	}
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

func pretty(raw []byte) string {
	var b bytes.Buffer
	if json.Indent(&b, raw, "", "  ") == nil {
		return b.String()
	}
	return string(raw)
}

type viewBlock struct {
	Type     string          `json:"type"`
	Text     string          `json:"text"`
	Thinking string          `json:"thinking"`
	Name     string          `json:"name"`
	Input    json.RawMessage `json:"input"`
	Content  json.RawMessage `json:"content"`
	IsError  bool            `json:"is_error"`
}

// blockText renders a tool_result content field: a string, or an array of
// {type:text,text} blocks.
func blockText(c json.RawMessage) string {
	var s string
	if json.Unmarshal(c, &s) == nil {
		return s
	}
	var parts []viewBlock
	if json.Unmarshal(c, &parts) == nil {
		var out []string
		for _, p := range parts {
			if p.Text != "" {
				out = append(out, p.Text)
			}
		}
		return strings.Join(out, "\n")
	}
	return string(c)
}

func viewOf(ev sessionevents.Event) eventView {
	v := eventView{Seq: ev.Seq, Turn: ev.Turn, Raw: pretty(ev.Raw), TruncatedBytes: ev.TruncatedBytes, IsError: ev.Index.IsError}
	if ev.Kind == sessionevents.KindGap {
		v.Gap, v.Label = true, "gap"
		v.Summary = fmt.Sprintf("⚠ %d events lost (seq %d–%d)", ev.GapTo-ev.GapFrom+1, ev.GapFrom, ev.GapTo)
		return v
	}
	var env struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
		Result string `json:"result"`
	}
	if json.Unmarshal(ev.Raw, &env) != nil {
		v.Label, v.Detail = "unparsed", string(ev.Raw)
		return v
	}
	var blocks []viewBlock
	_ = json.Unmarshal(env.Message.Content, &blocks)
	switch env.Type {
	case "system":
		v.Label = "system/" + env.Subtype
		v.Progress = env.Subtype == "thinking_tokens"
	case "assistant":
		v.Label = "assistant"
		for _, b := range blocks {
			switch b.Type {
			case "text":
				v.Summary, v.Detail = clip(b.Text, 300), b.Text
			case "thinking":
				v.Label, v.Summary, v.Detail = "thinking", clip(b.Thinking, 160), b.Thinking
			case "tool_use":
				v.Label, v.Summary, v.Detail = "tool_use "+b.Name, clip(string(b.Input), 160), pretty(b.Input)
			}
		}
	case "user":
		v.Label = "user"
		for _, b := range blocks {
			if b.Type == "tool_result" {
				text := blockText(b.Content)
				v.Label, v.Detail = "tool_result", text
				v.Summary = fmt.Sprintf("%d bytes", len(text))
				v.IsError = v.IsError || b.IsError
			}
		}
	case "result":
		v.Label = "result"
		v.Summary = fmt.Sprintf("$%.4f · in %d / out %d tokens · %s", ev.Index.CostUSD, ev.Index.InputTokens,
			ev.Index.OutputTokens, time.Duration(ev.Index.DurationMS)*time.Millisecond)
		v.Detail = env.Result
	default:
		v.Label = env.Type
	}
	return v
}

// sseLines normalizes CRLF and CR to LF: SSE parsers treat all three as line
// terminators, so any of them inside a field value would end the line early.
var sseLines = strings.NewReplacer("\r\n", "\n", "\r", "\n")

// sseWrite emits one SSE message; multi-line data gets one data: line each.
// Event and id are collapsed to a single line; data is split on every SSE
// line terminator so agent text can never inject fields.
func sseWrite(w http.ResponseWriter, event, id, data string) {
	oneLine := func(v string) string { return strings.ReplaceAll(sseLines.Replace(v), "\n", " ") }
	var b strings.Builder
	if id != "" {
		fmt.Fprintf(&b, "id: %s\n", oneLine(id))
	}
	fmt.Fprintf(&b, "event: %s\n", oneLine(event))
	for _, line := range strings.Split(sseLines.Replace(data), "\n") {
		fmt.Fprintf(&b, "data: %s\n", line)
	}
	b.WriteString("\n")
	_, _ = w.Write([]byte(b.String()))
}

func fragment(name string, data any) string {
	var b bytes.Buffer
	if err := pages["session"].ExecuteTemplate(&b, name, data); err != nil {
		return ""
	}
	// html/template leaves CR raw; keep it visible to the operator as an entity
	// (SSE framing is separately CR-safe in sseWrite).
	return strings.ReplaceAll(b.String(), "\r", "&#13;")
}

// serveSessionEvents streams one cove's events: subscribe FIRST (so nothing
// published during backfill is missed), backfill from the store, then drain
// live events, skipping any seq already sent. Totals are computed over the
// whole stream even when resuming via Last-Event-ID.
func serveSessionEvents(w http.ResponseWriter, r *http.Request, store sessionevents.Store, hub *sessionevents.Hub) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	actor := r.PathValue("id")
	stream := r.URL.Query().Get("stream")
	var resumeAfter uint64
	if lid := r.Header.Get("Last-Event-ID"); lid != "" {
		if s, n, ok := strings.Cut(lid, ":"); ok && sessionevents.ValidStreamID(s) {
			if seq, err := strconv.ParseUint(n, 10, 64); err == nil {
				stream, resumeAfter = s, seq
			}
		}
	}
	if stream != "" && !sessionevents.ValidStreamID(stream) {
		http.Error(w, "invalid stream id", http.StatusBadRequest)
		return
	}
	sub := hub.Subscribe(actor, sessionSubBuffer)
	defer sub.Close()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, "retry: 3000\n\n")

	var tot totals
	var last uint64 // highest seq handled (sent or counted)
	emit := func(ev sessionevents.Event) {
		tot.add(ev)
		last = ev.Seq
		if ev.Seq > resumeAfter {
			sseWrite(w, "ev", fmt.Sprintf("%s:%d", ev.StreamID, ev.Seq), fragment("session-event", viewOf(ev)))
		}
	}
	// backfill replays the store from `last`; false means the store failed.
	backfill := func() bool {
		for {
			evs, err := store.List(sessionevents.Filter{ActorID: actor, StreamID: stream, AfterSeq: last, Limit: sessionBackfillPage})
			if err != nil {
				return false
			}
			for _, ev := range evs {
				emit(ev)
			}
			if len(evs) < sessionBackfillPage {
				return true
			}
		}
	}
	if stream != "" {
		sseWrite(w, "stream", "", stream)
		if !backfill() {
			return
		}
		sseWrite(w, "totals", "", fragment("session-totals", tot))
	}
	fl.Flush()

	tick := time.NewTicker(25 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-sub.C:
			if !ok {
				return // dropped as a slow subscriber; EventSource reconnects with Last-Event-ID
			}
			if stream == "" { // no stream yet: follow the first one that appears
				stream = ev.StreamID
				sseWrite(w, "stream", "", stream)
				if !backfill() { // the subscription is live; ev.Seq <= last dedups below
					return
				}
				sseWrite(w, "totals", "", fragment("session-totals", tot))
			}
			if ev.StreamID != stream || ev.Seq <= last {
				continue
			}
			emit(ev)
			sseWrite(w, "totals", "", fragment("session-totals", tot))
		case <-tick.C:
			_, _ = fmt.Fprint(w, ": keep-alive\n\n")
		}
		fl.Flush()
	}
}
