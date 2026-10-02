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
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"

	"github.com/aethons-tools/cove/internal/intercom"
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

func TestBrowserWideCodeBlockScrollsInsideItsBubble(t *testing.T) {
	ctx := browserCtx(t)
	store, log, p := fixture()
	wide := "```\n" + strings.Repeat("x", 400) + "\n```"
	first := log.sq[0]
	// One wide block from someone else (left-aligned bubble) and one sent by
	// the viewer (right-aligned, sized to its content rather than stretched).
	log.sq = append(log.sq,
		intercom.Squawk{Seq: 2, From: intercom.Target{Kind: "cove", Ref: "bot"}, To: first.To, Body: wide, At: first.At, Project: "proj"},
		intercom.Squawk{Seq: 3, From: first.From, To: first.To, Body: wide, At: first.At, Project: "proj"},
	)
	h := Handler(store, log, nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, jam.WithParticipant(r, p))
	}))
	t.Cleanup(srv.Close)

	// A code line far wider than the window must stay inside its bubble and the
	// window, scrolling sideways within the block instead.
	type box struct {
		Kind                           string
		Left, Right, MsgLeft, MsgRight float64
		Client, Scroll                 float64
	}
	var boxes []box
	var viewport float64
	err := chromedp.Run(ctx,
		chromedp.EmulateViewport(1000, 700),
		chromedp.Navigate(srv.URL+"/me/?c="+url.QueryEscape("named:eng")),
		chromedp.WaitVisible(`.msg .body.md pre`, chromedp.ByQuery),
		chromedp.Evaluate(`window.innerWidth`, &viewport),
		chromedp.Evaluate(`Array.from(document.querySelectorAll('.msg .body.md pre')).map(function(pre){
			var m=pre.closest('.msg'), r=pre.getBoundingClientRect(), mr=m.getBoundingClientRect();
			return {Kind: m.className, Left: r.left, Right: r.right, MsgLeft: mr.left, MsgRight: mr.right,
				Client: pre.clientWidth, Scroll: pre.scrollWidth};
		})`, &boxes),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(boxes) != 2 {
		t.Fatalf("want 2 code blocks, got %d", len(boxes))
	}
	for _, b := range boxes {
		if b.Left < b.MsgLeft || b.Right > b.MsgRight {
			t.Errorf("%s: code block [%v, %v] spills out of its bubble [%v, %v]", b.Kind, b.Left, b.Right, b.MsgLeft, b.MsgRight)
		}
		if b.Left < 0 || b.Right > viewport {
			t.Errorf("%s: code block [%v, %v] spills out of the window [0, %v]", b.Kind, b.Left, b.Right, viewport)
		}
		if !(b.Scroll > b.Client) {
			t.Errorf("%s: code block does not scroll: client %v, scroll %v", b.Kind, b.Client, b.Scroll)
		}
	}
}
