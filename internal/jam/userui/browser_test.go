//go:build browser

// Browser tests drive the real /app/user page in headless Chrome, covering the
// composer JS that the string-presence tests in handler_test.go can only see as
// source. Run with `just test-browser`; each test skips when no browser is
// installed (see browserCtx).
package userui

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/appui"
	"github.com/aethons-tools/cove/internal/jam/push"
	"github.com/aethons-tools/cove/internal/jam/sessionevents"
	"github.com/aethons-tools/cove/internal/jam/uiassets"
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
	e, p := fixture()
	h := withStatic(Handler(e.Deps, nil))
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
		chromedp.Navigate(srv.URL+"/app/user/?c="+url.QueryEscape(engID)),
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
	e, p := fixture()
	h := withStatic(Handler(e.Deps, nil))
	// /app/user/send lives outside this package; answer it as a successful send.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/user/send" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.ServeHTTP(w, jam.WithParticipant(r, p))
	}))
	t.Cleanup(srv.Close)
	box := `.composer textarea`
	conv := srv.URL + "/app/user/?c=" + url.QueryEscape(engID)

	// A half-typed reply survives leaving the conversation (a full page load)
	// and coming back.
	var kept string
	err := chromedp.Run(ctx,
		chromedp.Navigate(conv),
		chromedp.WaitVisible(box, chromedp.ByQuery),
		chromedp.SendKeys(box, "half typed", chromedp.ByQuery),
		chromedp.Navigate(srv.URL+"/app/user/"),
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

func TestBrowserCopyButtons(t *testing.T) {
	ctx := browserCtx(t)
	e, p := fixture()
	src := "Intro **bold**\n\n- one\n- two\n\n```\nline 1\nline 2\n```\n"
	e.post("bot", src, "")
	h := withStatic(Handler(e.Deps, nil))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, jam.WithParticipant(r, p))
	}))
	t.Cleanup(srv.Close)

	// clip clicks the button sel (hovering its bubble first, as a user would)
	// and returns what landed on the clipboard.
	clip := func(sel string) string {
		t.Helper()
		var got string
		err := chromedp.Run(ctx,
			copy(srv.URL, "-"),
			chromedp.ScrollIntoView(sel, chromedp.ByQuery),
			chromedp.Click(sel, chromedp.ByQuery),
			chromedp.Poll(`navigator.clipboard.readText().then(function(s){ return s!=='-' ? s : null; })`, &got,
				chromedp.WithPollingTimeout(5*time.Second)),
		)
		if err != nil {
			t.Fatalf("copy via %s: %v", sel, err)
		}
		return got
	}
	if err := chromedp.Run(ctx,
		chromedp.Navigate(srv.URL+"/app/user/?c="+url.QueryEscape(engID)),
		chromedp.WaitVisible(`.msg.agent pre`, chromedp.ByQuery),
	); err != nil {
		t.Fatal(err)
	}

	// Text: what the bubble shows, formatting stripped, line structure kept.
	if got := clip(`.msg.agent .copy-text`); strings.Contains(got, "**") ||
		!strings.Contains(got, "Intro bold") || !strings.Contains(got, "one\ntwo") || !strings.Contains(got, "line 1\nline 2") {
		t.Errorf("copy text = %q", got)
	}
	// Markdown: the body exactly as sent.
	if got := clip(`.msg.agent .copy-md`); got != src {
		t.Errorf("copy markdown = %q, want %q", got, src)
	}
	// A code block: just its code.
	if got := clip(`.msg.agent .copy-code`); got != "line 1\nline 2" {
		t.Errorf("copy code = %q, want %q", got, "line 1\nline 2")
	}
	// On a screen that can hover, the controls stay hidden until the bubble or
	// code block is hovered. Headless Chrome reports hover:none (a touch
	// screen, which shows them always) and ignores emulating hover, so drop
	// that rule to get the stylesheet a desktop browser applies.
	opacity := `[getComputedStyle(document.querySelector('.msg.agent .acts')).opacity, getComputedStyle(document.querySelector('.msg.agent .copy-code')).opacity].join()`
	// settle moves the mouse to (x, y) and waits for the controls to reach
	// want (opacities of the bubble's pair and the code button).
	settle := func(x, y float64, want string) {
		t.Helper()
		var ok bool
		err := chromedp.Run(ctx,
			chromedp.MouseEvent(input.MouseMoved, x, y),
			chromedp.Poll(opacity+`===`+strconv.Quote(want), &ok, chromedp.WithPollingTimeout(3*time.Second)),
		)
		if err != nil {
			var got string
			_ = chromedp.Run(ctx, chromedp.Evaluate(opacity, &got))
			t.Errorf("controls opacity (bubble, code) with the mouse at (%v, %v) = %s, want %s", x, y, got, want)
		}
	}
	var pre []float64
	err := chromedp.Run(ctx,
		chromedp.Evaluate(`
			Array.from(document.styleSheets).forEach(function(ss){
				try{ for(var i=ss.cssRules.length-1;i>=0;i--){ var r=ss.cssRules[i]; if(r.media && r.media.mediaText.indexOf('hover')>=0){ ss.deleteRule(i); } } }catch(_){}
			});
			document.activeElement.blur()`, nil),
		chromedp.Evaluate(`(function(){ var r=document.querySelector('.msg.agent pre').getBoundingClientRect(); return [r.left+5, r.top+5]; })()`, &pre),
	)
	if err != nil {
		t.Fatal(err)
	}
	settle(1, 1, "0,0")
	settle(pre[0], pre[1], "1,1")

	// A stream refresh re-renders the bubbles; the code buttons come back.
	var n int
	err = chromedp.Run(ctx,
		chromedp.Evaluate(`document.querySelector('.msg.agent pre').parentNode.dataset.old='1'; htmx.trigger(document.body,'meChanged')`, nil),
		chromedp.Poll(`!document.querySelector('[data-old]') && document.querySelectorAll('.msg.agent .copy-code').length`, &n,
			chromedp.WithPollingTimeout(5*time.Second)),
	)
	if err != nil || n != 1 {
		t.Errorf("code copy buttons after a stream refresh: %d (err %v)", n, err)
	}
}

