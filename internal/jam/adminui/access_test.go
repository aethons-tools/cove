package adminui_test

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
	"github.com/aethons-tools/cove/internal/jam/appui"
	"github.com/aethons-tools/cove/internal/jam/collab"
	"github.com/aethons-tools/cove/internal/jam/collabhttp"
	"github.com/aethons-tools/cove/internal/jam/sshca"
)

// accessFixture: alice's live personal session sess-1 of acme/dev (members
// may ssh), and carol holding an ssh grant on it from op:local.
func accessFixture(t *testing.T) (jam.Store, ident.ID, time.Time) {
	t.Helper()
	store := newStore(t)
	mustCreateProject(t, store, "acme")
	if err := store.PutRole("acme", jam.Role{Name: "dev", Collaborate: jam.CollaboratePolicy{SSH: "members"}}); err != nil {
		t.Fatal(err)
	}
	alice, err := store.CreateUser(jam.User{Name: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	carol, err := store.CreateUser(jam.User{Name: "carol"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutInstance(jam.Instance{ActorID: "sess-1", Project: "acme", Role: "dev", Phase: jam.PhaseLive,
		SessionKind: jam.SessionKindPersonal, Owner: "alice", OwnerID: alice.ID}); err != nil {
		t.Fatal(err)
	}
	exp := time.Now().Add(3 * time.Hour).UTC().Truncate(time.Second)
	if err := store.PutShareGrant(jam.ShareGrant{SessionID: "sess-1", UserID: carol.ID, Rights: []string{"ssh"},
		CreatedAt: exp.Add(-4 * time.Hour), ExpiresAt: exp, GrantedBy: "op:local"}); err != nil {
		t.Fatal(err)
	}
	return store, carol.ID, exp
}

// The agent page's Access card says who can reach the studio and why; it
// is off without SSH collaboration.
func TestAccessAPIWhoAndWhy(t *testing.T) {
	store, carol, exp := accessFixture(t)
	h := adminui.Handler(store, testLogger(), &jam.Supervisor{}, nil, anyCred, nil, adminui.WithStudioAccess(collabhttp.NewLive()))
	var d adminui.AgentDetail
	apiDo(t, h, "GET", "/app/admin/api/agents/sess-1", nil, &d)
	a := d.Access
	if !a.Enabled || a.Requester == nil || a.Requester.ID != string(mustLookup(t, store, "alice")) || a.MembersPreview != "members" || a.MembersSSH != "members" {
		t.Fatalf("access = %+v", a)
	}
	if len(a.Shares) != 1 || a.Shares[0].UserID != string(carol) || a.Shares[0].User != "carol" || a.Shares[0].ExpiresAt != exp.Format("2006-01-02 15:04 MST") ||
		a.Shares[0].GrantedBy != "op:local" || len(a.Live) != 0 {
		t.Errorf("shares = %+v live = %+v", a.Shares, a.Live)
	}
	var off adminui.AgentDetail
	apiDo(t, adminui.Handler(store, testLogger(), &jam.Supervisor{}, nil, anyCred, nil), "GET", "/app/admin/api/agents/sess-1", nil, &off)
	if off.Access.Enabled {
		t.Errorf("without SSH collaboration: %+v", off.Access)
	}
}

// Unshare is a same-origin write: refused cross-origin, and when allowed it
// removes the grant and closes carol's live SSH connection, which the page
// lists while it is open.
func TestAccessAPIUnshareClosesConnections(t *testing.T) {
	store, carol, _ := accessFixture(t)
	live := collabhttp.NewLive()
	h := adminui.Handler(store, testLogger(), &jam.Supervisor{}, nil, anyCred, nil, adminui.WithStudioAccess(live))
	conn, br := sshAs(t, store, live, "carol")

	var d adminui.AgentDetail
	apiDo(t, h, "GET", "/app/admin/api/agents/sess-1", nil, &d)
	if len(d.Access.Live) != 1 || d.Access.Live[0].Name != "carol" {
		t.Fatalf("live connections = %+v", d.Access.Live)
	}
	req := httptest.NewRequest(http.MethodDelete, "/app/admin/api/agents/sess-1/shares/"+string(carol), nil)
	req.Header.Set("Origin", "http://evil.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, appui.WithPrincipal(req, appui.Principal{Admin: true, Local: true}))
	if rec.Code != http.StatusForbidden || len(store.ShareGrants("sess-1")) != 1 {
		t.Fatalf("cross-origin unshare = %d, grants %v", rec.Code, store.ShareGrants("sess-1"))
	}
	if rec := apiDo(t, h, "DELETE", "/app/admin/api/agents/sess-1/shares/"+string(carol), nil, nil); rec.Code != http.StatusOK || len(store.ShareGrants("sess-1")) != 0 {
		t.Fatalf("unshare = %d %s", rec.Code, rec.Body)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := br.ReadByte(); err == nil || errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatal("carol's SSH connection survived the unshare")
	}
}

func mustLookup(t *testing.T, store jam.Store, name string) ident.ID {
	t.Helper()
	id, ok := store.LookupName(ident.User, name)
	if !ok {
		t.Fatalf("no user %s", name)
	}
	return id
}

type pipeTunnels struct{}

func (p pipeTunnels) OpenPreview(ctx context.Context, a string, port int32) (net.Conn, error) {
	return p.Open(ctx, a, port)
}

func (pipeTunnels) Open(context.Context, string, int32) (net.Conn, error) {
	_, human := net.Pipe()
	return human, nil
}

// sshAs opens an SSH upgrade to sess-1 as user name through the real
// collaboration routes registering in live.
func sshAs(t *testing.T, store jam.Store, live *collabhttp.Live, name string) (net.Conn, *bufio.Reader) {
	t.Helper()
	_, caPriv, _ := ed25519.GenerateKey(rand.Reader)
	blk, _ := ssh.MarshalPrivateKey(caPriv, "")
	ca, err := sshca.Parse(pem.EncodeToMemory(blk))
	if err != nil {
		t.Fatal(err)
	}
	id := mustLookup(t, store, name)
	srv := httptest.NewServer(collabhttp.Routes("/me", collabhttp.Deps{
		Store: store, CA: ca, Tunnels: pipeTunnels{}, Live: live,
		Principal: func(*http.Request) (collab.Principal, string, error) {
			return collab.Principal{UserID: id}, name, nil
		},
	}))
	t.Cleanup(srv.Close)
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	fmt.Fprintf(conn, "GET /me/studios/sess-1/ssh HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\nUpgrade: jam-tunnel\r\n\r\n")
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil || resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade: %v %v", resp, err)
	}
	for range 200 { // the handler registers just after answering 101
		if len(live.List("sess-1")) == 1 {
			return conn, br
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("connection never registered")
	return nil, nil
}

// With previews on the card lists each exposure as a new-tab link (names
// escaped), and a live preview connection shows with right preview; with them
// off it says so.
func TestAccessAPIPreviews(t *testing.T) {
	store, _, _ := accessFixture(t)
	exposures := func(string) []jam.Exposure {
		return []jam.Exposure{{Name: "app", Port: 5173}, {Name: "<b>x</b>", Port: 3000}}
	}
	urlFor := func(_, name string) (string, error) { return "https://" + name + "--x.preview.jam.example/", nil }
	live := collabhttp.NewLive()
	h := adminui.Handler(store, testLogger(), &jam.Supervisor{}, nil, anyCred, nil, adminui.WithStudioAccess(live), adminui.WithStudioPreviews(exposures, urlFor))
	var d adminui.AgentDetail
	apiDo(t, h, "GET", "/app/admin/api/agents/sess-1", nil, &d)
	p := d.Access.Previews
	if !d.Access.PreviewsOn || len(p) != 2 || p[0].URL != "https://app--x.preview.jam.example/" || p[0].Port != 5173 || p[1].Name != "<b>x</b>" {
		t.Errorf("previews = %+v", d.Access)
	}
	apiDo(t, adminui.Handler(store, testLogger(), &jam.Supervisor{}, nil, anyCred, nil, adminui.WithStudioAccess(live)), "GET", "/app/admin/api/agents/sess-1", nil, &d)
	if d.Access.PreviewsOn {
		t.Error("previews on without previews configured")
	}
}
