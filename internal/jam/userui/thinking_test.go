package userui

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/sessionview"
)

func TestThinkingHiddenFromOutsiders(t *testing.T) {
	e, p, personal, store, _ := thinkEnv(t)
	appendText(t, store, personal.ActorID, 1, 3)
	bob := jam.Participant{UserID: ident.New(ident.User), Name: "bob", Projects: []string{"proj"}}
	h := Handler(e.Deps, nil)
	base := "/app/user/session/" + personal.ActorID + "/thinking/"
	paths := []string{base + "events"}
	for _, path := range paths {
		if c := thinkGet(h, bob, path).Code; c != http.StatusNotFound {
			t.Errorf("outsider %s = %d", path, c)
		}
	}
	// A member removed from the home channel loses it too.
	if err := e.st.LeaveChannel(ident.ID(e.homeOf(t, personal)), p.UserID, 0); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if c := thinkGet(h, p, path).Code; c != http.StatusNotFound {
			t.Errorf("removed member %s = %d", path, c)
		}
	}
}

func TestThinkingStreamCap(t *testing.T) {
	e, p, personal, _, _ := thinkEnv(t)
	h := &handler{d: e.Deps, lg: slog.New(slog.DiscardHandler), streams: map[ident.ID]int{p.UserID: maxThinkingStreams}}
	req := asPrincipal(httptest.NewRequest("GET", "/app/user/session/"+personal.ActorID+"/thinking/events", nil), p, false)
	req.SetPathValue("id", personal.ActorID)
	rec := httptest.NewRecorder()
	h.thinkingEvents(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("code %d", rec.Code)
	}
	if h.streams[p.UserID] != maxThinkingStreams {
		t.Errorf("a refused stream changed the count: %d", h.streams[p.UserID])
	}
}

func TestThinkingNoStore(t *testing.T) {
	e, p, personal, _, _ := sessionEnv(t)
	h := Handler(e.Deps, nil)
	if c := thinkGet(h, p, "/app/user/session/"+personal.ActorID+"/thinking/events").Code; c != http.StatusNoContent {
		t.Fatalf("no events store = %d", c)
	}
}

// An open feed ends (at the next keep-alive) once its viewer loses the
// session's home channel.
func TestThinkingEventsEndOnRevoke(t *testing.T) {
	old := sessionview.KeepAlive
	sessionview.KeepAlive = 10 * time.Millisecond
	defer func() { sessionview.KeepAlive = old }()
	e, p, personal, store, _ := thinkEnv(t)
	appendText(t, store, personal.ActorID, 1, 1)
	srv := httptest.NewServer(asWho(Handler(e.Deps, nil), p))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/app/user/session/"+personal.ActorID+"/thinking/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	rd := sseLines(resp.Body)
	var buf strings.Builder
	sseRead(t, rd, &buf, "id: "+thinkStream+":1")
	if err := e.st.LeaveChannel(ident.ID(e.homeOf(t, personal)), p.UserID, 0); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-rd:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("a removed member's feed is still open")
		}
	}
}

// Each ev of the feed is a ThinkingEvent: agent text as JSON data, never
// markup.
func TestThinkingEventsJSON(t *testing.T) {
	e, p, personal, store, _ := thinkEnv(t)
	actor := personal.ActorID
	if err := store.Append(textEvent(actor, 1, "<img src=x onerror=alert(1)>\r\nsecond line")); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(asWho(Handler(e.Deps, nil), p))
	defer func() { srv.CloseClientConnections(); srv.Close() }()
	res, err := http.Get(srv.URL + "/app/user/session/" + actor + "/thinking/events")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var buf strings.Builder
	sseRead(t, sseLines(res.Body), &buf, "event: ev")
	var data string
	for _, l := range strings.Split(buf.String(), "\n") {
		if strings.HasPrefix(l, "data: {") {
			data = strings.TrimPrefix(l, "data: ")
		}
	}
	var ev ThinkingEvent
	if err := json.Unmarshal([]byte(data), &ev); err != nil {
		t.Fatalf("ev data %q: %v\n%s", data, err, buf.String())
	}
	if ev.Stream != thinkStream || ev.Seq != 1 || ev.Kind != "text" || ev.At != time.Unix(1001, 0).UnixMilli() ||
		!strings.Contains(ev.Text+ev.Detail, "<img src=x onerror=alert(1)>") {
		t.Errorf("ev = %+v", ev)
	}
	if !strings.Contains(buf.String(), "id: "+thinkStream+":1") {
		t.Errorf("JSON feed lost its cursor:\n%s", buf.String())
	}
}

func TestThinkingEventOf(t *testing.T) {
	at := time.UnixMilli(1_700_000_000_123)
	for _, tc := range []struct {
		in   sessionview.EventView
		want ThinkingEvent
	}{
		{sessionview.EventView{StreamID: "s", Seq: 2, Turn: 1, At: at, Kind: sessionview.KindToolCall, Tool: "Bash", ToolUseID: "tu1", Narrative: "go test", Detail: "go test ./...", Raw: "{}"},
			ThinkingEvent{Stream: "s", Seq: 2, Turn: 1, At: at.UnixMilli(), Kind: "tool_call", Tool: "Bash", ToolUseID: "tu1", Text: "go test", Detail: "go test ./..."}},
		{sessionview.EventView{Kind: sessionview.KindGap, Gap: true, Summary: "3 events lost", Narrative: "ignored"},
			ThinkingEvent{Kind: "gap", Text: "3 events lost"}},
		{sessionview.EventView{Kind: sessionview.KindOther, Narrative: "system/init", Detail: "hidden"},
			ThinkingEvent{Kind: "other", Text: "system/init"}},
		{sessionview.EventView{Kind: sessionview.KindResult, CostUSD: 0.12, DurationMS: 38000, ClaudeSession: "c1", IsError: true, TruncatedBytes: 9, DetailTruncated: 4, Detail: "d"},
			ThinkingEvent{Kind: "result", CostUSD: 0.12, DurationMS: 38000, ClaudeSession: "c1", IsError: true, TruncatedBytes: 9, DetailTruncated: 4, Detail: "d"}},
	} {
		if got := thinkingEventOf(tc.in); got != tc.want {
			t.Errorf("thinkingEventOf(%+v)\n = %+v\nwant %+v", tc.in, got, tc.want)
		}
	}
}
