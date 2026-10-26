//go:build browser

package userui

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	cdplog "github.com/chromedp/cdproto/log"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"

	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/appui"
	"github.com/aethons-tools/cove/internal/jam/sessionevents"
	"github.com/aethons-tools/cove/internal/jam/uiassets"
)

// pageProblems collects, from when its step runs, the tab's CSP violations
// (Log "security" entries — they never reach the console API), error log
// entries, and uncaught exceptions.
type pageProblems struct {
	mu   sync.Mutex
	list []string
}

func (p *pageProblems) add(s string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.list = append(p.list, s)
}

func (p *pageProblems) all() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.list...)
}

func (p *pageProblems) watch(ctx context.Context) step {
	return chromedp.Func(func(_ context.Context, tg *chromedp.Target) error {
		// Subscribe before enabling, so the entries Enable replays arrive too.
		go func() {
			for e, err := range cdp.Events(ctx, tg, cdplog.EntryAdded) {
				if err != nil {
					return
				}
				if e.Entry.Source == cdplog.EntrySourceSecurity || e.Entry.Level == cdplog.EntryLevelError {
					p.add(string(e.Entry.Source) + ": " + e.Entry.Text)
				}
			}
		}()
		go func() {
			for e, err := range cdp.Events(ctx, tg, runtime.ExceptionThrown) {
				if err != nil {
					return
				}
				p.add("exception: " + e.ExceptionDetails.Text)
			}
		}()
		return nil
	})
}

