package githubissues

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/kit"
	"github.com/aethons-tools/cove/internal/ticketkey"
)

// serverTransport sends every request to srv whatever its URL's host, so the
// Client's fixed api.github.com root reaches an httptest GitHub (redirects
// included: the client resolves Location against api.github.com first).
type serverTransport struct{ to *url.URL }

func (s serverTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.URL.Scheme, r.URL.Host, r.Host = s.to.Scheme, s.to.Host, s.to.Host
	return http.DefaultTransport.RoundTrip(r)
}

func jamTracker() kit.GitHubTracker {
	return kit.GitHubTracker{Repo: "acme/board", ClassLabelPrefix: "class:", States: testCfg().Tracker.GitHub.States}
}

func newJamClientToken(t *testing.T, h http.Handler, token string) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	c, err := NewJam(jamTracker(), token, &http.Client{Transport: serverTransport{u}})
	if err != nil {
		t.Fatalf("NewJam: %v", err)
	}
	return c
}

func newJamClient(t *testing.T, h http.Handler) *Client { return newJamClientToken(t, h, "tok") }

func TestNewJamRejectsBadRepo(t *testing.T) {
	for _, repo := range []string{"", "acme", "acme/..", "acme/board/x"} {
		gt := jamTracker()
		gt.Repo = repo
		if _, err := NewJam(gt, "tok", nil); !errors.Is(err, ticketkey.ErrRepo) {
			t.Errorf("NewJam(repo %q) = %v; want ErrRepo", repo, err)
		}
	}
}

func TestNewJamQualifiesKeysAndGatesOnDispatchLabel(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /search/issues", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"items":[
		 {"number":5,"title":"T5","state":"open","labels":[{"name":"status:ready"},{"name":"dispatch:go"},{"name":"class:impl"}]},
		 {"number":6,"title":"T6","state":"open","labels":[{"name":"status:ready"}]}]}`)
	})
	got, err := newJamClient(t, mux).ListReady(context.Background())
	if err != nil {
		t.Fatalf("ListReady: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListReady = %+v", got)
	}
	if got[0].ID != "5" || got[0].Identifier != "acme/board#5" || got[0].Class != "impl" || !got[0].DispatchLabeled {
		t.Fatalf("labeled issue = %+v", got[0])
	}
	if got[1].Identifier != "acme/board#6" || got[1].DispatchLabeled {
		t.Fatalf("unlabeled issue = %+v", got[1])
	}
}

// Review Focus 1 and 3: GitHub's search can return a pull request, and its
// index lags a close — neither may be dispatched.
func TestListReadyDropsPullRequestsAndClosed(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /search/issues", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"items":[
		 {"number":7,"state":"open","labels":[{"name":"status:ready"},{"name":"dispatch:go"}]},
		 {"number":8,"state":"open","labels":[{"name":"status:ready"},{"name":"dispatch:go"}],"pull_request":{"url":"https://api.github.com/repos/acme/board/pulls/8"}},
		 {"number":9,"state":"closed","labels":[{"name":"status:ready"},{"name":"dispatch:go"}]}]}`)
	})
	got, err := newJamClient(t, mux).ListReady(context.Background())
	if err != nil {
		t.Fatalf("ListReady: %v", err)
	}
	if len(got) != 1 || got[0].Identifier != "acme/board#7" {
		t.Fatalf("ListReady = %+v; want only acme/board#7", got)
	}
}

func TestIssueByIdentifier(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/acme/board/issues/7", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"number":7,"state":"open"}`)
	})
	mux.HandleFunc("GET /repos/acme/board/issues/8", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"number":8,"state":"open","pull_request":{"url":"x"}}`)
	})
	c := newJamClient(t, mux)
	ctx := context.Background()
	for _, key := range []string{"acme/board#7", "Acme/Board#7"} { // GitHub owner/name are case-insensitive
		if id, err := c.IssueByIdentifier(ctx, key); err != nil || id != "7" {
			t.Fatalf("IssueByIdentifier(%q) = %q, %v", key, id, err)
		}
	}
	for key, want := range map[string]string{
		"#7":             "github ticket key must be owner/repo#<n>",
		"AET-7":          "github ticket key must be owner/repo#<n>",
		"other/repo#7":   "is not on repo acme/board",
		"acme/board#404": "no issue acme/board#404",
		"acme/board#8":   "is a pull request",
	} {
		if _, err := c.IssueByIdentifier(ctx, key); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("IssueByIdentifier(%q) = %v; want %q", key, err, want)
		}
	}
}

// A renamed repo: GitHub answers the old path with 301 (GET) or 307
// (writes) to the new one. Calls must still land, with their auth and body.
func TestRenamedRepoRedirects(t *testing.T) {
	var auth, body string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/acme/board/issues/7", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/repos/acme/renamed/issues/7", http.StatusMovedPermanently)
	})
	mux.HandleFunc("GET /repos/acme/renamed/issues/7", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"number":7,"state":"open"}`)
	})
	mux.HandleFunc("POST /repos/acme/board/issues/7/comments", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/repos/acme/renamed/issues/7/comments", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("POST /repos/acme/renamed/issues/7/comments", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		auth, body = r.Header.Get("Authorization"), string(b)
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{}`)
	})
	c := newJamClient(t, mux)
	id, err := c.IssueByIdentifier(context.Background(), "acme/board#7")
	if err != nil || id != "7" {
		t.Fatalf("IssueByIdentifier through 301 = %q, %v", id, err)
	}
	if err := c.PostComment(context.Background(), id, "hi"); err != nil {
		t.Fatalf("PostComment through 307: %v", err)
	}
	if auth != "Bearer tok" || !strings.Contains(body, `"body":"hi"`) {
		t.Fatalf("redirected POST auth=%q body=%q", auth, body)
	}
}

// F8: at-cove's New is unchanged by the Jam work — "#N" identifiers, no
// dispatch gate (DispatchLabeled stays false, even with a prefix configured),
// and the search hits are returned unfiltered.
func TestNewKeepsAtCoveOutputs(t *testing.T) {
	cfg := testCfg()
	cfg.Tracker.GitHub.DispatchLabelPrefix = "dispatch:"
	mux := http.NewServeMux()
	mux.HandleFunc("GET /search/issues", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"items":[
		 {"number":7,"state":"open","labels":[{"name":"status:ready"},{"name":"dispatch:go"},{"name":"class:impl"}]},
		 {"number":8,"state":"open","labels":[{"name":"status:ready"}],"pull_request":{"url":"x"}},
		 {"number":9,"state":"closed","labels":[{"name":"status:ready"}]}]}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	c, err := New(cfg, "tok", &http.Client{Transport: serverTransport{u}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, err := c.ListReady(context.Background())
	if err != nil {
		t.Fatalf("ListReady: %v", err)
	}
	want := []string{"#7", "#8", "#9"}
	if len(got) != len(want) {
		t.Fatalf("ListReady = %+v; want %v", got, want)
	}
	for i, iss := range got {
		if iss.Identifier != want[i] || iss.DispatchLabeled {
			t.Errorf("issue %d = %+v; want Identifier %q, DispatchLabeled false", i, iss, want[i])
		}
	}
	if got[0].Class != "impl" {
		t.Errorf("class = %q; want impl", got[0].Class)
	}
}
