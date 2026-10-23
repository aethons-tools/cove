package appui

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestForbiddenPages(t *testing.T) {
	const relogin = `href="/app/auth/logout"`
	const login = `href="/app/auth/login"`
	cases := []struct {
		name         string
		withLogin    bool
		loopback     bool
		idTok, atTok string
		path         string
		wantMsg      string
		wantLink     string // "" = no link at all
	}{
		{"user area, admin only", true, false, "stranger", "ok", "/app/user/x", "your sign-in may have expired", relogin},
		{"admin area, member only", true, false, "alice", "", "/app/admin/x", "this area needs admin rights", relogin},
		{"neither", true, false, "stranger", "", "/app/user/x", "not a member of any project and not an admin", relogin},
		{"neither, landing", true, false, "stranger", "", "/app/", "not a member of any project and not an admin", relogin},
		{"loopback local, user area", true, true, "", "", "/app/user/x", "Sign in to use the Inbox", login},
		{"loopback local, user area, no login", false, true, "", "", "/app/user/x", "Sign in to use the Inbox", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gc := GateConfig{Log: discardLog()}
			d := Deps{User: echo("user"), Admin: echo("admin")}
			if tc.withLogin {
				gc.Sessions, gc.LoginPath = testSessions(t), "/app/auth/login"
				d.AuthRoutes = echo("auth")
			}
			d.Gate = NewGate(gc)
			r := newReq(tc.loopback, tc.idTok, tc.atTok)
			r.URL.Path = tc.path
			rec := httptest.NewRecorder()
			Handler(d).ServeHTTP(rec, r)
			if rec.Code != 403 {
				t.Fatalf("code = %d, want 403 (body %q)", rec.Code, rec.Body)
			}
			body := rec.Body.String()
			if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
				t.Errorf("Content-Type = %q, want text/html", ct)
			}
			if !strings.Contains(body, tc.wantMsg) {
				t.Errorf("body missing %q:\n%s", tc.wantMsg, body)
			}
			if tc.wantLink != "" && !strings.Contains(body, tc.wantLink) {
				t.Errorf("body missing link %s:\n%s", tc.wantLink, body)
			}
			if tc.wantLink == "" && strings.Contains(body, "<a ") {
				t.Errorf("body has a link, want none:\n%s", body)
			}
			if tc.wantLink == relogin && strings.Contains(body, login) {
				t.Errorf("body links to login, want the logout (sign in again) link")
			}
		})
	}
}