// The Jam app loads under its CSP, shows the member's rail from the API, and
// deep-links on reload (spec slice 1).
func TestBrowserNextAppShowsRail(t *testing.T) {
	if !uiassets.AppBuilt() {
		t.Skip("the Jam app bundle isn't built (just app)")
	}
	ctx := browserCtx(t)
	e, p := fixture()
	api, app := Handler(e.Deps, nil), Next(e.Deps, nil, nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = appui.WithPrincipal(jam.WithParticipant(r, p), appui.Principal{User: &p})
		if strings.HasPrefix(r.URL.Path, "/app/next/") {
			app.ServeHTTP(w, r)
			return
		}
		api.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	var problems pageProblems
	rail := chromedp.CSS(`a[href="/app/next/c/` + engID + `"]`)
	var label string
	err := chromedp.Do(ctx,
		problems.watch(ctx),
		call(cdplog.Enable, cdp.Empty{}),
		call(runtime.Enable, cdp.Empty{}),
		chromedp.Navigate(srv.URL+"/app/next/c/"+engID),
		chromedp.WaitVisible(rail),
		into(&label, chromedp.Text(rail)),
		chromedp.WaitVisible(chromedp.CSS(`h2`)), // the conversation opened from the deep link
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(label, "eng") {
		t.Errorf("rail link = %q, want eng", label)
	}
	for _, pr := range problems.all() {
		t.Errorf("page problem: %s", pr)
	}
}

// testChanges is a Changes source the test fires by hand (the events stream's
// `changed`).
type testChanges struct {
	mu   sync.Mutex
	subs []chan struct{}
}

func (c *testChanges) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	c.mu.Lock()
	c.subs = append(c.subs, ch)
	c.mu.Unlock()
	return ch, func() {}
}

func (c *testChanges) fire() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, ch := range c.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// The conversation renders squawkrender's markdown (code blocks get a copy
// button), raw HTML stays inert under the CSP, a long conversation opens at
// its newest message, and a new message arrives live (events → refetch).
func TestBrowserNextConversation(t *testing.T) {
	if !uiassets.AppBuilt() {
		t.Skip("the Jam app bundle isn't built (just app)")
	}
	ctx := browserCtx(t)
	e, p := fixture()
	for i := 0; i < 60; i++ {
		e.post(p.UserID, fmt.Sprintf("filler %d", i), "")
	}
	e.post(p.UserID, "```\ngo test ./...\n```\n<img src=x onerror=\"window.__pwned=1\"> <script>window.__pwned=2</script>", "")
	changes := &testChanges{}
	api, app := Handler(e.Deps, nil, WithChanges(changes)), Next(e.Deps, nil, nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = appui.WithPrincipal(jam.WithParticipant(r, p), appui.Principal{User: &p})
		if strings.HasPrefix(r.URL.Path, "/app/next/") {
			app.ServeHTTP(w, r)
			return
		}
		api.ServeHTTP(w, r)
	}))
	// The open events stream would hold srv.Close until it times out.
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })

	var problems pageProblems
	var pwned, atBottom bool
	var codeButtons int
	err := chromedp.Do(ctx,
		problems.watch(ctx),
		call(cdplog.Enable, cdp.Empty{}),
		call(runtime.Enable, cdp.Empty{}),
		chromedp.Navigate(srv.URL+"/app/next/c/"+engID),
		chromedp.WaitVisible(chromedp.CSS(`button[aria-label="Copy code"]`)),
		into(&codeButtons, chromedp.Evaluate[int](`document.querySelectorAll('button[aria-label="Copy code"]').length`)),
		into(&pwned, chromedp.Evaluate[bool](`!!window.__pwned || !!document.querySelector('[data-seq] img[onerror], [data-seq] script')`)),
		chromedp.Sleep(300*time.Millisecond), // layout settles, then the scroll pin
		// The messages' viewport (the rail's comes first in the DOM).
		into(&atBottom, chromedp.Evaluate[bool](`(() => { const v = document.querySelector('[data-seq]')?.closest('.mantine-ScrollArea-viewport'); return !!v && v.scrollHeight - v.scrollTop - v.clientHeight < 40; })()`)),
	)
	if err != nil {
		t.Fatal(err)
	}
	if codeButtons != 1 {
		t.Errorf("code copy buttons = %d, want 1", codeButtons)
	}
	if pwned {
		t.Error("raw HTML in a body ran or reached the DOM")
	}
	if !atBottom {
		t.Error("a long conversation didn't open at its newest message")
	}

	// A new message shows up without a reload: the stream's `changed`
	// invalidates the conversation (an invalidation refetches even within
	// the queries' staleTime).
	e.post(p.UserID, "arrived live", "")
	changes.fire()
	var last string
	for i := 0; i < 50 && !strings.Contains(last, "arrived live"); i++ {
		changes.fire() // idempotent: covers a fire before the EventSource connected
		if err := chromedp.Do(ctx, into(&last, chromedp.Evaluate[string](`[...document.querySelectorAll('[data-seq]')].pop()?.innerText ?? ''`))); err != nil {
			t.Fatal(err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !strings.Contains(last, "arrived live") {
		t.Errorf("new message didn't arrive; last row = %q", last)
	}
	for _, pr := range problems.all() {
		t.Errorf("page problem: %s", pr)
	}
}

// Typing a reply and pressing Ctrl+Enter (headless Chrome reports a Linux
// platform) sends it through POST api/messages; it shows in the list, and
// the box is empty again. Shift+Ctrl+V pastes as code.
func TestBrowserNextComposerSends(t *testing.T) {
	if !uiassets.AppBuilt() {
		t.Skip("the Jam app bundle isn't built (just app)")
	}
	ctx := browserCtx(t)
	e, p := fixture()
	e.Send = jam.NewParticipantSendHandler(e.st, e.Intercom, nil)
	changes := &testChanges{}
	api, app := Handler(e.Deps, nil, WithChanges(changes)), Next(e.Deps, nil, nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = appui.WithPrincipal(jam.WithParticipant(r, p), appui.Principal{User: &p})
		if strings.HasPrefix(r.URL.Path, "/app/next/") {
			app.ServeHTTP(w, r)
			return
		}
		api.ServeHTTP(w, r)
	}))
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })

	box := chromedp.CSS(`textarea[aria-label="Message eng"]`)
	var problems pageProblems
	var value string
	err := chromedp.Do(ctx,
		problems.watch(ctx),
		call(cdplog.Enable, cdp.Empty{}),
		call(runtime.Enable, cdp.Empty{}),
		chromedp.Navigate(srv.URL+"/app/next/c/"+engID),
		chromedp.WaitVisible(box),
		chromedp.SendKeys(box, "sent from the app"),
		chromedp.KeyEvent("\r", chromedp.KeyModifiers(chromedp.ModifierCtrl)),
	)
	if err != nil {
		t.Fatal(err)
	}
	var last string
	for i := 0; i < 50 && !strings.Contains(last, "sent from the app"); i++ {
		if err := chromedp.Do(ctx, into(&last, chromedp.Evaluate[string](`[...document.querySelectorAll('[data-seq]')].pop()?.innerText ?? ''`))); err != nil {
			t.Fatal(err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !strings.Contains(last, "sent from the app") {
		t.Errorf("sent message didn't show; last row = %q", last)
	}
	if err := chromedp.Do(ctx, into(&value, chromedp.Value(box))); err != nil || value != "" {
		t.Errorf("box after send = %q, %v", value, err)
	}
	ms := e.lg.ChannelSince(e.room.ID, 0, 0)
	if got := ms[len(ms)-1]; got.Body != "sent from the app" || got.From != p.UserID {
		t.Errorf("log's last = %+v", got)
	}

	// Paste as code (the composer marks the box on the shortcut's keydown and
	// wraps the real paste that follows).
	err = chromedp.Do(ctx,
		copy(srv.URL, "a ```b``` c"),
		chromedp.SendKeys(box, "see:"),
		chromedp.KeyEvent("V", chromedp.KeyModifiers(chromedp.ModifierCtrl, chromedp.ModifierShift)),
		into(&value, chromedp.Value(box)),
	)
	if err != nil {
		t.Fatal(err)
	}
	if want := "see:\n````\na ```b``` c\n````\n"; value != want {
		t.Errorf("paste as code: box = %q, want %q", value, want)
	}
	for _, pr := range problems.all() {
		t.Errorf("page problem: %s", pr)
	}
}

// An ask renders as a card; choosing an option records the answer (the
// log's ask state) and the card shows who chose what.
func TestBrowserNextAnswersAnAsk(t *testing.T) {
	if !uiassets.AppBuilt() {
		t.Skip("the Jam app bundle isn't built (just app)")
	}
	ctx := browserCtx(t)
	e, p := fixture()
	m := e.ask(intercom.AskEvent{Question: "Merge PR #12?", Options: yesNoOpts})
	changes := &testChanges{}
	api, app := Handler(e.Deps, nil, WithChanges(changes)), Next(e.Deps, nil, nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = appui.WithPrincipal(jam.WithParticipant(r, p), appui.Principal{User: &p})
		if strings.HasPrefix(r.URL.Path, "/app/next/") {
			app.ServeHTTP(w, r)
			return
		}
		api.ServeHTTP(w, r)
	}))
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })

	var problems pageProblems
	var text string
	hold := chromedp.CSS(`#ask-` + m.ID + ` button:nth-of-type(2)`) // Ship it, Hold, Other…
	err := chromedp.Do(ctx,
		problems.watch(ctx),
		call(cdplog.Enable, cdp.Empty{}),
		call(runtime.Enable, cdp.Empty{}),
		chromedp.Navigate(srv.URL+"/app/next/c/"+engID),
		chromedp.WaitVisible(hold),
		chromedp.Click(hold),
		chromedp.WaitVisible(chromedp.CSS(`#ask-`+m.ID+` b`)),
		into(&text, chromedp.Text(chromedp.CSS(`#ask-`+m.ID))),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "alice chose") || !strings.Contains(text, "Hold") {
		t.Errorf("card after answering = %q", text)
	}
	if a, ok := e.Intercom.GetAsk(m.ID); !ok || a.Answer == nil || a.Answer.Choice != "no" {
		t.Errorf("ask state = %+v", a)
	}
	for _, pr := range problems.all() {
		t.Errorf("page problem: %s", pr)
	}
}

