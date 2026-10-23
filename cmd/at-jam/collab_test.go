package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/collab"
	"github.com/aethons-tools/cove/internal/jam/collabhttp"
	"github.com/aethons-tools/cove/internal/jam/sshca"
)

// The operator plane serves the collaboration routes under /admin/studios/,
// behind the admin authenticator, and names the operator in the cert key-id.
func TestOperatorPlaneIssuesSSHCert(t *testing.T) {
	st := jam.NewMemStore()
	if err := st.PutInstance(jam.Instance{ActorID: "ses_a", Project: "default", Role: "ic", Phase: jam.PhaseLive}); err != nil {
		t.Fatal(err)
	}
	_, caPriv, _ := ed25519.GenerateKey(rand.Reader)
	blk, _ := ssh.MarshalPrivateKey(caPriv, "")
	ca, err := sshca.Parse(pem.EncodeToMemory(blk))
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	routes := collabhttp.Routes("/admin", collabhttp.Deps{Store: st, CA: ca, Log: log, Principal: operatorPrincipal(st)})
	h := jam.NewAdminHandler(st, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, log, nil,
		jam.WithAdminRoute("GET /admin/attention", http.NotFoundHandler()),
		jam.WithAdminRoute("/admin/studios/", routes))

	userPub, _, _ := ed25519.GenerateKey(rand.Reader)
	k, _ := ssh.NewPublicKey(userPub)
	body := `{"public_key":` + strconv.Quote(string(ssh.MarshalAuthorizedKey(k))) + `}`
	req := httptest.NewRequest("POST", "/admin/studios/ses_a/ssh/cert", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var out struct{ Certificate string }
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	pk, _, _, _, err := ssh.ParseAuthorizedKey([]byte(out.Certificate))
	if err != nil {
		t.Fatal(err)
	}
	if got := pk.(*ssh.Certificate).KeyId; got != "user=op:local studio=ses_a" {
		t.Fatalf("key-id %q", got)
	}

	// Off the loopback the admin authenticator refuses before the routes run.
	req = httptest.NewRequest("POST", "/admin/studios/ses_a/ssh/cert", strings.NewReader(body))
	req.RemoteAddr = "10.0.0.1:1234"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden {
		t.Fatalf("non-loopback: %d", rec.Code)
	}
}

// /me/studios/ mounts only with device login, an OIDC operator authenticator
// and the SSH CA; any missing piece leaves it a 404 — never an open route.
func TestMountParticipantStudios(t *testing.T) {
	_, caPriv, _ := ed25519.GenerateKey(rand.Reader)
	blk, _ := ssh.MarshalPrivateKey(caPriv, "")
	ca, err := sshca.Parse(pem.EncodeToMemory(blk))
	if err != nil {
		t.Fatal(err)
	}
	var idp *httptest.Server
	idp = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"issuer": idp.URL, "jwks_uri": idp.URL + "/jwks"})
	}))
	t.Cleanup(idp.Close)
	oidcAuth, err := jam.NewOIDCAuthenticator(t.Context(), idp.URL, "https://jam.test/api", "")
	if err != nil {
		t.Fatal(err)
	}
	lc := &jam.OperatorLoginConfig{Issuer: idp.URL, ClientID: "device-cid", Scope: "openid"}
	st := jam.NewMemStore()
	for name, tc := range map[string]struct {
		lc      *jam.OperatorLoginConfig
		auth    jam.OperatorAuthenticator
		ca      *sshca.CA
		mounted bool
	}{
		"all set":         {lc, oidcAuth, ca, true},
		"no device login": {nil, oidcAuth, ca, false},
		"loopback auth":   {lc, jam.LoopbackAuthenticator{}, ca, false},
		"no SSH CA":       {lc, oidcAuth, nil, false},
	} {
		meMux := http.NewServeMux()
		called := false
		got := mountParticipantStudios(meMux, tc.lc, tc.auth, tc.ca, st, func(p func(*http.Request) (collab.Principal, string, error)) http.Handler {
			called = true
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// The mounted principal is the bearer: no header is no credentials.
				if _, _, err := p(r); errors.Is(err, collabhttp.ErrNoCredentials) {
					w.WriteHeader(http.StatusUnauthorized)
				}
			})
		})
		rec := httptest.NewRecorder()
		meMux.ServeHTTP(rec, httptest.NewRequest("GET", "/me/studios/ses_a/ssh", nil))
		wantCode := http.StatusNotFound
		if tc.mounted {
			wantCode = http.StatusUnauthorized
		}
		if got != tc.mounted || called != tc.mounted || rec.Code != wantCode {
			t.Errorf("%s: mounted=%v called=%v code=%d, want mounted=%v code=%d", name, got, called, rec.Code, tc.mounted, wantCode)
		}
	}
}
