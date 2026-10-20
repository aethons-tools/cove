package collabhttp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/attach"
	"github.com/aethons-tools/cove/internal/jam/collab"
	"github.com/aethons-tools/cove/internal/jam/preview"
)

const (
	previewSession = "ses_" + label26
	appHost        = "app--" + label26 + "." + testDomain
	appPort        = 5173
)

// appTunnels is a fake Tunnels whose Open dials the test app's listener,
// recording each (actor, port) it was asked for.
type appTunnels struct {
	mu    sync.Mutex
	addr  string
	err   error
	opens []string
	// onOpen, when set, runs at the start of each Open (outside mu).
	onOpen func()
}

// Open is the SSH-class open; the proxy must use OpenPreview, so an Open is
// recorded as "ssh:<actor>:<port>".
func (f *appTunnels) Open(ctx context.Context, actorID string, port int32) (net.Conn, error) {
	return f.open(ctx, "ssh:"+actorID, port)
}

func (f *appTunnels) OpenPreview(ctx context.Context, actorID string, port int32) (net.Conn, error) {
	return f.open(ctx, actorID, port)
}

func (f *appTunnels) open(ctx context.Context, actorID string, port int32) (net.Conn, error) {
	if f.onOpen != nil {
		f.onOpen()
	}
	f.mu.Lock()
	f.opens = append(f.opens, fmt.Sprintf("%s:%d", actorID, port))
	addr, err := f.addr, f.err
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	var d net.Dialer
	return d.DialContext(ctx, "tcp", addr)
}

func (f *appTunnels) opened() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.opens...)
}

type previewHarness struct {
	h     *PreviewHandler
	auth  *previewFixture
	st    *jam.MemStore
	live  *Live
	tun   *appTunnels
	clock *testClock
	logs  *lockedWriter
	d     Deps
	ids   map[string]ident.ID
}

// newPreviewHarness: session ses_<label26> (project acme, role ic with
// preview: members) exposes "app" at port 5173, reached at app's listener;
// alice and bob are members, carol is not. A participant logs in with ID
// token "id-<name>"; a bearer "Bearer participant-token" is alice.
func newPreviewHarness(t *testing.T, app *httptest.Server) *previewHarness {
	t.Helper()
	st := jam.NewMemStore()
	if err := st.CreateProject("acme"); err != nil {
		t.Fatal(err)
	}
	p, _ := st.GetProject("acme")
	if err := st.PutRole(string(p.ID), jam.Role{Name: "ic", Collaborate: jam.CollaboratePolicy{Preview: "members"}}); err != nil {
		t.Fatal(err)
	}
	ids := map[string]ident.ID{}
	for _, name := range []string{"alice", "bob", "carol"} {
		u, err := st.CreateUser(jam.User{Name: name})
		if err != nil {
			t.Fatal(err)
		}
		ids[name] = u.ID
		if name != "carol" {
			if err := st.AddMember(p.ID, u.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := st.PutInstance(jam.Instance{ActorID: previewSession, Project: "acme", Role: "ic", Phase: jam.PhaseLive}); err != nil {
		t.Fatal(err)
	}
	clock := &testClock{t: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)}
	logs := &bytes.Buffer{}
	lw := &lockedWriter{w: logs}
	log := slog.New(slog.NewTextHandler(lw, nil))
	auth := NewPreviewAuth(PreviewAuthOptions{
		Domain: testDomain,
		Port:   "8443",
		Participant: func(_ context.Context, tok string) (ident.ID, string, bool) {
			name, ok := strings.CutPrefix(tok, "id-")
			id, found := ids[name]
			return id, name, ok && found
		},
		Bearer: func(r *http.Request) (collab.Principal, string, error) {
			if r.Header.Get("Authorization") == "Bearer participant-token" {
				return collab.Principal{UserID: ids["alice"]}, "alice", nil
			}
			return collab.Principal{}, "", ErrNoCredentials
		},
		Now: clock.Now,
		Log: log,
	})
	tun := &appTunnels{}
	if app != nil {
		tun.addr = app.Listener.Addr().String()
	}
	live := NewLive()
	var mu sync.Mutex
	exps := []jam.Exposure{{Name: "app", Port: appPort}}
	d := Deps{
		Store:   st,
		Tunnels: tun,
		Now:     clock.Now,
		Log:     log,
		Live:    live,
		Exposures: func(actorID string) []jam.Exposure {
			mu.Lock()
			defer mu.Unlock()
			if actorID != previewSession {
				return nil
			}
			return append([]jam.Exposure(nil), exps...)
		},
		PreviewURL: func(actorID, name string) (string, error) { return "https://" + name + "--x." + testDomain + "/", nil },
	}
	return &previewHarness{
		h: Preview(d, auth, testDomain), auth: &previewFixture{PreviewAuth: auth, clock: clock, logs: logs},
		st: st, live: live, tun: tun, clock: clock, logs: lw, d: d, ids: ids,
	}
}

// lockedWriter serialises log writes from the proxy's goroutines.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func (ph *previewHarness) logged() string {
	ph.logs.mu.Lock()
	defer ph.logs.mu.Unlock()
	return ph.logs.w.(*bytes.Buffer).String()
}

// login gives user a preview session on appHost and returns its cookie.
func (ph *previewHarness) login(t *testing.T, user string) *http.Cookie {
	t.Helper()
	code := ph.auth.bounce(t, "id-"+user, "", "https://"+appHost+"/", appHost)
	w := ph.auth.trade(appHost, code, "/")
	if w.Code != http.StatusFound {
		t.Fatalf("trade = %d", w.Code)
	}
	return w.Result().Cookies()[0]
}

func (ph *previewHarness) get(path string, c *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "https://"+appHost+path, nil)
	if c != nil {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	ph.h.ServeHTTP(rec, req)
	return rec
}

func (ph *previewHarness) previewEntries() []*liveEntry {
	ph.live.mu.Lock()
	defer ph.live.mu.Unlock()
	var out []*liveEntry
	for e := range ph.live.conns {
		if e.right == collab.Preview {
			out = append(out, e)
		}
	}
	return out
}

func helloApp(t *testing.T, got *http.Header) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got != nil {
			mu.Lock()
			*got = r.Header.Clone()
			mu.Unlock()
		}
		fmt.Fprint(w, "hello from the app")
	}))
	t.Cleanup(app.Close)
	return app
}

