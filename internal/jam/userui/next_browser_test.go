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
