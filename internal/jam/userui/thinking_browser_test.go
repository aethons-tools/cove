//go:build browser

package userui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/sessionevents"
	"github.com/aethons-tools/cove/internal/jam/sessionview"
)

// thinkRaw is one stream-json line as the store keeps it.
func thinkRaw(v map[string]any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func assistantBlock(block map[string]any) []byte {
	return thinkRaw(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{block}}})
}

func toolResultBlock(tid, text string, isErr bool) []byte {
	return thinkRaw(map[string]any{"type": "user", "message": map[string]any{"content": []any{
		map[string]any{"type": "tool_result", "tool_use_id": tid, "content": text, "is_error": isErr}}}})
}

// The Thinking tab pairs a tool call with its result, filters, searches and
// switches format (remembered, search aside), follows new events, loads
// earlier pages, and shows an orphan result on its own line.
func TestBrowserThinking(t *testing.T) {
	ctx := browserCtx(t)
	e, p, personal, store, hub := thinkEnv(t)
	actor := personal.ActorID
	cur, old := strings.Repeat("c", 32), strings.Repeat("d", 32)
	t0 := time.Now().Add(-10 * time.Minute)
	add := func(ev sessionevents.Event) {
		t.Helper()
		ev.ActorID, ev.Kind = actor, sessionevents.KindEvent
		if ev.ReceivedAt.IsZero() {
			ev.ReceivedAt = ev.ObservedAt
		}
		if err := store.Append(ev); err != nil {
			t.Fatal(err)
		}
	}
	// The older stream: an orphan tool_result, then 304 text events.
	add(sessionevents.Event{StreamID: old, Seq: 1, Turn: 1, ObservedAt: t0.Add(-time.Hour),
		Raw: toolResultBlock("tu0", "orphaned output", false), Index: sessionevents.Index{Type: "user", ToolName: "Read"}})
	for s := uint64(2); s <= 305; s++ {
		add(sessionevents.Event{StreamID: old, Seq: s, Turn: 1, ObservedAt: t0.Add(-time.Hour + time.Duration(s)*time.Millisecond),
			Raw: assistantBlock(map[string]any{"type": "text", "text": fmt.Sprintf("old line %d", s)}), Index: sessionevents.Index{Type: "assistant"}})
	}
	// The current stream.
	cevs := []sessionevents.Event{
		{Raw: assistantBlock(map[string]any{"type": "thinking", "thinking": "pondering the tests"}), Index: sessionevents.Index{Type: "assistant"}},
		{Raw: assistantBlock(map[string]any{"type": "text", "text": "I will run the tests."}), Index: sessionevents.Index{Type: "assistant"}},
		{Raw: assistantBlock(map[string]any{"type": "tool_use", "id": "tu1", "name": "Bash", "input": map[string]any{"command": "go test ./..."}}),
			Index: sessionevents.Index{Type: "assistant", ToolName: "Bash"}},
		{Raw: toolResultBlock("tu1", "ok  \tpkg\t0.1s", false), Index: sessionevents.Index{Type: "user", ToolName: "Bash"}},
		{Raw: assistantBlock(map[string]any{"type": "tool_use", "id": "tu2", "name": "Edit", "input": map[string]any{"file_path": "/x.go"}}),
			Index: sessionevents.Index{Type: "assistant", ToolName: "Edit"}},
		{Raw: toolResultBlock("tu2", "no match", true), Index: sessionevents.Index{Type: "user", ToolName: "Edit", IsError: true}},
		{Raw: thinkRaw(map[string]any{"type": "result", "result": "done"}), Index: sessionevents.Index{Type: "result", CostUSD: 0.12, DurationMS: 38000}},
	}
	at := []time.Duration{0, 500 * time.Millisecond, time.Second, 3100 * time.Millisecond, 4 * time.Second, 5 * time.Second, 6 * time.Second}
	for i, ev := range cevs {
		ev.StreamID, ev.Seq, ev.Turn, ev.ObservedAt = cur, uint64(i+1), 1, t0.Add(at[i])
		add(ev)
	}
	seq := uint64(len(cevs))
	publish := func(n int) {
		for range n {
			seq++
			ev := sessionevents.Event{ActorID: actor, StreamID: cur, Seq: seq, Turn: 2, Kind: sessionevents.KindEvent, ObservedAt: time.Now(), ReceivedAt: time.Now(),
				Raw: assistantBlock(map[string]any{"type": "text", "text": fmt.Sprintf("live %d", seq)}), Index: sessionevents.Index{Type: "assistant"}}
			if err := store.Append(ev); err != nil {
				t.Fatal(err)
			}
			hub.Publish(ev)
		}
	}

	h := withStatic(Handler(e.Deps, nil))
	srv := httptest.NewServer(asWho(h, p))
	defer func() { srv.CloseClientConnections(); srv.Close() }() // the open event stream
	home := e.homeOf(t, personal)
	poll := func(js string) chromedp.Action {
		return chromedp.Poll(js, nil, chromedp.WithPollingTimeout(5*time.Second))
	}
	eval := func(js string, out any) {
		t.Helper()
		if err := chromedp.Run(ctx, chromedp.Evaluate(js, out)); err != nil {
			t.Fatalf("%s: %v", js, err)
		}
	}
	run := func(what string, acts ...chromedp.Action) {
		t.Helper()
		if err := chromedp.Run(ctx, acts...); err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	const js = `var L=document.querySelector('#thinking .th-list');
		function shown(el){ return !!el && getComputedStyle(el).display!=='none' && el.getClientRects().length>0; }
		function line(k,tool){ return L.querySelector('.te[data-kind="'+k+'"]'+(tool?'[data-tool="'+tool+'"]':'')); }`
	chip := func(f string) chromedp.Action {
		return chromedp.Click(`#thinking .th-chips [data-f="`+f+`"]`, chromedp.ByQuery)
	}
	setSel := func(sel, val string) chromedp.Action {
		return chromedp.Evaluate(`(function(){var s=document.querySelector('`+sel+`'); s.value='`+val+`'; s.dispatchEvent(new Event('change',{bubbles:true}));})()`, nil)
	}
	open := chromedp.Tasks{
		chromedp.Navigate(srv.URL + "/app/user/?c=" + home + "&tab=thinking"),
		poll(`(function(){` + js + `var x=line('tool_call','Edit'); return !!x && x.querySelector('.te-status').textContent.includes('✗');})()`),
	}
	run("open", chromedp.EmulateViewport(1400, 700), open)

	// (a) Pairing.
	var a struct {
		BashStatus string
		Results    int
		EditErr    bool
		EditStatus string
	}
	eval(`(function(){`+js+`var b=line('tool_call','Bash'), d=line('tool_call','Edit');
		return {BashStatus:b.querySelector('.te-status').textContent, Results:L.querySelectorAll('.te[data-kind="tool_result"]').length,
			EditErr:d.classList.contains('err'), EditStatus:d.querySelector('.te-status').textContent};})()`, &a)
	if !strings.Contains(a.BashStatus, "✓") || !strings.Contains(a.BashStatus, "2.1s") || a.Results != 0 || !a.EditErr || !strings.Contains(a.EditStatus, "✗") {
		t.Fatalf("(a) pairing: %+v", a)
	}

	// (b) Chips.
	var ok bool
	run("thinking chip", chip("thinking"))
	eval(`(function(){`+js+`return !shown(line('thinking')) && shown(line('text'));})()`, &ok)
	if !ok {
		t.Fatal("(b) the Thinking chip doesn't hide thinking lines")
	}
	run("errors chip", chip("errors"))
	var tools []string
	eval(`(function(){`+js+`return Array.prototype.filter.call(L.querySelectorAll('.k-tool_call,.k-tool_result'), shown).map(function(x){return x.dataset.tool;});})()`, &tools)
	if len(tools) != 1 || tools[0] != "Edit" {
		t.Fatalf("(b) errors only shows %v", tools)
	}
	run("errors chip off", chip("errors"))

	// (c) Tool select.
	var opts []string
	eval(`Array.prototype.map.call(document.querySelectorAll('#thinking .th-tool option'), function(o){return o.value;})`, &opts)
	if strings.Join(opts, ",") != ",Bash,Edit" {
		t.Fatalf("(c) tool options %q", opts)
	}
	run("tool Edit", setSel("#thinking .th-tool", "Edit"))
	eval(`(function(){`+js+`return !shown(line('tool_call','Bash')) && shown(line('tool_call','Edit'));})()`, &ok)
	if !ok {
		t.Fatal("(c) choosing Edit doesn't hide Bash")
	}
	run("tool all", setSel("#thinking .th-tool", ""))

	// (d) Search.
	run("search", chromedp.SendKeys(`#thinking .th-search`, "go test", chromedp.ByQuery))
	eval(`(function(){`+js+`return shown(line('tool_call','Bash')) && !shown(line('text'));})()`, &ok)
	if !ok {
		t.Fatal("(d) search 'go test'")
	}

	// (e) Formats.
	run("detailed", setSel("#thinking .th-format", "detailed"))
	eval(`(function(){`+js+`var o=line('tool_call','Bash').querySelector('.te-out'); return shown(o) && o.textContent.includes('ok');})()`, &ok)
	if !ok {
		t.Fatal("(e) Detailed doesn't show the Bash output")
	}
	run("raw", setSel("#thinking .th-format", "raw"),
		poll(`(function(){`+js+`var r=line('tool_call','Bash').querySelector('.te-raw'); return shown(r) && r.textContent.includes('"tool_use"');})()`))
	run("detailed again", setSel("#thinking .th-format", "detailed"))

	// (f) Persistence across a reload: chips and format, not search.
	run("reload", open)
	var f struct {
		Thinking, Format, Search string
		Hidden                   bool
	}
	eval(`(function(){`+js+`var t=document.querySelector('#thinking');
		return {Thinking:t.querySelector('[data-f="thinking"]').getAttribute('aria-pressed'), Format:t.querySelector('.th-format').value,
			Search:t.querySelector('.th-search').value, Hidden:!shown(line('thinking'))};})()`, &f)
	if f.Thinking != "false" || !f.Hidden || f.Format != "detailed" || f.Search != "" {
		t.Fatalf("(f) after reload: %+v", f)
	}
	run("reset", chip("thinking"), setSel("#thinking .th-format", "narrative"))

	// (g) Following.
	atBottom := `(function(){var L=document.querySelector('#thinking .th-list'); return L.scrollHeight-L.scrollTop-L.clientHeight<16;})()`
	publish(40)
	run("follow", poll(`!!document.querySelector('#thinking .te[data-seq="`+fmt.Sprint(seq)+`"]')`), chromedp.Sleep(100*time.Millisecond))
	var g struct {
		Bottom, Overflow, NewShown bool
	}
	eval(`(function(){var L=document.querySelector('#thinking .th-list');
		return {Bottom:`+atBottom+`, Overflow:L.scrollHeight>L.clientHeight+50, NewShown:!document.querySelector('#thinking .th-new').hidden};})()`, &g)
	if !g.Bottom || !g.Overflow || g.NewShown {
		t.Fatalf("(g) following at the bottom: %+v", g)
	}
	run("scroll up", chromedp.Evaluate(`document.querySelector('#thinking .th-list').scrollTop=0`, nil), chromedp.Sleep(100*time.Millisecond))
	publish(1)
	run("new count", poll(`(function(){var b=document.querySelector('#thinking .th-new'); return !b.hidden && b.querySelector('span').textContent==='1';})()`))
	eval(atBottom, &ok)
	if ok {
		t.Fatal("(g) scrolled to the bottom while reading above")
	}

	// (h) Load earlier, on the older stream.
	run("older stream", setSel("#thinking .th-stream", old),
		poll(`(function(){var L=document.querySelector('#thinking .th-list'); var m=L.querySelector('.te-more'); return !!m && m.dataset.before==='6' && L.querySelectorAll('.te').length===300;})()`))
	run("load earlier", chromedp.Click(`#thinking .te-more`, chromedp.ByQuery),
		poll(`document.querySelectorAll('#thinking .th-list .te').length===305`))
	var h2 struct {
		First  string
		More   bool
		Orphan bool
	}
	eval(`(function(){`+js+`return {First:L.querySelector('.te').dataset.seq, More:!!L.querySelector('.te-more'),
		Orphan:shown(L.querySelector('.te[data-kind="tool_result"][data-tid="tu0"]'))};})()`, &h2)
	if h2.First != "1" || h2.More {
		t.Fatalf("(h) after Load earlier: %+v", h2)
	}
	// (i) The orphan result (its call was never loaded) is its own line.
	if !h2.Orphan {
		t.Fatal("(i) orphan tool_result not shown")
	}

	// (j) The line cap on an inserted page: reload the stream, lower the cap
	// to 302, Load earlier: the newest lines go, the oldest stay, following
	// pauses behind the pill, which reloads the live tail.
	run("reload stream", setSel("#thinking .th-stream", old),
		poll(`(function(){var L=document.querySelector('#thinking .th-list'); return !!L.querySelector('.te-more') && L.querySelectorAll('.te').length===300;})()`),
		chromedp.Evaluate(`meThMax=302`, nil),
		chromedp.Click(`#thinking .te-more`, chromedp.ByQuery),
		poll(`!!document.querySelector('#thinking .th-list .te[data-seq="1"]')`))
	var j struct {
		Count       int
		First, Last string
		Pill        bool
	}
	eval(`(function(){`+js+`var all=L.querySelectorAll('.te'), b=document.querySelector('#thinking .th-new');
		return {Count:all.length, First:all[0].dataset.seq, Last:all[all.length-1].dataset.seq, Pill:!b.hidden};})()`, &j)
	if j.Count > 302 || j.First != "1" || j.Last == "305" || !j.Pill {
		t.Fatalf("(j) Load earlier past the cap: %+v", j)
	}
	run("pill reloads the tail", chromedp.Click(`#thinking .th-new`, chromedp.ByQuery),
		poll(`(function(){var L=document.querySelector('#thinking .th-list'), all=L.querySelectorAll('.te');
			return all.length===300 && all[all.length-1].dataset.seq==='305' && document.querySelector('#thinking .th-new').hidden;})()`))

	// (k) Raw's observer lets go of lines the caps remove (off-screen ones,
	// not yet fetched): the oldest by meThCap, the newest by meThTrimNewest.
	const leaked = `Array.from(window.__obs).filter(function(n){ return !n.isConnected; }).length`
	run("spy", chromedp.Evaluate(`window.__obs=new Set(); (function(){var P=IntersectionObserver.prototype, o=P.observe, u=P.unobserve, d=P.disconnect;
		P.observe=function(n){ __obs.add(n); return o.call(this, n); }; P.unobserve=function(n){ __obs.delete(n); return u.call(this, n); };
		P.disconnect=function(){ __obs.clear(); return d.call(this); };})()`, nil),
		setSel("#thinking .th-format", "raw"), chromedp.Sleep(200*time.Millisecond))
	var k struct{ Observed, AfterCap, AfterTrim int }
	eval(`window.__obs.size`, &k.Observed)
	eval(`(function(){ meThMax=250; meThCap(); return `+leaked+`; })()`, &k.AfterCap)
	run("scroll top", chromedp.Evaluate(`document.querySelector('#thinking .th-list').scrollTop=0`, nil), chromedp.Sleep(200*time.Millisecond))
	eval(`(function(){ meThMax=200; meThTrimNewest(); return `+leaked+`; })()`, &k.AfterTrim)
	if k.Observed == 0 || k.AfterCap != 0 || k.AfterTrim != 0 {
		t.Fatalf("(k) removed lines still observed: %+v", k)
	}
	run("narrative", setSel("#thinking .th-format", "narrative"))
}

// Load earlier when the top line is an orphan result whose call is on the
// page: the call pairs it, and the button keeps working. Result dividers
// show each one's cost since the previous loaded result of its episode.
func TestBrowserThinkingEarlier(t *testing.T) {
	oldTail, oldPage := thinkingTail, thinkingPageSize
	thinkingTail, thinkingPageSize = 2, 3
	defer func() { thinkingTail, thinkingPageSize = oldTail, oldPage }()
	ctx := browserCtx(t)
	e, p, personal, store, _ := thinkEnv(t)
	stream := strings.Repeat("e", 32)
	t0 := time.Now().Add(-10 * time.Minute)
	result := func(cost float64, ms int64) (json.RawMessage, sessionevents.Index) {
		return thinkRaw(map[string]any{"type": "result", "result": "done"}), sessionevents.Index{Type: "result", CostUSD: cost, DurationMS: ms}
	}
	evs := []sessionevents.Event{
		{Raw: assistantBlock(map[string]any{"type": "text", "text": "one"}), Index: sessionevents.Index{Type: "assistant"}},
		{}, // result .10
		{Raw: assistantBlock(map[string]any{"type": "text", "text": "two"}), Index: sessionevents.Index{Type: "assistant"}},
		{}, // result .25
		{Raw: assistantBlock(map[string]any{"type": "tool_use", "id": "tuA", "name": "Bash", "input": map[string]any{"command": "ls"}}),
			Index: sessionevents.Index{Type: "assistant", ToolName: "Bash"}},
		{Raw: toolResultBlock("tuA", "a.go", false), Index: sessionevents.Index{Type: "user", ToolName: "Bash"}},
		{}, // result .45
	}
	evs[1].Raw, evs[1].Index = result(0.10, 1000)
	evs[3].Raw, evs[3].Index = result(0.25, 2000)
	evs[6].Raw, evs[6].Index = result(0.45, 3000)
	for i, ev := range evs {
		ev.ActorID, ev.Kind, ev.StreamID, ev.Seq, ev.Turn = personal.ActorID, sessionevents.KindEvent, stream, uint64(i+1), 1
		ev.ObservedAt = t0.Add(time.Duration(i) * time.Second)
		ev.ReceivedAt = ev.ObservedAt
		if err := store.Append(ev); err != nil {
			t.Fatal(err)
		}
	}
	h := withStatic(Handler(e.Deps, nil))
	srv := httptest.NewServer(asWho(h, p))
	defer func() { srv.CloseClientConnections(); srv.Close() }()
	home := e.homeOf(t, personal)
	poll := func(js string) chromedp.Action {
		return chromedp.Poll(js, nil, chromedp.WithPollingTimeout(5*time.Second))
	}
	run := func(what string, acts ...chromedp.Action) {
		t.Helper()
		if err := chromedp.Run(ctx, acts...); err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	type state struct {
		Errs       []string
		Seqs       []string
		Dividers   map[string]string
		BashStatus string
		Orphans    int
		More       string
		MoreOff    bool
	}
	get := func() state {
		t.Helper()
		var s state
		run("state", chromedp.Evaluate(`(function(){var L=document.querySelector('#thinking .th-list'), d={};
			L.querySelectorAll('.te[data-kind="result"]').forEach(function(n){ d[n.dataset.seq]=n.querySelector('.te-text').textContent; });
			var c=L.querySelector('.te[data-kind="tool_call"]'), m=L.querySelector('.te-more');
			return {Errs:window.__errs, Seqs:Array.prototype.map.call(L.querySelectorAll('.te'), function(n){return n.dataset.seq;}), Dividers:d,
				BashStatus:c ? c.querySelector('.te-status').textContent : '', Orphans:L.querySelectorAll('.te[data-kind="tool_result"]').length,
				More:m ? m.dataset.before : '', MoreOff:!!m && m.disabled};})()`, &s))
		return s
	}
	run("open", chromedp.EmulateViewport(1400, 700),
		chromedp.Navigate(srv.URL+"/app/user/?c="+home+"&tab=thinking"),
		chromedp.Evaluate(`window.__errs=[]; window.addEventListener('error', function(e){ __errs.push(e.message); });
			window.addEventListener('unhandledrejection', function(e){ __errs.push(String(e.reason)); });`, nil),
		poll(`document.querySelectorAll('#thinking .th-list .te').length===2 && !!document.querySelector('#thinking .te-more')`))
	s := get()
	if s.Orphans != 1 || s.Dividers["7"] != "episode 1 · $0.45 episode total · 3s" {
		t.Fatalf("tail: %+v", s)
	}
	// Page 1 (seqs 3-5): the call (5) pairs the orphan result (6).
	run("load earlier", chromedp.Click(`#thinking .te-more`, chromedp.ByQuery),
		poll(`!!document.querySelector('#thinking .te[data-seq="3"]') && !!document.querySelector('#thinking .te-more:not([disabled])')`))
	s = get()
	if len(s.Errs) != 0 || strings.Join(s.Seqs, ",") != "3,4,5,7" || s.Orphans != 0 || !strings.Contains(s.BashStatus, "✓") ||
		s.More != "3" || s.MoreOff {
		t.Fatalf("after page 1: %+v", s)
	}
	if s.Dividers["4"] != "episode 1 · $0.25 episode total · 2s" || s.Dividers["7"] != "episode 1 · $0.20 · 3s" {
		t.Fatalf("dividers after page 1: %+v", s.Dividers)
	}
	// Page 2 (seqs 1-2): the button still works; every delta is recomputed.
	run("load earlier again", chromedp.Click(`#thinking .te-more`, chromedp.ByQuery),
		poll(`!!document.querySelector('#thinking .te[data-seq="1"]')`))
	s = get()
	if len(s.Errs) != 0 || strings.Join(s.Seqs, ",") != "1,2,3,4,5,7" || s.More != "" {
		t.Fatalf("after page 2: %+v", s)
	}
	want := map[string]string{"2": "episode 1 · $0.10 episode total · 1s", "4": "episode 1 · $0.15 · 2s", "7": "episode 1 · $0.20 · 3s"}
	for k, v := range want {
		if s.Dividers[k] != v {
			t.Errorf("divider %s = %q, want %q", k, s.Dividers[k], v)
		}
	}
}

// thinkOpen serves the inbox with a Thinking tab on a session with a few
// events, opens it busy (so a "working…" line is last) and returns the
// server and a JS state reader.
func thinkOpen(t *testing.T, ctx context.Context) (*env, jam.Participant, *httptest.Server, string, func(string) string) {
	t.Helper()
	e, p, personal, store, _ := thinkEnv(t)
	appendText(t, store, personal.ActorID, 1, 3)
	srv := httptest.NewServer(asWho(withStatic(Handler(e.Deps, nil)), p))
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })
	home := e.homeOf(t, personal)
	if err := chromedp.Run(ctx, chromedp.EmulateViewport(1400, 700),
		chromedp.Navigate(srv.URL+"/app/user/?c="+home+"&tab=thinking"),
		chromedp.Poll(`document.querySelectorAll('#thinking .th-list .te').length===3`, nil, chromedp.WithPollingTimeout(5*time.Second)),
		chromedp.Evaluate(`document.querySelector('#session-head .sh-state').classList.add('busy'); meThWorking();`, nil)); err != nil {
		t.Fatalf("open: %v", err)
	}
	return e, p, srv, home, func(cls string) string {
		t.Helper()
		var out string
		js := `(function(){var L=document.querySelector('#thinking .th-list'), m=L.querySelector('.` + cls + `');
			return m ? m.textContent+(m.nextElementSibling && m.nextElementSibling.classList.contains('te-working') ? ' [before working]' : '') : '';})()`
		if err := chromedp.Run(ctx, chromedp.Poll(js, &out, chromedp.WithPollingTimeout(10*time.Second))); err != nil {
			t.Fatalf("waiting for .%s: %v", cls, err)
		}
		return out
	}
}