func TestBrowserWideCodeBlockScrollsInsideItsBubble(t *testing.T) {
	ctx := browserCtx(t)
	e, p := fixture()
	wide := "```\n" + strings.Repeat("x", 400) + "\n```"
	// One wide block from someone else (left-aligned bubble) and one sent by
	// the viewer (right-aligned, sized to its content rather than stretched).
	e.post("bot", wide, "")
	e.post(e.alice.ID, wide, "")
	h := withStatic(Handler(e.Deps, nil))
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
		chromedp.Navigate(srv.URL+"/app/user/?c="+url.QueryEscape(engID)),
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

func TestBrowserPresenceStripLiveUpdates(t *testing.T) {
	ctx := browserCtx(t)
	e, p, pr := presenceFixture()
	var mu sync.Mutex
	h := withStatic(Handler(e.Deps, nil, WithPresence(lockedPresence{pr, &mu})))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, jam.WithParticipant(r, p))
	}))
	t.Cleanup(srv.Close)
	// The page holds /app/user/events open; drop it so Close doesn't wait on it.
	t.Cleanup(srv.CloseClientConnections)

	// The strip sits between the message list and the composer.
	var order bool
	err := chromedp.Run(ctx,
		chromedp.EmulateViewport(1000, 700),
		chromedp.Navigate(srv.URL+"/app/user/?c="+url.QueryEscape(engID)),
		chromedp.WaitVisible(`#presence .sess.busy .dots`, chromedp.ByQuery),
		chromedp.Evaluate(`(function(){ var s=document.getElementById('stream').getBoundingClientRect(),
			p=document.getElementById('presence').getBoundingClientRect(),
			c=document.querySelector('.composer').getBoundingClientRect();
			return s.bottom<=p.top+1 && p.bottom<=c.top+1; })()`, &order),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !order {
		t.Error("presence strip should sit between the messages and the composer")
	}

	// A status change pushed on /app/user/events updates the strip in place.
	mu.Lock()
	pr.st["busy"] = sessionevents.Status{State: sessionevents.StatusWriting}
	mu.Unlock()
	pr.ch <- struct{}{}
	var text string
	err = chromedp.Run(ctx,
		chromedp.Poll(`(function(){ var t=document.getElementById('presence').innerText; return t.indexOf('builder is writing')>=0 ? t : null; })()`, &text,
			chromedp.WithPollingTimeout(5*time.Second)),
	)
	if err != nil {
		var now string
		_ = chromedp.Run(ctx, chromedp.Evaluate(`document.getElementById('presence').innerText`, &now))
		t.Fatalf("strip did not update after a presence push: %v (now %q)", err, now)
	}
}

// lockedPresence guards fakePresence's map, which the test mutates while the
// handler reads it.
type lockedPresence struct {
	f  *fakePresence
	mu *sync.Mutex
}

func (l lockedPresence) Status(a string) (sessionevents.Status, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f.Status(a)
}
func (l lockedPresence) Subscribe() (<-chan struct{}, func()) { return l.f.Subscribe() }

// withStatic mounts the shared assets at /app/static/ the way appui does in
// production, so the page's htmx and stylesheet resolve in the browser.
func withStatic(h http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /app/static/", uiassets.Handler("/app/static/"))
	mux.Handle("/", h)
	return mux
}