// A session's home channel shows its header; Status opens the pane beside
// the messages, the keyboard widens it, ⇄ moves it below, and a session
// never opened starts from that layout — under the app's CSP.
func TestBrowserNextSessionPane(t *testing.T) {
	if !uiassets.AppBuilt() {
		t.Skip("the Jam app bundle isn't built (just app)")
	}
	ctx := browserCtx(t)
	e, p, personal, standing, _ := sessionEnv(t)
	api, app := Handler(e.Deps, nil), Next(e.Deps, nil, nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = asPrincipal(r, p, false)
		if strings.HasPrefix(r.URL.Path, "/app/next/") {
			app.ServeHTTP(w, r)
			return
		}
		api.ServeHTTP(w, r)
	}))
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })

	type layout struct {
		Open  bool   `json:"open"`
		Place string `json:"place"`
		Body  string `json:"body"`
		Size  int    `json:"size"`
	}
	var l layout
	read := into(&l, chromedp.Evaluate[layout](`(function(){
		var a=document.querySelector('aside[aria-label="Session pane"]'), s=a&&a.closest('[data-place]');
		if(!a) return {open:false, place:'', body:'', size:0};
		var r=a.getBoundingClientRect();
		return {open:true, place:s.dataset.place, body:a.textContent, size:Math.round(s.dataset.place==='right'?r.width:r.height)};
	})()`))
	waitBody := func(text string) step {
		return chromedp.Poll[chromedp.Void](`(document.getElementById('session-pane-body')||{textContent:''}).textContent.includes(`+"`"+text+"`"+`)`, chromedp.WithPollingTimeout(5*time.Second))
	}
	var problems pageProblems
	tab := chromedp.CSS(`[role="tab"][data-tab="status"]`)
	err := chromedp.Do(ctx,
		problems.watch(ctx),
		call(cdplog.Enable, cdp.Empty{}),
		call(runtime.Enable, cdp.Empty{}),
		chromedp.EmulateViewport(1400, 900),
		chromedp.Navigate(srv.URL+"/app/next/c/"+e.homeOf(t, personal)),
		chromedp.WaitVisible(tab),
		chromedp.Click(tab),
		waitBody("Turns"),
		read,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !l.Open || l.Place != "right" || !strings.Contains(l.Body, "$0.42") || l.Size != 380 {
		t.Fatalf("after Status: %+v", l)
	}
	if err := chromedp.Do(ctx, chromedp.Focus(chromedp.CSS(`[role="separator"]`)), chromedp.KeyEvent(kb.ArrowLeft), read); err != nil {
		t.Fatal(err)
	}
	if l.Size != 404 {
		t.Fatalf("resize: 380 → %d, want 404", l.Size)
	}
	if err := chromedp.Do(ctx, chromedp.Click(chromedp.CSS(`[aria-label="Move the pane right or below"]`)), read); err != nil {
		t.Fatal(err)
	}
	if l.Place != "below" {
		t.Fatalf("after ⇄: %+v", l)
	}
	// A session never opened starts from that layout.
	if err := chromedp.Do(ctx, chromedp.Navigate(srv.URL+"/app/next/c/"+e.homeOf(t, standing)), waitBody("Up"), read); err != nil {
		t.Fatal(err)
	}
	if !l.Open || l.Place != "below" {
		t.Fatalf("new session: %+v", l)
	}
	for _, pr := range problems.all() {
		t.Errorf("page problem: %s", pr)
	}
}

