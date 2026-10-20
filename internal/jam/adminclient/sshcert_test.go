package adminclient

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// IssueSSHCert posts the public key under the plane's prefix with the bearer
// and returns the certificate and its expiry.
func TestIssueSSHCert(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/me/studios/ses_a/ssh/cert" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer idtok" {
			t.Errorf("Authorization = %q", got)
		}
		var in struct {
			PublicKey string `json:"public_key"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.PublicKey != "ssh-ed25519 AAAA test\n" {
			t.Errorf("public_key = %q", in.PublicKey)
		}
		_, _ = w.Write([]byte(`{"certificate":"ssh-ed25519-cert-v01@openssh.com AAAA\n","expires_at":"2026-10-20T18:00:00Z"}`))
	}))
	defer ts.Close()
	cert, exp, err := New(ts.URL, "idtok").IssueSSHCert("/me", "ses_a", []byte("ssh-ed25519 AAAA test\n"))
	if err != nil {
		t.Fatal(err)
	}
	if string(cert) != "ssh-ed25519-cert-v01@openssh.com AAAA\n" || !exp.Equal(time.Date(2026, 10, 20, 18, 0, 0, 0, time.UTC)) {
		t.Fatalf("cert=%q exp=%v", cert, exp)
	}
}

// A refusal surfaces as a *StatusError carrying the HTTP status code.
func TestIssueSSHCertStatusError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "you may not use ssh on this studio", http.StatusForbidden)
	}))
	defer ts.Close()
	_, _, err := New(ts.URL, "").IssueSSHCert("/admin", "ses_a", []byte("k"))
	se, ok := errors.AsType[*StatusError](err)
	if !ok || se.Code != http.StatusForbidden || se.Body != "you may not use ssh on this studio" {
		t.Fatalf("err = %#v", err)
	}
}
