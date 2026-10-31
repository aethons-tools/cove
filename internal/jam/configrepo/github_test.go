package configrepo

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAPIBase(t *testing.T) {
	for in, want := range map[string][3]string{
		"https://github.com/acme/jam-config":       {"https://api.github.com", "acme", "jam-config"},
		"https://github.com/acme/jam-config.git":   {"https://api.github.com", "acme", "jam-config"},
		"https://ghe.example.com/acme/jam-config/": {"https://ghe.example.com/api/v3", "acme", "jam-config"},
	} {
		b, o, r, err := apiBase(in)
		if err != nil || [3]string{b, o, r} != want {
			t.Errorf("%s = %s %s %s %v", in, b, o, r, err)
		}
	}
	for _, bad := range []string{"https://github.com/acme", "not a url", "https://github.com/a/b/c"} {
		if _, _, _, err := apiBase(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

func ghServer(t *testing.T, h http.HandlerFunc) GitHub {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return GitHub{URL: "https://github.com/acme/jam", APIBase: ts.URL, Token: func() (string, error) { return "tok-123", nil }, Client: ts.Client()}
}

func TestOpenPR(t *testing.T) {
	var got map[string]string
	g := ghServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/repos/acme/jam/pulls" || r.Header.Get("Authorization") != "Bearer tok-123" {
			t.Errorf("%s %s auth %q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"number": 7, "html_url": "https://github.com/acme/jam/pull/7"}`))
	})
	pr, err := g.OpenPR(ctx, "jam/alice", "main", "Config from jam/alice", "body")
	if err != nil || pr.Number != 7 || pr.URL != "https://github.com/acme/jam/pull/7" {
		t.Fatalf("pr = %+v %v", pr, err)
	}
	if got["head"] != "jam/alice" || got["base"] != "main" || got["title"] != "Config from jam/alice" {
		t.Fatalf("sent %v", got)
	}
}

func TestOpenPRAlreadyExists(t *testing.T) {
	g := ghServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			w.WriteHeader(422)
			_, _ = w.Write([]byte(`{"message": "Validation Failed", "errors": [{"message": "A pull request already exists for acme:jam/alice."}]}`))
			return
		}
		if q := r.URL.Query(); q.Get("head") != "acme:jam/alice" || q.Get("base") != "main" || q.Get("state") != "open" {
			t.Errorf("query %v", q)
		}
		_, _ = w.Write([]byte(`[{"number": 3, "html_url": "https://github.com/acme/jam/pull/3"}]`))
	})
	pr, err := g.OpenPR(ctx, "jam/alice", "main", "t", "b")
	if err != nil || pr.Number != 3 {
		t.Fatalf("pr = %+v %v", pr, err)
	}
}

func TestFindPRNone(t *testing.T) {
	g := ghServer(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`[]`)) })
	if _, ok, err := g.FindPR(ctx, "jam/alice", "main"); err != nil || ok {
		t.Fatalf("find = %v %v", ok, err)
	}
}

func TestGitHubErrorHasNoToken(t *testing.T) {
	g := ghServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"message": "Bad credentials"}`))
	})
	_, err := g.OpenPR(ctx, "jam/alice", "main", "t", "b")
	if err == nil || strings.Contains(err.Error(), "tok-123") || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "Bad credentials") {
		t.Fatalf("err = %v", err)
	}
}
