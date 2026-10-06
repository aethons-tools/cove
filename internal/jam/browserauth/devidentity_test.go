package browserauth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
)

// devStore has users alice — login-linked and bound to an OIDC identity — and
// bob, who has neither; both members of project "proj".
func devStore(t *testing.T) jam.Store {
	return participantStore(t,
		jam.Human{Name: "alice", Login: "auth0|alice", Identity: []jam.OIDCIdentity{{Issuer: "https://idp/", Subject: "auth0|alice"}}},
		jam.Human{Name: "bob"})
}

// devReq is a /ui or /me request from addr with a loopback-literal Host.
func devReq(addr string) *http.Request {
	req := httptest.NewRequest("GET", "/me/", nil)
	req.RemoteAddr = addr
	req.Host = "localhost:8081"
	return req
}

func TestDevIdentityOperatorTrust(t *testing.T) {
	for _, tc := range []struct{ human, want string }{
		{"alice", "auth0|alice"}, // acts as alice's linked login
		{"bob", "local"},         // a user with no login → plain loopback operator
		{"nobody", "local"},      // unknown user → plain loopback operator
	} {
		d := DevIdentity{Store: devStore(t), User: tc.human}
		g := Gate{LoopbackTrust: d.OperatorLoopbackTrust(), Log: discard()}
		var got string
		g.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got = jam.OperatorID(r)
		})).ServeHTTP(httptest.NewRecorder(), devReq("127.0.0.1:5000"))
		if got != tc.want {
			t.Errorf("dev user %q: operator = %q, want %q", tc.human, got, tc.want)
		}
	}
}

func TestDevIdentityParticipantOnLoopbackOnly(t *testing.T) {
	d := DevIdentity{Store: devStore(t), User: "alice"}
	none := func(r *http.Request) (*http.Request, SessionOutcome) { return r, SessionNone }
	g := Gate{Session: d.ParticipantSession(none), LoginPath: "/me/auth/login", Log: discard()}
	serve := func(req *http.Request) (*httptest.ResponseRecorder, jam.Participant) {
		var p jam.Participant
		rec := httptest.NewRecorder()
		g.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, _ = jam.ParticipantFrom(r)
			w.WriteHeader(http.StatusOK)
		})).ServeHTTP(rec, req)
		return rec, p
	}

	// Loopback: no login, the request is alice.
	rec, p := serve(devReq("127.0.0.1:5000"))
	if rec.Code != http.StatusOK || p.Name != "alice" || len(p.Projects) != 1 || p.Projects[0] != "proj" {
		t.Fatalf("loopback = %d %+v, want 200 as alice in proj", rec.Code, p)
	}
	// Off-loopback: the dev identity never applies; falls to the real session.
	if rec, _ := serve(devReq("203.0.113.7:5555")); rec.Code != http.StatusFound {
		t.Fatalf("off-loopback = %d, want 302 to login (dev identity must not apply)", rec.Code)
	}
	// Loopback with a foreign Host is still refused (DNS-rebinding guard first).
	req := devReq("127.0.0.1:5000")
	req.Host = "evil.example"
	if rec, _ := serve(req); rec.Code != http.StatusForbidden {
		t.Fatalf("loopback foreign Host = %d, want 403", rec.Code)
	}
}

func TestDevIdentityParticipantNeedsOIDCIdentity(t *testing.T) {
	// bob has no OIDC identity, so no participant can be resolved: defer to
	// the real session rather than inventing one the send path can't attribute.
	d := DevIdentity{Store: devStore(t), User: "bob"}
	got, out := d.ParticipantSession(nil)(devReq("127.0.0.1:5000"))
	if out != SessionNone {
		t.Fatalf("outcome = %v, want SessionNone", out)
	}
	if _, ok := jam.ParticipantFrom(got); ok {
		t.Fatal("no participant should be injected for a human without an OIDC identity")
	}
}
