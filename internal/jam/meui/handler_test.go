package meui

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
)

// engID is the fixture room's channel id.
const engID = "chn_01j9q3eeeeeeeeeeeeeeeeeeee"

// env is an inbox over a real store and channel log: project proj with
// alice (the viewer, bound to an OIDC identity), a room eng, and alice's
// "hi from alice" in it.
type env struct {
	Deps
	st    *jam.MemStore
	lg    *intercom.Log
	alice jam.User
	room  jam.Channel
}

var fixtureAt = time.Date(2026, 9, 28, 14, 3, 0, 0, time.UTC)

// post appends body from a participant into the room (trusted, as relay
// ingress does), at the fixture time.
func (e *env) post(from ident.ID, body, contentType string) intercom.Squawk {
	m, err := e.Intercom.PostTrusted(e.room, intercom.Squawk{From: from, Body: body, ContentType: contentType, At: fixtureAt})
	if err != nil {
		panic(err)
	}
	return m
}

func fixture() (*env, jam.Participant) {
	st := jam.NewMemStore()
	if err := st.CreateProject("proj"); err != nil {
		panic(err)
	}
	if err := jam.AddPerson(st, "proj", jam.Human{Name: "alice", Identity: []jam.OIDCIdentity{{Issuer: "https://idp", Subject: "sub-alice"}}}); err != nil {
		panic(err)
	}
	proj, _ := st.GetProject("proj")
	aliceID, _ := st.LookupName(ident.User, "alice")
	alice, _ := st.GetUser(aliceID)
	room, err := st.CreateChannel(jam.Channel{ID: engID, ProjectID: proj.ID, Kind: jam.SourceRoom, Key: "eng", Label: "eng"})
	if err != nil {
		panic(err)
	}
	lg := intercom.NewMemLog(nil)
	ic := jam.NewIntercom(st, func() (ident.ID, bool) { return "", false }, lg, nil, nil)
	e := &env{Deps: Deps{Store: st, Intercom: ic, Log: lg}, st: st, lg: lg, alice: alice, room: room}
	e.post(alice.ID, "hi from alice", "")
	p := jam.Participant{Issuer: "https://idp", Subject: "sub-alice", UserID: alice.ID, Projects: []string{"proj"}, Name: "alice"}
	return e, p
}

func TestInboxFullPageRendersRailAndConversation(t *testing.T) {
	e, p := fixture()
	h := Handler(e.Deps, nil)

	req := httptest.NewRequest("GET", "/me/?c="+url.QueryEscape(engID), nil)
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
	e, p := fixture()
	h := Handler(e.Deps, nil)
	req := httptest.NewRequest("GET", "/me/?c="+url.QueryEscape(engID), nil)
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
	e, p := fixture()
	h := Handler(e.Deps, nil)
	req := httptest.NewRequest("GET", "/me/?c="+url.QueryEscape(engID), nil)
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
	e, p := fixture()
	h := Handler(e.Deps, nil)
	req := httptest.NewRequest("GET", "/me/?c="+url.QueryEscape(engID), nil)
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
	e, p := fixture()
	h := Handler(e.Deps, nil)
	req := httptest.NewRequest("GET", "/me/?c="+url.QueryEscape(engID), nil)
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
	e, p := fixture()
	h := Handler(e.Deps, nil)
	req := httptest.NewRequest("GET", "/me/?c="+url.QueryEscape(engID), nil)
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
	e, p := fixture()
	h := Handler(e.Deps, nil)
	req := httptest.NewRequest("GET", "/me/stream?c="+url.QueryEscape(engID), nil)
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
	e, _ := fixture()
	h := Handler(e.Deps, nil)
	req := httptest.NewRequest("GET", "/me/", nil) // no WithParticipant
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("no-participant /me/ = %d, want 401", rec.Code)
	}
}

