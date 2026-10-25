//go:build browser

package userui

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/aethons-tools/cove/internal/jam/collab"
)

// studioFrames is a fake Frames minting URLs on a second server (the
// "preview"): /<name>?<query>, the query taken from a per-name queue
// (empty once drained). It counts the mints per name.
type studioFrames struct {
	mu   sync.Mutex
	base string
	n    map[string]int
	next map[string][]string
}

func (f *studioFrames) FrameURL(_ collab.Principal, _, _, name string, _ collab.Right) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n[name]++
	q := ""
	if l := f.next[name]; len(l) > 0 {
		q, f.next[name] = l[0], l[1:]
	}
	return f.base + "/" + name + "?" + q, nil
}

func (f *studioFrames) queue(name string, qs ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next[name] = append(f.next[name], qs...)
}

func (f *studioFrames) mints(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n[name]
}

// framePage is the fake preview: <p id="ok">name</p>, posting a frame-error
// to its parent when ?post= asks (a number posts as a number), ?late= ms later.
func framePage(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html><p id="ok">%s</p>`, html.EscapeString(name))
	if post := r.URL.Query().Get("post"); post != "" {
		var status any = post
		if n, err := strconv.Atoi(post); err == nil {
			status = n
		}
		b, _ := json.Marshal(map[string]any{"jam": "frame-error", "status": status})
		delay := r.URL.Query().Get("late") // ms: post that much later
		if delay == "" {
			delay = "0"
		}
		fmt.Fprintf(w, `<script>setTimeout(function(){ parent.postMessage(%s, '*'); }, %s)</script>`, b, strconv.Quote(delay))
	}
}

// The Studio tab frames the IDE (kept alive across tabs, replaced by
// Reload), widens the pane on first open, shows a frame's refusal, re-mints
// once on "signin", ignores messages not from its frame, and a preview's View
// here frames it in the pane.
func TestBrowserStudio(t *testing.T) {
	ctx := browserCtx(t)
	e, p, personal, _, _ := frameEnv(t)
	pv := httptest.NewServer(http.HandlerFunc(framePage))
	defer pv.Close()
	ff := &studioFrames{base: pv.URL, n: map[string]int{}, next: map[string][]string{}}
	e.Deps.Frames = ff
	e.Deps.PreviewURL = func(actorID, name string) (string, error) {
		return "https://" + name + "--" + actorID + ".preview.test/", nil
	}
	h := withStatic(Handler(e.Deps, nil))
	srv := httptest.NewServer(asWho(h, p))
	defer srv.Close()
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
	eval := func(js string, out any) {
		t.Helper()
		if err := chromedp.Run(ctx, chromedp.Evaluate(js, out)); err != nil {
			t.Fatalf("%s: %v", js, err)
		}
	}
	const js = `function F(){ return document.querySelector('#studio iframe.st-iframe'); }
		function S(){ return document.querySelector('#studio .st-status').textContent; }`
	status := func(want string) chromedp.Action {
		return poll(`(function(){` + js + `return S().includes(` + strconv.Quote(want) + `);})()`)
	}
	settle := func() { time.Sleep(400 * time.Millisecond) }

	// (1) Opening Studio frames the minted URL; the status clears on load.
	// (7) The first open, with no size saved, widens the pane to 60%.
	run("open the page", chromedp.EmulateViewport(1400, 900),
		chromedp.Navigate(srv.URL+"/app/user/?c="+home),
		chromedp.WaitVisible(`.sh-tabs [data-tab="studio"]`, chromedp.ByQuery),
		chromedp.Click(`.sh-tabs [data-tab="studio"]`, chromedp.ByQuery),
		poll(`(function(){`+js+`return !!F() && S()==='';})()`))
	var one struct {
		Src, Open, Allow, Referrer string
		Sandboxed                  bool
		Ratio                      float64
		Saved                      int
	}
	eval(`(function(){`+js+`var f=F(), s=document.getElementById('cv-split'), a=document.getElementById('side-pane');
		var saved=JSON.parse(localStorage.getItem('jam-pane:'+s.dataset.session)||'{}');
		return {Src:f.src, Open:document.querySelector('#studio .st-open').href, Allow:f.getAttribute('allow'), Referrer:f.getAttribute('referrerpolicy'),
			Sandboxed:f.hasAttribute('sandbox'), Ratio:a.getBoundingClientRect().width/s.getBoundingClientRect().width, Saved:saved.w||0};})()`, &one)
	if !strings.HasPrefix(one.Src, pv.URL+"/ide?") || one.Open != "https://ide--"+personal.ActorID+".preview.test/" {
		t.Fatalf("(1) frame src %q, open %q", one.Src, one.Open)
	}
	if one.Allow != "clipboard-read; clipboard-write" || one.Referrer != "no-referrer" || one.Sandboxed {
		t.Fatalf("(1) frame attributes: %+v", one)
	}
	if one.Ratio < 0.58 || one.Ratio > 0.62 || one.Saved == 0 {
		t.Fatalf("(7) the first open should widen the pane to 60%% and save it: %+v", one)
	}
	if n := ff.mints("ide"); n != 1 {
		t.Fatalf("(1) mints = %d, want 1", n)
	}

	// (2) Thinking and back keeps the same iframe; the pane's refresh leaves it.
	var ok bool
	run("to thinking and back",
		chromedp.Evaluate(`document.querySelector('#studio iframe').dataset.mark='1'`, nil),
		chromedp.Click(`.sh-tabs [data-tab="thinking"]`, chromedp.ByQuery),
		poll(`document.getElementById('side-pane-body').textContent.includes('events')`),
		chromedp.Click(`.sh-tabs [data-tab="studio"]`, chromedp.ByQuery),
		chromedp.Evaluate(`document.body.dispatchEvent(new Event('meChanged'))`, nil))
	settle()
	eval(`(function(){`+js+`var f=F(); return !!f && f.dataset.mark==='1' && f.getClientRects().length>0 && document.getElementById('side-pane-body').hidden;})()`, &ok)
	if !ok || ff.mints("ide") != 1 {
		t.Fatalf("(2) switching tabs should keep the same, visible iframe (mints %d)", ff.mints("ide"))
	}

	// (5) A message from anywhere but the frame is ignored, signin included.
	run("forge", chromedp.Evaluate(`window.postMessage({jam:'frame-error',status:403},'*'); window.postMessage({jam:'frame-error',status:'signin'},'*')`, nil))
	settle()
	var st string
	eval(`(function(){`+js+`return S();})()`, &st)
	if st != "" || ff.mints("ide") != 1 {
		t.Fatalf("(5) a forged message changed the view: status %q, mints %d", st, ff.mints("ide"))
	}

	// (3) Reload mints anew and replaces the iframe.
	run("reload", chromedp.Click(`#studio .st-reload`, chromedp.ByQuery),
		poll(`(function(){`+js+`var f=F(); return !!f && f.dataset.mark!=='1';})()`), status(""))
	if n := ff.mints("ide"); n != 2 {
		t.Fatalf("(3) reload mints = %d, want 2", n)
	}

	// (4) A frame posting a 403 shows it.
	ff.queue("ide", "post=403")
	run("403", chromedp.Click(`#studio .st-reload`, chromedp.ByQuery), status("No longer shared with you."))
	settle()
	eval(`(function(){`+js+`return S();})()`, &st)
	if st != "No longer shared with you." {
		t.Fatalf("(4) the load cleared the refusal: %q", st)
	}

	// "signin" re-mints exactly once for the same name, and loads it.
	ff.queue("ide", "post=signin")
	before := ff.mints("ide")
	run("signin", chromedp.Click(`#studio .st-reload`, chromedp.ByQuery),
		poll(`(function(){`+js+`var f=F(); return !!f && !f.src.includes('post=');})()`), status(""))
	settle()
	if n := ff.mints("ide") - before; n != 2 {
		t.Fatalf("signin: %d mints, want 2 (the reload, one re-mint)", n)
	}
	// Twice in a row: no more re-mints, and an offer to reopen.
	ff.queue("ide", "post=signin", "post=signin")
	before = ff.mints("ide")
	run("signin twice", chromedp.Click(`#studio .st-reload`, chromedp.ByQuery), status("Reopen this view"))
	settle()
	if n := ff.mints("ide") - before; n != 2 {
		t.Fatalf("signin twice: %d mints, want 2", n)
	}
	// "cookies" retries once; a second time says so.
	ff.queue("ide", "post=cookies", "post=cookies")
	before = ff.mints("ide")
	run("cookies", chromedp.Click(`#studio .st-reload`, chromedp.ByQuery),
		status("This browser can't sign in inside the pane — use Open in new tab."))
	if n := ff.mints("ide") - before; n != 2 {
		t.Fatalf("cookies: %d mints, want 2", n)
	}

	// (6) View here frames a preview in the Previews tab; back restores the list.
	run("view here",
		chromedp.Click(`.sh-tabs [data-tab="previews"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`#side-pane-body .pv-here[data-name="app"]`, chromedp.ByQuery),
		chromedp.Click(`#side-pane-body .pv-here[data-name="app"]`, chromedp.ByQuery),
		poll(`(function(){var f=document.querySelector('#side-pane-body iframe.st-iframe'); return !!f && f.src.indexOf(`+strconv.Quote(pv.URL+"/app?")+`)===0;})()`))
	var six struct {
		Text, Open string
		Rows       int
		StudioGone bool
	}
	eval(`(function(){var b=document.getElementById('side-pane-body'), k=document.getElementById('studio-keep');
		return {Text:b.textContent, Open:(b.querySelector('.st-open')||{}).href||'', Rows:b.querySelectorAll('.sp-row').length, StudioGone:!k || k.hidden};})()`, &six)
	if !strings.Contains(six.Text, "← All previews") || !strings.Contains(six.Text, "app") || six.Rows != 0 ||
		six.Open != "https://app--"+personal.ActorID+".preview.test/" || !six.StudioGone {
		t.Fatalf("(6) framed preview: %+v", six)
	}
	// The pane's refresh leaves the framed preview alone.
	run("refresh", chromedp.Evaluate(`document.querySelector('#side-pane-body iframe').dataset.mark='p'; document.body.dispatchEvent(new Event('meChanged'))`, nil))
	settle()
	eval(`(function(){var f=document.querySelector('#side-pane-body iframe'); return !!f && f.dataset.mark==='p';})()`, &ok)
	if !ok {
		t.Fatal("(6) the pane's refresh replaced the framed preview")
	}
	run("back", chromedp.Click(`#side-pane-body .pv-back`, chromedp.ByQuery),
		poll(`!!document.querySelector('#side-pane-body .sp-row') && !document.querySelector('#side-pane-body iframe')`))

	// Closing the pane removes the IDE's iframe.
	run("close", chromedp.Click(`.sh-tabs [data-tab="previews"]`, chromedp.ByQuery),
		poll(`document.getElementById('side-pane').hidden && !document.querySelector('iframe.st-iframe')`))
}

