package collabhttp_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/collab"
	"github.com/aethons-tools/cove/internal/jam/collabhttp"
)

func do(h http.Handler, method, path, who, body string) *httptest.ResponseRecorder {
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, withTestPrincipal(httptest.NewRequest(method, path, r), who))
	return rec
}

func TestShareAPI(t *testing.T) {
	d, st := testDeps(t)
	var buf bytes.Buffer
	d.Log = slog.New(slog.NewTextHandler(&buf, nil))
	h := collabhttp.Routes("/admin", d)
	carol, _ := st.LookupName(ident.User, "carol")

	rec := do(h, "POST", "/admin/studios/ses_a/shares", "op:local", `{"user":"user:carol","rights":["ssh"]}`)
	if rec.Code != 201 {
		t.Fatalf("share: %d %s", rec.Code, rec.Body)
	}
	g := st.ShareGrants("ses_a")
	if len(g) != 1 || g[0].UserID != carol || g[0].ExpiresAt.Sub(g[0].CreatedAt) != collab.DefaultGrant || g[0].GrantedBy != "op:local" {
		t.Fatalf("grants %+v", g)
	}
	var out collabhttp.GrantView
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.User != "carol" || out.UserID != carol {
		t.Fatalf("response %s (%v)", rec.Body, err)
	}
	for _, f := range []string{"192h", "8d"} {
		rec := do(h, "POST", "/admin/studios/ses_a/shares", "op:local", `{"user":"carol","rights":["ssh"],"for":"`+f+`"}`)
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), "longer than 168h0m0s") {
			t.Fatalf("over-long grant %s: %d %s", f, rec.Code, rec.Body)
		}
	}
	rec = do(h, "POST", "/admin/studios/ses_a/shares", "op:local", `{"user":"carol","rights":["ssh","preview"],"for":"7d"}`)
	if rec.Code != 201 {
		t.Fatalf("7d share: %d %s", rec.Code, rec.Body)
	}
	if g := st.ShareGrants("ses_a"); len(g) != 1 || g[0].ExpiresAt.Sub(g[0].CreatedAt) != collab.MaxGrant || len(g[0].Rights) != 2 {
		t.Fatalf("re-share did not replace: %+v", g)
	}
	if rec := do(h, "DELETE", "/admin/studios/ses_a/shares/carol", "op:local", ""); rec.Code != 204 {
		t.Fatalf("unshare: %d %s", rec.Code, rec.Body)
	}
	if len(st.ShareGrants("ses_a")) != 0 {
		t.Fatalf("unshare did not remove: %v", st.ShareGrants("ses_a"))
	}
	if rec := do(h, "DELETE", "/admin/studios/ses_a/shares/carol", "op:local", ""); rec.Code != 404 {
		t.Fatalf("unshare of nothing: %d", rec.Code)
	}
	logged := buf.String()
	for _, want := range []string{`msg="studio shared"`, `msg="studio unshared"`, "grantor=op:local", "user=" + string(carol), "session=ses_a", "rights=ssh", "expires="} {
		if !strings.Contains(logged, want) {
			t.Errorf("audit lacks %q: %s", want, logged)
		}
	}
}

func TestShareRefusals(t *testing.T) {
	d, st := testDeps(t)
	_ = st.PutInstance(jam.Instance{ActorID: "ses_p", Project: "acme", Role: "ic", Phase: jam.PhaseIdled})
	h := collabhttp.Routes("/me", d)
	for _, tc := range []struct {
		name, who, path, body string
		want                  int
	}{
		{"anonymous", "", "/me/studios/ses_a/shares", `{"user":"carol","rights":["ssh"]}`, 401},
		{"unknown studio", "alice", "/me/studios/ses_nope/shares", `{"user":"carol","rights":["ssh"]}`, 404},
		{"unknown user", "alice", "/me/studios/ses_a/shares", `{"user":"dave","rights":["ssh"]}`, 404},
		{"bad right", "alice", "/me/studios/ses_a/shares", `{"user":"carol","rights":["root"]}`, 400},
		{"no rights", "alice", "/me/studios/ses_a/shares", `{"user":"carol"}`, 400},
		{"bad duration", "alice", "/me/studios/ses_a/shares", `{"user":"carol","rights":["ssh"],"for":"soon"}`, 400},
		{"negative", "alice", "/me/studios/ses_a/shares", `{"user":"carol","rights":["ssh"],"for":"-1h"}`, 400},
		{"bad json", "alice", "/me/studios/ses_a/shares", `{`, 400},
		{"stranger shares", "carol", "/me/studios/ses_a/shares", `{"user":"alice","rights":["ssh"]}`, 403},
		{"stranger reads access", "carol", "/me/studios/ses_a/access", ``, 403},
		{"stranger unshares", "carol", "/me/studios/ses_a/shares/alice", ``, 403},
	} {
		method := "POST"
		if strings.HasSuffix(tc.path, "/access") {
			method = "GET"
		} else if strings.Contains(tc.path, "/shares/") {
			method = "DELETE"
		}
		if rec := do(h, method, tc.path, tc.who, tc.body); rec.Code != tc.want {
			t.Errorf("%s: %d, want %d (%s)", tc.name, rec.Code, tc.want, rec.Body)
		}
	}
	// Sharing does not need the studio running.
	if rec := do(h, "POST", "/me/studios/ses_p/shares", "alice", `{"user":"carol","rights":["ssh"]}`); rec.Code != 201 {
		t.Errorf("share an idled studio: %d %s", rec.Code, rec.Body)
	}
}