// A viewer who loses the session sees why, above the "working…" line.
func TestBrowserThinkingGone(t *testing.T) {
	old := sessionview.KeepAlive
	sessionview.KeepAlive = 50 * time.Millisecond
	defer func() { sessionview.KeepAlive = old }()
	ctx := browserCtx(t)
	e, p, _, home, msg := thinkOpen(t, ctx)
	if err := e.st.LeaveChannel(ident.ID(home), p.UserID, 0); err != nil {
		t.Fatal(err)
	}
	if got := msg("te-gone"); got != "This session is no longer available to you [before working]" {
		t.Fatalf("gone message %q", got)
	}
}

// A feed the server refuses (429: too many open) is retried with backoff,
// then the tab says so.
func TestBrowserThinkingTooMany(t *testing.T) {
	ctx := browserCtx(t)
	_, _, srv, _, msg := thinkOpen(t, ctx)
	var id string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`meTh.id`, &id)); err != nil {
		t.Fatal(err)
	}
	// hold opens a feed of the same viewer, kept open until the test ends.
	hold := func() int {
		c, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		req, _ := http.NewRequestWithContext(c, "GET", srv.URL+"/app/user/session/"+id+"/thinking/events", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
		}
		return resp.StatusCode
	}
	for range maxThinkingStreams - 1 { // with the page's own: full
		if c := hold(); c != http.StatusOK {
			t.Fatalf("feed refused early: %d", c)
		}
	}
	// Free the page's slot and take it, then reconnect the page.
	if err := chromedp.Run(ctx, chromedp.Evaluate(`meThBackoff=[20, 20, 20]; meTh.es.close();`, nil)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for hold() != http.StatusOK {
		if time.Now().After(deadline) {
			t.Fatal("the page's feed never released its slot")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(`meThConnect(meTh.pinned, false)`, nil)); err != nil {
		t.Fatal(err)
	}
	if got := msg("te-gone"); got != "Too many Thinking views open — close one, or reload [before working]" {
		t.Fatalf("refused message %q", got)
	}
}

// Prompts get a line; events the view doesn't render show only their type,
// muted, with no detail; thinking without visible text is a muted
// "thinking" line, a run of them collapsing; every line shows its time, and
// a tool's ✓/✗ and duration sit under its summary.
func TestBrowserThinkingKinds(t *testing.T) {
	ctx := browserCtx(t)
	e, p, personal, store, _ := thinkEnv(t)
	stream := strings.Repeat("e", 32)
	t0 := time.Date(2026, 10, 24, 14, 31, 2, 0, time.Local)
	raws := [][]byte{
		thinkRaw(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": "You were woken:\nnew squawk"}}),
		thinkRaw(map[string]any{"type": "system", "subtype": "hook_response", "output": "hook output"}),
		assistantBlock(map[string]any{"type": "thinking", "thinking": "", "signature": "a"}),
		thinkRaw(map[string]any{"type": "system", "subtype": "thinking_tokens"}), // hidden progress: doesn't break the run
		assistantBlock(map[string]any{"type": "thinking", "thinking": "", "signature": "b"}),
		assistantBlock(map[string]any{"type": "tool_use", "id": "tu1", "name": "Bash", "input": map[string]any{"command": "ls"}}),
		toolResultBlock("tu1", "a\nb", false),
	}
	for i, raw := range raws {
		ev := sessionevents.Event{ActorID: personal.ActorID, StreamID: stream, Seq: uint64(i + 1), Turn: 1, Kind: sessionevents.KindEvent,
			ObservedAt: t0.Add(time.Duration(i) * time.Second), Raw: raw}
		ev.ReceivedAt = ev.ObservedAt
		if err := store.Append(ev); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(asWho(withStatic(Handler(e.Deps, nil)), p))
	defer func() { srv.CloseClientConnections(); srv.Close() }()
	var got struct {
		Prompt, Other, Think, Status, Time string
		OtherDetail, Thinks                int
		StatusInLine                       bool
	}
	if err := chromedp.Run(ctx,
		chromedp.Navigate(srv.URL+"/app/user/?c="+e.homeOf(t, personal)+"&tab=thinking"),
		chromedp.Poll(`!!document.querySelector('#thinking .te[data-kind="tool_call"][data-rseq]')`, nil, chromedp.WithPollingTimeout(5*time.Second)),
		chromedp.Evaluate(`(function(){var L=document.querySelector('#thinking .th-list'), q=function(s){var x=L.querySelector(s); if(!x) throw new Error("missing "+s); return x};
			var call=q('.te[data-kind="tool_call"]');
			return {Prompt:q('.k-prompt .te-text').textContent, Other:q('.k-other .te-text').textContent,
				OtherDetail:L.querySelectorAll('.k-other .te-detail').length,
				Think:q('.k-thinking .te-text').textContent, Thinks:L.querySelectorAll('.k-thinking:not(.te-absorbed)').length,
				Status:call.querySelector('.te-meta .te-status').textContent, StatusInLine:!!call.querySelector('.te-line .te-status'),
				Time:q('.k-prompt .te-time').textContent};})()`, &got),
	); err != nil {
		t.Fatal(err)
	}
	if got.Prompt != "You were woken:" || got.Other != "system/hook_response" || got.OtherDetail != 0 {
		t.Errorf("prompt/other = %+v", got)
	}
	if got.Think != "thinking ×2" || got.Thinks != 1 {
		t.Errorf("empty thinking = %q (%d lines)", got.Think, got.Thinks)
	}
	// Showing Progress puts the progress line between them: the run splits.
	var split int
	if err := chromedp.Run(ctx, chromedp.Click(`#thinking .th-chips [data-f="progress"]`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelectorAll('#thinking .k-thinking:not(.te-absorbed)').length`, &split)); err != nil || split != 2 {
		t.Errorf("with Progress shown, %d thinking lines (%v); want 2", split, err)
	}
	if !strings.HasPrefix(got.Status, "✓") || got.StatusInLine {
		t.Errorf("status = %q (in the summary line: %v)", got.Status, got.StatusInLine)
	}
	if got.Time != "14:31:02" {
		t.Errorf("time = %q", got.Time)
	}
}
