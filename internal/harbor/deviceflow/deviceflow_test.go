package deviceflow

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// fakeProvider serves discovery + device-auth + a token endpoint that returns
// authorization_pending a set number of times before issuing a token.
type fakeProvider struct {
	url            string
	pendingLeft    int
	slowDownOnce   bool
	tokenToReturn  string
	deviceCodeSeen string
}

func newFakeProvider(t *testing.T, pending int, token string) *fakeProvider {
	t.Helper()
	f := &fakeProvider{pendingLeft: pending, tokenToReturn: token}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"issuer":                        f.url,
			"device_authorization_endpoint": f.url + "/oauth/device/code",
			"token_endpoint":                f.url + "/oauth/token",
		})
	})
	mux.HandleFunc("/oauth/device/code", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"device_code": "DEV-123", "user_code": "WXYZ-1234",
			"verification_uri":          f.url + "/activate",
			"verification_uri_complete": f.url + "/activate?code=WXYZ-1234",
			"interval":                  1, "expires_in": 600,
		})
	})
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		f.deviceCodeSeen = r.Form.Get("device_code")
		if f.slowDownOnce {
			f.slowDownOnce = false
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(map[string]any{"error": "slow_down"})
			return
		}
		if f.pendingLeft > 0 {
			f.pendingLeft--
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(map[string]any{"error": "authorization_pending"})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": f.tokenToReturn, "expires_in": 3600})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	f.url = srv.URL
	return f
}

func TestRequestDeviceCode(t *testing.T) {
	f := newFakeProvider(t, 0, "TOK")
	dc, err := RequestDeviceCode(context.Background(), http.DefaultClient, Config{
		Issuer: f.url, Audience: "https://harbor.test/api", ClientID: "cid", Scope: "openid",
	})
	if err != nil {
		t.Fatalf("RequestDeviceCode: %v", err)
	}
	if dc.UserCode != "WXYZ-1234" || dc.DeviceCode != "DEV-123" || dc.Interval != 1 {
		t.Fatalf("device code = %+v", dc)
	}
	if dc.TokenEndpoint != f.url+"/oauth/token" {
		t.Fatalf("token endpoint = %q", dc.TokenEndpoint)
	}
}

func TestPollTokenPendingThenSuccess(t *testing.T) {
	f := newFakeProvider(t, 2, "ACCESS-TOK")
	var slept int
	sleep := func(time.Duration) { slept++ }
	tok, err := PollToken(context.Background(), http.DefaultClient, sleep, f.url+"/oauth/token", "cid", "DEV-123", 1)
	if err != nil {
		t.Fatalf("PollToken: %v", err)
	}
	if tok.AccessToken != "ACCESS-TOK" {
		t.Fatalf("token = %q", tok.AccessToken)
	}
	if slept < 2 {
		t.Fatalf("expected to sleep between polls, slept=%d", slept)
	}
	if f.deviceCodeSeen != "DEV-123" {
		t.Fatalf("device_code sent = %q", f.deviceCodeSeen)
	}
}

func TestPollTokenSlowDownWidensInterval(t *testing.T) {
	f := newFakeProvider(t, 0, "T")
	f.slowDownOnce = true
	var durations []time.Duration
	sleep := func(d time.Duration) { durations = append(durations, d) }
	if _, err := PollToken(context.Background(), http.DefaultClient, sleep, f.url+"/oauth/token", "cid", "DEV-123", 1); err != nil {
		t.Fatalf("PollToken: %v", err)
	}
	if len(durations) == 0 || durations[0] <= time.Second {
		t.Fatalf("slow_down should widen the interval beyond 1s, got %v", durations)
	}
}

// A canceled/expired context must stop the poll loop — this is what bounds
// login by the device code's lifetime instead of looping forever on pending.
func TestPollTokenRespectsContext(t *testing.T) {
	f := newFakeProvider(t, 1000, "never") // always authorization_pending
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := PollToken(ctx, http.DefaultClient, func(time.Duration) {}, f.url+"/oauth/token", "cid", "DEV-123", 1); err == nil {
		t.Fatal("expected error when the context is done")
	}
}

func TestPollTokenDenied(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]any{"error": "access_denied"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	if _, err := PollToken(context.Background(), http.DefaultClient, func(time.Duration) {}, srv.URL+"/oauth/token", "cid", "DEV-123", 1); err == nil {
		t.Fatal("expected error on access_denied")
	}
}