func TestPreviewStripsJamCredentials(t *testing.T) {
	var got http.Header
	app := helloApp(t, &got)
	ph := newPreviewHarness(t, app)
	sess := ph.login(t, "alice")
	req := httptest.NewRequest("GET", "https://"+appHost+"/x", nil)
	req.Header.Set("Cookie", "theme=dark; "+PreviewCookie+"="+sess.Value+"; jam_preview=stale; other=1")
	req.Header.Set("X-Jam-User", "forged")
	req.Header.Set("Forwarded", "for=6.6.6.6;host=evil.example;proto=http")
	req.Header.Set("X-Real-IP", "6.6.6.6")
	rec := httptest.NewRecorder()
	ph.h.ServeHTTP(rec, req)
	if rec.Code != 200 || rec.Body.String() != "hello from the app" {
		t.Fatalf("%d %q", rec.Code, rec.Body)
	}
	if c := got.Get("Cookie"); strings.Contains(c, "jam_preview") || strings.Contains(c, sess.Value) || !strings.Contains(c, "theme=dark") || !strings.Contains(c, "other=1") {
		t.Fatalf("app saw Cookie %q", c)
	}
	if got.Get("X-Jam-User") != "alice" || got.Get("X-Forwarded-Proto") != "https" || got.Get("X-Forwarded-Host") != appHost {
		t.Fatalf("forwarding headers %v", got)
	}
	if got.Get("Forwarded") != "" || got.Get("X-Real-IP") != "" {
		t.Fatalf("client Forwarded/X-Real-IP reached the app: %v", got)
	}
	if len(got.Values("X-Jam-User")) != 1 {
		t.Fatalf("X-Jam-User %v", got.Values("X-Jam-User"))
	}
	// A request carrying a Jam bearer has it removed.
	breq := httptest.NewRequest("GET", "https://"+appHost+"/x", nil)
	breq.Header.Set("Authorization", "Bearer participant-token")
	brec := httptest.NewRecorder()
	ph.h.ServeHTTP(brec, breq)
	if brec.Code != 200 {
		t.Fatalf("bearer: %d %q", brec.Code, brec.Body)
	}
	if got.Get("Authorization") != "" {
		t.Fatal("Jam bearer forwarded to the app")
	}
	// A cookie-authenticated request keeps the app's own Authorization.
	areq := httptest.NewRequest("GET", "https://"+appHost+"/x", nil)
	areq.AddCookie(sess)
	areq.Header.Set("Authorization", "Basic YXBwOnB3")
	ph.h.ServeHTTP(httptest.NewRecorder(), areq)
	if got.Get("Authorization") != "Basic YXBwOnB3" {
		t.Fatalf("app's own Authorization dropped: %q", got.Get("Authorization"))
	}
	// Only the session cookie reached the app as nothing at all.
	ph.get("/x", sess)
	if c := got.Get("Cookie"); c != "" {
		t.Fatalf("app saw Cookie %q", c)
	}
}