func TestMarkReadCommitsCursor(t *testing.T) {
	e, p := fixture()
	h := Handler(e.Deps, nil)

	form := url.Values{"channel": {engID}, "seq": {"1"}}
	req := httptest.NewRequest("POST", "/me/read", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = jam.WithParticipant(req, p)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != 204 {
		t.Fatalf("POST /me/read = %d, want 204", rec.Code)
	}
	if got := e.st.ChannelReads(p.UserID)[engID]; got != 1 {
		t.Errorf("cursor = %d, want 1", got)
	}
	// A channel the participant can't see is never marked.
	other, err := e.st.CreateChannel(jam.Channel{ProjectID: e.room.ProjectID, Kind: jam.SourceChat, Key: "x", Label: "x"})
	if err != nil {
		t.Fatal(err)
	}
	form = url.Values{"channel": {string(other.ID)}, "seq": {"5"}}
	req = httptest.NewRequest("POST", "/me/read", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(httptest.NewRecorder(), jam.WithParticipant(req, p))
	if _, ok := e.st.ChannelReads(p.UserID)[other.ID]; ok {
		t.Error("a chat alice isn't in must not get a read cursor")
	}
}

func TestStreamRendersMarkdownAndPlain(t *testing.T) {
	e, p := fixture()
	e.post(e.alice.ID, "**bold** <script>x</script>", intercom.ContentMarkdown)
	e.post(e.alice.ID, "**literal** a_b", intercom.ContentPlain)
	h := Handler(e.Deps, nil)
	req := httptest.NewRequest("GET", "/me/stream?c="+url.QueryEscape(engID), nil)
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
	e, p := fixture()
	h := Handler(e.Deps, nil)
	req := httptest.NewRequest("GET", "/me/?c="+url.QueryEscape(engID), nil)
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
	e, p := fixture()
	h := Handler(e.Deps, nil)
	req := httptest.NewRequest("GET", "/me/?c="+url.QueryEscape(engID), nil)
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
	e, p := fixture()
	h := Handler(e.Deps, nil)
	req := httptest.NewRequest("GET", "/me/?c="+url.QueryEscape(engID), nil)
	req = jam.WithParticipant(req, p)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body := rec.Body.String()
	// Cmd-Down pushes the draft onto a per-conversation stack (tab
	// sessionStorage, with an in-memory fallback); a successful send pops it
	// back; Cmd-Up or the chip pops by hand, but never over text in the box.
	for _, want := range []string{
		"function mePushDraft(", "function mePopDraft(", "'me-drafts:'", "sessionStorage",
		"e.key==='ArrowDown'", "e.key==='ArrowUp'", "f.reset();\n       meKeepReply(f.body);\n       mePopDraft(f);",
		"if(t.value.trim()) return false;", ".draft-chip[hidden]{display:none}", "⌘↓",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing draft-stack wiring %q", want)
		}
	}
}

func TestComposerKeepsReply(t *testing.T) {
	e, p := fixture()
	h := Handler(e.Deps, nil)
	req := httptest.NewRequest("GET", "/me/?c="+url.QueryEscape(engID), nil)
	req = jam.WithParticipant(req, p)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body := rec.Body.String()
	// The reply box is kept per recipient in sessionStorage as it is typed and
	// restored on load (and in a New message composer); emptying it or a
	// successful send forgets it.
	for _, want := range []string{
		"function meKeepReply(", "function meRestoreReply(", "'me-reply:'", "sessionStorage.removeItem(meReplyKey(t.form))",
		"document.addEventListener('input'", "meRestoreReply(form);", "meRestoreReply(f); meDraftChip(f);",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing reply-persistence wiring %q", want)
		}
	}
}

