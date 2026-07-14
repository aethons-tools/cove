package main

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"strings"
	"testing"
)

// envMap builds an env lookup func from a map.
func envMap(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func TestRunVersion(t *testing.T) {
	old := version
	defer func() { version = old }()
	version = "9.9.9"
	var out, errOut bytes.Buffer
	if code := run([]string{"version"}, envMap(nil), http.DefaultClient, &out, &errOut); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out.String(), "9.9.9") {
		t.Fatalf("version not printed: %q", out.String())
	}
}

func TestRunUnknownCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"bogus"}, envMap(nil), http.DefaultClient, &out, &errOut); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}

func TestRunGitHubPrintsOnlyToken(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	httpc := &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		return resp(201, `{"token":"ghs_ok"}`), nil
	})}
	env := envMap(map[string]string{
		"AT_MINT_GITHUB_APP_KEY": string(keyPEM),
	})
	var out, errOut bytes.Buffer
	code := run([]string{"github", "--app-id", "1", "--install-id", "2", "--repo", "acme/widgets"}, env, httpc, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d; stderr=%q", code, errOut.String())
	}
	if strings.TrimSpace(out.String()) != "ghs_ok" {
		t.Fatalf("stdout must be exactly the token, got %q", out.String())
	}
}

func TestRunGitHubFailsClosedNoStdout(t *testing.T) {
	// Missing key env -> error, non-zero, nothing on stdout.
	env := envMap(nil)
	var out, errOut bytes.Buffer
	code := run([]string{"github", "--app-id", "1", "--install-id", "2", "--repo", "o/r"}, env, http.DefaultClient, &out, &errOut)
	if code == 0 {
		t.Fatal("missing app key must fail closed")
	}
	if out.Len() != 0 {
		t.Fatalf("stdout must be empty on failure, got %q", out.String())
	}
	if errOut.Len() == 0 {
		t.Fatal("expected a diagnostic on stderr")
	}
}

func TestRunAnthropicPrintsOnlyToken(t *testing.T) {
	httpc := &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.String(), "auth0.com") {
			return resp(200, `{"access_token":"j"}`), nil
		}
		return resp(200, `{"access_token":"sk-ant-oat01-ok"}`), nil
	})}
	env := envMap(map[string]string{"AT_MINT_AUTH0_CLIENT_SECRET": "shh"})
	var out, errOut bytes.Buffer
	code := run([]string{"anthropic",
		"--auth0-tenant", "t.us.auth0.com", "--auth0-client-id", "c", "--auth0-audience", "a",
		"--anthropic-org", "o", "--anthropic-rule", "fdrl_1", "--anthropic-service-account", "svac_1",
	}, env, httpc, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d; stderr=%q", code, errOut.String())
	}
	if strings.TrimSpace(out.String()) != "sk-ant-oat01-ok" {
		t.Fatalf("stdout = %q", out.String())
	}
}