// An app cannot set (fix or toss) Jam's own cookies through the proxy.
func TestPreviewStripsJamSetCookies(t *testing.T) {
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, c := range []string{
			"__Host-jam_preview=fixed; Path=/; Secure",
			"__host-JAM_PREVIEW=fixed; Path=/; Secure",
			"jam_preview=fixed",
			"__Host-jam_oauth_state=x; Path=/; Secure",
			"jam_oauth=x",
			"app_session=keep; Path=/",
			"theme=x; Domain=" + testDomain + "; Path=/",
			"t2=y; domain=.jam.example",
		} {
			w.Header().Add("Set-Cookie", c)
		}
		w.Header().Set("Clear-Site-Data", `"cookies"`)
		fmt.Fprint(w, "ok")
	}))
	defer app.Close()
	ph := newPreviewHarness(t, app)
	rec := ph.get("/", ph.login(t, "alice"))
	if rec.Code != 200 {
		t.Fatalf("%d %q", rec.Code, rec.Body)
	}
	want := []string{"app_session=keep; Path=/", "theme=x; Path=/", "t2=y"}
	if got := rec.Header().Values("Set-Cookie"); !slices.Equal(got, want) {
		t.Fatalf("Set-Cookie %q, want %q", got, want)
	}
	if v := rec.Header().Values("Clear-Site-Data"); len(v) != 0 {
		t.Fatalf("Clear-Site-Data %q reached the viewer", v)
	}
}

func TestPreviewNotExposedIs404(t *testing.T) {
	app := helloApp(t, nil)
	ph := newPreviewHarness(t, app)
	c := ph.login(t, "alice")
	for _, host := range []string{
		"api--" + label26 + "." + testDomain,                 // name not exposed
		"app--" + strings.Repeat("z", 26) + "." + testDomain, // no such session
		"nonsense." + testDomain,                             // not a preview host
		testDomain,
	} {
		for _, cookie := range []*http.Cookie{c, nil} {
			req := httptest.NewRequest("GET", "https://"+host+"/", nil)
			if cookie != nil {
				req.AddCookie(cookie)
			}
			req.Header.Set("Accept", "text/html")
			rec := httptest.NewRecorder()
			ph.h.ServeHTTP(rec, req)
			if rec.Code != 404 || !strings.Contains(rec.Body.String(), "nothing is exposed at this address") {
				t.Errorf("%s: %d %q", host, rec.Code, rec.Body)
			}
		}
	}
	// An instance that is not live or idled has no previews.
	_ = ph.st.PutInstance(jam.Instance{ActorID: previewSession, Project: "acme", Role: "ic", Phase: jam.PhaseTerminating})
	if rec := ph.get("/", c); rec.Code != 404 {
		t.Errorf("stopping studio: %d", rec.Code)
	}
	if n := len(ph.tun.opened()); n != 0 {
		t.Fatalf("opened %d tunnels", n)
	}
}

