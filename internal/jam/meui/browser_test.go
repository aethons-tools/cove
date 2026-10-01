//go:build browser

// Browser tests drive the real /me page in headless Chrome, covering the
// composer JS that the string-presence tests in handler_test.go can only see as
// source. Run with `just test-browser`; each test skips when no browser is
// installed (see browserCtx).
package meui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/chromedp"

	"github.com/aethons-tools/cove/internal/jam"
)

// browserCtx returns a chromedp context on a fresh headless browser, skipping
// the test when none is found. COVE_BROWSER names the binary explicitly;
// otherwise the first of the usual names on PATH is used.
func browserCtx(t *testing.T) context.Context {
	t.Helper()
	path := os.Getenv("COVE_BROWSER")
	for _, name := range []string{"chrome-headless-shell", "headless-shell", "chromium", "google-chrome"} {
		if path != "" {
			break
		}
		path, _ = exec.LookPath(name)
	}
	if path == "" {
		t.Skip("no headless browser found (set COVE_BROWSER or install chrome-headless-shell)")
	}
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(path),
		// The sandbox VM has no user namespaces for Chrome's own sandbox.
		chromedp.NoSandbox,
	)
	actx, cancelA := chromedp.NewExecAllocator(context.Background(), opts...)
	ctx, cancelB := chromedp.NewContext(actx)
	ctx, cancelC := context.WithTimeout(ctx, 30*time.Second)
	t.Cleanup(func() { cancelC(); cancelB(); cancelA() })
	return ctx
}

// serveInbox serves the fixture inbox as participant p on a local server.
func serveInbox(t *testing.T) *httptest.Server {
	t.Helper()
	store, log, p := fixture()
	h := Handler(store, log, nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, jam.WithParticipant(r, p))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// pasteJS fires a paste event carrying text at the focused element, as the
// browser does for a real paste; headless Chrome has no system clipboard.
const pasteJS = `(function(text){
  var dt=new DataTransfer(); dt.setData('text/plain', text);
  document.activeElement.dispatchEvent(new ClipboardEvent('paste',{clipboardData:dt, bubbles:true, cancelable:true}));
})`

func TestBrowserComposerPastesAsCode(t *testing.T) {
	ctx := browserCtx(t)
	srv := serveInbox(t)
	box := `.composer textarea`

	// Ctrl-Shift-V (the non-Mac binding: headless Chrome reports a Linux
	// platform) marks the box, so the next paste is wrapped. The pasted text
	// has its own ``` run, so the fence must grow to four backticks.
	var wrapped string
	err := chromedp.Run(ctx,
		chromedp.Navigate(srv.URL+"/me/?c="+url.QueryEscape("named:eng")),
		chromedp.WaitVisible(box),
		chromedp.SendKeys(box, "see:"),
		chromedp.KeyEvent("V", chromedp.KeyModifiers(input.ModifierCtrl, input.ModifierShift)),
		chromedp.Evaluate(pasteJS+"('a ```b``` c')", nil),
		chromedp.Value(box, &wrapped),
	)
	if err != nil {
		t.Fatal(err)
	}
	if want := "see:\n````\na ```b``` c\n````\n"; wrapped != want {
		t.Errorf("paste as code: box = %q, want %q", wrapped, want)
	}

	// Any other key clears the mark: a later plain paste is left to the
	// browser (and a synthetic paste inserts nothing), so the box is unchanged.
	var plain string
	err = chromedp.Run(ctx,
		chromedp.SetValue(box, "x"),
		chromedp.KeyEvent("V", chromedp.KeyModifiers(input.ModifierCtrl, input.ModifierShift)),
		chromedp.SendKeys(box, "y"),
		chromedp.Evaluate(pasteJS+"('z')", nil),
		chromedp.Value(box, &plain),
	)
	if err != nil {
		t.Fatal(err)
	}
	if plain != "xy" {
		t.Errorf("plain paste after another key: box = %q, want %q", plain, "xy")
	}
}
