package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func resp(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func testKeyPEM(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}

func TestMintGitHubHappyPath(t *testing.T) {
	var gotURL, gotAuth string
	var gotBody map[string]any
	c := &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		gotURL = r.URL.String()
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		return resp(201, `{"token":"ghs_installationtoken"}`), nil
	})}
	in := githubInput{AppID: "123", InstallID: "456", KeyPEM: testKeyPEM(t), Repo: "acme/widgets"}
	tok, err := mintGitHub(context.Background(), c, in, time.Unix(1_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	if tok != "ghs_installationtoken" {
		t.Fatalf("token = %q", tok)
	}
	if gotURL != "https://api.github.com/app/installations/456/access_tokens" {
		t.Fatalf("url = %q", gotURL)
	}
	if !strings.HasPrefix(gotAuth, "Bearer ") || strings.Count(gotAuth, ".") != 2 {
		t.Fatalf("auth header not a bearer JWT: %q", gotAuth)
	}
	repos, _ := gotBody["repositories"].([]any)
	if len(repos) != 1 || repos[0] != "widgets" { // owner/name -> name
		t.Fatalf("repositories = %v, want [widgets]", gotBody["repositories"])
	}
	perms, _ := gotBody["permissions"].(map[string]any)
	if perms["contents"] != "write" || perms["pull_requests"] != "write" {
		t.Fatalf("permissions = %v", gotBody["permissions"])
	}
}

func TestMintGitHubNon2xxFailsClosed(t *testing.T) {
	c := &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		return resp(403, `{"message":"Resource not accessible by integration"}`), nil
	})}
	in := githubInput{AppID: "1", InstallID: "2", KeyPEM: testKeyPEM(t), Repo: "o/r"}
	if _, err := mintGitHub(context.Background(), c, in, time.Now()); err == nil {
		t.Fatal("non-2xx must be an error")
	}
}

func TestMintGitHubRequiresRepo(t *testing.T) {
	in := githubInput{AppID: "1", InstallID: "2", KeyPEM: testKeyPEM(t), Repo: ""}
	if _, err := mintGitHub(context.Background(), &http.Client{}, in, time.Now()); err == nil {
		t.Fatal("empty repo must be an error")
	}
}