// The app's Thinking tab: the backfill (agent markup inert), a tool call with
// its result folded in, a live event, and Load earlier — under the CSP.
func TestBrowserNextThinking(t *testing.T) {
	if !uiassets.AppBuilt() {
		t.Skip("the Jam app bundle isn't built (just app)")
	}
	ctx := browserCtx(t)
	e, p, personal, store, hub := thinkEnv(t)
	old := thinkingTail
	thinkingTail = 5
	t.Cleanup(func() { thinkingTail = old })
	actor, cur := personal.ActorID, strings.Repeat("c", 32)
	t0 := time.Now().Add(-time.Minute)
	add := func(seq uint64, raw []byte, ix sessionevents.Index, at time.Duration) {
		t.Helper()
		ev := sessionevents.Event{ActorID: actor, StreamID: cur, Seq: seq, Turn: 1, Kind: sessionevents.KindEvent, ObservedAt: t0.Add(at), ReceivedAt: t0.Add(at), Raw: raw, Index: ix}
		if err := store.Append(ev); err != nil {
			t.Fatal(err)
		}
	}
	for s := uint64(1); s <= 4; s++ {
		add(s, assistantBlock(map[string]any{"type": "text", "text": fmt.Sprintf("early %d", s)}), sessionevents.Index{Type: "assistant"}, time.Duration(s)*time.Millisecond)
	}
	add(5, assistantBlock(map[string]any{"type": "text", "text": `<img src=x onerror="document.title='pwned'">`}), sessionevents.Index{Type: "assistant"}, time.Second)
	add(6, assistantBlock(map[string]any{"type": "tool_use", "id": "tu1", "name": "Bash", "input": map[string]any{"command": "go test ./..."}}), sessionevents.Index{Type: "assistant", ToolName: "Bash"}, 2*time.Second)
	add(7, toolResultBlock("tu1", "ok  \tpkg\t0.1s", false), sessionevents.Index{Type: "user", ToolName: "Bash"}, 5100*time.Millisecond)

	api, app := Handler(e.Deps, nil), Next(e.Deps, nil, nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = asPrincipal(r, p, false)
		if strings.HasPrefix(r.URL.Path, "/app/next/") {
			app.ServeHTTP(w, r)
			return
		}
		api.ServeHTTP(w, r)
	}))
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })

	has := func(js string) step { return chromedp.Poll[chromedp.Void](js, chromedp.WithPollingTimeout(5*time.Second)) }
	text := `document.querySelector('[role="log"][aria-label="Session events"]').textContent`
	var problems pageProblems
	var title, status string
	err := chromedp.Do(ctx,
		problems.watch(ctx),
		call(cdplog.Enable, cdp.Empty{}),
		call(runtime.Enable, cdp.Empty{}),
		chromedp.EmulateViewport(1400, 900),
		chromedp.Navigate(srv.URL+"/app/next/c/"+e.homeOf(t, personal)+"?tab=thinking"),
		has(`!!document.querySelector('[role="log"][aria-label="Session events"]') && `+text+`.includes('go test')`),
		into(&status, chromedp.Text(chromedp.CSS(`[data-kind="tool_call"] .te-status`))),
		into(&title, chromedp.Evaluate[string](`document.title`)),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(status, "✓ 3.1s") || title == "pwned" {
		t.Fatalf("status %q, title %q", status, title)
	}
	// A live event, then the page below the tail.
	live := sessionevents.Event{ActorID: actor, StreamID: cur, Seq: 8, Turn: 2, Kind: sessionevents.KindEvent, ObservedAt: time.Now(), ReceivedAt: time.Now(),
		Raw: assistantBlock(map[string]any{"type": "text", "text": "live 8"}), Index: sessionevents.Index{Type: "assistant"}}
	if err := store.Append(live); err != nil {
		t.Fatal(err)
	}
	hub.Publish(live)
	if err := chromedp.Do(ctx,
		has(text+`.includes('live 8')`),
		chromedp.Click(chromedp.CSS(`.te-more`)),
		has(text+`.includes('early 2')`),
	); err != nil {
		t.Fatal(err)
	}
	for _, pr := range problems.all() {
		t.Errorf("page problem: %s", pr)
	}
}