func TestPreviewLoginHostAndReservedPaths(t *testing.T) {
	ph := newPreviewHarness(t, helloApp(t, nil))
	rt := "https://" + appHost + "/x"
	req := httptest.NewRequest("GET", "https://"+loginHost+"/__jam/start?return_to="+url.QueryEscape(rt), nil)
	rec := httptest.NewRecorder()
	ph.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), "/__jam/auth/login?") {
		t.Fatalf("login host: %d %v", rec.Code, rec.Header())
	}
	c := ph.login(t, "alice")
	if rec := ph.get("/__jam/nope", c); rec.Code != 404 {
		t.Fatalf("/__jam/nope: %d", rec.Code)
	}
	if n := len(ph.tun.opened()); n != 0 {
		t.Fatalf("a reserved path was proxied (%d opens)", n)
	}
}

func TestPreviewNoCredentials(t *testing.T) {
	ph := newPreviewHarness(t, helloApp(t, nil))
	req := httptest.NewRequest("GET", "https://"+appHost+"/x?y=1", nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	rec := httptest.NewRecorder()
	ph.h.ServeHTTP(rec, req)
	loc := rec.Header().Get("Location")
	if rec.Code != http.StatusFound || !strings.HasPrefix(loc, "https://"+loginHost+"/__jam/start?return_to=") {
		t.Fatalf("%d %q", rec.Code, loc)
	}
	if rec := ph.get("/x", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("non-browser: %d", rec.Code)
	}
	if n := len(ph.tun.opened()); n != 0 {
		t.Fatalf("opened %d tunnels", n)
	}
}

func TestPreviewRefusesWithoutRight(t *testing.T) {
	ph := newPreviewHarness(t, helloApp(t, nil))
	rec := ph.get("/", ph.login(t, "carol"))
	if rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "hello") {
		t.Fatalf("%d %q", rec.Code, rec.Body)
	}
	if n := len(ph.tun.opened()); n != 0 {
		t.Fatalf("opened %d tunnels", n)
	}
	// An unbound bearer login is 403 too, an invalid token 401.
	for err, want := range map[error]int{ErrUnbound: 403, ErrInvalidToken: 401} {
		ph.auth.o.Bearer = func(*http.Request) (collab.Principal, string, error) { return collab.Principal{}, "", err }
		req := httptest.NewRequest("GET", "https://"+appHost+"/", nil)
		req.Header.Set("Authorization", "Bearer whatever")
		rec := httptest.NewRecorder()
		ph.h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("%v: %d, want %d", err, rec.Code, want)
		}
	}
}

func TestPreviewResumesPausedStudio(t *testing.T) {
	app := helloApp(t, nil)
	ph := newPreviewHarness(t, app)
	_ = ph.st.PutInstance(jam.Instance{ActorID: previewSession, Project: "acme", Role: "ic", Phase: jam.PhaseIdled})
	resumed := 0
	ph.d.Resume = func(_ context.Context, id string) error {
		resumed++
		inst, _ := ph.st.GetInstance(id)
		inst.Phase = jam.PhaseLive
		return ph.st.PutInstance(inst)
	}
	ph.d.Connected = func(string) bool { return true }
	ph.h = Preview(ph.d, ph.auth.PreviewAuth, testDomain)
	rec := ph.get("/", ph.login(t, "alice"))
	if rec.Code != 200 || rec.Body.String() != "hello from the app" || resumed != 1 {
		t.Fatalf("%d %q resumed=%d", rec.Code, rec.Body, resumed)
	}
}

func TestPreviewDialErrorPages(t *testing.T) {
	ph := newPreviewHarness(t, nil)
	c := ph.login(t, "alice")
	for _, tc := range []struct {
		err  error
		code int
		msg  string
	}{
		{attach.ErrDialBackTimeout, 504, "studio could not open the preview (is its image current?)"},
		{attach.ErrStudioNotConnected, 503, "studio not connected"},
		{attach.ErrTooManyTunnels, 429, "too many"},
		{attach.ErrTooManyPreviewTunnels, 429, "kept for SSH"},
		{errors.New("connection refused"), 502, "the app at port 5173 is not answering"},
	} {
		ph.tun.mu.Lock()
		ph.tun.err = tc.err
		ph.tun.mu.Unlock()
		rec := ph.get("/", c)
		if rec.Code != tc.code || !strings.Contains(rec.Body.String(), tc.msg) || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
			t.Errorf("%v: %d %q", tc.err, rec.Code, rec.Body)
		}
	}
	// The app's listener is gone: the dial succeeds but nothing answers.
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	ph.tun.mu.Lock()
	ph.tun.err, ph.tun.addr = nil, dead.Listener.Addr().String()
	ph.tun.mu.Unlock()
	if rec := ph.get("/", c); rec.Code != 502 || !strings.Contains(rec.Body.String(), "the app at port 5173 is not answering") {
		t.Errorf("dead app: %d %q", rec.Code, rec.Body)
	}
}

