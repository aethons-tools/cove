package collabhttp_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/collabhttp"
)

// participantStore: alice bound to issuer https://idp / sub "alice-sub", a
// member of acme.
func participantStore(t *testing.T) *jam.MemStore {
	t.Helper()
	st := jam.NewMemStore()
	if err := st.CreateProject("acme"); err != nil {
		t.Fatal(err)
	}
	p, _ := st.GetProject("acme")
	alice, err := st.CreateUser(jam.User{Name: "alice", OIDC: []jam.OIDCIdentity{{Issuer: "https://idp", Subject: "alice-sub"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddMember(p.ID, alice.ID); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestParticipantBearer(t *testing.T) {
	st := participantStore(t)
	var seen string
	verify := func(_ context.Context, raw string) (string, string, error) {
		seen = raw
		switch raw {
		case "good":
			return "https://idp", "alice-sub", nil
		case "unbound":
			return "https://idp", "nobody", nil
		}
		return "", "", errors.New("bad token")
	}
	pr := collabhttp.ParticipantBearer(verify, st)
	for name, tc := range map[string]struct {
		hdr  string
		want error
	}{
		"good":              {"Bearer good", nil},
		"lowercase scheme":  {"bearer good", nil},
		"unbound":           {"Bearer unbound", collabhttp.ErrUnbound},
		"expired or forged": {"Bearer bad", collabhttp.ErrInvalidToken},
		"empty bearer":      {"Bearer ", collabhttp.ErrNoCredentials},
		"missing":           {"", collabhttp.ErrNoCredentials},
		"basic":             {"Basic Z29vZA==", collabhttp.ErrNoCredentials},
	} {
		req := httptest.NewRequest("GET", "/me/studios/x/ssh", nil)
		if tc.hdr != "" {
			req.Header.Set("Authorization", tc.hdr)
		}
		p, who, err := pr(req)
		if !errors.Is(err, tc.want) || (tc.want == nil) != (err == nil) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
			continue
		}
		if err == nil && (p.UserID == "" || p.Operator != "" || who != "alice") {
			t.Errorf("%s: principal %+v %q", name, p, who)
		}
		if err == nil && seen != "good" {
			t.Errorf("%s: verify saw %q, want the bare token", name, seen)
		}
	}
}

// The /me plane end to end: a participant bearer on the routes maps each
// failure to its status and lets a bound member with the right through.
func TestParticipantBearerOnRoutes(t *testing.T) {
	d, st := testDeps(t)
	p, _ := st.GetProject("acme")
	dave, err := st.CreateUser(jam.User{Name: "dave", OIDC: []jam.OIDCIdentity{{Issuer: "https://idp", Subject: "dave-sub"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddMember(p.ID, dave.ID); err != nil {
		t.Fatal(err)
	}
	d.Principal = collabhttp.ParticipantBearer(func(_ context.Context, raw string) (string, string, error) {
		switch raw {
		case "dave":
			return "https://idp", "dave-sub", nil
		case "stranger":
			return "https://idp", "nobody", nil
		}
		return "", "", errors.New("expired")
	}, st)
	h := collabhttp.Routes("/me", d)
	pub := testUserPub(t)
	for name, tc := range map[string]struct {
		auth     string
		code     int
		wwwAuth  bool
		bodyHint string
	}{
		"bound member": {"Bearer dave", http.StatusOK, false, "certificate"},
		"bad token":    {"Bearer forged", http.StatusUnauthorized, true, "jam login --participant"},
		"unbound":      {"Bearer stranger", http.StatusForbidden, false, "not linked"},
		"no header":    {"", http.StatusUnauthorized, false, "sign in"},
	} {
		req := certReq(pub, "")
		if tc.auth != "" {
			req.Header.Set("Authorization", tc.auth)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tc.code || !strings.Contains(rec.Body.String(), tc.bodyHint) {
			t.Errorf("%s: %d %q, want %d containing %q", name, rec.Code, rec.Body.String(), tc.code, tc.bodyHint)
		}
		if got := rec.Header().Get("WWW-Authenticate") != ""; got != tc.wwwAuth {
			t.Errorf("%s: WWW-Authenticate set = %v, want %v", name, got, tc.wwwAuth)
		}
	}
}

// A share grant reaches a collaborator from outside every project: the bearer
// resolves the user by OIDC binding alone and collab.Decide authorizes on the
// grant.
func TestParticipantBearerGrantHolderWithoutProject(t *testing.T) {
	d, st := testDeps(t)
	erin, err := st.CreateUser(jam.User{Name: "erin", OIDC: []jam.OIDCIdentity{{Issuer: "https://idp", Subject: "erin-sub"}}})
	if err != nil {
		t.Fatal(err)
	}
	now := d.Now()
	if err := st.PutShareGrant(jam.ShareGrant{SessionID: "ses_a", UserID: erin.ID, Rights: []string{"ssh"}, CreatedAt: now, ExpiresAt: now.Add(time.Hour), GrantedBy: "op:local"}); err != nil {
		t.Fatal(err)
	}
	d.Principal = collabhttp.ParticipantBearer(func(context.Context, string) (string, string, error) {
		return "https://idp", "erin-sub", nil
	}, st)
	h := collabhttp.Routes("/me", d)
	req := certReq(testUserPub(t), "")
	req.Header.Set("Authorization", "Bearer erin")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "certificate") {
		t.Fatalf("grant holder without a project: %d %q, want 200 with a certificate", rec.Code, rec.Body.String())
	}

	// Without the grant, Decide (not the bearer) refuses.
	if err := st.RemoveShareGrant("ses_a", erin.ID); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	req = certReq(testUserPub(t), "")
	req.Header.Set("Authorization", "Bearer erin")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "not linked") {
		t.Fatalf("no grant: %d %q, want 403 from Decide", rec.Code, rec.Body.String())
	}
}

func TestAppTokenBearer(t *testing.T) {
	st := participantStore(t)
	alice, _ := st.UserByOIDC("https://idp", "alice-sub")
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	mint := func(op string) string {
		tok, raw, err := jam.NewAppToken(alice.ID, "VS Code", op, now)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.PutAppToken(tok); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	member, operator := mint(""), mint("op-1")
	clock := now
	pr := collabhttp.AppTokenBearer(st, func() time.Time { return clock }, nil)
	call := func(hdr string) (string, string, error) {
		req := httptest.NewRequest("GET", "/me/studios/x/ssh", nil)
		if hdr != "" {
			req.Header.Set("Authorization", hdr)
		}
		p, who, err := pr(req)
		if err == nil && (p.UserID != alice.ID || who != "alice") {
			t.Errorf("%q: principal %+v %q", hdr, p, who)
		}
		return p.Operator, who, err
	}
	if op, _, err := call("Bearer " + member); err != nil || op != "" {
		t.Fatalf("member: op=%q err=%v", op, err)
	}
	if op, _, err := call("Bearer " + operator); err != nil || op != "op-1" {
		t.Fatalf("operator: op=%q err=%v", op, err)
	}
	if _, _, err := call(""); !errors.Is(err, collabhttp.ErrNoCredentials) {
		t.Fatalf("missing: %v", err)
	}
	if _, _, err := call("Bearer forged"); !errors.Is(err, collabhttp.ErrInvalidToken) {
		t.Fatalf("forged: %v", err)
	}
	clock = now.Add(jam.AppTokenTTL)
	if _, _, err := call("Bearer " + operator); !errors.Is(err, collabhttp.ErrInvalidToken) {
		t.Fatalf("expired: %v", err)
	}
}