// At 500px the channel list hides but the rail header's controls stay visible.
func TestBrowserNarrowScreenKeepsRailHeadControls(t *testing.T) {
	ctx := browserCtx(t)
	srv := serveInbox(t)
	var vis bool
	err := chromedp.Run(ctx,
		chromedp.EmulateViewport(500, 800),
		chromedp.Navigate(srv.URL+"/app/user/"),
		chromedp.WaitVisible(`#newmsg summary`, chromedp.ByQuery),
		chromedp.Evaluate(`(function(){ function v(s){var r=document.querySelector(s).getBoundingClientRect();return r.width>0&&r.height>0&&r.right<=innerWidth;}
			return v('#newmsg summary') && getComputedStyle(document.getElementById('rail')).display==='none'; })()`, &vis),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !vis {
		t.Error("rail header controls should be visible and the list hidden at 500px")
	}
}

// A 64-character brand at 500px must not push the principal menu off-screen
// or squeeze the tabs to nothing.
func TestBrowserLongBrandKeepsPrincipalMenuAndTabs(t *testing.T) {
	ctx := browserCtx(t)
	e, p := fixture()
	h := withStatic(appui.WithShell(Handler(e.Deps, nil), appui.Shell{Brand: strings.Repeat("W", 64) + " Jam"}))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = jam.WithParticipant(r, p)
		h.ServeHTTP(w, appui.WithPrincipal(r, appui.Principal{User: &p}))
	}))
	t.Cleanup(srv.Close)
	var ok bool
	err := chromedp.Run(ctx,
		chromedp.EmulateViewport(500, 800),
		chromedp.Navigate(srv.URL+"/app/user/"),
		chromedp.WaitVisible(`.app-principal summary`, chromedp.ByQuery),
		chromedp.Evaluate(`(function(){ function v(s){var r=document.querySelector(s).getBoundingClientRect();return r.width>0&&r.height>0&&r.left>=0&&r.right<=innerWidth;}
			return v('.app-principal summary') && v('.app-tabs a'); })()`, &ok),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("principal menu and a tab should stay inside the 500px viewport")
	}
}

// At 900px a 64-character brand is capped so the first tabs and the principal
// menu keep real room.
func TestBrowserLongBrandKeepsTabsAtMidWidth(t *testing.T) {
	ctx := browserCtx(t)
	e, p := fixture()
	h := withStatic(appui.WithShell(Handler(e.Deps, nil), appui.Shell{Brand: strings.Repeat("W", 64) + " Jam"}))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = jam.WithParticipant(r, p)
		h.ServeHTTP(w, appui.WithPrincipal(r, appui.Principal{User: &p, Admin: true, Operator: jam.Operator{ID: "op"}}))
	}))
	t.Cleanup(srv.Close)
	var ok bool
	err := chromedp.Run(ctx,
		chromedp.EmulateViewport(900, 800),
		chromedp.Navigate(srv.URL+"/app/user/"),
		chromedp.WaitVisible(`.app-principal summary`, chromedp.ByQuery),
		chromedp.Evaluate(`(function(){ function v(e){var r=e.getBoundingClientRect();return r.width>0&&r.height>0&&r.left>=0&&r.right<=innerWidth;}
			var tabs=document.querySelectorAll('.app-tabs a');
			return tabs.length>=2 && v(tabs[0]) && v(tabs[1]) && v(document.querySelector('.app-principal summary')); })()`, &ok),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("first two tabs and the principal menu should be visible at 900px")
	}
}

// Behind the app mux, the Inbox registers the service worker at scope /app/
// and links the manifest: the page is installable.
func TestBrowserInboxRegistersServiceWorker(t *testing.T) {
	ctx := browserCtx(t)
	e, p := fixture()
	inbox := Handler(e.Deps, nil)
	m := appui.Handler(appui.Deps{Gate: appui.NewGate(appui.GateConfig{Log: slog.New(slog.NewTextHandler(io.Discard, nil))})})
	m.Handle("/app/user/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = jam.WithParticipant(r, p)
		inbox.ServeHTTP(w, appui.WithPrincipal(r, appui.Principal{User: &p}))
	}))
	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)
	var scope, manifest string
	await := func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) }
	err := chromedp.Run(ctx,
		chromedp.Navigate(srv.URL+"/app/user/"),
		chromedp.WaitVisible(`.app-titlebar`, chromedp.ByQuery),
		chromedp.Evaluate(`navigator.serviceWorker.ready.then(function(r){ return r.scope; })`, &scope, await),
		chromedp.Evaluate(`document.querySelector('link[rel=manifest]').href`, &manifest),
	)
	if err != nil {
		t.Fatal(err)
	}
	if scope != srv.URL+"/app/" {
		t.Errorf("service worker scope = %q, want %q", scope, srv.URL+"/app/")
	}
	if manifest != srv.URL+"/app/manifest.webmanifest" {
		t.Errorf("manifest link = %q", manifest)
	}
}

