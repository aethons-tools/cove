package collabhttp_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
		"bad token":    {"Bearer forged", http.StatusUnauthorized, true, "at-jam login --participant"},
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