// A participant may share only rights they hold: alice holds ssh (role
// policy) but not preview (the role says preview: none here).
func TestParticipantSharesOnlyRightsHeld(t *testing.T) {
	d, st := testDeps(t)
	p, _ := st.GetProject("acme")
	_ = st.PutRole(string(p.ID), jam.Role{Name: "ic", Collaborate: jam.CollaboratePolicy{SSH: "members", Preview: "none"}})
	h := collabhttp.Routes("/me", d)
	if rec := do(h, "POST", "/me/studios/ses_a/shares", "alice", `{"user":"carol","rights":["ssh"],"for":"2h"}`); rec.Code != 201 {
		t.Fatalf("share held ssh: %d %s", rec.Code, rec.Body)
	}
	alice, _ := st.LookupName(ident.User, "alice")
	if g := st.ShareGrants("ses_a"); len(g) != 1 || g[0].GrantedBy != string(alice) || g[0].ExpiresAt.Sub(g[0].CreatedAt) != 2*time.Hour {
		t.Fatalf("grants %+v", g)
	}
	rec := do(h, "POST", "/me/studios/ses_a/shares", "alice", `{"user":"carol","rights":["ssh","preview"]}`)
	if rec.Code != 403 || !strings.Contains(rec.Body.String(), "preview") {
		t.Fatalf("share unheld preview: %d %s", rec.Code, rec.Body)
	}
	// carol, now holding ssh by grant, may view access and re-share ssh — but
	// not beyond her own grant.
	if rec := do(h, "GET", "/me/studios/ses_a/access", "carol", ""); rec.Code != 200 {
		t.Fatalf("grantee reads access: %d %s", rec.Code, rec.Body)
	}
	bob, err := st.CreateUser(jam.User{Name: "bob"})
	if err != nil {
		t.Fatal(err)
	}
	if rec := do(h, "POST", "/me/studios/ses_a/shares", "carol", `{"user":"bob","rights":["ssh"],"for":"1h"}`); rec.Code != 201 {
		t.Fatalf("grantee re-shares within her grant: %d %s", rec.Code, rec.Body)
	}
	rec = do(h, "POST", "/me/studios/ses_a/shares", "carol", `{"user":"bob","rights":["ssh"],"for":"3h"}`)
	if rec.Code != 403 || !strings.Contains(rec.Body.String(), "beyond your own") {
		t.Fatalf("grantee outlives her grant: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "POST", "/me/studios/ses_a/shares", "carol", `{"user":"carol","rights":["ssh"],"for":"7d"}`); rec.Code != 403 {
		t.Fatalf("grantee extends her own grant: %d %s", rec.Code, rec.Body)
	}
	// carol may remove the grant she holds the rights of; bob's is ssh only.
	if rec := do(h, "DELETE", "/me/studios/ses_a/shares/bob", "carol", ""); rec.Code != 204 {
		t.Fatalf("grantee unshares: %d %s", rec.Code, rec.Body)
	}
	_ = bob
}

func TestAccessView(t *testing.T) {
	d, st := testDeps(t)
	carol, _ := st.LookupName(ident.User, "carol")
	alice, _ := st.LookupName(ident.User, "alice")
	now := d.Now()
	_ = st.PutInstance(jam.Instance{ActorID: "ses_a", Project: "acme", Role: "ic", Phase: jam.PhaseLive, SessionKind: jam.SessionKindPersonal, OwnerID: alice, Owner: "alice"})
	_ = st.PutShareGrant(jam.ShareGrant{SessionID: "ses_a", UserID: carol, Rights: []string{"ssh"}, CreatedAt: now, ExpiresAt: now.Add(time.Hour), GrantedBy: "op:local"})
	rec := do(collabhttp.Routes("/admin", d), "GET", "/admin/studios/ses_a/access", "op:local", "")
	if rec.Code != 200 {
		t.Fatalf("access: %d %s", rec.Code, rec.Body)
	}
	var v collabhttp.AccessView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Requester == nil || v.Requester.Name != "alice" || v.Role.Project != "acme" || v.Role.Role != "ic" || v.Role.SSH != "members" || v.Role.Preview != "members" {
		t.Fatalf("why: %+v", v)
	}
	if len(v.Grants) != 1 || v.Grants[0].User != "carol" || v.Grants[0].GrantedBy != "op:local" || v.Grants[0].Expired || v.Live == nil {
		t.Fatalf("grants/live: %s", rec.Body)
	}
}

// pipeTunnels hands each Open a fresh pipe, keeping the studio ends.
type pipeTunnels struct{ studios chan net.Conn }

func (p pipeTunnels) Open(context.Context, string, int32) (net.Conn, error) {
	studio, human := net.Pipe()
	p.studios <- studio
	return human, nil
}

// dialSSH opens the upgrade as who and returns the client connection.
func dialSSH(t *testing.T, srv *httptest.Server, who string) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	fmt.Fprintf(conn, "GET /admin/studios/ses_a/ssh HTTP/1.1\r\nHost: x\r\n%s: %s\r\nConnection: Upgrade\r\nUpgrade: jam-tunnel\r\n\r\n", testHeader, who)
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil || resp.StatusCode != 101 {
		t.Fatalf("upgrade %s: %v %v", who, resp, err)
	}
	return conn, br
}

