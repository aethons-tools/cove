package configrepo

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGitLabProject(t *testing.T) {
	for in, want := range map[string][2]string{
		"https://gitlab.com/aethons-tools/jam-config":         {"https://gitlab.com/api/v4", "aethons-tools/jam-config"},
		"https://gitlab.com/aethons-tools/sub/jam-config.git": {"https://gitlab.com/api/v4", "aethons-tools/sub/jam-config"},
		"https://git.example.com/g/r/":                        {"https://git.example.com/api/v4", "g/r"},
	} {
		b, p, err := gitlabProject(in)
		if err != nil || [2]string{b, p} != want {
			t.Errorf("%s = %s %s %v", in, b, p, err)
		}
	}
	for _, bad := range []string{"https://gitlab.com/onlygroup", "nope"} {
		if _, _, err := gitlabProject(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

func glServer(t *testing.T, h http.HandlerFunc) GitLab {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return GitLab{URL: "https://gitlab.com/aethons-tools/jam-config", APIBase: ts.URL, Token: func() (string, error) { return "glpat-123", nil }, Client: ts.Client()}
}

func TestGitLabOpenMR(t *testing.T) {
	var got map[string]string
	g := glServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.EscapedPath() != "/projects/aethons-tools%2Fjam-config/merge_requests" || r.Header.Get("Authorization") != "Bearer glpat-123" {
			t.Errorf("%s %s auth %q", r.Method, r.URL.EscapedPath(), r.Header.Get("Authorization"))
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"iid": 4, "web_url": "https://gitlab.com/aethons-tools/jam-config/-/merge_requests/4"}`))
	})
	pr, err := g.OpenPR(ctx, "jam/alice", "main", "Jam config: jam/alice", "body")
	if err != nil || pr.Number != 4 || pr.URL != "https://gitlab.com/aethons-tools/jam-config/-/merge_requests/4" {
		t.Fatalf("mr = %+v %v", pr, err)
	}
	if got["source_branch"] != "jam/alice" || got["target_branch"] != "main" || got["title"] != "Jam config: jam/alice" || got["description"] != "body" {
		t.Fatalf("sent %v", got)
	}
}

func TestGitLabOpenMRAlreadyExists(t *testing.T) {
	g := glServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			w.WriteHeader(409)
			_, _ = w.Write([]byte(`{"message": ["Another open merge request already exists for this source branch: !2"]}`))
			return
		}
		if q := r.URL.Query(); q.Get("source_branch") != "jam/alice" || q.Get("target_branch") != "main" || q.Get("state") != "opened" {
			t.Errorf("query %v", q)
		}
		_, _ = w.Write([]byte(`[{"iid": 2, "web_url": "https://gitlab.com/x/-/merge_requests/2"}]`))
	})
	pr, err := g.OpenPR(ctx, "jam/alice", "main", "t", "b")
	if err != nil || pr.Number != 2 {
		t.Fatalf("mr = %+v %v", pr, err)
	}
	if _, ok, err := g.FindPR(ctx, "jam/alice", "main"); err != nil || !ok {
		t.Fatalf("find = %v %v", ok, err)
	}
}

func TestGitLabErrorHasNoToken(t *testing.T) {
	g := glServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"message": "401 Unauthorized"}`))
	})
	_, err := g.OpenPR(ctx, "jam/alice", "main", "t", "b")
	if err == nil || strings.Contains(err.Error(), "glpat-123") || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v", err)
	}
}

// Both clients are PR providers.
var _ = []PRs{GitHub{}, GitLab{}}
