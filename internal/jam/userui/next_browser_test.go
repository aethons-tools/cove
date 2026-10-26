//go:build browser

package userui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/chromedp/cdproto/cdp"
	cdplog "github.com/chromedp/cdproto/log"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/appui"
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
		chromedp.WaitVisible(chromedp.CSS(`a[href="/app/user/?c=`+engID+`"]`)),
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
