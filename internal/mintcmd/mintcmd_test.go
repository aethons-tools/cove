package mintcmd

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
	var out, errOut bytes.Buffer
	if code := Run([]string{"version"}, "dev", envMap(nil), http.DefaultClient, &out, &errOut); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if got := strings.TrimSpace(out.String()); got != "jam mint dev" {
		t.Fatalf("version = %q, want %q", got, "jam mint dev")
	}
}

func TestRunUnknownCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := Run([]string{"bogus"}, "dev", envMap(nil), http.DefaultClient, &out, &errOut); code != 2 {
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
		"JAM_MINT_GITHUB_APP_KEY": string(keyPEM),
	})
	var out, errOut bytes.Buffer
	code := Run([]string{"github", "--app-id", "1", "--install-id", "2", "--repo", "acme/widgets"}, "dev", env, httpc, &out, &errOut)
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
	code := Run([]string{"github", "--app-id", "1", "--install-id", "2", "--repo", "o/r"}, "dev", env, http.DefaultClient, &out, &errOut)
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
	env := envMap(map[string]string{"JAM_MINT_AUTH0_CLIENT_SECRET": "shh"})
	var out, errOut bytes.Buffer
	code := Run([]string{"anthropic",
		"--auth0-tenant", "t.us.auth0.com", "--auth0-client-id", "c", "--auth0-audience", "a",
		"--anthropic-org", "o", "--anthropic-rule", "fdrl_1", "--anthropic-service-account", "svac_1",
	}, "dev", env, httpc, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d; stderr=%q", code, errOut.String())
	}
	if strings.TrimSpace(out.String()) != "sk-ant-oat01-ok" {
		t.Fatalf("stdout = %q", out.String())
	}
}