func TestPreviewDialBackTimeoutPage(t *testing.T) {
	ph := newPreviewHarness(t, nil)
	ph.tun.err = attach.ErrDialBackTimeout
	start := time.Now()
	rec := ph.get("/", ph.login(t, "alice"))
	if rec.Code != http.StatusGatewayTimeout || !strings.Contains(rec.Body.String(), "studio could not open the preview (is its image current?)") {
		t.Fatalf("%d %q", rec.Code, rec.Body)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("dial-back timeout page took too long")
	}
}

// Two users' requests never share a pooled connection; one user's do.
func TestPreviewUsersNeverSharePooledConns(t *testing.T) {
	ph := newPreviewHarness(t, helloApp(t, nil))
	alice, bob := ph.login(t, "alice"), ph.login(t, "bob")
	for _, c := range []*http.Cookie{alice, bob, alice, bob, alice} {
		if rec := ph.get("/", c); rec.Code != 200 {
			t.Fatalf("%d %q", rec.Code, rec.Body)
		}
	}
	if n := len(ph.tun.opened()); n != 2 {
		t.Fatalf("opened %d tunnels, want one per user: %v", n, ph.tun.opened())
	}
	users := map[ident.ID]int{}
	for _, e := range ph.previewEntries() {
		if e.Session != previewSession || e.port != appPort || e.operator {
			t.Errorf("entry %+v", e)
		}
		users[e.userID]++
	}
	if len(users) != 2 || users[ph.ids["alice"]] != 1 || users[ph.ids["bob"]] != 1 {
		t.Fatalf("registered users %v", users)
	}
	if got := ph.tun.opened(); got[0] != previewSession+":5173" {
		t.Fatalf("opened %v", got)
	}
	// Preview connections are listed too, with their right.
	l := ph.live.List(previewSession)
	if len(l) != 2 || l[0].Right != "preview" || l[1].Right != "preview" {
		t.Fatalf("List = %+v", l)
	}
}

// The audit logs the first request per (session, user, name) per 10 minutes.
func TestPreviewOpenedAudit(t *testing.T) {
	ph := newPreviewHarness(t, helloApp(t, nil))
	c := ph.login(t, "alice")
	for range 3 {
		ph.get("/", c)
	}
	if n := strings.Count(ph.logged(), `msg="preview opened"`); n != 1 {
		t.Fatalf("%d preview opened lines: %s", n, ph.logged())
	}
	ph.clock.Add(11 * time.Minute)
	ph.get("/", c)
	logged := ph.logged()
	if n := strings.Count(logged, `msg="preview opened"`); n != 2 {
		t.Fatalf("%d preview opened lines after 11m", n)
	}
	for _, want := range []string{"user=" + string(ph.ids["alice"]), "session=" + previewSession, "name=app", "port=5173", "right=preview"} {
		if !strings.Contains(logged, want) {
			t.Errorf("audit lacks %q: %s", want, logged)
		}
	}
	if strings.Contains(logged, c.Value) {
		t.Fatal("audit leaked the session cookie")
	}
}

// wsApp is an echo "WebSocket" app: it answers Upgrade: websocket with 101
// and echoes every byte after it.
func wsApp(t *testing.T) *httptest.Server {
	t.Helper()
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			http.Error(w, "want websocket", http.StatusBadRequest)
			return
		}
		conn, brw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		_ = brw.Flush()
		_, _ = io.Copy(conn, brw)
	}))
	t.Cleanup(app.Close)
	return app
}

