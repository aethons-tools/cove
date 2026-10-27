package userui

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/sessionevents"
	"github.com/aethons-tools/cove/internal/jam/sessionview"
)

var thinkStream = strings.Repeat("a", 32)

// thinkEnv is sessionEnv with an events store and hub wired into Deps.
func thinkEnv(t *testing.T) (*env, jam.Participant, jam.Instance, *sessionevents.MemStore, *sessionevents.Hub) {
	t.Helper()
	e, p, personal, _, _ := sessionEnv(t)
	store, hub := sessionevents.NewMemStore(), sessionevents.NewHub()
	e.Deps.Events, e.Deps.Hub = store, hub
	return e, p, personal, store, hub
}

// textEvent is an assistant text block.
func textEvent(actor string, seq uint64, text string) sessionevents.Event {
	raw, _ := json.Marshal(map[string]any{"type": "assistant", "message": map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}}}})
	return sessionevents.Event{ActorID: actor, StreamID: thinkStream, Seq: seq, Kind: sessionevents.KindEvent,
		ObservedAt: time.Unix(1000+int64(seq), 0), Raw: raw}
}

func appendText(t *testing.T, st sessionevents.Store, actor string, from, to uint64) {
	t.Helper()
	for s := from; s <= to; s++ {
		if err := st.Append(textEvent(actor, s, fmt.Sprintf("said %d", s))); err != nil {
			t.Fatal(err)
		}
	}
}

// asWho wraps h so every request comes from p.
func asWho(h http.Handler, p jam.Participant) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.ServeHTTP(w, asPrincipal(r, p, false)) })
}

func thinkGet(h http.Handler, p jam.Participant, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, asPrincipal(httptest.NewRequest("GET", path, nil), p, false))
	return rec
}

// sseLines reads an SSE body line by line onto a channel (closed at EOF).
func sseLines(body io.Reader) <-chan string {
	out := make(chan string, 64)
	go func() {
		defer close(out)
		sc := bufio.NewScanner(body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			out <- sc.Text()
		}
	}()
	return out
}

// sseRead collects lines into buf until want appears and its message ends
// (or the deadline).
func sseRead(t *testing.T, lines <-chan string, buf *strings.Builder, want string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for !strings.Contains(buf.String(), want) || !strings.HasSuffix(buf.String(), "\n\n") {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatalf("stream ended before %q:\n%s", want, buf.String())
			}
			buf.WriteString(l + "\n")
		case <-deadline:
			t.Fatalf("timed out waiting for %q", want)
		}
	}
}

func TestThinkingEventsBackfillLiveInert(t *testing.T) {
	e, p, personal, store, hub := thinkEnv(t)
	appendText(t, store, personal.ActorID, 1, 2)
	if err := store.Append(textEvent(personal.ActorID, 3, "<script>x</script>")); err != nil {
		t.Fatal(err)
	}
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
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("code %d", resp.StatusCode)
	}
	rd := sseLines(resp.Body)
	var buf strings.Builder
	sseRead(t, rd, &buf, "id: "+thinkStream+":3")
	body := buf.String()
	for _, want := range []string{`data-kind="text"`, "&lt;script&gt;", `class="te k-text"`, `data-seq="1"`} {
		if !strings.Contains(body, want) {
			t.Errorf("backfill missing %q:\n%s", want, body)
		}
	}
	// Live: seq 4 arrives.
	ev := textEvent(personal.ActorID, 4, "live one")
	if err := store.Append(ev); err != nil {
		t.Fatal(err)
	}
	hub.Publish(ev)
	var live strings.Builder
	sseRead(t, rd, &live, "id: "+thinkStream+":4")
	if strings.Contains(buf.String()+live.String(), "<script>x") {
		t.Error("agent content is live markup")
	}
}