// pushStub replaces the browser's push service and notification permission
// (headless Chrome denies it outright) before the page's scripts run:
// requestPermission grants, and subscribe returns a fake subscription, as a
// push service would.
const pushStub = `
(function(){
  // The fake subscription and permission outlive a reload, as a real one would.
  var store = sessionStorage;
  function make(){
    return {
      endpoint: 'https://fcm.googleapis.com/fcm/send/device-1',
      unsubscribe: function(){ store.removeItem('__fakeSub'); return Promise.resolve(true); },
      toJSON: function(){ return {endpoint: this.endpoint, expirationTime: null,
        keys: {p256dh: '` + "BAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" + `', auth: 'AAAAAAAAAAAAAAAAAAAAAA'}}; }
    };
  }
  Object.defineProperty(Notification, 'permission', {get: function(){ return store.getItem('__perm') || 'default'; }});
  Notification.requestPermission = function(){ store.setItem('__perm', 'granted'); return Promise.resolve('granted'); };
  PushManager.prototype.getSubscription = function(){ return Promise.resolve(store.getItem('__fakeSub') ? make() : null); };
  PushManager.prototype.subscribe = function(opts){
    window.__pushKeyLen = opts.applicationServerKey.length;
    store.setItem('__fakeSub', '1');
    return Promise.resolve(make());
  };
})();`

// The principal menu's Notifications toggle subscribes this device and
// stores the subscription with Jam, then Off deletes it.
func TestBrowserPushToggleSubscribes(t *testing.T) {
	ctx := browserCtx(t)
	e, p := fixture()
	var mu sync.Mutex
	var seen []string
	srv := servePush(t, e, p, func(r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path)
		mu.Unlock()
	})

	stored := func() []jam.PushSubscription { return e.st.PushSubscriptions(p.UserID) }
	err := chromedp.Run(ctx,
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, err := page.AddScriptToEvaluateOnNewDocument(pushStub).Do(ctx)
			return err
		}),
		chromedp.Navigate(srv.URL+"/app/user/"),
		chromedp.Click(`.app-principal summary`, chromedp.ByQuery),
		chromedp.Poll(`document.getElementById('push-toggle').textContent === 'Notifications: Off' && !document.getElementById('push-toggle').disabled`, nil),
		chromedp.Click(`#push-toggle`, chromedp.ByQuery),
		chromedp.Poll(`document.getElementById('push-toggle').textContent === 'Notifications: On'`, nil),
	)
	if err != nil {
		t.Fatal(err)
	}
	subs := stored()
	if len(subs) != 1 || subs[0].Endpoint != "https://fcm.googleapis.com/fcm/send/device-1" || subs[0].UserID != p.UserID {
		t.Fatalf("stored = %+v (requests %v)", subs, seen)
	}
	var keyLen int
	if err := chromedp.Run(ctx, chromedp.Evaluate(`window.__pushKeyLen`, &keyLen)); err != nil || keyLen != 11 {
		t.Errorf("applicationServerKey length = %d (%v), want the decoded key's 11 bytes", keyLen, err)
	}

	// A reload in the same page session trusts the first registration; a
	// new session (marker gone) re-sends the live subscription once.
	isOn := `document.getElementById('push-toggle').textContent === 'Notifications: On' && !document.getElementById('push-toggle').disabled`
	err = chromedp.Run(ctx,
		chromedp.Reload(),
		chromedp.Poll(isOn, nil),
		chromedp.Evaluate(`Object.keys(sessionStorage).filter(function(k){ return k.indexOf('jam.push.synced:') === 0; }).forEach(function(k){ sessionStorage.removeItem(k); })`, nil),
		chromedp.Reload(),
		chromedp.Poll(isOn, nil),
		chromedp.Click(`.app-principal summary`, chromedp.ByQuery),
		chromedp.Click(`#push-toggle`, chromedp.ByQuery),
		chromedp.Poll(`document.getElementById('push-toggle').textContent === 'Notifications: Off'`, nil),
	)
	if err != nil {
		t.Fatal(err)
	}
	if subs := stored(); len(subs) != 0 {
		t.Errorf("after Off stored = %+v", subs)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"GET /app/user/push/key", "POST /app/user/push/subscriptions",
		"GET /app/user/push/key", "POST /app/user/push/subscriptions", "DELETE /app/user/push/subscriptions"}
	if strings.Join(seen, ",") != strings.Join(want, ",") {
		t.Errorf("requests = %v, want %v", seen, want)
	}
}

// servePush serves the fixture inbox with push and focus reporting on, as
// member p, with a no-op service worker and a Sign out link; onPush sees each
// /app/user/push/ request and /app/auth/logout.
func servePush(t *testing.T, e *env, p jam.Participant, onPush func(*http.Request)) *httptest.Server {
	t.Helper()
	principal := appui.Principal{User: &p}
	h := appui.WithShell(withStatic(Handler(e.Deps, nil, WithPush("BOtestPublicKey"), WithPushFocus(push.NewFocus(nil)))), appui.Shell{Push: true, SignOut: true})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/sw.js" {
			w.Header().Set("Content-Type", "text/javascript")
			_, _ = io.WriteString(w, "self.addEventListener('activate', e => e.waitUntil(self.clients.claim()));")
			return
		}
		if (strings.HasPrefix(r.URL.Path, "/app/user/push/") || r.URL.Path == "/app/auth/logout") && onPush != nil {
			onPush(r)
		}
		h.ServeHTTP(w, appui.WithPrincipal(jam.WithParticipant(r, p), principal))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// Without Web Push the item says how to get it on iOS (the Home Screen app)