// thinkingApp serves the app and its API for a Thinking scroll test: the
// session's stream holds n text events of lines lines each.
func thinkingApp(t *testing.T, n, lines int) (*env, string, *httptest.Server, func(seq uint64)) {
	t.Helper()
	e, p, personal, store, hub := thinkEnv(t)
	actor, cur := personal.ActorID, strings.Repeat("c", 32)
	t0 := time.Now().Add(-time.Hour)
	text := func(seq uint64) []byte {
		ls := make([]string, lines)
		for i := range ls {
			ls[i] = fmt.Sprintf("event %d line %d", seq, i)
		}
		return assistantBlock(map[string]any{"type": "text", "text": strings.Join(ls, "\n")})
	}
	for s := uint64(1); s <= uint64(n); s++ {
		ev := sessionevents.Event{ActorID: actor, StreamID: cur, Seq: s, Turn: 1, Kind: sessionevents.KindEvent, ObservedAt: t0.Add(time.Duration(s) * time.Second),
			ReceivedAt: t0.Add(time.Duration(s) * time.Second), Raw: text(s), Index: sessionevents.Index{Type: "assistant"}}
		if err := store.Append(ev); err != nil {
			t.Fatal(err)
		}
	}
	publish := func(seq uint64) {
		ev := sessionevents.Event{ActorID: actor, StreamID: cur, Seq: seq, Turn: 2, Kind: sessionevents.KindEvent, ObservedAt: time.Now(), ReceivedAt: time.Now(),
			Raw: text(seq), Index: sessionevents.Index{Type: "assistant"}}
		if err := store.Append(ev); err != nil {
			t.Fatal(err)
		}
		hub.Publish(ev)
	}
	api, app := Handler(e.Deps, nil), Next(e.Deps, nil, nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = asPrincipal(r, p, false)
		if strings.HasPrefix(r.URL.Path, "/app/next/") {
			app.ServeHTTP(w, r)
			return
		}
		api.ServeHTTP(w, r)
	}))
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })
	return e, e.homeOf(t, personal), srv, publish
}

