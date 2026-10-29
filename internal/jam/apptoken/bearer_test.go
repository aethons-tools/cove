package apptoken

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/appui"
	"github.com/aethons-tools/cove/internal/jam/userui"
)

// token stores an app token for user and returns its row and raw value.
func (f *fixture) token(user jam.User, operatorID string) (jam.AppToken, string) {
	f.t.Helper()
	tok, raw, err := jam.NewAppToken(user.ID, "VS Code", operatorID, f.now)
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.store.PutAppToken(tok); err != nil {
		f.t.Fatal(err)
	}
	return tok, raw
}

func (f *fixture) user(name string) jam.User {
	for _, u := range f.store.ListUsers() {
		if u.Name == name {
			return u
		}
	}
	f.t.Fatalf("no user %s", name)
	return jam.User{}
}

func TestBearerMemberPrincipal(t *testing.T) {
	f := newFixture(t)
	_, raw := f.token(f.alice, "")
	rr, done, err := f.svc.Bearer()(httptest.NewRequest("GET", "/app/user/api/x", nil), raw)
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	p, ok := appui.PrincipalFrom(rr)
	if !ok || p.User == nil || p.User.UserID != f.alice.ID || p.User.Name != "alice" || p.Admin || p.Local {
		t.Fatalf("principal %+v", p)
	}
	if part, ok := jam.ParticipantFrom(rr); !ok || part.UserID != f.alice.ID {
		t.Fatal("no participant on the request")
	}
	if _, _, err := f.svc.Bearer()(httptest.NewRequest("GET", "/", nil), "bogus"); err == nil {
		t.Fatal("bogus token accepted")
	}
}

func TestBearerOperatorStanding(t *testing.T) {
	f := newFixture(t)
	_, raw := f.token(f.alice, "op-1")
	rr, done, err := f.svc.Bearer()(httptest.NewRequest("GET", "/", nil), raw)
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	p, _ := appui.PrincipalFrom(rr)
	if !p.Admin || p.Operator.ID != "op-1" || p.User == nil {
		t.Fatalf("principal %+v", p)
	}
	if op := jam.OperatorID(rr); op != "op-1" {
		t.Fatalf("operator %q", op)
	}
}

func TestBearerUserInNoProject(t *testing.T) {
	f := newFixture(t)
	_, raw := f.token(f.user("dave"), "")
	rr, done, err := f.svc.Bearer()(httptest.NewRequest("GET", "/", nil), raw)
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	if p, _ := appui.PrincipalFrom(rr); p.User != nil || p.Admin {
		t.Fatalf("principal %+v", p)
	}
}

// fakeChanges is a userui.Changes that never fires.
type fakeChanges struct{ ch chan struct{} }

func (f *fakeChanges) Subscribe() (<-chan struct{}, func()) { return f.ch, func() {} }

func TestRevokeEndsEventStream(t *testing.T) {
	f := newFixture(t)
	tok, raw := f.token(f.alice, "")
	user := userui.Handler(userui.Deps{Store: f.store}, nil, userui.WithChanges(&fakeChanges{ch: make(chan struct{})}))
	h := appui.Handler(appui.Deps{User: user, Admin: http.NotFoundHandler(), Bearer: f.svc.Bearer(), SessionCookies: []string{idCookie, atCookie}})
	srv := httptest.NewServer(h)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/app/user/events", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	ended := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
		}
		close(ended)
	}()
	// Wait for the stream to be tracked, then revoke it.
	deadline := time.Now().Add(time.Second)
	for f.svc.live.count() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("stream never tracked")
		}
		time.Sleep(5 * time.Millisecond)
	}
	f.svc.live.Revoke(tok.ID)
	select {
	case <-ended:
	case <-time.After(time.Second):
		t.Fatal("stream still open a second after revoke")
	}
}

func TestSweepEndsRemovedUsersStream(t *testing.T) {
	f := newFixture(t)
	tok, raw := f.token(f.alice, "")
	rr, done, err := f.svc.Bearer()(httptest.NewRequest("GET", "/", nil), raw)
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	f.svc.sweep()
	if rr.Context().Err() != nil {
		t.Fatal("sweep cancelled a valid token's request")
	}
	if err := f.store.RemoveUser(tok.UserID); err != nil {
		t.Fatal(err)
	}
	f.svc.sweep()
	if rr.Context().Err() == nil {
		t.Fatal("sweep left a removed user's request running")
	}
}

func TestExchangeEvictionRevokesLive(t *testing.T) {
	f := newFixture(t)
	var first jam.AppToken
	for i := range jam.MaxAppTokensPerUser {
		tok, _ := f.token(f.alice, "")
		if i == 0 {
			first = tok
		}
		f.now = f.now.Add(time.Second)
	}
	// The least recently used token has a request open.
	ctx, done := f.svc.live.Track(context.Background(), first.ID)
	defer done()
	code := f.allow(consentQuery(nil), idCookie+"=id:sub-alice")
	if w := f.exchange(map[string]string{"code": code, "code_verifier": testVerifier, "redirect": testRedirect}); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if _, ok := f.store.AppTokenByID(first.ID); ok {
		t.Fatal("least recently used token kept")
	}
	if ctx.Err() == nil {
		t.Fatal("evicted token's request still running")
	}
}

// touchDeleteStore deletes the token right after its touch, standing in for a
// revoke that lands between ResolveAppToken and Live.Track.
type touchDeleteStore struct {
	jam.Store
	del func(ident.ID)
}

func (s touchDeleteStore) TouchAppToken(id ident.ID, used, exp time.Time) error {
	err := s.Store.TouchAppToken(id, used, exp)
	s.del(id)
	return err
}

func TestBearerRevokedBetweenResolveAndTrackIsCancelled(t *testing.T) {
	f := newFixture(t)
	tok, raw := f.token(f.alice, "")
	svc := New(Config{Store: touchDeleteStore{Store: f.store, del: func(id ident.ID) { _, _ = f.store.DeleteUserAppToken(f.alice.ID, id) }},
		Sessions: f.svc.c.Sessions, Now: func() time.Time { return f.now.Add(jam.AppTokenTouchEvery) }}) // late enough to touch
	rr, done, err := svc.Bearer()(httptest.NewRequest("GET", "/app/user/events", nil), raw)
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	select {
	case <-rr.Context().Done():
	default:
		t.Fatalf("request of revoked token %s still open", tok.ID)
	}
}