// and is hidden elsewhere.
func TestBrowserPushToggleWithoutWebPush(t *testing.T) {
	e, p := fixture()
	srv := servePush(t, e, p, nil)
	for _, c := range []struct{ ua, want string }{
		{"Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X)", "Add Jam to your Home Screen to get notifications|visible"},
		{"Mozilla/5.0 (X11; Linux x86_64)", "Notifications: Off|hidden"},
	} {
		ctx := browserCtx(t)
		stub := `delete window.PushManager; Object.defineProperty(navigator, 'userAgent', {get: function(){ return ` + strconv.Quote(c.ua) + `; }});`
		var got string
		err := chromedp.Run(ctx,
			chromedp.ActionFunc(func(ctx context.Context) error {
				_, err := page.AddScriptToEvaluateOnNewDocument(stub).Do(ctx)
				return err
			}),
			chromedp.Navigate(srv.URL+"/app/user/"),
			chromedp.WaitReady(`#push-toggle`, chromedp.ByQuery),
			chromedp.Evaluate(`(function(){ var b = document.getElementById('push-toggle'); return b.textContent + '|' + (b.hidden ? 'hidden' : 'visible'); })()`, &got),
		)
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("%s: item = %q, want %q", c.ua, got, c.want)
		}
	}
}

// requestLog records "METHOD path body" lines for servePush's onPush.
type requestLog struct {
	mu   sync.Mutex
	seen []string
}

func (l *requestLog) record(r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(strings.NewReader(string(b)))
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seen = append(l.seen, strings.TrimSpace(r.Method+" "+r.URL.Path+" "+string(b)))
}

func (l *requestLog) has(line string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, s := range l.seen {
		if s == line {
			return true
		}
	}
	return false
}

func (l *requestLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.seen, "\n")
}

// waitFor polls cond until it holds or ctx ends.
func waitFor(ctx context.Context, cond func() bool) bool {
	for !cond() {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(20 * time.Millisecond):
		}
	}
	return true
}

// An open, focused conversation is reported to /app/user/push/focus; the
// page going hidden reports null.
func TestBrowserPushFocusReportsOpenConversation(t *testing.T) {
	ctx := browserCtx(t)
	e, p := fixture()
	var log requestLog
	srv := servePush(t, e, p, log.record)
	focused := `POST /app/user/push/focus {"channel":"` + engID + `"}`
	err := chromedp.Run(ctx,
		emulation.SetFocusEmulationEnabled(true),
		chromedp.Navigate(srv.URL+"/app/user/?c="+engID),
		chromedp.WaitVisible(`#stream`, chromedp.ByQuery),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !waitFor(ctx, func() bool { return log.has(focused) }) {
		t.Fatalf("no focus report; requests:\n%s", log.String())
	}
	err = chromedp.Run(ctx, chromedp.Evaluate(`Object.defineProperty(document, 'visibilityState', {configurable: true, get: function(){ return 'hidden'; }});
document.dispatchEvent(new Event('visibilitychange'));`, nil))
	if err != nil {
		t.Fatal(err)
	}
	if !waitFor(ctx, func() bool { return log.has(`POST /app/user/push/focus {"channel":null}`) }) {
		t.Fatalf("no null report on hidden; requests:\n%s", log.String())
	}
}

// The app badge follows the rail's unread total: set on load, cleared once
// the conversation is read and the rail refreshes.
func TestBrowserAppBadgeFollowsUnreadTotal(t *testing.T) {
	ctx := browserCtx(t)
	e, p := fixture()
	if err := jam.AddPerson(e.st, "proj", jam.Human{Name: "bob"}); err != nil {
		t.Fatal(err)
	}
	bobID, _ := e.st.LookupName(ident.User, "bob")
	e.post(bobID, "one", "")
	want := jam.UnreadTotal(e.st, e.Intercom, e.lg, p.UserID)
	srv := servePush(t, e, p, nil)
	stub := `window.__badge = [];
navigator.setAppBadge = function(n){ window.__badge.push(n); return Promise.resolve(); };
navigator.clearAppBadge = function(){ window.__badge.push('clear'); return Promise.resolve(); };`
	var got string
	err := chromedp.Run(ctx,
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, err := page.AddScriptToEvaluateOnNewDocument(stub).Do(ctx)
			return err
		}),
		chromedp.Navigate(srv.URL+"/app/user/"),
		chromedp.WaitVisible(`#rail`, chromedp.ByQuery),
		chromedp.Evaluate(`window.__badge.join(',')`, &got),
	)
	if err != nil {
		t.Fatal(err)
	}
	if got != strconv.Itoa(want) || want == 0 {
		t.Fatalf("badge calls on load = %q, want %d", got, want)
	}
	// Opening the conversation marks it read; a rail refresh clears the badge.
	err = chromedp.Run(ctx,
		chromedp.Navigate(srv.URL+"/app/user/?c="+engID),
		chromedp.WaitVisible(`#stream`, chromedp.ByQuery),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !waitFor(ctx, func() bool { return jam.UnreadTotal(e.st, e.Intercom, e.lg, p.UserID) == 0 }) {
		t.Fatal("opening the conversation did not mark it read")
	}
	err = chromedp.Run(ctx,
		chromedp.Evaluate(`htmx.trigger(document.body, 'meChanged')`, nil),
		chromedp.Poll(`window.__badge[window.__badge.length-1] === 'clear'`, nil),
	)
	if err != nil {
		var calls string
		_ = chromedp.Run(ctx, chromedp.Evaluate(`window.__badge.join(',')`, &calls))
		t.Fatalf("badge not cleared: %v (calls %q)", err, calls)
	}
}