func TestCopyControls(t *testing.T) {
	e, p := fixture()
	h := Handler(e.Deps, nil)
	req := httptest.NewRequest("GET", "/me/?c="+url.QueryEscape(engID), nil)
	req = jam.WithParticipant(req, p)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body := rec.Body.String()
	// Each bubble carries Text / Markdown copy buttons beside its sender; code
	// blocks get an icon button added client-side, on load and after each
	// stream swap. All show on hover or keyboard focus, always on touch.
	for _, want := range []string{
		`class="copy-text" onclick="meCopyBubble(this, false)"`, `class="copy-md" onclick="meCopyBubble(this, true)"`,
		"function meCopy(", "function meCopyBubble(", "function meCodeButtons(", "meCodeButtons(document);",
		"if(s && s.id==='stream'){ meCodeButtons(s); }", "@media (hover:none)", ".msg .acts:has(:focus-visible)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing copy-control wiring %q", want)
		}
	}
}

func TestRawViewToggleAndMonospaceComposer(t *testing.T) {
	e, p := fixture()
	e.post(e.alice.ID, "**bold** & <b>", "")
	h := Handler(e.Deps, nil)
	req := httptest.NewRequest("GET", "/me/?c="+url.QueryEscape(engID), nil)
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

func railFor(t *testing.T, h http.Handler, p jam.Participant) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, jam.WithParticipant(httptest.NewRequest("GET", "/me/rail", nil), p))
	return rec.Body.String()
}

// The rail lists the channels the viewer takes part in with their unread
// deliveries, then the legacy log's conversations as a read-only History.
func TestRailUnreadAndHistory(t *testing.T) {
	e, p := fixture()
	if err := jam.AddPerson(e.st, "proj", jam.Human{Name: "bob"}); err != nil {
		t.Fatal(err)
	}
	bob, _ := e.st.LookupName(ident.User, "bob")
	pl, err := e.Intercom.PlanPersonChat(bob, p.UserID)
	if err != nil {
		t.Fatal(err)
	}
	m, err := e.Intercom.Post(pl, intercom.Squawk{From: bob, Body: "ping"})
	if err != nil {
		t.Fatal(err)
	}
	legacy := intercom.NewLegacyMemLog()
	if _, err := legacy.Append(intercom.LegacySquawk{From: intercom.Target{Kind: "actor", Ref: "old-cove"}, To: []intercom.Target{{Kind: "human", Ref: "alice"}},
		Body: "from the old log", Project: "proj"}); err != nil {
		t.Fatal(err)
	}
	e.Legacy = legacy
	h := Handler(e.Deps, nil)

	rail := railFor(t, h, p)
	for _, want := range []string{">bob<", `<span class="badge unread">1</span>`, "History (before the upgrade)", `href="/me/?c=legacy%3a`} {
		if !strings.Contains(rail, want) {
			t.Errorf("rail missing %q:\n%s", want, rail)
		}
	}
	if err := e.st.CommitChannelRead(p.UserID, m.Channel, m.Seq); err != nil {
		t.Fatal(err)
	}
	if rail := railFor(t, h, p); strings.Contains(rail, "badge unread") {
		t.Errorf("read chat still unread:\n%s", rail)
	}

	// A History conversation reads but takes no reply.
	i := strings.Index(rail, `href="/me/?c=legacy%3a`)
	id := rail[i+len(`href="/me/?c=`) : i+strings.Index(rail[i:], `">`)]
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, jam.WithParticipant(httptest.NewRequest("GET", "/me/?c="+id, nil), p))
	body := rec.Body.String()
	if !strings.Contains(body, "from the old log") || !strings.Contains(body, `<div class="cv-readonly">`) || strings.Contains(body, `hx-post="/me/read"`) {
		t.Errorf("history conversation (%q) = %s", id, body[strings.Index(body, `<section class="convo"`):])
	}

	// Leaving the project takes its channels off the rail at once.
	if err := e.st.RemoveMember(e.room.ProjectID, p.UserID); err != nil {
		t.Fatal(err)
	}
	if rail := railFor(t, h, p); strings.Contains(rail, ">eng<") || strings.Contains(rail, ">bob<") {
		t.Errorf("rail after leaving the project:\n%s", rail)
	}
}

