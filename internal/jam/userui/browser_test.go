//go:build browser

// Browser tests drive the user area in headless Chrome: the Jam app
// (app_browser_test.go) and the service worker. Run with `just
// test-browser`; each test skips when no browser is installed (see
// browserCtx).
package userui

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/appui"
	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// browserCtx returns a chromedp context on a fresh headless browser, skipping
// the test when none is found. JAM_BROWSER names the binary explicitly;
// otherwise the first of the usual names on PATH is used.
func browserCtx(t *testing.T) context.Context {
	t.Helper()
	path := os.Getenv("JAM_BROWSER")
	for _, name := range []string{"chrome-headless-shell", "headless-shell", "chromium", "google-chrome"} {
		if path != "" {
			break
		}
		path, _ = exec.LookPath(name)
	}
	if path == "" {
		t.Skip("no headless browser found (set JAM_BROWSER or install chrome-headless-shell)")
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

// step is a browser action that yields no value: what chromedp.Do runs.
type step = chromedp.Action[chromedp.Void]

// into runs a and stores its value in *dst, so an action that returns a value
// can sit among the steps of one chromedp.Do.
func into[T any](dst *T, a chromedp.Action[T]) step {
	return func(ctx context.Context, tg *chromedp.Target) (chromedp.Void, error) {
		v, err := a(ctx, tg)
		if err == nil {
			*dst = v
		}
		return chromedp.Void{}, err
	}
}

// call sends one protocol command to the tab, ignoring its result.
func call[P, R any](c cdp.Command[P, R], params P) step {
	return chromedp.Func(func(ctx context.Context, tg *chromedp.Target) error {
		_, err := cdp.Call(ctx, tg, c, params)
		return err
	})
}

// onNewDocument runs src in every document the tab loads, before its scripts.
func onNewDocument(src string) step {
	return call(page.AddScriptToEvaluateOnNewDocument, page.AddScriptToEvaluateOnNewDocumentParams{Source: src})
}

// copy puts text on the browser's clipboard (headless Chrome keeps its own,
// in-process), so a paste shortcut pastes it as a real, trusted paste event.
// writeText is refused on later calls unless clipboard-read is granted too.
func copy(origin, text string) step {
	return chromedp.Steps(
		call(browser.SetPermission, browser.SetPermissionParams{Permission: &browser.PermissionDescriptor{Name: "clipboard-write"}, Setting: browser.PermissionSettingGranted, Origin: origin}),
		call(browser.SetPermission, browser.SetPermissionParams{Permission: &browser.PermissionDescriptor{Name: "clipboard-read"}, Setting: browser.PermissionSettingGranted, Origin: origin}),
		chromedp.Evaluate[chromedp.Void](`navigator.clipboard.writeText(`+strconv.Quote(text)+`)`,
			chromedp.EvalAwaitPromise),
	)
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

// The service worker's notification logic, run in a page: a conversation's
// first push shows its body; a later one replaces it, counting messages; a
// click opens the conversation.
func TestBrowserServiceWorkerNotice(t *testing.T) {
	ctx := browserCtx(t)
	m := appui.Handler(appui.Deps{Gate: appui.NewGate(appui.GateConfig{Log: slog.New(slog.NewTextHandler(io.Discard, nil))})})
	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)
	var got string
	await := chromedp.EvalAwaitPromise
	err := chromedp.Do(ctx,
		chromedp.Navigate(srv.URL+"/app/manifest.webmanifest"),
		into(&got, chromedp.Evaluate[string](`fetch('/app/sw.js').then(function(r){ return r.text(); }).then(function(src){
  (0, eval)(src);
  var p = {title: 'bob · eng', body: 'hello', channel: 'chn_1', id: 'sq_1', unread: 3};
  var a = jamNotice(p, null), b = jamNotice({title: 'bob · eng', body: 'again', channel: 'chn_1'}, a.options.data);
  return JSON.stringify([a.title, a.options.body, a.options.tag, a.options.renotify, a.options.data.n,
    b.options.body, b.options.data.n, jamTarget('chn_1')]);
})`, await)),
	)
	if err != nil {
		t.Fatal(err)
	}
	want := `["bob · eng","hello","chn_1",true,1,"2 new messages · latest: again",2,"/app/user/c/chn_1"]`
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
	await := chromedp.EvalAwaitPromise
	err := chromedp.Do(ctx,
		chromedp.Navigate(srv.URL+"/app/manifest.webmanifest"),
		into(&got, chromedp.Evaluate[string](`fetch('/app/sw.js').then(function(r){ return r.text(); }).then(function(src){
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
})`, await)),
	)
	if err != nil {
		t.Fatal(err)
	}
	want := `[["{}","{}","{}","{}","{\"body\":\"x\"}"],"Jam","New message","jam",1,"Jam","New message","chn_1","Jam","New message",1,-1,-1]`
	if got != want {
		t.Errorf("robust = %s\nwant     %s", got, want)
	}
}

// A notification click reuses an open Jam app window (/app/user/, or a tab
// still on its pre-cutover /app/next/), opening the conversation there;
// never an admin window.
func TestBrowserServiceWorkerClickWindow(t *testing.T) {
	ctx := browserCtx(t)
	m := appui.Handler(appui.Deps{Gate: appui.NewGate(appui.GateConfig{Log: slog.New(slog.NewTextHandler(io.Discard, nil))})})
	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)
	var got string
	err := chromedp.Do(ctx,
		chromedp.Navigate(srv.URL+"/app/manifest.webmanifest"),
		into(&got, chromedp.Evaluate[string](`fetch('/app/sw.js').then(function(r){ return r.text(); }).then(function(src){
  (0, eval)(src);
  var o = 'https://jam.example';
  return JSON.stringify([
    jamPickClient([o + '/app/admin/', o + '/app/next/c/x', o + '/app/user/']),
    jamPickClient([o + '/app/admin/', o + '/app/user/c/y']),
    jamPickClient([o + '/app/admin/']),
    jamTarget('chn_1'), jamTarget('')]);
})`, chromedp.EvalAwaitPromise)),
	)
	if err != nil {
		t.Fatal(err)
	}
	want := `[1,1,-1,"/app/user/c/chn_1","/app/user/"]`
	if got != want {
		t.Errorf("click window = %s\nwant            %s", got, want)
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
	await := chromedp.EvalAwaitPromise
	err := chromedp.Do(ctx,
		chromedp.Navigate(srv.URL+"/app/manifest.webmanifest"),
		into(&got, chromedp.Evaluate[string](`fetch('/app/sw.js').then(function(r){ return r.text(); }).then(function(src){
  (0, eval)(src);
  var now = 1e12, off = {messages: 'off', needs: 'chime'};
  return JSON.stringify([
    jamSilent('messages', null, now - 30000, now),
    jamSilent('messages', null, now - 200000, now),
    jamSilent('messages', off, null, now),
    jamSilent('needs', off, null, now),
    jamNotice({}, null, true).options.silent,
    jamNotice({}, null).options.silent]);
})`, await)),
	)
	if err != nil {
		t.Fatal(err)
	}
	if want := `[true,false,true,false,true,false]`; got != want {
		t.Errorf("silent = %s, want %s", got, want)
	}
}
