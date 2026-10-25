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
// to its parent when ?post= asks (a number posts as a number).
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
		fmt.Fprintf(w, `<script>parent.postMessage(%s, '*')</script>`, b)
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