// History matches the viewer by their user name only: the legacy log is not
// project-scoped, so a pre-registry name that now belongs to someone else
// (a migration clash renamed the viewer) must not show that person's history.
func TestHistoryIsTheViewersNameOnly(t *testing.T) {
	e, p := fixture()
	legacy := intercom.NewLegacyMemLog()
	if _, err := legacy.Append(intercom.LegacySquawk{From: intercom.Target{Kind: "actor", Ref: "old-cove"}, To: []intercom.Target{{Kind: "human", Ref: "alice"}},
		Body: "for the other alice", Project: "elsewhere"}); err != nil {
		t.Fatal(err)
	}
	e.Legacy = legacy
	p.Name = "alice-proj" // as the clash renamed them
	if rail := railFor(t, Handler(e.Deps, nil), p); strings.Contains(rail, "History (before the upgrade)") {
		t.Errorf("a renamed viewer sees human:alice's history:\n%s", rail)
	}
}

// postAs posts a form to path as participant p.
func postAs(t *testing.T, h http.Handler, p jam.Participant, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, jam.WithParticipant(req, p))
	return rec
}

func pageFor(t *testing.T, h http.Handler, p jam.Participant, c string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, jam.WithParticipant(httptest.NewRequest("GET", "/me/?c="+url.QueryEscape(c), nil), p))
	return rec.Body.String()
}

func isIn(st *jam.MemStore, ch, p ident.ID) bool {
	for _, m := range st.ChannelMembers(ch) {
		if m.ParticipantID == p && !m.Left {
			return true
		}
	}
	return false
}

// A member calls someone in, leaves; a person who can see a channel joins it.
func TestCallInLeaveJoin(t *testing.T) {
	e, p := fixture()
	if err := jam.AddPerson(e.st, "proj", jam.Human{Name: "bob"}); err != nil {
		t.Fatal(err)
	}
	bob, _ := e.st.LookupName(ident.User, "bob")
	h := Handler(e.Deps, nil)

	page := pageFor(t, h, p, engID)
	for _, want := range []string{`hx-post="/me/leave"`, `hx-post="/me/call-in"`, `value="user:` + string(bob) + `"`} {
		if !strings.Contains(page, want) {
			t.Errorf("member's page lacks %q", want)
		}
	}
	if strings.Contains(page, `hx-post="/me/join"`) {
		t.Error("a member is offered Join")
	}
	if rec := postAs(t, h, p, "/me/call-in", url.Values{"channel": {engID}, "who": {"user:" + string(bob)}}); rec.Code != 204 || !isIn(e.st, e.room.ID, bob) {
		t.Fatalf("call-in = %d %s", rec.Code, rec.Body)
	}
	if rec := postAs(t, h, p, "/me/leave", url.Values{"channel": {engID}}); rec.Code != 204 || isIn(e.st, e.room.ID, p.UserID) {
		t.Fatalf("leave = %d %s", rec.Code, rec.Body)
	}
	if page := pageFor(t, h, p, engID); !strings.Contains(page, `hx-post="/me/join"`) || strings.Contains(page, `hx-post="/me/leave"`) {
		t.Errorf("a non-member who can see the room is offered Join, not Leave:\n%s", page)
	}
	if rec := postAs(t, h, p, "/me/join", url.Values{"channel": {engID}}); rec.Code != 204 || !isIn(e.st, e.room.ID, p.UserID) {
		t.Fatalf("join = %d %s", rec.Code, rec.Body)
	}
	for path, form := range map[string]url.Values{
		"/me/join":    {"channel": {"chn_01j9q3zzzzzzzzzzzzzzzzzz"}},
		"/me/call-in": {"channel": {engID}, "who": {"user:nobody"}},
	} {
		if rec := postAs(t, h, p, path, form); rec.Code == 204 {
			t.Errorf("%s %v succeeded", path, form)
		}
	}
	if rec := postAs(t, h, p, "/me/join", url.Values{}); rec.Code != http.StatusBadRequest {
		t.Errorf("join with no channel = %d", rec.Code)
	}
}