// Sign out on a device with a subscription unsubscribes it and deletes it
// from Jam before going to /app/auth/logout.
func TestBrowserSignOutDropsPushSubscription(t *testing.T) {
	ctx := browserCtx(t)
	e, p := fixture()
	var log requestLog
	srv := servePush(t, e, p, log.record)
	err := chromedp.Run(ctx,
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, err := page.AddScriptToEvaluateOnNewDocument(pushStub).Do(ctx)
			return err
		}),
		chromedp.Navigate(srv.URL+"/app/user/"),
		chromedp.Click(`.app-principal summary`, chromedp.ByQuery),
		chromedp.Poll(`document.getElementById('push-toggle').textContent === 'Notifications: Off' && !document.getElementById('push-toggle').disabled`, nil),
		chromedp.Click(`#push-toggle`, chromedp.ByQuery),
		chromedp.Poll(`document.getElementById('push-toggle').textContent === 'Notifications: On'`, nil),
	)
	if err != nil {
		t.Fatal(err)
	}
	if subs := e.st.PushSubscriptions(p.UserID); len(subs) != 1 {
		t.Fatalf("stored before sign out = %+v", subs)
	}
	var fakeSub string
	err = chromedp.Run(ctx,
		chromedp.Click(`.app-principal a[href="/app/auth/logout"]`, chromedp.ByQuery),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !waitFor(ctx, func() bool { return log.has("GET /app/auth/logout") }) {
		t.Fatalf("never signed out; requests:\n%s", log.String())
	}
	if err := chromedp.Run(ctx, chromedp.WaitReady(`body`, chromedp.ByQuery), chromedp.Evaluate(`sessionStorage.getItem('__fakeSub') || ''`, &fakeSub)); err != nil {
		t.Fatal(err)
	}
	if subs := e.st.PushSubscriptions(p.UserID); len(subs) != 0 {
		t.Errorf("subscription survived sign out: %+v", subs)
	}
	if fakeSub != "" {
		t.Error("browser subscription not unsubscribed")
	}
	if !log.has(`DELETE /app/user/push/subscriptions {"endpoint":"https://fcm.googleapis.com/fcm/send/device-1"}`) || !log.has("GET /app/auth/logout") {
		t.Errorf("requests:\n%s", log.String())
	}
}

// Sign out with no subscription on the device just signs out.
func TestBrowserSignOutWithoutSubscription(t *testing.T) {
	ctx := browserCtx(t)
	e, p := fixture()
	var log requestLog
	srv := servePush(t, e, p, log.record)
	err := chromedp.Run(ctx,
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, err := page.AddScriptToEvaluateOnNewDocument(pushStub).Do(ctx)
			return err
		}),
		chromedp.Navigate(srv.URL+"/app/user/"),
		chromedp.Click(`.app-principal summary`, chromedp.ByQuery),
		chromedp.Click(`.app-principal a[href="/app/auth/logout"]`, chromedp.ByQuery),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !waitFor(ctx, func() bool { return log.has("GET /app/auth/logout") }) {
		t.Fatalf("never signed out; requests:\n%s", log.String())
	}
	if strings.Contains(log.String(), "DELETE") {
		t.Errorf("deleted with no subscription:\n%s", log.String())
	}
}

