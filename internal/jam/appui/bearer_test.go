package appui

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
)

// fakeBearer accepts "good" (a member) and "op" (a member with operator
// standing); done counts the finished requests.
type fakeBearer struct{ done int }

func (f *fakeBearer) fn(r *http.Request, raw string) (*http.Request, func(), error) {
	p := Principal{User: &jam.Participant{UserID: "usr_a", Name: "alice", Projects: []string{"proj"}}}
	switch raw {
	case "good":
	case "op":
		p.Admin, p.Operator = true, jam.Operator{ID: "op-1"}
	default:
		return nil, nil, errors.New("invalid")
	}
	return WithPrincipal(r, p), func() { f.done++ }, nil
}

// whoami answers with the principal the request carries.
func whoami(tag string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := PrincipalFrom(r)
		fmt.Fprintf(w, "%s %s set=%v user=%v admin=%v local=%v op=%s", tag, r.URL.Path, ok, p.User != nil, p.Admin, p.Local, p.Operator.ID)
	})
}

func bearerMux(t *testing.T, fb *fakeBearer) *http.ServeMux {
	t.Helper()
	return Handler(Deps{
		Gate:           NewGate(GateConfig{Sessions: testSessions(t), LoginPath: "/app/auth/login", Log: discardLog()}),
		AuthRoutes:     echo("auth"),
		User:           whoami("user"),
		Admin:          whoami("admin"),
		TrustedOrigins: []string{"https://jam.example"},
		Bearer:         fb.fn,
		SessionCookies: []string{"jam_app_id", "jam_app_at"},
	})
}

func bearerReq(method, path, tok string, loopback bool) *http.Request {
	r := muxReq(method, path, "", "")
	if loopback {
		r.RemoteAddr, r.Host = "127.0.0.1:1234", "127.0.0.1:8443"
	}
	r.Header.Set("Authorization", "Bearer "+tok)
	return r
}

func serveReq(m http.Handler, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	m.ServeHTTP(w, r)
	return w
}

func TestUserBearerReachesHandlerWithoutOrigin(t *testing.T) {
	fb := &fakeBearer{}
	w := serveReq(bearerMux(t, fb), bearerReq("POST", "/app/user/send", "good", false))
	if w.Code != 200 || w.Body.String() != "user /app/user/send set=true user=true admin=false local=false op=" {
		t.Fatalf("%d %q", w.Code, w.Body)
	}
	if fb.done != 1 {
		t.Fatalf("done called %d times", fb.done)
	}
	w = serveReq(bearerMux(t, fb), bearerReq("GET", "/app/user/api/x", "op", false))
	if w.Code != 200 || w.Body.String() != "user /app/user/api/x set=true user=true admin=true local=false op=op-1" {
		t.Fatalf("%d %q", w.Code, w.Body)
	}
}

func TestUserBearerWithCookie400(t *testing.T) {
	for _, c := range []string{"jam_app_id", "jam_app_at"} {
		r := bearerReq("GET", "/app/user/api/x", "good", false)
		r.AddCookie(&http.Cookie{Name: c, Value: "alice"})
		if w := serveReq(bearerMux(t, &fakeBearer{}), r); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %q", c, w.Code, w.Body)
		}
	}
}

func TestUserBearerInvalid401(t *testing.T) {
	w := serveReq(bearerMux(t, &fakeBearer{}), bearerReq("GET", "/app/user/api/x", "nope", false))
	if w.Code != http.StatusUnauthorized || w.Header().Get("WWW-Authenticate") != `Bearer error="invalid_token"` {
		t.Fatalf("%d %v", w.Code, w.Header())
	}
}

func TestUserBearerPushRoutesRefused(t *testing.T) {
	m := bearerMux(t, &fakeBearer{})
	for _, c := range []struct{ method, path string }{
		{"POST", "/app/user/push/subscriptions"}, {"DELETE", "/app/user/push/subscriptions"}, {"GET", "/app/user/push/key"},
	} {
		if w := serveReq(m, bearerReq(c.method, c.path, "good", false)); w.Code != http.StatusForbidden {
			t.Errorf("%s %s: %d", c.method, c.path, w.Code)
		}
	}
	if w := serveReq(m, bearerReq("POST", "/app/user/push/focus", "good", false)); w.Code != 200 {
		t.Errorf("focus: %d", w.Code)
	}
}

func TestUserBearerOnLoopbackNotLocalAdmin(t *testing.T) {
	m := bearerMux(t, &fakeBearer{})
	w := serveReq(m, bearerReq("GET", "/app/user/api/x", "good", true))
	if w.Code != 200 || w.Body.String() != "user /app/user/api/x set=true user=true admin=false local=false op=" {
		t.Fatalf("%d %q", w.Code, w.Body)
	}
	if w := serveReq(m, bearerReq("GET", "/app/user/api/x", "nope", true)); w.Code != http.StatusUnauthorized {
		t.Fatalf("invalid on loopback: %d %q", w.Code, w.Body)
	}
}

func TestAdminRefusesBearer(t *testing.T) {
	m := bearerMux(t, &fakeBearer{})
	for _, loop := range []bool{false, true} {
		for _, tok := range []string{"good", "op", "nope"} {
			if w := serveReq(m, bearerReq("GET", "/app/admin/api/users", tok, loop)); w.Code != http.StatusUnauthorized {
				t.Errorf("loopback=%v %s: %d %q", loop, tok, w.Code, w.Body)
			}
		}
	}
	// Without a bearer, loopback is still the local admin.
	r := muxReq("GET", "/app/admin/x", "", "")
	r.RemoteAddr, r.Host = "127.0.0.1:1234", "127.0.0.1:8443"
	if w := serveReq(m, r); w.Code != 200 {
		t.Fatalf("loopback admin: %d", w.Code)
	}
}

func TestNoBearerUnchanged(t *testing.T) {
	m := bearerMux(t, &fakeBearer{})
	r := muxReq("POST", "/app/user/send", "alice", "")
	r.Header.Set("Origin", "https://evil.example")
	if w := serveReq(m, r); w.Code != http.StatusForbidden {
		t.Fatalf("cross-origin cookie post: %d", w.Code)
	}
	r = muxReq("POST", "/app/user/send", "alice", "")
	r.Header.Set("Origin", "https://jam.example")
	if w := serveReq(m, r); w.Code != 200 {
		t.Fatalf("same-origin cookie post: %d %q", w.Code, w.Body)
	}
	if w := serveReq(m, muxReq("GET", "/app/user/", "", "")); w.Code != http.StatusFound {
		t.Fatalf("signed out: %d", w.Code)
	}
}
