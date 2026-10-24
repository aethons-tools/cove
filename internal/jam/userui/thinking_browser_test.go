//go:build browser

package userui

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/aethons-tools/cove/internal/jam/sessionevents"
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
}