// The service worker's notification logic, run in a page: a conversation's
// first push shows its body; a later one replaces it, counting messages; a
// click opens the conversation.
func TestBrowserServiceWorkerNotice(t *testing.T) {
	ctx := browserCtx(t)
	m := appui.Handler(appui.Deps{Gate: appui.NewGate(appui.GateConfig{Log: slog.New(slog.NewTextHandler(io.Discard, nil))})})
	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)
	var got string
	await := func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) }
	err := chromedp.Run(ctx,
		chromedp.Navigate(srv.URL+"/app/manifest.webmanifest"),
		chromedp.Evaluate(`fetch('/app/sw.js').then(function(r){ return r.text(); }).then(function(src){
  (0, eval)(src);
  var p = {title: 'bob · eng', body: 'hello', channel: 'chn_1', id: 'sq_1', unread: 3};
  var a = jamNotice(p, null), b = jamNotice({title: 'bob · eng', body: 'again', channel: 'chn_1'}, a.options.data);
  return JSON.stringify([a.title, a.options.body, a.options.tag, a.options.renotify, a.options.data.n,
    b.options.body, b.options.data.n, jamTarget('chn_1')]);
})`, &got, await),
	)
	if err != nil {
		t.Fatal(err)
	}
	want := `["bob · eng","hello","chn_1",true,1,"2 new messages · latest: again",2,"/app/user/?c=chn_1"]`
	if got != want {
		t.Errorf("notice = %s\nwant     %s", got, want)
	}
}

// The service worker's robustness helpers, run in a page: a payload that
// isn't a JSON object reads as {}; a garbled one still makes a notification
// (title "Jam", body "New message"); a click reuses only an /app/user/
// window, never an /app/admin/ one.
func TestBrowserServiceWorkerRobust(t *testing.T) {
	ctx := browserCtx(t)
	m := appui.Handler(appui.Deps{Gate: appui.NewGate(appui.GateConfig{Log: slog.New(slog.NewTextHandler(io.Discard, nil))})})
	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)
	var got string
	await := func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) }
	err := chromedp.Run(ctx,
		chromedp.Navigate(srv.URL+"/app/manifest.webmanifest"),
		chromedp.Evaluate(`fetch('/app/sw.js').then(function(r){ return r.text(); }).then(function(src){
  (0, eval)(src);
  var o = 'https://jam.example';
  var ps = [null, 5, 'str', [1], {body: 'x'}].map(function(v){ return JSON.stringify(jamPayload(v)); });
  var g = jamNotice({title: 7, body: {}, channel: ['c']}, {n: 'x'});
  var f = jamFallback({channel: 'chn_1'});
  var e = jamNotice({}, null);
  return JSON.stringify([ps, g.title, g.options.body, g.options.tag, g.options.data.n, f.title, f.options.body,
    f.options.data.channel, e.title, e.options.body,
    jamPickClient([o + '/app/admin/', o + '/app/user/?c=1', o + '/app/user/']),
    jamPickClient([o + '/app/admin/x', o + '/app/', 'not a url']),
    jamPickClient([])]);
})`, &got, await),
	)
	if err != nil {
		t.Fatal(err)
	}
	want := `[["{}","{}","{}","{}","{\"body\":\"x\"}"],"Jam","New message","jam",1,"Jam","New message","chn_1","Jam","New message",1,-1,-1]`
	if got != want {
		t.Errorf("robust = %s\nwant     %s", got, want)
	}
}

// Shift+Enter inserts a newline; Ctrl+Enter (headless Chrome reports Linux)
// sends.
func TestBrowserComposerSendKey(t *testing.T) {
	ctx := browserCtx(t)
	e, p := fixture()
	h := withStatic(Handler(e.Deps, nil))
	sent := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/user/send" {
			b, _ := io.ReadAll(r.Body)
			sent <- string(b)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.ServeHTTP(w, jam.WithParticipant(r, p))
	}))
	defer srv.Close()
	box := `.composer textarea`
	var val, kbd string
	if err := chromedp.Run(ctx,
		chromedp.Navigate(srv.URL+"/app/user/?c="+url.QueryEscape(engID)),
		chromedp.WaitVisible(box, chromedp.ByQuery),
		chromedp.Click(box, chromedp.ByQuery),
		chromedp.SendKeys(box, "line one", chromedp.ByQuery),
		chromedp.KeyEvent("\r", chromedp.KeyModifiers(input.ModifierShift)),
		chromedp.SendKeys(box, "line two", chromedp.ByQuery),
		chromedp.Value(box, &val, chromedp.ByQuery),
		chromedp.Text(`.composer kbd`, &kbd, chromedp.ByQuery),
	); err != nil {
		t.Fatal(err)
	}
	if val != "line one\nline two" {
		t.Fatalf("after Shift+Enter the box holds %q", val)
	}
	select {
	case b := <-sent:
		t.Fatalf("Shift+Enter sent %s", b)
	default:
	}
	if kbd != "Ctrl+↵" {
		t.Fatalf("send key label %q off a Mac", kbd)
	}
	if err := chromedp.Run(ctx, chromedp.KeyEvent("\r", chromedp.KeyModifiers(input.ModifierCtrl))); err != nil {
		t.Fatal(err)
	}
	select {
	case b := <-sent:
		if !strings.Contains(b, `line one\nline two`) {
			t.Fatalf("sent %s", b)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Ctrl+Enter didn't send")
	}
}