const thinkingListJS = `document.querySelector('[role="log"][aria-label="Session events"]')`

// A burst of live events, with the reader at the bottom, stays followed:
// rows measured taller than estimated mustn't strand the reader above them.
func TestBrowserNextThinkingFollowsBursts(t *testing.T) {
	if !uiassets.AppBuilt() {
		t.Skip("the Jam app bundle isn't built (just app)")
	}
	ctx := browserCtx(t)
	_, home, srv, publish := thinkingApp(t, 60, 1)
	has := func(js string) step { return chromedp.Poll[chromedp.Void](js, chromedp.WithPollingTimeout(5*time.Second)) }
	if err := chromedp.Do(ctx, chromedp.EmulateViewport(1400, 700),
		chromedp.Navigate(srv.URL+"/app/next/c/"+home+"?tab=thinking"),
		has(`!!`+thinkingListJS+` && `+thinkingListJS+`.textContent.includes('event 60 line 0')`),
		chromedp.Sleep(300*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	for s := uint64(61); s <= 100; s++ {
		publish(s)
	}
	var gap int
	var pill bool
	if err := chromedp.Do(ctx,
		has(thinkingListJS+`.textContent.includes('event 100 line 0')`),
		chromedp.Sleep(500*time.Millisecond),
		into(&gap, chromedp.Evaluate[int](`(function(){var L=`+thinkingListJS+`; return Math.round(L.scrollHeight-L.scrollTop-L.clientHeight);})()`)),
		into(&pill, chromedp.Evaluate[bool](`!!document.querySelector('[aria-label="Show new events"]')`)),
	); err != nil {
		t.Fatal(err)
	}
	if gap > 16 || pill {
		t.Fatalf("after a burst: %dpx from the bottom, pill %v (want followed)", gap, pill)
	}
	// A reader who scrolled up stays where they are, told of what's new.
	var top int
	if err := chromedp.Do(ctx,
		chromedp.Evaluate[chromedp.Void](thinkingListJS+`.scrollTop=0`),
		chromedp.Sleep(200*time.Millisecond),
	); err != nil {
		t.Fatal(err)
	}
	publish(101)
	if err := chromedp.Do(ctx,
		has(`(function(){var b=document.querySelector('[aria-label="Show new events"]'); return !!b && b.textContent.includes('1');})()`),
		into(&top, chromedp.Evaluate[int](`Math.round(`+thinkingListJS+`.scrollTop)`)),
	); err != nil {
		t.Fatal(err)
	}
	if top > 50 {
		t.Fatalf("a reader scrolled to the top was moved to %dpx", top)
	}
}

// Load earlier keeps the reader's place in Detailed format, where rows are
// much taller than the virtualizer's estimate.
func TestBrowserNextThinkingEarlierKeepsPlace(t *testing.T) {
	if !uiassets.AppBuilt() {
		t.Skip("the Jam app bundle isn't built (just app)")
	}
	ctx := browserCtx(t)
	_, home, srv, _ := thinkingApp(t, 600, 6) // the real tail and page: 300 each
	has := func(js string) step { return chromedp.Poll[chromedp.Void](js, chromedp.WithPollingTimeout(5*time.Second)) }
	url := srv.URL + "/app/next/c/" + home + "?tab=thinking"
	// The row's offset from the top of the list.
	at := func(seq int) string {
		return fmt.Sprintf(`(function(){var L=%s, r=L.querySelector('[data-testid="te"][data-seq="%d"]'); return r ? Math.round(r.getBoundingClientRect().top-L.getBoundingClientRect().top) : -9999;})()`, thinkingListJS, seq)
	}
	var before, after int
	if err := chromedp.Do(ctx, chromedp.EmulateViewport(1400, 700),
		chromedp.Navigate(url),
		chromedp.Evaluate[chromedp.Void](`localStorage.setItem('jam-thinking', JSON.stringify({format:'detailed'}))`),
		chromedp.Navigate(url),
		has(`!!`+thinkingListJS+` && `+thinkingListJS+`.textContent.includes('event 600 line 0')`),
		chromedp.Evaluate[chromedp.Void](thinkingListJS+`.scrollTop=0`),
		has(`!!document.querySelector('.te-more')`), // a virtual row: rendered near the top only
		chromedp.Sleep(300*time.Millisecond),
		into(&before, chromedp.Evaluate[int](at(301))),
		chromedp.Click(chromedp.CSS(`.te-more`)),
		has(`!!`+thinkingListJS+`.querySelector('[data-testid="te"][data-seq="300"]') || `+at(301)+`!==`+fmt.Sprint(-9999)),
		chromedp.Sleep(500*time.Millisecond),
		into(&after, chromedp.Evaluate[int](at(301))),
	); err != nil {
		t.Fatal(err)
	}
	if before < 0 || after-before > 30 || before-after > 30 {
		t.Fatalf("row 301 was %dpx from the top before Load earlier, %dpx after (want its place kept)", before, after)
	}
}

// The app frames the Studio's IDE and a preview's View here from the frame
// route, under a CSP whose frame-src is the preview origin: the frame loads
// (status clears), a forged message from the page itself changes nothing,
// and a real frame-error from the frame is shown.
func TestBrowserNextStudio(t *testing.T) {
	if !uiassets.AppBuilt() {
		t.Skip("the Jam app bundle isn't built (just app)")
	}
	ctx := browserCtx(t)
	e, p, personal, _, _ := frameEnv(t)
	pv := httptest.NewServer(http.HandlerFunc(framePage))
	t.Cleanup(pv.Close)
	ff := &studioFrames{base: pv.URL, n: map[string]int{}, next: map[string][]string{}}
	e.Deps.Frames = ff
	api, app := Handler(e.Deps, nil), Next(e.Deps, nil, []string{pv.URL})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = asPrincipal(r, p, false)
		if strings.HasPrefix(r.URL.Path, "/app/next/") {
			app.ServeHTTP(w, r)
			return
		}
		api.ServeHTTP(w, r)
	}))
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })

	has := func(js string) step {
		return chromedp.Poll[chromedp.Void](js, chromedp.WithPollingTimeout(5*time.Second))
	}
	st := `(document.querySelector('#studio .st-status')||{textContent:'?'}).textContent`
	var problems pageProblems
	err := chromedp.Do(ctx,
		problems.watch(ctx),
		call(cdplog.Enable, cdp.Empty{}),
		call(runtime.Enable, cdp.Empty{}),
		chromedp.EmulateViewport(1400, 900),
		chromedp.Navigate(srv.URL+"/app/next/c/"+e.homeOf(t, personal)),
		chromedp.Click(chromedp.CSS(`[role="tab"][data-tab="studio"]`)),
		has(`!!document.querySelector('#studio iframe.st-iframe') && `+st+`===''`), // loaded
		chromedp.Evaluate[chromedp.Void](`window.postMessage({jam:'frame-error',status:403},'*')`),
		chromedp.Sleep(300*time.Millisecond),
		has(st+`===''`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if n := ff.mints("ide"); n != 1 {
		t.Fatalf("ide mints = %d, want 1", n)
	}
	// A real refusal from the frame itself; then View here frames a preview.
	ff.queue("ide", "post=403")
	if err := chromedp.Do(ctx,
		chromedp.Click(chromedp.CSS(`#studio .st-reload`)),
		has(st+`==='No longer shared with you.'`),
		chromedp.Click(chromedp.CSS(`[role="tab"][data-tab="previews"]`)),
		chromedp.Click(chromedp.CSS(`.pv-here`)),
		has(`!!document.querySelector('.pv-framed iframe.st-iframe') && document.querySelector('.pv-framed .st-status').textContent===''`),
	); err != nil {
		t.Fatal(err)
	}
	for _, pr := range problems.all() {
		t.Errorf("page problem: %s", pr)
	}
}
