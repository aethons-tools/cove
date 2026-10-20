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
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
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

func TestAccessPanelShowsWhoAndWhy(t *testing.T) {
	store, carol, exp := accessFixture(t)
	h := adminui.Handler(store, testLogger(), &jam.Supervisor{}, nil, anyCred, nil, adminui.WithStudioAccess(collabhttp.NewLive()))
	rec := get(t, h, "/ui/agents/sess-1")
	if rec.Code != http.StatusOK {
		t.Fatalf("page = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"<h2>Access</h2>",
		"Operators</dt><dd>always",
		`href="/ui/users/` + string(mustLookup(t, store, "alice")) + `">alice</a>`,
		"preview: members · ssh: members",
		`>carol</a>`, "<td>ssh</td>", exp.Format("2006-01-02 15:04 MST"), "op:local",
		`hx-delete="/ui/agents/sess-1/shares/` + string(carol) + `"`,
		"No open connections.",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("access panel lacks %q", want)
		}
	}
	// The card follows the Egress card.
	if e, a := strings.Index(body, "<h2>Egress</h2>"), strings.Index(body, "<h2>Access</h2>"); e < 0 || a < e {
		t.Errorf("Access card (at %d) not after Egress (at %d)", a, e)
	}
}

func TestAccessPanelOffWithoutSSH(t *testing.T) {
	store, _, _ := accessFixture(t)
	body := get(t, adminui.Handler(store, testLogger(), &jam.Supervisor{}, nil, anyCred, nil), "/ui/agents/sess-1").Body.String()
	if !strings.Contains(body, "Studio collaboration is off") || strings.Contains(body, "/shares/") {
		t.Fatalf("panel without SSH collaboration:\n%s", body)
	}
}

// The Unshare button is a CSRF-guarded write: refused cross-origin, and when
// allowed removes the grant and closes carol's live SSH connection, which the
// panel lists while it is open.
func TestAccessPanelUnshare(t *testing.T) {
	store, carol, _ := accessFixture(t)
	live := collabhttp.NewLive()
	h := adminui.Handler(store, testLogger(), &jam.Supervisor{}, nil, anyCred, nil, adminui.WithStudioAccess(live))
	conn, br := sshAs(t, store, live, "carol")

	if body := get(t, h, "/ui/agents/sess-1").Body.String(); !strings.Contains(body, "<td>carol</td>") || strings.Contains(body, "No open connections.") {
		t.Fatalf("live connection not listed:\n%s", body)
	}

	del := func(origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodDelete, "/ui/agents/sess-1/shares/"+string(carol), nil)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := del("http://evil.example"); rec.Code != http.StatusForbidden || len(store.ShareGrants("sess-1")) != 1 {
		t.Fatalf("cross-origin unshare = %d, grants %v", rec.Code, store.ShareGrants("sess-1"))
	}
	if rec := del(""); rec.Code != http.StatusForbidden {
		t.Fatalf("no-origin unshare = %d", rec.Code)
	}
	if rec := del("http://example.com"); rec.Code != http.StatusOK || len(store.ShareGrants("sess-1")) != 0 {
		t.Fatalf("unshare = %d %s, grants %v", rec.Code, rec.Body, store.ShareGrants("sess-1"))
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := br.ReadByte(); err == nil || errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatal("carol's SSH connection survived the unshare")
	}
	if rec := del("http://example.com"); rec.Code != http.StatusNotFound {
		t.Fatalf("second unshare = %d", rec.Code)
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
func TestAccessPanelPreviews(t *testing.T) {
	store, _, _ := accessFixture(t)
	exposures := func(string) []jam.Exposure {
		return []jam.Exposure{{Name: "app", Port: 5173}, {Name: "<b>x</b>", Port: 3000}}
	}
	urlFor := func(_, name string) (string, error) { return "https://" + name + "--x.preview.jam.example/", nil }
	live := collabhttp.NewLive()
	h := adminui.Handler(store, testLogger(), &jam.Supervisor{}, nil, anyCred, nil, adminui.WithStudioAccess(live), adminui.WithStudioPreviews(exposures, urlFor))
	body := get(t, h, "/ui/agents/sess-1").Body.String()
	for _, want := range []string{
		`<a href="https://app--x.preview.jam.example/" target="_blank" rel="noopener noreferrer">app</a>`,
		`&lt;b&gt;x&lt;/b&gt;</a>`, "<td>5173</td>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("previews list lacks %q", want)
		}
	}
	if strings.Contains(body, "<b>x</b>") || strings.Contains(body, "Previews are off") {
		t.Errorf("unescaped name or off notice:\n%s", body)
	}
	body = get(t, adminui.Handler(store, testLogger(), &jam.Supervisor{}, nil, anyCred, nil, adminui.WithStudioAccess(live)), "/ui/agents/sess-1").Body.String()
	if !strings.Contains(body, "Previews are off on this Jam") {
		t.Errorf("no off notice:\n%s", body)
	}
}
