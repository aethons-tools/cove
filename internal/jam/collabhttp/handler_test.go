package collabhttp_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/attach"
	"github.com/aethons-tools/cove/internal/jam/collab"
	"github.com/aethons-tools/cove/internal/jam/collabhttp"
	"github.com/aethons-tools/cove/internal/jam/sshca"
)

// testHeader carries the test caller: a user name, "op:<id>" for an operator,
// or "!invalid" / "!unbound" for the principal's error cases.
const testHeader = "X-Test-Caller"

type fakeTunnels struct {
	conn net.Conn
	err  error
}

func (f fakeTunnels) Open(context.Context, string, int32) (net.Conn, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.conn, nil
}

// testDeps: a MemStore with a live instance ses_a (project acme, role ic with
// ssh: members), alice a member, carol not.
func testDeps(t *testing.T) (collabhttp.Deps, *jam.MemStore) {
	t.Helper()
	st := jam.NewMemStore()
	if err := st.CreateProject("acme"); err != nil {
		t.Fatal(err)
	}
	p, _ := st.GetProject("acme")
	if err := st.PutRole(string(p.ID), jam.Role{Name: "ic", Collaborate: jam.CollaboratePolicy{SSH: "members"}}); err != nil {
		t.Fatal(err)
	}
	alice, err := st.CreateUser(jam.User{Name: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddMember(p.ID, alice.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser(jam.User{Name: "carol"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutInstance(jam.Instance{ActorID: "ses_a", Project: "acme", Role: "ic", Phase: jam.PhaseLive}); err != nil {
		t.Fatal(err)
	}
	_, caPriv, _ := ed25519.GenerateKey(rand.Reader)
	blk, _ := ssh.MarshalPrivateKey(caPriv, "")
	ca, err := sshca.Parse(pem.EncodeToMemory(blk))
	if err != nil {
		t.Fatal(err)
	}
	d := collabhttp.Deps{
		Store:   st,
		CA:      ca,
		Tunnels: fakeTunnels{err: attach.ErrStudioNotConnected},
		Notify:  func(jam.Instance, string) {},
		Now:     func() time.Time { return time.Unix(1_000_000, 0) },
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Principal: func(r *http.Request) (collab.Principal, string, error) {
			who := r.Header.Get(testHeader)
			switch {
			case who == "":
				return collab.Principal{}, "", collabhttp.ErrNoCredentials
			case who == "!invalid":
				return collab.Principal{}, "", collabhttp.ErrInvalidToken
			case who == "!unbound":
				return collab.Principal{}, "", collabhttp.ErrUnbound
			case strings.HasPrefix(who, "op:"):
				return collab.Principal{Operator: strings.TrimPrefix(who, "op:")}, who, nil
			}
			id, ok := st.LookupName(ident.User, who)
			if !ok {
				return collab.Principal{}, "", collabhttp.ErrUnbound
			}
			return collab.Principal{UserID: id}, who, nil
		},
	}
	return d, st
}

func withTestPrincipal(r *http.Request, who string) *http.Request {
	if who != "" {
		r.Header.Set(testHeader, who)
	}
	return r
}

func withTestPrincipalMW(h http.Handler, who string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.ServeHTTP(w, withTestPrincipal(r, who)) })
}

func testUserPub(t *testing.T) string {
	t.Helper()
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	k, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return string(ssh.MarshalAuthorizedKey(k))
}

func certReq(pub, who string) *http.Request {
	req := httptest.NewRequest("POST", "/me/studios/ses_a/ssh/cert", strings.NewReader(`{"public_key":`+strconv.Quote(pub)+`}`))
	return withTestPrincipal(req, who)
}

func TestCertEndpointIssuesForAuthorizedOnly(t *testing.T) {
	d, st := testDeps(t) // MemStore with an instance ses_a (role ic, ssh: members) and users alice (member), carol (none)
	h := collabhttp.Routes("/me", d)
	pub := testUserPub(t)
	for name, tc := range map[string]struct {
		who  string
		want int
	}{
		"member allowed": {"alice", 200}, "stranger": {"carol", 403}, "anonymous": {"", 401},
	} {
		req := httptest.NewRequest("POST", "/me/studios/ses_a/ssh/cert", strings.NewReader(`{"public_key":`+strconv.Quote(pub)+`}`))
		req = withTestPrincipal(req, tc.who)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	req := httptest.NewRequest("POST", "/me/studios/ses_nope/ssh/cert", strings.NewReader(`{"public_key":"x"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, withTestPrincipal(req, "alice"))
	if rec.Code != 404 {
		t.Errorf("unknown studio: %d", rec.Code)
	}
	_ = st
}

// The cert's key-id names the user and studio for sshd's auth log:
// user=<user-id> studio=<session-id>, operators user=op:<operator-id>.
func TestCertKeyIDNamesUserAndStudio(t *testing.T) {
	d, st := testDeps(t)
	alice, _ := st.LookupName(ident.User, "alice")
	h := collabhttp.Routes("/me", d)
	for who, want := range map[string]string{
		"alice":    "user=" + string(alice) + " studio=ses_a",
		"op:local": "user=op:local studio=ses_a",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, certReq(testUserPub(t), who))
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", who, rec.Code, rec.Body)
		}
		var out struct {
			Certificate string    `json:"certificate"`
			ExpiresAt   time.Time `json:"expires_at"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(out.Certificate))
		if err != nil {
			t.Fatal(err)
		}
		cert, ok := k.(*ssh.Certificate)
		if !ok {
			t.Fatalf("%s: not a certificate", who)
		}
		if cert.KeyId != want {
			t.Errorf("%s: key-id %q, want %q", who, cert.KeyId, want)
		}
		if !out.ExpiresAt.Equal(d.Now().Add(sshca.Validity)) {
			t.Errorf("%s: expires_at %v", who, out.ExpiresAt)
		}
	}
}

func TestCertEndpointRefusals(t *testing.T) {
	d, st := testDeps(t)
	_ = st.PutInstance(jam.Instance{ActorID: "ses_p", Project: "acme", Role: "ic", Phase: jam.PhaseIdled})
	h := collabhttp.Routes("/me", d)
	cases := []struct {
		name string
		req  *http.Request
		want int
	}{
		{"invalid token", certReq(testUserPub(t), "!invalid"), 401},
		{"unbound login", certReq(testUserPub(t), "!unbound"), 403},
		{"not live", withTestPrincipal(httptest.NewRequest("POST", "/me/studios/ses_p/ssh/cert", strings.NewReader(`{"public_key":"x"}`)), "op:local"), 409},
		{"bad key", certReq("not a key", "alice"), 400},
		{"bad json", withTestPrincipal(httptest.NewRequest("POST", "/me/studios/ses_a/ssh/cert", strings.NewReader(`{`)), "alice"), 400},
		{"oversize body", certReq(strings.Repeat("a", 17<<10), "alice"), 413},
		{"wrong method", withTestPrincipal(httptest.NewRequest("GET", "/me/studios/ses_a/ssh/cert", nil), "alice"), 405},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, tc.req)
		if rec.Code != tc.want {
			t.Errorf("%s: %d, want %d (%s)", tc.name, rec.Code, tc.want, rec.Body)
		}
		if tc.name == "invalid token" {
			if got := rec.Header().Get("WWW-Authenticate"); got != `Bearer error="invalid_token"` {
				t.Errorf("WWW-Authenticate %q", got)
			}
			if !strings.Contains(rec.Body.String(), "at-jam login --participant") {
				t.Errorf("no sign-in hint: %q", rec.Body)
			}
		}
	}
}

func TestNilCAIsFeatureOff(t *testing.T) {
	d, _ := testDeps(t)
	d.CA = nil
	h := collabhttp.Routes("/me", d)
	for _, req := range []*http.Request{certReq(testUserPub(t), "alice"), withTestPrincipal(httptest.NewRequest("GET", "/me/studios/ses_a/ssh", nil), "alice")} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 404 {
			t.Errorf("%s %s: %d, want 404", req.Method, req.URL.Path, rec.Code)
		}
	}
}

// The audit lines name user, session and reason — never the key or cert.
func TestCertAuditNeverLogsKeys(t *testing.T) {
	d, _ := testDeps(t)
	var buf bytes.Buffer
	d.Log = slog.New(slog.NewTextHandler(&buf, nil))
	pub := testUserPub(t)
	rec := httptest.NewRecorder()
	collabhttp.Routes("/me", d).ServeHTTP(rec, certReq(pub, "alice"))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	logged := buf.String()
	for _, want := range []string{`msg="ssh cert issued"`, "session=ses_a", "reason=role", "user=usr_"} {
		if !strings.Contains(logged, want) {
			t.Errorf("audit lacks %q: %s", want, logged)
		}
	}
	keyBody := strings.Fields(pub)[1]
	var out struct{ Certificate string }
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if strings.Contains(logged, keyBody) || strings.Contains(logged, strings.Fields(out.Certificate)[1]) {
		t.Errorf("audit leaked key material: %s", logged)
	}
}

func TestUpgradeSplicesAndNotifies(t *testing.T) {
	d, _ := testDeps(t)
	var mu sync.Mutex
	var notices []string
	d.Notify = func(inst jam.Instance, body string) { mu.Lock(); notices = append(notices, body); mu.Unlock() }
	studio, human := net.Pipe()
	d.Tunnels = fakeTunnels{conn: human}
	srv := httptest.NewServer(withTestPrincipalMW(collabhttp.Routes("/me", d), "alice"))
	defer srv.Close()
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "GET /me/studios/ses_a/ssh HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\nUpgrade: jam-tunnel\r\n\r\n")
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil || resp.StatusCode != 101 {
		t.Fatalf("upgrade: %v %v", resp, err)
	}
	go func() { _, _ = studio.Write([]byte("SSH-2.0-x\r\n")) }()
	line, _ := br.ReadString('\n')
	if line != "SSH-2.0-x\r\n" {
		t.Fatalf("relayed %q", line)
	}
	// And the other way: the human's bytes reach the studio.
	go func() { _, _ = conn.Write([]byte("SSH-2.0-client\r\n")) }()
	got, _ := bufio.NewReader(studio).ReadString('\n')
	if got != "SSH-2.0-client\r\n" {
		t.Fatalf("to studio %q", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(notices) != 1 || !strings.Contains(notices[0], "alice connected over SSH") {
		t.Fatalf("notices %v", notices)
	}
}

func TestUpgradeMapsBrokerErrors(t *testing.T) {
	for err, want := range map[error]int{attach.ErrStudioNotConnected: 503, attach.ErrDialBackTimeout: 504, attach.ErrTooManyTunnels: 429} {
		d, _ := testDeps(t)
		d.Tunnels = fakeTunnels{err: err}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/me/studios/ses_a/ssh", nil)
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Upgrade", "jam-tunnel")
		collabhttp.Routes("/me", d).ServeHTTP(rec, withTestPrincipal(req, "alice"))
		if rec.Code != want {
			t.Errorf("%v → %d, want %d", err, rec.Code, want)
		}
	}
}

func TestUpgradeRefusals(t *testing.T) {
	d, st := testDeps(t)
	_ = st.PutInstance(jam.Instance{ActorID: "ses_p", Project: "acme", Role: "ic", Phase: jam.PhaseIdled})
	opened := false
	d.Tunnels = tunnelsFunc(func() { opened = true })
	h := collabhttp.Routes("/me", d)
	up := func(path, who string, upgrade bool) *http.Request {
		req := httptest.NewRequest("GET", path, nil)
		if upgrade {
			req.Header.Set("Connection", "Upgrade")
			req.Header.Set("Upgrade", "jam-tunnel")
		}
		return withTestPrincipal(req, who)
	}
	for _, tc := range []struct {
		name string
		req  *http.Request
		want int
	}{
		{"anonymous", up("/me/studios/ses_a/ssh", "", true), 401},
		{"stranger", up("/me/studios/ses_a/ssh", "carol", true), 403},
		{"unknown", up("/me/studios/ses_nope/ssh", "alice", true), 404},
		{"not live", up("/me/studios/ses_p/ssh", "op:local", true), 409},
		{"no upgrade", up("/me/studios/ses_a/ssh", "alice", false), 426},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, tc.req)
		if rec.Code != tc.want {
			t.Errorf("%s: %d, want %d (%s)", tc.name, rec.Code, tc.want, rec.Body)
		}
	}
	if opened {
		t.Error("a refused request opened a tunnel")
	}
}

type tunnelsFunc func()

func (f tunnelsFunc) Open(context.Context, string, int32) (net.Conn, error) {
	f()
	return nil, errors.New("unexpected open")
}

// A cert or tunnel request on an idled (paused) studio: with no wake path it
// is refused 409 saying so; with one the studio is resumed, and the request
// waits (bounded) for it to reconnect before it proceeds.
func TestIdledStudioWakes(t *testing.T) {
	idle := func(t *testing.T) (collabhttp.Deps, *jam.MemStore) {
		d, st := testDeps(t)
		_ = st.PutInstance(jam.Instance{ActorID: "ses_a", Project: "acme", Role: "ic", Phase: jam.PhaseIdled})
		return d, st
	}
	t.Run("no wake path", func(t *testing.T) {
		d, _ := idle(t)
		rec := httptest.NewRecorder()
		collabhttp.Routes("/me", d).ServeHTTP(rec, certReq(testUserPub(t), "alice"))
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "studio is idle; it resumes when it gets a message") {
			t.Fatalf("%d %q", rec.Code, rec.Body)
		}
	})
	t.Run("resumed and reconnected", func(t *testing.T) {
		d, st := idle(t)
		var resumed atomic.Int32
		var connected atomic.Bool
		d.Resume = func(_ context.Context, id string) error {
			resumed.Add(1)
			inst, _ := st.GetInstance(id)
			inst.Phase = jam.PhaseLive
			_ = st.PutInstance(inst)
			go func() { time.Sleep(50 * time.Millisecond); connected.Store(true) }() // the stream reconnects a little later
			return nil
		}
		d.Connected = func(string) bool { return connected.Load() }
		rec := httptest.NewRecorder()
		collabhttp.Routes("/me", d).ServeHTTP(rec, certReq(testUserPub(t), "alice"))
		if rec.Code != http.StatusOK || resumed.Load() != 1 || !connected.Load() {
			t.Fatalf("%d %q resumed=%d connected=%v", rec.Code, rec.Body, resumed.Load(), connected.Load())
		}
	})
	t.Run("resumed by someone else first", func(t *testing.T) {
		d, st := idle(t)
		d.Resume = func(_ context.Context, id string) error {
			inst, _ := st.GetInstance(id) // wake-on got there first: unpause fails, but it is live
			inst.Phase = jam.PhaseLive
			_ = st.PutInstance(inst)
			return errors.New("container is not paused")
		}
		d.Connected = func(string) bool { return true }
		rec := httptest.NewRecorder()
		collabhttp.Routes("/me", d).ServeHTTP(rec, certReq(testUserPub(t), "alice"))
		if rec.Code != http.StatusOK {
			t.Fatalf("%d %q", rec.Code, rec.Body)
		}
	})
	t.Run("resume fails", func(t *testing.T) {
		d, _ := idle(t)
		d.Resume = func(context.Context, string) error { return errors.New("docker: no") }
		d.Connected = func(string) bool { return false }
		rec := httptest.NewRecorder()
		collabhttp.Routes("/me", d).ServeHTTP(rec, certReq(testUserPub(t), "alice"))
		if rec.Code != http.StatusServiceUnavailable || strings.Contains(rec.Body.String(), "docker") {
			t.Fatalf("%d %q", rec.Code, rec.Body)
		}
	})
	t.Run("never reconnects", func(t *testing.T) {
		d, st := idle(t)
		d.Resume = func(_ context.Context, id string) error {
			inst, _ := st.GetInstance(id)
			inst.Phase = jam.PhaseLive
			_ = st.PutInstance(inst)
			return nil
		}
		d.Connected = func(string) bool { return false }
		d.WakeWait = 100 * time.Millisecond
		rec := httptest.NewRecorder()
		collabhttp.Routes("/me", d).ServeHTTP(rec, certReq(testUserPub(t), "alice"))
		if rec.Code != http.StatusGatewayTimeout {
			t.Fatalf("%d %q", rec.Code, rec.Body)
		}
	})
	t.Run("refused callers never wake it", func(t *testing.T) {
		d, _ := idle(t)
		d.Resume = func(context.Context, string) error { t.Error("resumed for a caller without ssh"); return nil }
		d.Connected = func(string) bool { return true }
		rec := httptest.NewRecorder()
		collabhttp.Routes("/me", d).ServeHTTP(rec, certReq(testUserPub(t), "carol"))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%d %q", rec.Code, rec.Body)
		}
	})
}

// A studio that is pausing or stopping refuses the tunnel with 409.
func TestUpgradeTunnelsClosed(t *testing.T) {
	d, _ := testDeps(t)
	d.Tunnels = fakeTunnels{err: attach.ErrTunnelsClosed}
	req := httptest.NewRequest("GET", "/me/studios/ses_a/ssh", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "jam-tunnel")
	rec := httptest.NewRecorder()
	collabhttp.Routes("/me", d).ServeHTTP(rec, withTestPrincipal(req, "alice"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("%d %q", rec.Code, rec.Body)
	}
}