// The service worker silences a push while a page that can chime has said so
// recently, or when the kind's sound is Off; otherwise the OS sound plays.
func TestBrowserServiceWorkerSilent(t *testing.T) {
	ctx := browserCtx(t)
	m := appui.Handler(appui.Deps{Gate: appui.NewGate(appui.GateConfig{Log: slog.New(slog.NewTextHandler(io.Discard, nil))})})
	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)
	var got string
	await := func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) }
	err := chromedp.Run(ctx,
		chromedp.Navigate(srv.URL+"/app/manifest.webmanifest"),
		chromedp.Evaluate(`fetch('/app/sw.js').then(function(r){ return r.text(); }).then(function(src){
  (0, eval)(src);
  var now = 1e12, off = {messages: 'off', needs: 'chime'};
  return JSON.stringify([
    jamSilent('messages', null, now - 30000, now),
    jamSilent('messages', null, now - 200000, now),
    jamSilent('messages', off, null, now),
    jamSilent('needs', off, null, now),
    jamNotice({}, null, true).options.silent,
    jamNotice({}, null).options.silent]);
})`, &got, await),
	)
	if err != nil {
		t.Fatal(err)
	}
	if want := `[true,false,true,false,true,false]`; got != want {
		t.Errorf("silent = %s, want %s", got, want)
	}
}

// The Inbox chimes after a rail refresh: the Needs you sound when a
// conversation starts needing you, else the Messages sound for more unread;
// never on the first rail.
func TestBrowserInboxChimes(t *testing.T) {
	ctx := browserCtx(t)
	srv := serveInbox(t)
	var got string
	err := chromedp.Run(ctx,
		chromedp.Navigate(srv.URL+"/app/user/"),
		chromedp.WaitReady(`#rail`, chromedp.ByQuery),
		chromedp.Evaluate(`(function(){
  var played=[]; window.jamSound={play:function(k){ played.push(k); return true; }};
  var rail=document.getElementById('rail');
  function row(ch, u, n){ var a=document.createElement('a'); a.className='thread'; a.dataset.ch=ch; a.dataset.unread=String(u); a.dataset.needs=String(n); return a; }
  function set(rows){ rail.innerHTML=''; rows.forEach(function(r){ rail.appendChild(r); }); meChimes(); }
  meChimes.prev=undefined;
  set([row('a',0,false), row('b',0,false)]);          // baseline: silent
  set([row('a',1,false), row('b',0,false)]);          // more unread: messages
  set([row('a',1,false), row('b',0,true)]);           // starts needing you: needs
  set([row('a',2,false), row('b',0,true)]);           // more unread again: messages
  return JSON.stringify(played);
})()`, &got),
	)
	if err != nil {
		t.Fatal(err)
	}
	if want := `["messages","needs","messages"]`; got != want {
		t.Errorf("chimes = %s, want %s", got, want)
	}
}

// sounds.js: the panel lists Off and the chimes, a change persists (and
// reaches IndexedDB for the service worker), and play respects Off.
func TestBrowserSoundsSettings(t *testing.T) {
	ctx := browserCtx(t)
	srv := serveInbox(t)
	var got string
	await := func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) }
	err := chromedp.Run(ctx,
		chromedp.Navigate(srv.URL+"/app/user/"),
		chromedp.WaitReady(`#rail`, chromedp.ByQuery),
		chromedp.Evaluate(`(async function(){
  localStorage.removeItem('jam-sounds');
  document.body.insertAdjacentHTML('beforeend', '<button id="sounds-toggle" hidden></button><div id="sounds-panel" hidden><select name="messages"></select><select name="needs"></select><input type="range" name="volume"><button data-preview="messages"></button></div>');
  (0, eval)(await (await fetch('/app/static/sounds.js')).text());
  var msg=document.querySelector('[name=messages]');
  var opts=Array.from(msg.options).map(function(o){ return o.value; });
  msg.value='off'; msg.dispatchEvent(new Event('change'));
  var saved=JSON.parse(localStorage.getItem('jam-sounds'));
  var played=window.jamSound.play('messages');
  var idb=await new Promise(function(res){ var r=indexedDB.open('jam',1); r.onsuccess=function(){ var g=r.result.transaction('prefs').objectStore('prefs').get('sounds'); g.onsuccess=function(){ res(g.result); }; }; });
  return JSON.stringify([opts, saved.messages, played, idb && idb.messages, !document.getElementById('sounds-toggle').hidden]);
})()`, &got, await),
	)
	if err != nil {
		t.Fatal(err)
	}
	if want := `[["off","pop","chime","ping","bell"],"off",false,"off",true]`; got != want {
		t.Errorf("sounds = %s, want %s", got, want)
	}
}