// openWS upgrades through the proxy as cookie's user and checks a frame
// round-trips, returning the client side.
func (ph *previewHarness) openWS(t *testing.T, c *http.Cookie) (net.Conn, *bufio.Reader) {
	t.Helper()
	srv := httptest.NewServer(ph.h)
	t.Cleanup(srv.Close)
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	fmt.Fprintf(conn, "GET /ws HTTP/1.1\r\nHost: %s\r\nOrigin: https://%s\r\nCookie: %s=%s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n", appHost, appHost, c.Name, c.Value)
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil || resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade: %v %v", resp, err)
	}
	fmt.Fprint(conn, "frame-1\n")
	if line, err := br.ReadString('\n'); err != nil || line != "frame-1\n" {
		t.Fatalf("echo %q %v", line, err)
	}
	return conn, br
}

func expectEOFWithin(t *testing.T, conn net.Conn, br *bufio.Reader, d time.Duration) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(d))
	if _, err := br.ReadByte(); !errors.Is(err, io.EOF) {
		t.Fatalf("read after close: %v, want EOF", err)
	}
}

func TestPreviewWebSocketClosedOnUnexpose(t *testing.T) {
	ph := newPreviewHarness(t, wsApp(t))
	conn, br := ph.openWS(t, ph.login(t, "alice"))
	if n := ph.live.CloseUnexposed(previewSession, []int32{appPort + 1, appPort}); n != 0 {
		t.Fatalf("a still-exposed port closed %d", n)
	}
	if n := ph.live.CloseUnexposed("ses_other", nil); n != 0 {
		t.Fatalf("another session's change closed %d", n)
	}
	if n := ph.live.CloseUnexposed(previewSession, []int32{appPort + 1}); n != 1 {
		t.Fatalf("CloseUnexposed closed %d", n)
	}
	expectEOFWithin(t, conn, br, time.Second)
}

// An operator's preview connection is closed on unexpose too.
func TestPreviewOperatorClosedOnUnexpose(t *testing.T) {
	ph := newPreviewHarness(t, wsApp(t))
	ph.auth.o.Operator = func(_ context.Context, tok string) (string, bool) { return "op1", tok == "op-token" }
	code := ph.auth.bounce(t, "", "op-token", "https://"+appHost+"/", appHost)
	w := ph.auth.trade(appHost, code, "/")
	conn, br := ph.openWS(t, w.Result().Cookies()[0])
	if n := ph.live.CloseUnexposed(previewSession, nil); n != 1 {
		t.Fatalf("CloseUnexposed closed %d", n)
	}
	expectEOFWithin(t, conn, br, time.Second)
}

