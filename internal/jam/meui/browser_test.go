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
	"strconv"
	"testing"
	"time"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/runtime"
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

// copy puts text on the browser's clipboard (headless Chrome keeps its own,
// in-process), so a paste shortcut pastes it as a real, trusted paste event.
// writeText is refused on later calls unless clipboard-read is granted too.
func copy(origin, text string) chromedp.Action {
	return chromedp.Tasks{
		browser.SetPermission(&browser.PermissionDescriptor{Name: "clipboard-write"}, browser.PermissionSettingGranted).WithOrigin(origin),
		browser.SetPermission(&browser.PermissionDescriptor{Name: "clipboard-read"}, browser.PermissionSettingGranted).WithOrigin(origin),
		chromedp.Evaluate(`navigator.clipboard.writeText(`+strconv.Quote(text)+`)`, nil,
			func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) }),
	}
}

func TestBrowserComposerPastesAsCode(t *testing.T) {
	ctx := browserCtx(t)
	srv := serveInbox(t)
	// ByQuery throughout: chromedp's default BySearch never matches on this
	// Chrome, so every selector action would wait out the deadline.
	box := `.composer textarea`

	// Ctrl-Shift-V (the non-Mac binding: headless Chrome reports a Linux
	// platform) is itself a paste shortcut, so the browser fires the paste and
	// the composer wraps it. The pasted text has its own ``` run, so the fence
	// must grow to four backticks.
	var wrapped string
	err := chromedp.Run(ctx,
		chromedp.Navigate(srv.URL+"/me/?c="+url.QueryEscape("named:eng")),
		chromedp.WaitVisible(box, chromedp.ByQuery),
		copy(srv.URL, "a ```b``` c"),
		chromedp.SendKeys(box, "see:", chromedp.ByQuery),
		chromedp.KeyEvent("V", chromedp.KeyModifiers(input.ModifierCtrl, input.ModifierShift)),
		chromedp.Value(box, &wrapped, chromedp.ByQuery),
	)
	if err != nil {
		t.Fatal(err)
	}
	if want := "see:\n````\na ```b``` c\n````\n"; wrapped != want {
		t.Errorf("paste as code: box = %q, want %q", wrapped, want)
	}

	// A plain Ctrl-V does not mark the box, so the browser's paste goes in
	// unwrapped.
	var plain string
	err = chromedp.Run(ctx,
		chromedp.Focus(box, chromedp.ByQuery),
		copy(srv.URL, "z"),
		chromedp.SetValue(box, "x", chromedp.ByQuery),
		chromedp.KeyEvent("v", chromedp.KeyModifiers(input.ModifierCtrl)),
		chromedp.Value(box, &plain, chromedp.ByQuery),
	)
	if err != nil {
		t.Fatal(err)
	}
	if plain != "xz" {
		t.Errorf("plain paste after paste as code: box = %q, want %q", plain, "xz")
	}
}

func TestBrowserComposerKeepsReplyAcrossNavigation(t *testing.T) {
	ctx := browserCtx(t)
	store, log, p := fixture()
	h := Handler(store, log, nil)
	// /me/send lives outside this package; answer it as a successful send.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/me/send" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.ServeHTTP(w, jam.WithParticipant(r, p))
	}))
	t.Cleanup(srv.Close)
	box := `.composer textarea`
	conv := srv.URL + "/me/?c=" + url.QueryEscape("named:eng")

	// A half-typed reply survives leaving the conversation (a full page load)
	// and coming back.
	var kept string
	err := chromedp.Run(ctx,
		chromedp.Navigate(conv),
		chromedp.WaitVisible(box, chromedp.ByQuery),
		chromedp.SendKeys(box, "half typed", chromedp.ByQuery),
		chromedp.Navigate(srv.URL+"/me/"),
		chromedp.Navigate(conv),
		chromedp.WaitVisible(box, chromedp.ByQuery),
		chromedp.Value(box, &kept, chromedp.ByQuery),
	)
	if err != nil {
		t.Fatal(err)
	}
	if kept != "half typed" {
		t.Errorf("after navigating away and back: box = %q, want %q", kept, "half typed")
	}

	// A successful send forgets it.
	var after string
	err = chromedp.Run(ctx,
		chromedp.Click(`.composer button[type=submit]`, chromedp.ByQuery),
		chromedp.Poll(`document.querySelector('.composer textarea').value===''`, nil),
		chromedp.Navigate(conv),
		chromedp.WaitVisible(box, chromedp.ByQuery),
		chromedp.Value(box, &after, chromedp.ByQuery),
	)
	if err != nil {
		t.Fatal(err)
	}
	if after != "" {
		t.Errorf("after send and reload: box = %q, want empty", after)
	}
}