// frameGate wraps the user handler: the frame route answers code (when
// non-zero) or is held for hold before it is served.
type frameGate struct {
	mu   sync.Mutex
	code int
	hold time.Duration
	h    http.Handler
}

func (g *frameGate) set(code int, hold time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.code, g.hold = code, hold
}

func (g *frameGate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/frame") {
		g.mu.Lock()
		code, hold := g.code, g.hold
		g.mu.Unlock()
		if code != 0 {
			http.Error(w, "refused", code)
			return
		}
		time.Sleep(hold)
	}
	g.h.ServeHTTP(w, r)
}

// The pane's framed views at their edges: a remembered Studio tab stays
// closed for a viewer without ssh; a refused mint keeps the plain Open in new
// tab; a non-http(s) frame URL isn't loaded; a late message from a replaced
// frame can't fail the new one; a preview's 502 reads as such; and a page
// restored from the back/forward cache reopens the Studio.
func TestBrowserStudioEdges(t *testing.T) {
	ctx := browserCtx(t)
	e, p, personal, standing, _ := frameEnv(t)
	pv := httptest.NewServer(http.HandlerFunc(framePage))
	defer pv.Close()
	ff := &studioFrames{base: pv.URL, n: map[string]int{}, next: map[string][]string{}}
	e.Deps.Frames = ff
	e.Deps.PreviewURL = func(actorID, name string) (string, error) {
		return "https://" + name + "--" + actorID + ".preview.test/", nil
	}
	gate := &frameGate{h: withStatic(Handler(e.Deps, nil))}
	srv := httptest.NewServer(asWho(gate, p))
	defer srv.Close()
	home, sHome := e.homeOf(t, personal), e.homeOf(t, standing)

	poll := func(js string) chromedp.Action {
		return chromedp.Poll(js, nil, chromedp.WithPollingTimeout(5*time.Second))
	}
	run := func(what string, acts ...chromedp.Action) {
		t.Helper()
		if err := chromedp.Run(ctx, acts...); err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	eval := func(js string, out any) {
		t.Helper()
		if err := chromedp.Run(ctx, chromedp.Evaluate(js, out)); err != nil {
			t.Fatalf("%s: %v", js, err)
		}
	}
	const js = `function F(){ return document.querySelector('#studio iframe.st-iframe'); }
		function S(){ var x=document.querySelector('#studio .st-status'); return x ? x.textContent : null; }`
	status := func(want string) chromedp.Action {
		return poll(`(function(){` + js + `return S()===` + strconv.Quote(want) + `;})()`)
	}
	settle := func() { time.Sleep(400 * time.Millisecond) }
	openStudio := func(what string) {
		t.Helper()
		run(what, chromedp.Evaluate(`localStorage.clear()`, nil), chromedp.Navigate(srv.URL+"/app/user/?c="+home),
			chromedp.WaitVisible(`.sh-tabs [data-tab="studio"]`, chromedp.ByQuery),
			chromedp.Click(`.sh-tabs [data-tab="studio"]`, chromedp.ByQuery),
			poll(`!!document.querySelector('#studio')`))
	}

	// (1) A remembered Studio tab, or &tab=studio, stays closed for a viewer
	// without ssh (no Studio in the header), and isn't saved over.
	run("open", chromedp.EmulateViewport(1400, 900), chromedp.Navigate(srv.URL+"/app/user/?c="+home),
		chromedp.WaitVisible(`#session-head`, chromedp.ByQuery),
		chromedp.Evaluate(`localStorage.clear(); localStorage.setItem('jam-pane:last', JSON.stringify({tab:'studio'}))`, nil))
	var closed struct {
		Hidden, Button bool
		Last           string
	}
	for _, extra := range []string{"", "&tab=studio"} {
		run("no ssh"+extra, chromedp.Navigate(srv.URL+"/app/user/?c="+sHome+extra), chromedp.WaitVisible(`#session-head`, chromedp.ByQuery))
		settle()
		eval(`({Hidden:document.getElementById('side-pane').hidden, Button:!!document.querySelector('.sh-tabs [data-tab="studio"]'),
			Last:localStorage.getItem('jam-pane:last')})`, &closed)
		if !closed.Hidden || closed.Button || closed.Last != `{"tab":"studio"}` {
			t.Fatalf("(1) a remembered Studio without ssh%s: %+v", extra, closed)
		}
	}

	// (6, 7) A refused mint says so and keeps the plain Open in new tab.
	gate.set(http.StatusForbidden, 0)
	openStudio("refused")
	run("refused", status("No longer shared with you."))
	var open string
	eval(`document.querySelector('#studio .st-open').href`, &open)
	if open != "https://ide--"+personal.ActorID+".preview.test/" {
		t.Fatalf("(6) Open in new tab after a refused mint: %q", open)
	}
	gate.set(http.StatusTooManyRequests, 0)
	run("429", chromedp.Click(`#studio .st-reload`, chromedp.ByQuery), status("Too many views opened; wait a minute."))
	gate.set(0, 0)

	// (4) A frame URL that isn't http(s) isn't loaded.
	ff.mu.Lock()
	ff.base = "javascript:parent.document.body.dataset.pwned='1'//"
	ff.mu.Unlock()
	run("bad scheme", chromedp.Click(`#studio .st-reload`, chromedp.ByQuery), status("Couldn't open this view."))
	var pwned bool
	eval(`(function(){`+js+`return !!F() || document.body.dataset.pwned==='1';})()`, &pwned)
	if pwned {
		t.Fatal("(4) a javascript: frame URL was loaded")
	}
	ff.mu.Lock()
	ff.base = pv.URL
	ff.mu.Unlock()

	// (3) A frame that posts after a new mint has started can't fail the new frame.
	ff.queue("ide", "post=403&late=1000")
	run("late frame", chromedp.Click(`#studio .st-reload`, chromedp.ByQuery),
		poll(`(function(){`+js+`var f=F(); return !!f && f.src.includes('late=');})()`), status(""))
	gate.set(0, 2500*time.Millisecond)
	run("re-mint", chromedp.Click(`#studio .st-reload`, chromedp.ByQuery),
		poll(`(function(){`+js+`var f=F(); return !!f && !f.src.includes('late=');})()`))
	gate.set(0, 0)
	run("new frame loads", status(""))
	settle()
	var st string
	eval(`(function(){`+js+`return S();})()`, &st)
	if st != "" {
		t.Fatalf("(3) the replaced frame's late message failed the new one: %q", st)
	}

	// (5) Restored from the back/forward cache (pagehide emptied the keep),
	// the Studio opens again.
	before := ff.mints("ide")
	run("bfcache", chromedp.Evaluate(`window.dispatchEvent(new PageTransitionEvent('pagehide',{persisted:true}))`, nil),
		poll(`!document.querySelector('#studio')`),
		chromedp.Evaluate(`window.dispatchEvent(new PageTransitionEvent('pageshow',{persisted:true}))`, nil),
		poll(`(function(){`+js+`return !!F();})()`), status(""))
	if n := ff.mints("ide") - before; n != 1 {
		t.Fatalf("(5) bfcache restore: %d mints, want 1", n)
	}

	// (7) A framed preview posting 502 isn't answering.
	ff.queue("app", "post=502")
	run("preview 502",
		chromedp.Click(`.sh-tabs [data-tab="previews"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`#side-pane-body .pv-here[data-name="app"]`, chromedp.ByQuery),
		chromedp.Click(`#side-pane-body .pv-here[data-name="app"]`, chromedp.ByQuery),
		poll(`(function(){var x=document.querySelector('#side-pane-body .pv-status'); return !!x && x.textContent==="The preview isn't answering.";})()`))
}