func TestPreviewSweepRevokes(t *testing.T) {
	ph := newPreviewHarness(t, wsApp(t))
	if err := ph.st.PutShareGrant(jam.ShareGrant{SessionID: previewSession, UserID: ph.ids["carol"], Rights: []string{"preview"}, ExpiresAt: ph.clock.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	conn, br := ph.openWS(t, ph.login(t, "carol"))
	if n := ph.live.Sweep(ph.st, ph.clock.Now(), ph.d.Log); n != 0 {
		t.Fatalf("sweep with a live grant closed %d", n)
	}
	if n := ph.live.Sweep(ph.st, ph.clock.Now().Add(2*time.Hour), ph.d.Log); n != 1 {
		t.Fatalf("sweep closed %d", n)
	}
	expectEOFWithin(t, conn, br, time.Second)
	logged := ph.logged()
	var line string
	for l := range strings.SplitSeq(logged, "\n") {
		if strings.Contains(l, "revoked") {
			line = l
		}
	}
	if !strings.Contains(line, "right=preview") || !strings.Contains(line, `reason="grant expired"`) || strings.Contains(line, "ssh") {
		t.Fatalf("revoke line %q", line)
	}
}

func TestPreviewOffWithoutPreviewURL(t *testing.T) {
	ph := newPreviewHarness(t, helloApp(t, nil))
	c := ph.login(t, "alice")
	ph.d.PreviewURL = nil
	h := Preview(ph.d, ph.auth.PreviewAuth, testDomain)
	req := httptest.NewRequest("GET", "https://"+appHost+"/", nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatalf("%d", rec.Code)
	}
}

func TestStripJamCookies(t *testing.T) {
	h := http.Header{"Cookie": {"a=1; __Host-jam_preview=s; b=\"q\"", "jam_preview=x"}}
	stripJamCookies(h)
	if got := h.Values("Cookie"); len(got) != 1 || got[0] != `a=1; b="q"` {
		t.Fatalf("%q", got)
	}
	h = http.Header{"Cookie": {"__Host-jam_preview=s"}}
	stripJamCookies(h)
	if _, ok := h["Cookie"]; ok {
		t.Fatalf("%q", h)
	}
}

// countingStore counts ListInstances calls.
type countingStore struct {
	*jam.MemStore
	lists atomic.Int32
}

func (c *countingStore) ListInstances() []jam.Instance {
	c.lists.Add(1)
	return c.MemStore.ListInstances()
}

// A burst of made-up labels costs at most one store scan; rebuilds are at
// most one a second, and an unknown label is answered from memory for a while.
func TestPreviewUnknownLabelsThrottleRebuilds(t *testing.T) {
	ph := newPreviewHarness(t, helloApp(t, nil))
	cs := &countingStore{MemStore: ph.st}
	ph.d.Store = cs
	ph.h = Preview(ph.d, ph.auth.PreviewAuth, testDomain)
	unknown := func(i int) string { return fmt.Sprintf("app--%026d.%s", i, testDomain) }
	hit := func(host string) int {
		rec := httptest.NewRecorder()
		ph.h.ServeHTTP(rec, httptest.NewRequest("GET", "https://"+host+"/", nil))
		return rec.Code
	}
	var wg sync.WaitGroup
	for i := range 64 {
		wg.Go(func() {
			if code := hit(unknown(i)); code != 404 {
				t.Errorf("unknown label %d: %d", i, code)
			}
		})
	}
	wg.Wait()
	if n := cs.lists.Load(); n != 1 {
		t.Fatalf("burst: %d ListInstances, want 1", n)
	}
	// Within the second: a further new label is not rebuilt for.
	hit(unknown(999))
	if n := cs.lists.Load(); n != 1 {
		t.Fatalf("throttled: %d ListInstances", n)
	}
	// Past the throttle, a label still in the miss cache is answered from it.
	ph.clock.Add(1500 * time.Millisecond)
	hit(unknown(1))
	if n := cs.lists.Load(); n != 1 {
		t.Fatalf("negative cache: %d ListInstances", n)
	}
	// A label not yet asked for rebuilds again; a known one never does.
	hit(unknown(1000))
	hit(appHost)
	if n := cs.lists.Load(); n != 2 {
		t.Fatalf("after throttle: %d ListInstances, want 2", n)
	}
}

func fakeJWT(claims string) string {
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"RS256"}`)) + "." + enc([]byte(claims)) + ".c2ln"
}

// Beside a cookie session, a bearer that is Jam's — verified, or merely
// claiming Jam's issuer — is kept from the app; an app's own bearer is not.
func TestPreviewStripsJamBearerBesideCookie(t *testing.T) {
	var got http.Header
	ph := newPreviewHarness(t, helloApp(t, &got))
	ph.auth.o.BearerIssuer = "https://idp.example/"
	c := ph.login(t, "alice")
	for _, tc := range []struct {
		name, authz string
		stripped    bool
	}{
		{"valid Jam bearer", "Bearer participant-token", true},
		{"expired Jam token", "Bearer " + fakeJWT(`{"iss":"https://idp.example","exp":1}`), true},
		{"Jam issuer, lowercase scheme", "bearer " + fakeJWT(`{"iss":"https://idp.example/"}`), true},
		{"app's own JWT", "Bearer " + fakeJWT(`{"iss":"https://app.example"}`), false},
		{"app's opaque token", "Bearer app-token", false},
	} {
		got = nil
		req := httptest.NewRequest("GET", "https://"+appHost+"/", nil)
		req.AddCookie(c)
		req.Header.Set("Authorization", tc.authz)
		rec := httptest.NewRecorder()
		ph.h.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %q", tc.name, rec.Code, rec.Body)
		}
		if a := got.Get("Authorization"); (a == "") != tc.stripped {
			t.Errorf("%s: app saw Authorization %q", tc.name, a)
		}
	}
}

// Access that ends while the tunnel opens (an unshare racing the request) is
// refused 403 and leaves no live connection.
func TestPreviewDialRecheckRefuses(t *testing.T) {
	ph := newPreviewHarness(t, helloApp(t, nil))
	carol := ph.ids["carol"]
	if err := ph.st.PutShareGrant(jam.ShareGrant{SessionID: previewSession, UserID: carol, Rights: []string{"preview"}, ExpiresAt: ph.clock.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	ph.tun.onOpen = func() { _ = ph.st.RemoveShareGrant(previewSession, carol) }
	rec := ph.get("/", ph.login(t, "carol"))
	if rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "hello") {
		t.Fatalf("%d %q", rec.Code, rec.Body)
	}
	if n := len(ph.previewEntries()); n != 0 {
		t.Fatalf("%d live preview entries remain", n)
	}
}

// CONNECT (plain, or an RFC 8441 extended CONNECT) is never forwarded.
func TestPreviewRefusesConnect(t *testing.T) {
	ph := newPreviewHarness(t, helloApp(t, nil))
	req := httptest.NewRequest("CONNECT", appHost+":443", nil)
	req.Host = appHost
	req.Header.Set(":protocol", "websocket") // as Go's h2 server would carry an extended CONNECT
	req.AddCookie(ph.login(t, "alice"))
	rec := httptest.NewRecorder()
	ph.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed || len(ph.tun.opened()) != 0 {
		t.Fatalf("%d, %d opens", rec.Code, len(ph.tun.opened()))
	}
}

// A session exposed just after another label's rebuild would 404 on the
// rebuild throttle; InvalidateLabels (the exposure fan-out's hook) lifts it.
func TestPreviewInvalidateLabels(t *testing.T) {
	ph := newPreviewHarness(t, nil)
	if _, ok := ph.h.p.actor(preview.Label(previewSession)); !ok {
		t.Fatal("known session not found")
	}
	const fresh = "ses_fresh"
	if err := ph.st.PutInstance(jam.Instance{ActorID: fresh, Project: "acme", Role: "ic", Phase: jam.PhaseLive}); err != nil {
		t.Fatal(err)
	}
	if _, ok := ph.h.p.actor(preview.Label(fresh)); ok {
		t.Fatal("throttled rebuild should still miss")
	}
	ph.h.InvalidateLabels()
	if id, ok := ph.h.p.actor(preview.Label(fresh)); !ok || id != fresh {
		t.Fatalf("after invalidation: %q %v", id, ok)
	}
	(*PreviewHandler)(nil).InvalidateLabels() // previews off: a no-op
	Preview(Deps{}, nil, testDomain).InvalidateLabels()
}

// A refused cross-site use of the session cookie is a plain 403 — never the
// app, and never a login redirect (which a framing page could ride).
func TestPreviewCrossSiteCookieRefused(t *testing.T) {
	app := helloApp(t, nil)
	ph := newPreviewHarness(t, app)
	c := ph.login(t, "alice")
	req := httptest.NewRequest("GET", "https://"+appHost+"/", nil)
	req.AddCookie(c)
	req.Header.Set("Accept", "text/html")
	req.Header.Set("Sec-Fetch-Site", "same-site")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Dest", "iframe")
	rec := httptest.NewRecorder()
	ph.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || strings.TrimSpace(rec.Body.String()) != "cross-site request refused" || rec.Header().Get("Location") != "" {
		t.Fatalf("%d %q loc=%q", rec.Code, rec.Body, rec.Header().Get("Location"))
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("Content-Type %q", rec.Header().Get("Content-Type"))
	}
	if n := len(ph.tun.opened()); n != 0 {
		t.Fatalf("%d tunnels opened for a refused request", n)
	}
}

// Pooled preview connections hold studio tunnels (shared with SSH), so idle
// ones are given back after 30 s.
func TestPreviewIdlePoolTimeout(t *testing.T) {
	ph := newPreviewHarness(t, nil)
	tr, ok := ph.h.p.rp.Transport.(*http.Transport)
	if !ok || tr.IdleConnTimeout != 30*time.Second {
		t.Fatalf("transport %T idle timeout %v, want 30s", ph.h.p.rp.Transport, tr.IdleConnTimeout)
	}
}
