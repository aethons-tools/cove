package meui

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
)

type fakeStore struct {
	rosters map[string]jam.Roster
	insts   []jam.Instance
	cursors map[string]map[string]int64
}

func (f *fakeStore) GetRoster(p string) (jam.Roster, bool) { r, ok := f.rosters[p]; return r, ok }
func (f *fakeStore) ListInstances() []jam.Instance         { return f.insts }
func (f *fakeStore) UnreadCursors(participant string) map[string]int64 {
	return f.cursors[participant]
}
func (f *fakeStore) CommitUnread(participant, channel string, seq int64) error {
	if f.cursors == nil {
		f.cursors = map[string]map[string]int64{}
	}
	if f.cursors[participant] == nil {
		f.cursors[participant] = map[string]int64{}
	}
	f.cursors[participant][channel] = seq
	return nil
}

type fakeLog struct{ sq []intercom.Squawk }

func (f fakeLog) ListSince(after int64, _ int) []intercom.Squawk {
	var out []intercom.Squawk
	for _, m := range f.sq {
		if m.Seq > after {
			out = append(out, m)
		}
	}
	return out
}

func fixture() (*fakeStore, fakeLog, jam.Participant) {
	roster := jam.Roster{
		Humans: []jam.Human{{
			Name: "alice", Handle: "alice",
			Identity: []jam.OIDCIdentity{{Issuer: "https://idp", Subject: "sub-alice"}},
		}},
		Channels: []jam.Channel{{Name: "eng"}},
	}
	store := &fakeStore{rosters: map[string]jam.Roster{"proj": roster}}
	log := fakeLog{sq: []intercom.Squawk{{
		Seq:  1,
		From: intercom.Target{Kind: "human", Ref: "alice"},
		To:   []intercom.Target{{Kind: "channel", Ref: "eng"}},
		Body: "hi from alice", At: time.Date(2026, 9, 28, 14, 3, 0, 0, time.UTC), Project: "proj",
	}}}
	p := jam.Participant{Issuer: "https://idp", Subject: "sub-alice", Projects: []string{"proj"}, Name: "alice"}
	return store, log, p
}

func TestInboxFullPageRendersRailAndConversation(t *testing.T) {
	store, log, p := fixture()
	h := Handler(store, log, nil)

	req := httptest.NewRequest("GET", "/me/?c="+url.QueryEscape("named:eng"), nil)
	req = jam.WithParticipant(req, p)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("GET /me/ = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"Jam", "New message", "eng", "hi from alice", "/me/static/htmx.min.js"} {
		if !strings.Contains(body, want) {
			t.Errorf("inbox page missing %q", want)
		}
	}
	// The message from the viewer renders on the "human" (own) side.
	if !strings.Contains(body, `msg human`) {
		t.Error("viewer's own message should render on the human side")
	}
	// The composer must be OUTSIDE the polled region: the poll lives on #stream
	// (innerHTML), and the composer is a later sibling — so a 3s refresh never
	// wipes a typed reply.
	if strings.Index(body, `id="stream"`) > strings.Index(body, `class="composer"`) {
		t.Error("composer should render after the stream, as a sibling outside it")
	}
	if !strings.Contains(body, `hx-swap="innerHTML"`) {
		t.Error("stream should poll with innerHTML swap (not outerHTML on the whole pane)")
	}
}

func TestInboxComposerSendsOnDoubleEnter(t *testing.T) {
	store, log, p := fixture()
	h := Handler(store, log, nil)
	req := httptest.NewRequest("GET", "/me/?c="+url.QueryEscape("named:eng"), nil)
	req = jam.WithParticipant(req, p)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body := rec.Body.String()
	// Enter+Enter sends; Shift+Enter never arms it. The handler is delegated on
	// document so it also covers the meCompose (New message) composer.
	for _, want := range []string{"Enter twice to send", "document.addEventListener('keydown'", "e.shiftKey", "requestSubmit()"} {
		if !strings.Contains(body, want) {
			t.Errorf("inbox page missing composer key wiring %q", want)
		}
	}
}

