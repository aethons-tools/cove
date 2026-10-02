package snippet

import (
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLegacyExpandMatchesEnv(t *testing.T) {
	if got, want := Legacy(false).Expand("https://jam.local/", "TOK"), Env("https://jam.local/", "TOK"); !maps.Equal(got, want) {
		t.Fatalf("Legacy(false).Expand = %v, want %v", got, want)
	}
}

func TestLegacySubscriptionUsesAuthToken(t *testing.T) {
	e := Legacy(true).Expand("https://jam.local", "TOK")
	if e["ANTHROPIC_AUTH_TOKEN"] != "TOK" || e["ANTHROPIC_API_KEY"] != "" {
		t.Fatalf("subscription env = %v", e)
	}
}

func ghConnector() Connector {
	return Connector{Env: map[string]string{"GH_HOST": "{host}", "GH_ENTERPRISE_TOKEN": "{token}", "X_URL": "{base}/api/v3"}}
}

func TestExpandResolvesPlaceholders(t *testing.T) {
	e := ghConnector().Expand("https://jam.example/", "TOK")
	if e["GH_HOST"] != "jam.example" || e["GH_ENTERPRISE_TOKEN"] != "TOK" || e["X_URL"] != "https://jam.example/api/v3" || e["AT_JAM_IDENTITY_TOKEN"] != "TOK" {
		t.Fatalf("Expand = %v", e)
	}
}

func TestRenderReferencesTokenOnce(t *testing.T) {
	out := ghConnector().Render("https://jam.example", "TOK123")
	for _, want := range []string{
		"export AT_JAM_IDENTITY_TOKEN=TOK123\n",
		`export GH_ENTERPRISE_TOKEN="${AT_JAM_IDENTITY_TOKEN}"`,
		`export GH_HOST="jam.example"`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("render missing %q:\n%s", want, out)
		}
	}
	if n := strings.Count(out, "TOK123"); n != 1 {
		t.Fatalf("raw token appears %d times:\n%s", n, out)
	}
	if strings.Index(out, "GH_ENTERPRISE_TOKEN") > strings.Index(out, "GH_HOST") {
		t.Fatalf("env keys not rendered sorted:\n%s", out)
	}
	if strings.Contains(out, "insteadOf") {
		t.Fatalf("no GitRoute → no git config:\n%s", out)
	}
}

func TestRenderQuotesLiteralShellChars(t *testing.T) {
	c := Connector{Env: map[string]string{"X": "a\"b$c`d\\e"}}
	if out := c.Render("https://j", "T"); !strings.Contains(out, `export X="a\"b\$c\`+"`"+`d\\e"`) {
		t.Fatalf("literal not shell-escaped:\n%s", out)
	}
}

func TestConnectorGitConfigUsesRoute(t *testing.T) {
	g := Connector{GitRoute: "/gh/"}.GitConfig("https://jam.example/")
	if !strings.Contains(g, `url."https://jam.example/gh/".insteadOf https://github.com/`) {
		t.Fatalf("GitConfig = %s", g)
	}
	if (Connector{}).GitConfig("https://jam.example") != "" {
		t.Fatal("no GitRoute → empty git config")
	}
}

func TestFetch(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/connector":
			auth = r.Header.Get("Authorization")
			_, _ = w.Write([]byte(`{"env":{"GH_HOST":"{host}"},"git_route":"/git/"}`))
		case "/old/connector":
			http.NotFound(w, r)
		default:
			http.Error(w, "boom", http.StatusInternalServerError)
		}
	}))
	defer srv.Close()
	c, err := Fetch(srv.Client(), srv.URL, "TOK")
	if err != nil || c.Env["GH_HOST"] != "{host}" || c.GitRoute != "/git/" || auth != "Bearer TOK" {
		t.Fatalf("Fetch = %+v, %v (auth %q)", c, err, auth)
	}
	if _, err := Fetch(srv.Client(), srv.URL+"/old", "TOK"); !errors.Is(err, ErrNoConnectorEndpoint) {
		t.Fatalf("404 err = %v, want ErrNoConnectorEndpoint", err)
	}
	if _, err := Fetch(srv.Client(), srv.URL+"/broken", "TOK"); err == nil || errors.Is(err, ErrNoConnectorEndpoint) {
		t.Fatalf("500 err = %v", err)
	}
}

func TestRenderBracesTokenReference(t *testing.T) {
	out := Connector{Env: map[string]string{"X": "{token}_suffix"}}.Render("https://j", "T")
	if !strings.Contains(out, `export X="${AT_JAM_IDENTITY_TOKEN}_suffix"`) {
		t.Fatalf("token reference must be braced so trailing name chars aren't absorbed:\n%s", out)
	}
}
