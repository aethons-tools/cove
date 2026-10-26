//go:build browser

package userui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cdplog "github.com/chromedp/cdproto/log"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/appui"
	"github.com/aethons-tools/cove/internal/jam/uiassets"
)

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

	// CSP violations arrive on the Log domain as "security" entries (not
	// console calls); page exceptions on Runtime.
	var problems []string
	chromedp.ListenTarget(ctx, func(ev any) {
		switch e := ev.(type) {
		case *cdplog.EventEntryAdded:
			if e.Entry.Source == cdplog.SourceSecurity || e.Entry.Level == cdplog.LevelError {
				problems = append(problems, string(e.Entry.Source)+": "+e.Entry.Text)
			}
		case *runtime.EventExceptionThrown:
			problems = append(problems, "exception: "+e.ExceptionDetails.Error())
		}
	})
	var label string
	err := chromedp.Run(ctx,
		cdplog.Enable(),
		chromedp.Navigate(srv.URL+"/app/next/c/"+engID),
		chromedp.WaitVisible(`a[href="/app/next/c/`+engID+`"]`, chromedp.ByQuery),
		chromedp.Text(`a[href="/app/next/c/`+engID+`"]`, &label, chromedp.ByQuery),
		chromedp.WaitVisible(`a[href="/app/user/?c=`+engID+`"]`, chromedp.ByQuery),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(label, "eng") {
		t.Errorf("rail link = %q, want eng", label)
	}
	for _, pr := range problems {
		t.Errorf("page problem: %s", pr)
	}
}