func TestInboxRefreshesOnLivePush(t *testing.T) {
	store, log, p := fixture()
	h := Handler(store, log, nil)
	req := httptest.NewRequest("GET", "/me/?c="+url.QueryEscape("named:eng"), nil)
	req = jam.WithParticipant(req, p)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body := rec.Body.String()
	// The page listens on /me/events and fires meChanged, which the rail and
	// stream refresh on (a 30s poll is the fallback); a push blocked by a text
	// selection is replayed once the selection clears, and a reconnect catches
	// up on anything missed while the stream was down.
	for _, want := range []string{
		"new EventSource('/me/events')", "addEventListener('changed'", "htmx.trigger(document.body, 'meChanged')",
		"selectionchange", `hx-trigger="meChanged from:body, every 30s"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("inbox page missing live-push wiring %q", want)
		}
	}
}

func TestStreamSticksToBottomOnNewMessages(t *testing.T) {
	store, log, p := fixture()
	h := Handler(store, log, nil)
	req := httptest.NewRequest("GET", "/me/?c="+url.QueryEscape("named:eng"), nil)
	req = jam.WithParticipant(req, p)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body := rec.Body.String()
	// A #stream swap records whether the view was at the bottom before, and if
	// so scrolls to the new end after, so a new squawk is fully in view; a
	// viewer scrolled up into history is left where they are.
	for _, want := range []string{"htmx:beforeSwap", "htmx:afterSwap", "_atBottom", "scrollHeight"} {
		if !strings.Contains(body, want) {
			t.Errorf("inbox page missing stick-to-bottom wiring %q", want)
		}
	}
}

func TestConversationOpensAndSendsAtBottom(t *testing.T) {
	store, log, p := fixture()
	h := Handler(store, log, nil)
	req := httptest.NewRequest("GET", "/me/?c="+url.QueryEscape("named:eng"), nil)
	req = jam.WithParticipant(req, p)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body := rec.Body.String()
	// Opening a conversation (a full page load) starts at its end, and a Send
	// always ends there: the scroll runs once the send's refresh has swapped
	// (htmx.ajax's promise), whatever the scroll position was.
	for _, want := range []string{"meScrollToEnd(document.getElementById('stream'))", "}).then(function(){ meScrollToEnd(s, 'smooth'); })"} {
		if !strings.Contains(body, want) {
			t.Errorf("inbox page missing open/send-at-bottom wiring %q", want)
		}
	}
}

func TestStreamPollPausesWhileTextSelected(t *testing.T) {
	store, log, p := fixture()
	h := Handler(store, log, nil)
	req := httptest.NewRequest("GET", "/me/?c="+url.QueryEscape("named:eng"), nil)
	req = jam.WithParticipant(req, p)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body := rec.Body.String()
	// Swapping #stream replaces the text nodes under a selection, which the
	// browser then drops — so the poll is filtered off while the viewer has a
	// selection inside the stream, letting them copy a message.
	for _, want := range []string{`hx-trigger="meChanged[!meSelecting()] from:body, every 30s [!meSelecting()]"`, "function meSelecting()"} {
		if !strings.Contains(body, want) {
			t.Errorf("inbox page missing selection-safe poll wiring %q", want)
		}
	}
}

func TestStreamFragmentIsMessagesOnly(t *testing.T) {
	store, log, p := fixture()
	h := Handler(store, log, nil)
	req := httptest.NewRequest("GET", "/me/stream?c="+url.QueryEscape("named:eng"), nil)
	req = jam.WithParticipant(req, p)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("GET /me/stream = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "hi from alice") {
		t.Error("stream fragment should contain the messages")
	}
	// It is ONLY the messages — no composer, no topbar chrome to clobber.
	for _, absent := range []string{"class=\"composer\"", "New message", "<header"} {
		if strings.Contains(body, absent) {
			t.Errorf("stream fragment should not contain %q (it must not re-render chrome)", absent)
		}
	}
}

func TestInboxFailsClosedWithoutParticipant(t *testing.T) {
	store, log, _ := fixture()
	h := Handler(store, log, nil)
	req := httptest.NewRequest("GET", "/me/", nil) // no WithParticipant
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("no-participant /me/ = %d, want 401", rec.Code)
	}
}

func TestMarkReadCommitsCursor(t *testing.T) {
	store, log, p := fixture()
	h := Handler(store, log, nil)

	form := url.Values{"channel": {"named:eng"}, "seq": {"1"}}
	req := httptest.NewRequest("POST", "/me/read", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = jam.WithParticipant(req, p)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != 204 {
		t.Fatalf("POST /me/read = %d, want 204", rec.Code)
	}
	// Committed under the participant's self ref in the channel's project.
	if got := store.cursors["human:alice"]["named:eng"]; got != 1 {
		t.Errorf("cursor = %d, want 1", got)
	}
}

func TestStreamRendersMarkdownAndPlain(t *testing.T) {
	store, _, p := fixture()
	eng := []intercom.Target{{Kind: "channel", Ref: "eng"}}
	alice := intercom.Target{Kind: "human", Ref: "alice"}
	at := time.Date(2026, 9, 28, 14, 3, 0, 0, time.UTC)
	log := fakeLog{sq: []intercom.Squawk{
		{Seq: 1, From: alice, To: eng, Body: "**bold** <script>x</script>", At: at, Project: "proj", ContentType: intercom.ContentMarkdown},
		{Seq: 2, From: alice, To: eng, Body: "**literal** a_b", At: at, Project: "proj", ContentType: intercom.ContentPlain},
	}}
	h := Handler(store, log, nil)
	req := httptest.NewRequest("GET", "/me/stream?c="+url.QueryEscape("named:eng"), nil)
	req = jam.WithParticipant(req, p)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body := rec.Body.String()
	for _, want := range []string{`class="body md"`, "<strong>bold</strong>", `class="body plain"`, "**literal** a_b"} {
		if !strings.Contains(body, want) {
			t.Errorf("stream missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "<script>") {
		t.Errorf("raw HTML must not reach the page:\n%s", body)
	}
}

func TestComposerOffersPlainTextOptOut(t *testing.T) {
	store, log, p := fixture()
	h := Handler(store, log, nil)
	req := httptest.NewRequest("GET", "/me/?c="+url.QueryEscape("named:eng"), nil)
	req = jam.WithParticipant(req, p)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body := rec.Body.String()
	// Markdown is the default; a per-message "Plain text" box (in the reply and
	// New message composers) sends content_type text/plain.
	for _, want := range []string{`<input type="checkbox" name="plain">`, "content_type: f.plain && f.plain.checked ? 'text/plain' : undefined"} {
		if strings.Count(body, want) < 1 {
			t.Errorf("page missing plain-text opt-out wiring %q", want)
		}
	}
	if strings.Count(body, `name="plain"`) < 2 {
		t.Error("both the reply composer and the New message composer should offer the opt-out")
	}
}

func TestComposerPastesAsCodeBlock(t *testing.T) {
	store, log, p := fixture()
	h := Handler(store, log, nil)
	req := httptest.NewRequest("GET", "/me/?c="+url.QueryEscape("named:eng"), nil)
	req = jam.WithParticipant(req, p)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body := rec.Body.String()
	// Cmd-Shift-V (Ctrl-Shift-V off the Mac) marks the box, and the browser's
	// own paste event (which needs no clipboard permission, in any browser)
	// is wrapped in a fenced code block, with a fence longer than any backtick
	// run inside, via an undoable insert. The clipboard is never read directly.
	for _, want := range []string{
		"t._pasteAsCode", "addEventListener('paste'", "e.clipboardData.getData('text/plain')",
		"function meFence(", "execCommand('insertText'", "⇧⌘V",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing paste-as-code wiring %q", want)
		}
	}
	if strings.Contains(body, "navigator.clipboard.readText") {
		t.Error("paste-as-code must use the paste event, not a permission-gated clipboard read")
	}
}

func TestComposerDraftStack(t *testing.T) {
	store, log, p := fixture()
	h := Handler(store, log, nil)
	req := httptest.NewRequest("GET", "/me/?c="+url.QueryEscape("named:eng"), nil)
	req = jam.WithParticipant(req, p)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body := rec.Body.String()
	// Cmd-Down pushes the draft onto a per-conversation stack (tab
	// sessionStorage, with an in-memory fallback); a successful send pops it
	// back; Cmd-Up or the chip pops by hand, but never over text in the box.
	for _, want := range []string{
		"function mePushDraft(", "function mePopDraft(", "'me-drafts:'", "sessionStorage",
		"e.key==='ArrowDown'", "e.key==='ArrowUp'", "f.reset();\n       mePopDraft(f);",
		"if(t.value.trim()) return false;", ".draft-chip[hidden]{display:none}", "⌘↓",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing draft-stack wiring %q", want)
		}
	}
}

func TestRawViewToggleAndMonospaceComposer(t *testing.T) {
	store, _, p := fixture()
	eng := []intercom.Target{{Kind: "channel", Ref: "eng"}}
	alice := intercom.Target{Kind: "human", Ref: "alice"}
	log := fakeLog{sq: []intercom.Squawk{{Seq: 1, From: alice, To: eng, Body: "**bold** & <b>", At: time.Date(2026, 9, 28, 14, 3, 0, 0, time.UTC), Project: "proj"}}}
	h := Handler(store, log, nil)
	req := httptest.NewRequest("GET", "/me/?c="+url.QueryEscape("named:eng"), nil)
	req = jam.WithParticipant(req, p)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body := rec.Body.String()
	// Each bubble carries its raw text (escaped) beside the rendered body; a
	// Rendered/Raw selector flips html.raw, remembered in localStorage and
	// applied in <head> before first paint. Composers are monospace.
	for _, want := range []string{
		`<div class="body raw">**bold** &amp; &lt;b&gt;</div>`, `data-view="rendered"`, `data-view="raw"`,
		"localStorage.getItem('me-view')", "html.raw .msg .body.raw{display:block}",
		`.composer textarea{flex:1;`, `font-family:"IBM Plex Mono"`, "IBM+Plex+Mono:wght@400",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	if i, j := strings.Index(body, "localStorage.getItem('me-view')"), strings.Index(body, "</head>"); i < 0 || i > j {
		t.Error("the saved view must be applied in <head>, before first paint")
	}
}