// Unshare closes the user's own live SSH connections on that session — and
// only when the user no longer holds ssh by another rule.
func TestUnshareClosesThatUsersLiveConnections(t *testing.T) {
	d, st := testDeps(t)
	carol, _ := st.LookupName(ident.User, "carol")
	alice, _ := st.LookupName(ident.User, "alice")
	now := d.Now()
	for _, u := range []ident.ID{carol, alice} {
		_ = st.PutShareGrant(jam.ShareGrant{SessionID: "ses_a", UserID: u, Rights: []string{"ssh"}, CreatedAt: now, ExpiresAt: now.Add(time.Hour), GrantedBy: "op:local"})
	}
	tun := pipeTunnels{studios: make(chan net.Conn, 4)}
	d.Tunnels = tun
	d.Live = collabhttp.NewLive()
	h := collabhttp.Routes("/admin", d)
	srv := httptest.NewServer(h)
	defer srv.Close()
	carolConn, carolR := dialSSH(t, srv, "carol")
	<-tun.studios
	_, aliceR := dialSSH(t, srv, "alice")
	aliceStudio := <-tun.studios

	waitLive := func(n int) collabhttp.AccessView {
		t.Helper()
		var v collabhttp.AccessView
		for range 200 {
			rec := do(h, "GET", "/admin/studios/ses_a/access", "op:local", "")
			_ = json.Unmarshal(rec.Body.Bytes(), &v)
			if len(v.Live) == n {
				return v
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("live = %+v, want %d", v.Live, n)
		return v
	}
	v := waitLive(2)
	for _, c := range v.Live {
		if c.Session != "ses_a" || c.Since.IsZero() || (c.Name != "carol" && c.Name != "alice") {
			t.Fatalf("live %+v", v.Live)
		}
	}

	if rec := do(h, "DELETE", "/admin/studios/ses_a/shares/carol", "op:local", ""); rec.Code != 204 {
		t.Fatalf("unshare carol: %d %s", rec.Code, rec.Body)
	}
	_ = carolConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := carolR.ReadByte(); err == nil {
		t.Fatal("carol's connection still open after unshare")
	}
	// alice keeps ssh by role, so unsharing her grant leaves her connected.
	if rec := do(h, "DELETE", "/admin/studios/ses_a/shares/alice", "op:local", ""); rec.Code != 204 {
		t.Fatalf("unshare alice: %d %s", rec.Code, rec.Body)
	}
	waitLive(1)
	go func() { _, _ = aliceStudio.Write([]byte("still here\n")) }()
	if line, err := aliceR.ReadString('\n'); err != nil || line != "still here\n" {
		t.Fatalf("alice's connection: %q %v", line, err)
	}
	_ = d.Live.CloseUser("ses_a", alice)
}

func TestParseGrantFor(t *testing.T) {
	for in, want := range map[string]time.Duration{"": collab.DefaultGrant, "4h": 4 * time.Hour, "90m": 90 * time.Minute, "7d": 7 * 24 * time.Hour, "1d": 24 * time.Hour} {
		got, err := collabhttp.ParseGrantFor(in)
		if err != nil || got != want {
			t.Errorf("%q: %v %v, want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"soon", "d", "1.5d", "-1d", "1w"} {
		if _, err := collabhttp.ParseGrantFor(in); err == nil {
			t.Errorf("%q: no error", in)
		}
	}
}
