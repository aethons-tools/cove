package appui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/browserauth"
)

const testIssuer = "https://idp/"

func testStore(t *testing.T) jam.Store {
	t.Helper()
	s := jam.NewMemStore()
	if err := s.CreateProject("proj"); err != nil {
		t.Fatal(err)
	}
	h := jam.Human{Name: "alice", Login: "auth0|alice", Identity: []jam.OIDCIdentity{{Issuer: testIssuer, Subject: "alice"}}}
	if err := jam.AddPerson(s, "proj", h); err != nil {
		t.Fatal(err)
	}
	return s
}

func testSessions(t *testing.T) *Sessions {
	return &Sessions{
		Issuer:       testIssuer,
		IDCookie:     "jam_app_id",
		AccessCookie: "jam_app_at",
		VerifyID: func(_ context.Context, raw string) (string, error) {
			if raw == "bad" {
				return "", errors.New("bad token")
			}
			return raw, nil
		},
		VerifyAccess: func(_ context.Context, raw string) (jam.Operator, error) {
			if raw == "bad" || raw == "expired" {
				return jam.Operator{}, errors.New("bad token")
			}
			return jam.Operator{ID: "op-" + raw}, nil
		},
		Store: testStore(t),
	}
}

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func probe() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := PrincipalFrom(r)
		fmt.Fprintf(w, "user=%v admin=%v local=%v", p.User != nil, p.Admin, p.Local)
	})
}

func newReq(loopback bool, idTok, atTok string) *http.Request {
	r := httptest.NewRequest("GET", "/app/", nil)
	if loopback {
		r.RemoteAddr = "127.0.0.1:1234"
		r.Host = "127.0.0.1:8443"
	} else {
		r.RemoteAddr = "203.0.113.5:1234"
	}
	if idTok != "" {
		r.AddCookie(&http.Cookie{Name: "jam_app_id", Value: idTok})
	}
	if atTok != "" {
		r.AddCookie(&http.Cookie{Name: "jam_app_at", Value: atTok})
	}
	return r
}

func TestGateMatrix(t *testing.T) {
	cases := []struct {
		name         string
		loopback     bool
		idTok, atTok string // "" = cookie absent
		wrap         func(http.Handler) http.Handler
		wantCode     int
		wantBody     string // substring
		wantLocation string
	}{
		{"member only", false, "alice", "", nil, 200, "user=true admin=false", ""},
		{"admin only", false, "stranger", "ok", nil, 200, "user=false admin=true", ""},
		{"both", false, "alice", "ok", nil, 200, "user=true admin=true", ""},
		{"neither", false, "stranger", "", nil, 403, "", ""},
		{"no cookies", false, "", "", nil, 302, "", "/app/auth/login"},
		{"bad id token only", false, "bad", "", nil, 302, "", "/app/auth/login"},
		{"expired access, valid member", false, "alice", "expired", nil, 200, "user=true admin=false", ""},
		{"loopback no session", true, "", "", nil, 200, "user=false admin=true local=true", ""},
		{"loopback member session", true, "alice", "", nil, 200, "user=true admin=true local=false", ""},
		{"user area, admin only", false, "stranger", "ok", RequireUser, 403, "", ""},
		{"admin area, member only", false, "alice", "", RequireAdmin, 403, "", ""},
		{"admin area, loopback", true, "", "", RequireAdmin, 200, "admin=true", ""},
		{"user area, loopback no session", true, "", "", RequireUser, 403, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gate := NewGate(GateConfig{Sessions: testSessions(t), LoginPath: "/app/auth/login", Log: discardLog()})
			h := probe()
			if tc.wrap != nil {
				h = tc.wrap(h)
			}
			rec := httptest.NewRecorder()
			gate.Wrap(h).ServeHTTP(rec, newReq(tc.loopback, tc.idTok, tc.atTok))
			if rec.Code != tc.wantCode {
				t.Fatalf("code = %d, want %d (body %q)", rec.Code, tc.wantCode, rec.Body)
			}
			if !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Errorf("body = %q, want substring %q", rec.Body, tc.wantBody)
			}
			if got := rec.Header().Get("Location"); got != tc.wantLocation {
				t.Errorf("Location = %q, want %q", got, tc.wantLocation)
			}
		})
	}
}

func TestGateRebindingHostRefused(t *testing.T) {
	gate := NewGate(GateConfig{Sessions: testSessions(t), LoginPath: "/app/auth/login", Log: discardLog()})
	r := newReq(true, "", "")
	r.Host = "evil.example:8443"
	rec := httptest.NewRecorder()
	gate.Wrap(probe()).ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code = %d, want 403", rec.Code)
	}
}

func TestGateNoBrowserLogin(t *testing.T) {
	gate := NewGate(GateConfig{Log: discardLog()})
	rec := httptest.NewRecorder()
	gate.Wrap(probe()).ServeHTTP(rec, newReq(true, "", ""))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "admin=true local=true") {
		t.Fatalf("loopback = %d %q, want 200 admin+local", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	gate.Wrap(probe()).ServeHTTP(rec, newReq(false, "alice", "ok"))
	if rec.Code != http.StatusForbidden || rec.Header().Get("Location") != "" {
		t.Fatalf("off-loopback = %d loc %q, want 403 without redirect", rec.Code, rec.Header().Get("Location"))
	}
}

// devStore adds the registry reads DevIdentity needs; MemStore provides them.
func TestGateDevIdentity(t *testing.T) {
	store := testStore(t)
	dev := &browserauth.DevIdentity{Store: store, User: "alice"}
	gate := NewGate(GateConfig{Sessions: testSessions(t), Dev: dev, LoginPath: "/app/auth/login", Log: discardLog()})
	rec := httptest.NewRecorder()
	gate.Wrap(probe()).ServeHTTP(rec, newReq(true, "", ""))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "user=true admin=true local=true") {
		t.Fatalf("dev loopback = %d %q, want user+admin+local", rec.Code, rec.Body)
	}
	// Off-loopback the dev identity never applies.
	rec = httptest.NewRecorder()
	gate.Wrap(probe()).ServeHTTP(rec, newReq(false, "", ""))
	if rec.Code != http.StatusFound {
		t.Fatalf("dev off-loopback = %d, want 302", rec.Code)
	}
}

func TestWithPrincipalInjectsLegacyContext(t *testing.T) {
	p := jam.Participant{Name: "alice"}
	r := WithPrincipal(httptest.NewRequest("GET", "/", nil),
		Principal{User: &p, Admin: true, Operator: jam.Operator{ID: "x"}})
	if got, ok := jam.ParticipantFrom(r); !ok || got.Name != "alice" {
		t.Errorf("ParticipantFrom = %+v, %v", got, ok)
	}
	if got := jam.OperatorID(r); got != "x" {
		t.Errorf("OperatorID = %q, want x", got)
	}
}

func TestDisplayName(t *testing.T) {
	u := &jam.Participant{Name: "alice"}
	for _, tc := range []struct {
		p    Principal
		want string
	}{
		{Principal{User: u, Admin: true, Operator: jam.Operator{ID: "op"}}, "alice"},
		{Principal{Admin: true, Local: true}, "Local admin"},
		{Principal{Admin: true, Operator: jam.Operator{ID: "op"}}, "op"},
	} {
		if got := tc.p.DisplayName(); got != tc.want {
			t.Errorf("DisplayName = %q, want %q", got, tc.want)
		}
	}
}
