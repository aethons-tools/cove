//go:build browser

package userui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// The session pane opens from the header's tabs, moves below, resizes, and
// is remembered per session; a session never opened starts from the last
// layout used, and ?tab= opens a tab.
func TestBrowserSessionPane(t *testing.T) {
	ctx := browserCtx(t)
	e, p, personal, standing, _ := sessionEnv(t)
	h := withStatic(Handler(e.Deps, nil))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, asPrincipal(r, p, false))
	}))
	defer srv.Close()
	open := func(inst string, extra string) chromedp.Action {
		return chromedp.Tasks{
			chromedp.Navigate(srv.URL + "/app/user/?c=" + inst + extra),
			chromedp.WaitVisible(`#session-head`, chromedp.ByQuery),
		}
	}
	type layout struct {
		Open     bool   `json:"open"`
		Place    string `json:"place"`
		Selected string `json:"selected"`
		Body     string `json:"body"`
		Size     int    `json:"size"`
	}
	read := func(l *layout) chromedp.Action {
		return chromedp.Evaluate(`(function(){
			var s=document.getElementById('cv-split'), a=document.getElementById('side-pane');
			var sel=document.querySelector('.sh-tabs button[aria-selected="true"]');
			var r=a.getBoundingClientRect();
			return {open:!a.hidden, place:s.dataset.place, selected:sel?sel.dataset.tab:'', body:document.getElementById('side-pane-body').textContent,
				size:Math.round(s.dataset.place==='right'?r.width:r.height)};
		})()`, l)
	}
	waitBody := func(text string) chromedp.Action {
		return chromedp.Poll(`document.getElementById('side-pane-body').textContent.includes(`+"`"+text+"`"+`)`, nil, chromedp.WithPollingTimeout(5*time.Second))
	}
	pHome, sHome := e.homeOf(t, personal), e.homeOf(t, standing)
	var l layout
	if err := chromedp.Run(ctx, chromedp.EmulateViewport(1400, 900), open(pHome, ""), read(&l)); err != nil {
		t.Fatal(err)
	}
	if l.Open {
		t.Fatal("the pane starts open with no layout saved")
	}
	// Open Status: beside the messages, the tab marked.
	if err := chromedp.Run(ctx, chromedp.Click(`.sh-tabs [data-tab="status"]`, chromedp.ByQuery), waitBody("Turns"), read(&l)); err != nil {
		t.Fatal(err)
	}
	if !l.Open || l.Place != "right" || l.Selected != "status" || !strings.Contains(l.Body, "$0.42") {
		t.Fatalf("after Status: %+v", l)
	}
	// Widen it from the keyboard, then move it below.
	before := l.Size
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`document.querySelector('.pane-div').dispatchEvent(new KeyboardEvent('keydown',{key:'ArrowLeft',bubbles:true}))`, nil),
		read(&l)); err != nil {
		t.Fatal(err)
	}
	if l.Size != before+24 {
		t.Fatalf("resize: %d → %d", before, l.Size)
	}
	if err := chromedp.Run(ctx, chromedp.Click(`.sp-place`, chromedp.ByQuery), read(&l)); err != nil {
		t.Fatal(err)
	}
	if l.Place != "below" {
		t.Fatalf("after ⇄: %+v", l)
	}
	// A session never opened starts from that layout.
	if err := chromedp.Run(ctx, open(sHome, ""), waitBody("Up"), read(&l)); err != nil {
		t.Fatal(err)
	}
	if !l.Open || l.Place != "below" || l.Selected != "status" {
		t.Fatalf("new session: %+v", l)
	}
	// Closing it there leaves the first session's pane as it was.
	if err := chromedp.Run(ctx, chromedp.Click(`.sh-tabs [data-tab="status"]`, chromedp.ByQuery), read(&l)); err != nil {
		t.Fatal(err)
	}
	if l.Open || l.Selected != "" {
		t.Fatalf("toggle closed: %+v", l)
	}
	if err := chromedp.Run(ctx, open(pHome, ""), waitBody("Turns"), read(&l)); err != nil {
		t.Fatal(err)
	}
	if !l.Open || l.Place != "below" || l.Selected != "status" {
		t.Fatalf("back to the first session: %+v", l)
	}
	// ?tab= opens a tab; a narrow window puts the pane below.
	if err := chromedp.Run(ctx, open(sHome, "&tab=artifacts"), waitBody("Nothing shared"), read(&l)); err != nil {
		t.Fatal(err)
	}
	if !l.Open || l.Selected != "artifacts" {
		t.Fatalf("?tab=artifacts: %+v", l)
	}
	if err := chromedp.Run(ctx, chromedp.Click(`.sp-place`, chromedp.ByQuery), chromedp.EmulateViewport(800, 900), chromedp.Sleep(100*time.Millisecond), read(&l)); err != nil {
		t.Fatal(err)
	}
	if l.Place != "below" {
		t.Fatalf("narrow: %+v", l)
	}
	// The composer stays under the messages.
	var composerInMain bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(`!!document.querySelector('.cv-main .composer')`, &composerInMain)); err != nil || !composerInMain {
		t.Fatalf("composer outside the messages column (%v)", err)
	}
}