func TestThinkingHiddenFromOutsiders(t *testing.T) {
	e, p, personal, store, _ := thinkEnv(t)
	appendText(t, store, personal.ActorID, 1, 3)
	bob := jam.Participant{UserID: ident.New(ident.User), Name: "bob", Projects: []string{"proj"}}
	h := Handler(e.Deps, nil)
	base := "/app/user/session/" + personal.ActorID + "/thinking/"
	paths := []string{base + "events", base + "page?stream=" + thinkStream + "&before=3", base + "raw?stream=" + thinkStream + "&seq=1"}
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

func TestThinkingRawCapped(t *testing.T) {
	e, p, personal, store, _ := thinkEnv(t)
	if err := store.Append(textEvent(personal.ActorID, 1, strings.Repeat("y", 2<<20))); err != nil {
		t.Fatal(err)
	}
	h := Handler(e.Deps, nil)
	rec := thinkGet(h, p, "/app/user/session/"+personal.ActorID+"/thinking/raw?stream="+thinkStream+"&seq=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Errorf("content type %q", ct)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("no nosniff")
	}
	body := rec.Body.String()
	if len(body) > rawMax+64 || !strings.Contains(body[len(body)-40:], "truncated") {
		t.Fatalf("raw not capped: %d bytes, tail %q", len(body), body[len(body)-40:])
	}
	// A small one is whole, pretty JSON; a missing seq is 404; bad params 400.
	appendText(t, store, personal.ActorID, 2, 2)
	rec = thinkGet(h, p, "/app/user/session/"+personal.ActorID+"/thinking/raw?stream="+thinkStream+"&seq=2")
	if !strings.Contains(rec.Body.String(), `"said 2"`) || strings.Contains(rec.Body.String(), "truncated") {
		t.Errorf("small raw: %q", rec.Body.String())
	}
	if c := thinkGet(h, p, "/app/user/session/"+personal.ActorID+"/thinking/raw?stream="+thinkStream+"&seq=9").Code; c != http.StatusNotFound {
		t.Errorf("missing seq = %d", c)
	}
	for _, q := range []string{"stream=nope&seq=1", "stream=" + thinkStream + "&seq=x", "stream=" + thinkStream + "&seq=0"} {
		if c := thinkGet(h, p, "/app/user/session/"+personal.ActorID+"/thinking/raw?"+q).Code; c != http.StatusBadRequest {
			t.Errorf("raw?%s = %d", q, c)
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

func TestThinkingPage(t *testing.T) {
	e, p, personal, store, _ := thinkEnv(t)
	appendText(t, store, personal.ActorID, 1, 5)
	h := Handler(e.Deps, nil)
	page := func(q string) *httptest.ResponseRecorder {
		return thinkGet(h, p, "/app/user/session/"+personal.ActorID+"/thinking/page?"+q)
	}
	rec := page("stream=" + thinkStream + "&before=4")
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d", rec.Code)
	}
	for _, s := range []string{`data-seq="1"`, `data-seq="2"`, `data-seq="3"`} {
		if !strings.Contains(body, s) {
			t.Errorf("page missing %s", s)
		}
	}
	if strings.Contains(body, `data-seq="4"`) || strings.Contains(body, "te-more") {
		t.Errorf("page before=4 from seq 1:\n%s", body)
	}

	old := thinkingPageSize
	thinkingPageSize = 2
	defer func() { thinkingPageSize = old }()
	body = page("stream=" + thinkStream + "&before=5").Body.String()
	if !strings.Contains(body, `data-seq="3"`) || !strings.Contains(body, `data-seq="4"`) || strings.Contains(body, `data-seq="2"`) {
		t.Errorf("page size 2, before=5:\n%s", body)
	}
	more := strings.Index(body, `<button class="te-more" data-before="3">`)
	if more < 0 || more > strings.Index(body, `data-seq="3"`) {
		t.Errorf("no Load earlier at the top:\n%s", body)
	}

	// Empty (purged): 200, nothing.
	rec = page("stream=" + strings.Repeat("b", 32) + "&before=5")
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "te-more") || strings.Contains(rec.Body.String(), "data-seq") {
		t.Errorf("empty page = %d %q", rec.Code, rec.Body.String())
	}
	for _, q := range []string{"stream=nope&before=3", "stream=" + thinkStream + "&before=x", "stream=" + thinkStream} {
		if c := page(q).Code; c != http.StatusBadRequest {
			t.Errorf("page?%s = %d", q, c)
		}
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

// ?format=json: the same feed, each ev a ThinkingEvent (agent text as JSON
// data, never markup); without format the HTML fragments are unchanged.
func TestThinkingEventsJSON(t *testing.T) {
	e, p, personal, store, _ := thinkEnv(t)
	actor := personal.ActorID
	if err := store.Append(textEvent(actor, 1, "<img src=x onerror=alert(1)>\r\nsecond line")); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(asWho(Handler(e.Deps, nil), p))
	defer func() { srv.CloseClientConnections(); srv.Close() }()
	res, err := http.Get(srv.URL + "/app/user/session/" + actor + "/thinking/events?format=json")
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
	if strings.Contains(buf.String(), `class="te`) {
		t.Errorf("JSON feed carries HTML fragments:\n%s", buf.String())
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
