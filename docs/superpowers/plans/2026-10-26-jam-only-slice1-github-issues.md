# Jam only — slice 1: GitHub Issues in Jam Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Jam's Requisitioner, ticket reports, wake-on close and comment relay run against a GitHub repo's Issues as well as against Linear. Both trackers sit behind one Jam-side interface, and GitHub ticket keys are `owner/repo#<n>`.

**Architecture:**
- **One contract.** A new leaf package `internal/tracker` holds the Jam-side `Tracker` interface, the shared `FeedComment` and `RateLimitError`. `linear.FeedComment` becomes an alias of `tracker.FeedComment`.
- **Ticket keys.** A dependency-free package `internal/ticketkey` validates and builds `owner/repo#<n>`. Both `internal/jam` and the GitHub client use it.
- **The GitHub client stays where it is.** `internal/dispatch/githubissues` already sits beside Jam's Linear tracker (`internal/dispatch/linear`), so this slice does not move it and needs no forwarding shim. It gains a Jam constructor `NewJam`: qualified keys and a dispatch-label gate. It also gains `IssueByIdentifier`, `CommentFeed` and `Viewer`. at-cove's `New` keeps `#N` identifiers, so `cmd/at-cove` and the scheduler engine (`WorkBranch`, `closesRef`) behave exactly as before.
- **Jam reads its GitHub config from `kit.GitHubTracker`.** It is referenced from `runtime.requisitioner.github`, just as `kit.LinearTracker` already is from `linear:`. The type gains `dispatch-label-prefix`. It leaves `internal/kit` with the kit's `tracker:` block in slice 2.
- **Serve picks the tracker.** It builds the tracker from whichever block is set, resolves a connection of the matching kind, and wires the same value into the Requisitioner, the ticketer and the relay surface. The relay surface and directory generalise from the literal `"linear"` to the tracker's kind.

**Tech Stack:** Go 1.26, `net/http` / `net/http/httptest`, GitHub REST v3, Linear GraphQL, `path.Match` globs.

**Spec:** [docs/superpowers/specs/2026-10-26-jam-only-design.md](../specs/2026-10-26-jam-only-design.md) — §1 (this slice), §5 (errors), §6 (testing), Decisions 2 and 8.

## Global Constraints

- **Purely additive.** `cmd/at-cove` must build and pass unchanged. `githubissues.New` keeps its signature and its `"#N"` identifiers.
- **Config:** `runtime.requisitioner` takes exactly one of `linear:` or `github:`. Both, or neither, fail with the fixed text `requisitioner: set exactly one of linear or github`.
- **`github:` fields:**
  - `repo` (`owner/name`)
  - `poll-interval`
  - `class-label-prefix`
  - `dispatch-label-prefix` (new; default `dispatch:`; an issue is dispatchable only with a label under it)
  - `states` (status labels; done = the issue is closed)
- **Credentials:** a new connection kind `github`, chosen with `connection:`. Its token needs issues read/write on the repo. It resolves through the existing credentials path, and its value is never logged or echoed in an error.
- **Ticket keys:** `owner/repo#<n>` everywhere Jam shows or stores a GitHub key: ticket channels (`ticket:<key>`), intercom targets, escalation and reports. Keys are validated with `^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+#[0-9]+$` before use. A key that fails is refused with the fixed text `github ticket key must be owner/repo#<n>`.
- **Endpoints:**
  - `CommentFeed` = `GET /repos/{r}/issues/comments?since=` (paginated; the cursor is monotonic)
  - `Viewer` = `GET /user`
  - Blockers stay `Depends on #N` / `Blocked by #N` in the issue body.
- **Tests:** hermetic only, against `httptest` servers; no network. The Requisitioner and the relay are tested end to end on the shared interface with both trackers.
- **Build hygiene:** `GOPROXY=https://proxy.golang.org,direct`. `gofmt` clean. `just test` and `just lint` exit 0, and `go vet -tags integration ./...` is clean.
- **Commits:** every commit message ends with a blank line, then `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- **Docs:** one fact lives in one place. The docs audit (`python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs`) must gain no errors beyond this plan file's own.

## Review Focus

1. **GitHub lists pull requests as issues.** A PR carrying `status:ready` + `dispatch:go`, a ticket key that names a PR, and a comment on a PR conversation must never be dispatched, resolved or relayed. Tests:
   - Task 2: `TestListReadyDropsPullRequestsAndClosed`, `TestIssueByIdentifier`
   - Task 3: `TestCommentFeedMapsAndFilters`
   - Task 4: the fake's PR `#3` in `TestTrackersShareTheInterface`
2. **The comments feed spans pages and gets rate-limited.**
   - It follows `Link: rel="next"`, but only on `api.github.com`, and it stops at `limit`.
   - A 403 or 429 with `Retry-After` (or `X-RateLimit-Remaining: 0`) becomes a `tracker.RateLimitError`.
   - The relay surface makes no further calls until that time has passed, and never moves its cursor backwards.
   - Tests:
     - Task 3: `TestCommentFeedFollowsLinkNext`, `TestCommentFeedRateLimited`
     - Task 6: `TestTrackerSurfaceWaitsOutRateLimit`, `TestTrackerSurfaceCursorNeverMovesBack`
3. **An issue carrying the dispatch label but already closed.** The search index lags, so a closed hit is dropped and never raised. Test: Task 2 `TestListReadyDropsPullRequestsAndClosed`.
4. **A key with `/` and `#` reaches channel keys, `ticket:` targets and addressing globs.**
   - `ticket:acme/board#7`, `ticket:<conn>/acme/board#7`, `ticket:*` and `ticket:acme/*` all resolve or match.
   - A malformed key is refused with the fixed text.
   - Linear globs keep their exact semantics.
   - Tests (Task 5): `TestGitHubTicketChannel`, `TestPlanGitHubTicketTargets`, `TestGlobMatchGitHubTickets`, `TestListTargetsGitHubTicketSessions`.
5. **Jam's own comments, and other bots, come back on the feed.** The relay drops comments by the Viewer's identity (a Linear displayName or a GitHub login) and comments by GitHub accounts of type `Bot`. A person's reply still lands in the ticket channel. Test: Task 6 `TestRelayEndToEndBothTrackers`.

Covered by task tests beyond these five:
- A renamed repo: the API answers 301/307 and calls still land with auth (Task 2 `TestRenamedRepoRedirects`).
- Owner/name case (Task 2 `TestIssueByIdentifier`).
- Tokens in error bodies (Task 3 `TestErrorsNeverCarryTheToken`).

---

### Task 1: The shared contract — `internal/tracker` and `internal/ticketkey`

**Files:**
- Create: `internal/ticketkey/ticketkey.go`
- Create: `internal/ticketkey/ticketkey_test.go`
- Create: `internal/tracker/tracker.go`
- Create: `internal/tracker/tracker_test.go`
- Modify: `internal/dispatch/linear/linear.go:383-393` (`FeedComment` becomes an alias; add the `tracker.Tracker` assertion at the end of the file)

**Interfaces:**
- Consumes: `scheduler.Issue`, `scheduler.Comment`, `scheduler.Role` (`internal/dispatch/scheduler`)
- Produces:
  - `ticketkey.ErrGitHub` (text `github ticket key must be owner/repo#<n>`)
  - `ticketkey.ErrRepo` (text `github repo must be owner/name`)
  - `func ticketkey.ParseGitHub(key string) (repo string, n int, err error)`
  - `func ticketkey.IsGitHub(key string) bool`
  - `func ticketkey.GitHub(repo string, n int) string`
  - `func ticketkey.ValidRepo(repo string) error`
  - `tracker.Tracker` (interface below)
  - `tracker.FeedComment{ID, Body string; CreatedAt time.Time; Author, AuthorID string; AuthorBot bool; IssueIdentifier, ParentID string}`
  - `tracker.RateLimitError{Service string; RetryAfter time.Duration}`
  - `tracker.KindLinear = "linear"`, `tracker.KindGitHub = "github"`

- [ ] **Step 1: Write the failing tests**

`internal/ticketkey/ticketkey_test.go`:

```go
package ticketkey

import (
	"errors"
	"testing"
)

func TestParseGitHub(t *testing.T) {
	for _, c := range []struct {
		key  string
		repo string
		n    int
		ok   bool
	}{
		{"acme/board#12", "acme/board", 12, true},
		{"Acme-Co/board.go#1", "Acme-Co/board.go", 1, true},
		{"a_b/c-d#999999", "a_b/c-d", 999999, true},
		{"#12", "", 0, false},              // at-cove's bare form is not a Jam key
		{"AET-42", "", 0, false},           // a Linear key
		{"acme/board#", "", 0, false},      // no number
		{"acme/board#0", "", 0, false},     // issue numbers start at 1
		{"acme/board#x1", "", 0, false},    // not a number
		{"acme#12", "", 0, false},          // no repo
		{"acme/board/x#12", "", 0, false},  // a path, not owner/repo
		{"acme/..#12", "", 0, false},       // would walk the URL path
		{"../board#12", "", 0, false},      // would walk the URL path
		{"acme/board#12 ", "", 0, false},   // trailing space
		{"acme/bo ard#12", "", 0, false},   // space
		{"acme/board#99999999999999999999", "", 0, false}, // overflows int
	} {
		repo, n, err := ParseGitHub(c.key)
		if c.ok != (err == nil) || repo != c.repo || n != c.n {
			t.Errorf("ParseGitHub(%q) = %q, %d, %v; want %q, %d, ok=%v", c.key, repo, n, err, c.repo, c.n, c.ok)
		}
		if err != nil && !errors.Is(err, ErrGitHub) {
			t.Errorf("ParseGitHub(%q) error %v is not ErrGitHub", c.key, err)
		}
		if IsGitHub(c.key) != c.ok {
			t.Errorf("IsGitHub(%q) = %v", c.key, !c.ok)
		}
	}
	if ErrGitHub.Error() != "github ticket key must be owner/repo#<n>" {
		t.Fatalf("ErrGitHub text = %q", ErrGitHub.Error())
	}
}

func TestGitHubBuildsAKeyThatParses(t *testing.T) {
	k := GitHub("acme/board", 7)
	if k != "acme/board#7" {
		t.Fatalf("GitHub = %q", k)
	}
	if repo, n, err := ParseGitHub(k); err != nil || repo != "acme/board" || n != 7 {
		t.Fatalf("round trip = %q %d %v", repo, n, err)
	}
}

func TestValidRepo(t *testing.T) {
	for repo, ok := range map[string]bool{
		"acme/board": true, "a.b/c_d-e": true,
		"": false, "acme": false, "acme/": false, "/board": false, "acme/board/x": false,
		"acme/..": false, "./board": false, "acme/...": false, "acme/bo ard": false,
	} {
		if err := ValidRepo(repo); (err == nil) != ok {
			t.Errorf("ValidRepo(%q) = %v; want ok=%v", repo, err, ok)
		} else if err != nil && !errors.Is(err, ErrRepo) {
			t.Errorf("ValidRepo(%q) error %v is not ErrRepo", repo, err)
		}
	}
}
```

`internal/tracker/tracker_test.go`:

```go
package tracker

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestRateLimitErrorUnwrapsAndReads(t *testing.T) {
	err := fmt.Errorf("poll: %w", &RateLimitError{Service: KindGitHub, RetryAfter: 90 * time.Second})
	var rl *RateLimitError
	if !errors.As(err, &rl) || rl.RetryAfter != 90*time.Second {
		t.Fatalf("errors.As = %v, %+v", err, rl)
	}
	if got := rl.Error(); got != "github: rate limited; retry after 1m30s" {
		t.Fatalf("Error() = %q", got)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/ticketkey ./internal/tracker`
Expected: FAIL. The build reports `undefined: ParseGitHub` and `undefined: RateLimitError`.

- [ ] **Step 3: Write the implementation**

`internal/ticketkey/ticketkey.go`:

```go
// Package ticketkey validates and builds the ticket keys Jam stores for
// GitHub issues: "owner/repo#<n>". Linear keys ("AET-42") are opaque to it.
// It imports only the standard library so internal/jam and the trackers can
// both use it.
package ticketkey

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

var (
	// ErrGitHub is the fixed refusal for a malformed GitHub ticket key.
	ErrGitHub = errors.New("github ticket key must be owner/repo#<n>")
	// ErrRepo is the fixed refusal for a malformed GitHub repo.
	ErrRepo = errors.New("github repo must be owner/name")

	githubKey = regexp.MustCompile(`^([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)#([0-9]+)$`)
	repoRE    = regexp.MustCompile(`^([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)$`)
)

// ValidRepo checks an "owner/name" repo. A segment made only of dots is
// refused: GitHub allows none, and "." or ".." would walk an API URL path.
func ValidRepo(repo string) error {
	m := repoRE.FindStringSubmatch(repo)
	if m == nil || onlyDots(m[1]) || onlyDots(m[2]) {
		return ErrRepo
	}
	return nil
}

func onlyDots(s string) bool { return strings.Trim(s, ".") == "" }

// ParseGitHub splits "owner/repo#<n>" into its repo and issue number (n >= 1).
func ParseGitHub(key string) (repo string, n int, err error) {
	m := githubKey.FindStringSubmatch(key)
	if m == nil || ValidRepo(m[1]) != nil {
		return "", 0, ErrGitHub
	}
	n, err = strconv.Atoi(m[2])
	if err != nil || n < 1 {
		return "", 0, ErrGitHub
	}
	return m[1], n, nil
}

// IsGitHub reports whether key is a well-formed GitHub ticket key.
func IsGitHub(key string) bool {
	_, _, err := ParseGitHub(key)
	return err == nil
}

// GitHub builds the ticket key of issue n in repo.
func GitHub(repo string, n int) string { return repo + "#" + strconv.Itoa(n) }
```

`internal/tracker/tracker.go`:

```go
// Package tracker is Jam's one tracker contract: the methods the
// Requisitioner, ticket reports, wake-on close and the comment relay use.
// Both *linear.Client and *githubissues.Client implement it; serve builds
// one from runtime.requisitioner and hands the same value to all four.
package tracker

import (
	"context"
	"time"

	"github.com/aethons-tools/cove/internal/dispatch/scheduler"
)

// The tracker kinds — also the connection kinds and relay service names.
const (
	KindLinear = "linear"
	KindGitHub = "github"
)

// Tracker is what Jam needs from an issue tracker. Ids are the tracker's
// own (a Linear uuid, a GitHub issue number); identifiers are ticket keys
// ("AET-42", "owner/repo#12").
type Tracker interface {
	ListReady(ctx context.Context) ([]scheduler.Issue, error)
	Comments(ctx context.Context, issueID string) ([]scheduler.Comment, error)
	Transition(ctx context.Context, issueID string, role scheduler.Role) error
	IssueByIdentifier(ctx context.Context, identifier string) (string, error)
	PostComment(ctx context.Context, issueID, body string) error
	// CommentFeed lists comments created after since (GitHub: at or after),
	// oldest first, at most limit — the relay's ingress.
	CommentFeed(ctx context.Context, since time.Time, limit int) ([]FeedComment, error)
	// Viewer is the identity the token posts as (the relay's self-filter).
	Viewer(ctx context.Context) (string, error)
}

// FeedComment is one comment from a tracker's comment feed.
type FeedComment struct {
	ID              string
	Body            string
	CreatedAt       time.Time
	Author          string // display name (Linear) or login (GitHub)
	AuthorID        string // the service's immutable user id
	AuthorBot       bool   // a bot account (GitHub user type "Bot"); never a person
	IssueIdentifier string // the ticket key
	ParentID        string // the comment replied to ("" = top level; GitHub has none)
}

// RateLimitError is a tracker's refusal to answer until RetryAfter has
// passed. Callers back off rather than retry at once.
type RateLimitError struct {
	Service    string
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	return e.Service + ": rate limited; retry after " + e.RetryAfter.String()
}
```

In `internal/dispatch/linear/linear.go`, delete the `FeedComment` struct (lines 383-393) and put this in its place:

```go
// FeedComment is one comment from the team-scoped comments feed (Jam's
// shared shape; ParentID is the Linear parent comment id).
type FeedComment = tracker.FeedComment
```

Add `"github.com/aethons-tools/cove/internal/tracker"` to its imports, and add this line after the existing `var _ scheduler.Tracker = (*Client)(nil)`:

```go
var _ tracker.Tracker = (*Client)(nil)
```

- [ ] **Step 4: Run the tests and the build**

Run: `gofmt -l internal/ticketkey internal/tracker internal/dispatch/linear && go test ./internal/ticketkey ./internal/tracker ./internal/dispatch/linear ./cmd/at-jam -run 'Parse|Valid|GitHub|RateLimit|Linear|Feed' && go build ./...`
Expected: no gofmt output; PASS; the build succeeds. `cmd/at-jam` still compiles, because `linear.FeedComment` is an alias.

- [ ] **Step 5: Commit**

```bash
git add internal/ticketkey internal/tracker internal/dispatch/linear/linear.go
git commit -m "feat(tracker): Jam's shared tracker contract and GitHub ticket keys

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: GitHub tracker for Jam — qualified keys, the dispatch gate, `IssueByIdentifier`

**Files:**
- Modify: `internal/kit/config.go:272-278` (`GitHubTracker` gains `DispatchLabelPrefix`)
- Modify: `internal/dispatch/scheduler/scheduler.go` (add `ClassifyLabels`, moved from linear)
- Modify: `internal/dispatch/linear/linear.go:183,190-206` (use `scheduler.ClassifyLabels`; delete `classifyLabels`)
- Modify: `internal/dispatch/linear/linear_test.go:405` (`classifyLabels` → `scheduler.ClassifyLabels`)
- Modify: `internal/dispatch/githubissues/githubissues.go`
- Create: `internal/dispatch/githubissues/jam_test.go`

**Interfaces:**
- Consumes: `ticketkey.ParseGitHub`, `ticketkey.GitHub`, `ticketkey.ValidRepo` (Task 1)
- Produces:
  - `func githubissues.NewJam(gt kit.GitHubTracker, token string, httpc *http.Client) (*Client, error)`: identifiers are `owner/repo#N`; `dispatch-label-prefix` defaults to `dispatch:`.
  - `func (c *Client) IssueByIdentifier(ctx context.Context, identifier string) (string, error)`: returns the issue number as a string.
  - `func scheduler.ClassifyLabels(labels []string, classPrefix, dispatchPrefix string) (class string, dispatchLabeled bool)`
  - `kit.GitHubTracker.DispatchLabelPrefix` (yaml `dispatch-label-prefix`)

- [ ] **Step 1: Write the failing tests**

`internal/dispatch/githubissues/jam_test.go`:

```go
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
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/dispatch/githubissues -run 'NewJam|DropsPull|IssueByIdentifier|Renamed'`
Expected: FAIL to build (`undefined: NewJam`, `c.IssueByIdentifier undefined`).

- [ ] **Step 3: Implement**

`internal/kit/config.go`: add the field to `GitHubTracker`, after `ClassLabelPrefix`:

```go
	// DispatchLabelPrefix gates which ready issues Jam's Requisitioner raises
	// a studio for (presence-only; default "dispatch:", applied by
	// githubissues.NewJam). at-cove's scheduler ignores it.
	DispatchLabelPrefix string `yaml:"dispatch-label-prefix"`
```

`internal/dispatch/scheduler/scheduler.go`: append the function, moved verbatim from linear and exported:

```go
// ClassifyLabels derives an issue's handler class (the value after
// classPrefix; the first match wins) and whether it is tagged for dispatch
// (any label under dispatchPrefix; presence-only). An empty dispatchPrefix
// means no gate: every issue is dispatchable.
func ClassifyLabels(labels []string, classPrefix, dispatchPrefix string) (class string, dispatchLabeled bool) {
	dispatchLabeled = dispatchPrefix == ""
	for _, name := range labels {
		if class == "" && strings.HasPrefix(name, classPrefix) {
			class = strings.TrimPrefix(name, classPrefix)
		}
		if dispatchPrefix != "" && strings.HasPrefix(name, dispatchPrefix) {
			dispatchLabeled = true
		}
	}
	return class, dispatchLabeled
}
```

Add `"strings"` to scheduler.go's imports if it is not already there. In `internal/dispatch/linear/linear.go`, delete `classifyLabels` and its comment (lines 190-206), and change line 183 to `class, dispatchLabeled := scheduler.ClassifyLabels(names, c.prefix, c.dispatchPrefix)`. In `linear_test.go:405`, change `classifyLabels(` to `scheduler.ClassifyLabels(`.

`internal/dispatch/githubissues/githubissues.go`:

1. Imports: add `"cmp"` and `"github.com/aethons-tools/cove/internal/ticketkey"`.
2. `Client` gains two fields:

```go
type Client struct {
	http           *http.Client
	token          string
	repo           string                    // "owner/name"
	prefix         string                    // class label prefix, e.g. "class:"
	dispatchPrefix string                    // dispatch label prefix; "" = no gate (at-cove)
	qualified      bool                      // identifiers "owner/repo#N" (Jam) instead of "#N" (at-cove)
	labels         map[scheduler.Role]string // role → status label
}
```

3. In `New`, set `dispatchPrefix: gt.DispatchLabelPrefix` in the returned struct literal.
4. Add `NewJam` after `New`:

```go
// NewJam builds the Client Jam uses. Ticket keys are "owner/repo#N", and
// the dispatch-label gate defaults to "dispatch:". The repo is required (Jam
// has no source-control block to inherit it from). at-cove keeps New and its
// "#N" identifiers.
func NewJam(gt kit.GitHubTracker, token string, httpc *http.Client) (*Client, error) {
	if err := ticketkey.ValidRepo(strings.TrimSpace(gt.Repo)); err != nil {
		return nil, fmt.Errorf("githubissues: %w", err)
	}
	c, err := New(kit.Config{Tracker: &kit.Tracker{GitHub: &gt}}, token, httpc)
	if err != nil {
		return nil, err
	}
	c.qualified = true
	c.dispatchPrefix = cmp.Or(gt.DispatchLabelPrefix, "dispatch:")
	return c, nil
}
```

5. `ghIssue` gains the state and the PR marker:

```go
type ghIssue struct {
	Number      int       `json:"number"`
	Title       string    `json:"title"`
	Body        string    `json:"body"`
	State       string    `json:"state"`
	PullRequest *struct{} `json:"pull_request"` // set on a pull request: GitHub lists PRs as issues
	Labels      []struct {
		Name string `json:"name"`
	} `json:"labels"`
}
```

6. Replace `toIssue`, and add `key`:

```go
func (c *Client) toIssue(n ghIssue) scheduler.Issue {
	names := make([]string, len(n.Labels))
	for i, l := range n.Labels {
		names[i] = l.Name
	}
	class, dispatchLabeled := scheduler.ClassifyLabels(names, c.prefix, c.dispatchPrefix)
	return scheduler.Issue{
		ID: strconv.Itoa(n.Number), Identifier: c.key(n.Number), Title: n.Title,
		Description: n.Body, Class: class, DispatchLabeled: dispatchLabeled,
	}
}

// key is issue n's identifier: "owner/repo#N" for Jam, "#N" for at-cove.
func (c *Client) key(n int) string {
	if c.qualified {
		return ticketkey.GitHub(c.repo, n)
	}
	return "#" + strconv.Itoa(n)
}
```

7. In `search`, filter what the index returns before handing it back. Replace `return out.Items, nil` with:

```go
	// The search index lags: a just-closed issue can still match is:open,
	// and a pull request is an issue to GitHub. Neither is work to dispatch.
	items := out.Items[:0]
	for _, n := range out.Items {
		if n.PullRequest == nil && n.State != "closed" {
			items = append(items, n)
		}
	}
	return items, nil
```

8. Add `IssueByIdentifier` after `Comments`:

```go
// IssueByIdentifier resolves a ticket key ("owner/repo#N") to the issue
// number Transition, PostComment and Comments take. The key must name this
// Client's repo; GitHub owner/name are case-insensitive. A missing issue, or a
// key that names a pull request, is an error.
func (c *Client) IssueByIdentifier(ctx context.Context, identifier string) (string, error) {
	repo, n, err := ticketkey.ParseGitHub(identifier)
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(repo, c.repo) {
		return "", fmt.Errorf("githubissues: ticket %s is not on repo %s", identifier, c.repo)
	}
	num := strconv.Itoa(n)
	code, raw, err := c.do(ctx, http.MethodGet, api+"/repos/"+c.repo+"/issues/"+num, nil)
	if err != nil {
		return "", err
	}
	if code == http.StatusNotFound {
		return "", fmt.Errorf("githubissues: no issue %s", identifier)
	}
	if code != http.StatusOK {
		return "", fmt.Errorf("githubissues: get issue %s: http %d: %s", identifier, code, strings.TrimSpace(string(raw)))
	}
	var out ghIssue
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	if out.PullRequest != nil {
		return "", fmt.Errorf("githubissues: %s is a pull request, not an issue", identifier)
	}
	return num, nil
}
```

9. Update the package comment's first sentence: "...a scheduler.Tracker (at-cove, via New) and Jam's tracker.Tracker (via NewJam)..."

- [ ] **Step 4: Run the tests and verify they pass, and that at-cove is unchanged**

Run: `go test ./internal/dispatch/... ./internal/kit ./cmd/at-cove`
Expected: PASS. The existing `TestListReadyParsesClassAndIdentity` still expects `Identifier "#42"`, which proves `New` is unchanged for at-cove.

- [ ] **Step 5: Commit**

```bash
git add internal/kit/config.go internal/dispatch/scheduler/scheduler.go internal/dispatch/linear internal/dispatch/githubissues
git commit -m "feat(githubissues): Jam constructor with owner/repo#N keys, dispatch gate, IssueByIdentifier

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: GitHub tracker — `CommentFeed`, `Viewer`, rate limits, no token in errors

**Files:**
- Modify: `internal/dispatch/githubissues/githubissues.go`
- Create: `internal/dispatch/githubissues/feed_test.go`

**Interfaces:**
- Consumes: `tracker.FeedComment`, `tracker.RateLimitError`, `tracker.KindGitHub` (Task 1); `newJamClient`, `newJamClientToken` (Task 2, `jam_test.go`)
- Produces:
  - `func (c *Client) CommentFeed(ctx context.Context, since time.Time, limit int) ([]tracker.FeedComment, error)`
  - `func (c *Client) Viewer(ctx context.Context) (string, error)`
  - `var _ tracker.Tracker = (*Client)(nil)`

- [ ] **Step 1: Write the failing tests**

`internal/dispatch/githubissues/feed_test.go`:

```go
package githubissues

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/tracker"
)

func ghComment(id int64, issue string, login, typ, created, updated string) string {
	return fmt.Sprintf(`{"id":%d,"body":"b%d","created_at":%q,"updated_at":%q,
	 "html_url":"https://github.com/acme/board/%s#issuecomment-%d",
	 "issue_url":"https://api.github.com/repos/acme/board/issues/%s",
	 "user":{"login":%q,"id":42,"type":%q}}`, id, id, created, updated, issue, id, strings.TrimPrefix(strings.TrimPrefix(issue, "issues/"), "pull/"), login, typ)
}

func TestCommentFeedMapsAndFilters(t *testing.T) {
	since := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	var q map[string]string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/acme/board/issues/comments", func(w http.ResponseWriter, r *http.Request) {
		q = map[string]string{"since": r.URL.Query().Get("since"), "sort": r.URL.Query().Get("sort"), "direction": r.URL.Query().Get("direction")}
		io.WriteString(w, "["+strings.Join([]string{
			ghComment(1001, "issues/7", "brent", "User", "2026-10-26T12:00:00Z", "2026-10-26T12:00:00Z"), // exactly at since: kept
			ghComment(1002, "pull/8", "brent", "User", "2026-10-26T12:01:00Z", "2026-10-26T12:01:00Z"),   // a PR conversation: skipped
			ghComment(1003, "issues/7", "ci[bot]", "Bot", "2026-10-26T12:02:00Z", "2026-10-26T12:02:00Z"), // a bot: kept, flagged
			ghComment(1004, "issues/7", "brent", "User", "2026-10-26T11:00:00Z", "2026-10-26T12:03:00Z"),  // edited old comment: skipped
		}, ",")+"]")
	})
	got, err := newJamClient(t, mux).CommentFeed(context.Background(), since, 100)
	if err != nil {
		t.Fatalf("CommentFeed: %v", err)
	}
	if q["since"] != "2026-10-26T12:00:00Z" || q["sort"] != "created" || q["direction"] != "asc" {
		t.Fatalf("query = %v", q)
	}
	if len(got) != 2 || got[0].ID != "1001" || got[1].ID != "1003" {
		t.Fatalf("feed = %+v; want 1001, 1003", got)
	}
	c := got[0]
	if c.IssueIdentifier != "acme/board#7" || c.Author != "brent" || c.AuthorID != "42" || c.AuthorBot || c.ParentID != "" || !c.CreatedAt.Equal(since) {
		t.Fatalf("comment = %+v", c)
	}
	if !got[1].AuthorBot {
		t.Fatalf("bot comment = %+v; want AuthorBot", got[1])
	}
}

// Review Focus 2: the feed follows Link rel="next" (only on api.github.com),
// stops at limit, and never fetches past it.
func TestCommentFeedFollowsLinkNext(t *testing.T) {
	pages := 0
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/acme/board/issues/comments", func(w http.ResponseWriter, r *http.Request) {
		pages++
		switch r.URL.Query().Get("page") {
		case "", "1":
			w.Header().Set("Link", `<https://api.github.com/repos/acme/board/issues/comments?page=2&per_page=2>; rel="next", <https://api.github.com/repos/acme/board/issues/comments?page=2&per_page=2>; rel="last"`)
			io.WriteString(w, "["+ghComment(1, "issues/1", "a", "User", "2026-10-26T12:00:01Z", "2026-10-26T12:00:01Z")+","+
				ghComment(2, "issues/1", "a", "User", "2026-10-26T12:00:02Z", "2026-10-26T12:00:02Z")+"]")
		case "2":
			w.Header().Set("Link", `<https://evil.example/steal?page=3>; rel="next"`) // never followed: not the API host
			io.WriteString(w, "["+ghComment(3, "issues/1", "a", "User", "2026-10-26T12:00:03Z", "2026-10-26T12:00:03Z")+"]")
		default:
			t.Errorf("unexpected page %q", r.URL.Query().Get("page"))
			io.WriteString(w, "[]")
		}
	})
	c := newJamClient(t, mux)
	since := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	all, err := c.CommentFeed(context.Background(), since, 100)
	if err != nil || len(all) != 3 || pages != 2 {
		t.Fatalf("all = %d comments, %d pages, %v; want 3, 2", len(all), pages, err)
	}
	pages = 0
	two, err := c.CommentFeed(context.Background(), since, 2)
	if err != nil || len(two) != 2 || pages != 1 {
		t.Fatalf("limit 2 = %d comments, %d pages, %v; want 2, 1", len(two), pages, err)
	}
}

func TestCommentFeedRateLimited(t *testing.T) {
	reset := time.Now().Add(120 * time.Second).Unix()
	for name, tc := range map[string]struct {
		code    int
		headers map[string]string
		want    time.Duration // 0 = not a rate limit
	}{
		"403 Retry-After":         {403, map[string]string{"Retry-After": "60"}, 60 * time.Second},
		"429 Retry-After":         {429, map[string]string{"Retry-After": "5"}, 5 * time.Second},
		"403 primary limit reset": {403, map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": strconv.FormatInt(reset, 10)}, 120 * time.Second},
		"429 bare":                {429, nil, time.Minute},
		"403 permissions":         {403, nil, 0},
	} {
		t.Run(name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("GET /repos/acme/board/issues/comments", func(w http.ResponseWriter, r *http.Request) {
				for k, v := range tc.headers {
					w.Header().Set(k, v)
				}
				w.WriteHeader(tc.code)
				io.WriteString(w, `{"message":"API rate limit exceeded"}`)
			})
			_, err := newJamClient(t, mux).CommentFeed(context.Background(), time.Now(), 100)
			var rl *tracker.RateLimitError
			isRL := errors.As(err, &rl)
			switch {
			case err == nil:
				t.Fatal("want an error")
			case tc.want == 0 && isRL:
				t.Fatalf("a permissions 403 is not a rate limit: %v", err)
			case tc.want != 0 && (!isRL || rl.Service != tracker.KindGitHub || rl.RetryAfter > tc.want || rl.RetryAfter < tc.want-5*time.Second):
				t.Fatalf("err = %v; want RateLimitError ~%v", err, tc.want)
			}
		})
	}
}

func TestViewer(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /user", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"login":"jam-bot","id":1,"type":"User"}`)
	})
	if got, err := newJamClient(t, mux).Viewer(context.Background()); err != nil || got != "jam-bot" {
		t.Fatalf("Viewer = %q, %v", got, err)
	}
}

// An error body that echoes the request (a proxy, a misbehaving mirror)
// must not carry the token into a log line.
func TestErrorsNeverCarryTheToken(t *testing.T) {
	const secret = "ghp_s3cretTokenValue"
	echo := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"message":"bad credentials: `+r.Header.Get("Authorization")+`"}`)
	})
	c := newJamClientToken(t, echo, secret)
	ctx := context.Background()
	for name, call := range map[string]func() error{
		"ListReady":         func() error { _, err := c.ListReady(ctx); return err },
		"IssueByIdentifier": func() error { _, err := c.IssueByIdentifier(ctx, "acme/board#1"); return err },
		"PostComment":       func() error { return c.PostComment(ctx, "1", "x") },
		"Transition":        func() error { return c.Transition(ctx, "1", 0) },
		"CommentFeed":       func() error { _, err := c.CommentFeed(ctx, time.Now(), 10); return err },
		"Viewer":            func() error { _, err := c.Viewer(ctx); return err },
	} {
		if err := call(); err == nil || strings.Contains(err.Error(), secret) {
			t.Errorf("%s error = %v; want an error without the token", name, err)
		}
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/dispatch/githubissues -run 'CommentFeed|Viewer|NeverCarry'`
Expected: FAIL to build (`c.CommentFeed undefined`, `c.Viewer undefined`).

- [ ] **Step 3: Implement**

In `githubissues.go`, add `"github.com/aethons-tools/cove/internal/tracker"` to the imports.

1. Split `do` so that headers are visible, and redact the token from every error body:

```go
// send issues a REST call and returns the status, headers, body and any
// transport error: Bearer auth, GitHub media type.
func (c *Client) send(ctx context.Context, method, u string, payload []byte) (int, http.Header, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	raw := new(bytes.Buffer)
	_, _ = raw.ReadFrom(resp.Body)
	return resp.StatusCode, resp.Header, raw.Bytes(), nil
}

// do is send without the headers.
func (c *Client) do(ctx context.Context, method, u string, payload []byte) (int, []byte, error) {
	code, _, raw, err := c.send(ctx, method, u, payload)
	return code, raw, err
}

// bodyText is an error body for a message: trimmed, with the token
// redacted should a proxy have echoed the request.
func (c *Client) bodyText(raw []byte) string {
	return strings.ReplaceAll(strings.TrimSpace(string(raw)), c.token, "[redacted]")
}
```

(`bytes.NewReader(nil)` reads as empty, which preserves the old nil-payload branch. `http.NewRequestWithContext` sets `GetBody` for a `*bytes.Reader`, which is what lets a 307 replay the body.) Then replace every `strings.TrimSpace(string(raw))` in the file with `c.bodyText(raw)`:

Run: `sed -i 's/strings.TrimSpace(string(raw))/c.bodyText(raw)/g' internal/dispatch/githubissues/githubissues.go`

2. Recognise rate limits, in `getJSON` and in the feed:

```go
// rateLimit reads a GitHub rate-limit refusal: a 403 or 429 with Retry-After,
// or with X-RateLimit-Remaining: 0 (wait until X-RateLimit-Reset), or any 429
// (wait a minute). A 403 without those headers is a permissions error: nil.
func rateLimit(code int, h http.Header, now time.Time) error {
	if code != http.StatusForbidden && code != http.StatusTooManyRequests {
		return nil
	}
	if s, err := strconv.Atoi(h.Get("Retry-After")); err == nil && s >= 0 {
		return &tracker.RateLimitError{Service: tracker.KindGitHub, RetryAfter: time.Duration(s) * time.Second}
	}
	if h.Get("X-RateLimit-Remaining") == "0" {
		if reset, err := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			return &tracker.RateLimitError{Service: tracker.KindGitHub, RetryAfter: max(time.Unix(reset, 0).Sub(now), time.Second)}
		}
	}
	if code == http.StatusTooManyRequests {
		return &tracker.RateLimitError{Service: tracker.KindGitHub, RetryAfter: time.Minute}
	}
	return nil
}
```

Replace `getJSON`'s first lines so a rate limit surfaces as one:

```go
func (c *Client) getJSON(ctx context.Context, u string, out any) error {
	code, h, raw, err := c.send(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	if rl := rateLimit(code, h, time.Now()); rl != nil {
		return rl
	}
	if code != http.StatusOK {
		return fmt.Errorf("githubissues: GET %s: http %d: %s", u, code, c.bodyText(raw))
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}
```

3. The feed and the viewer, after `IssueByIdentifier`:

```go
// ghComment is the projection of a repo issue comment.
type ghComment struct {
	ID        int64  `json:"id"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
	HTMLURL   string `json:"html_url"`  // …/issues/N#… or …/pull/N#…
	IssueURL  string `json:"issue_url"` // …/repos/{r}/issues/N (also for a PR)
	User      struct {
		Login string `json:"login"`
		ID    int64  `json:"id"`
		Type  string `json:"type"` // "User" | "Bot" | "Organization"
	} `json:"user"`
}

// linkNext matches a Link header's rel="next" target.
var linkNext = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

// nextPage is the Link header's next page, if it is on the API host. A
// next link anywhere else is not followed (the token goes with it).
func nextPage(link string) string {
	m := linkNext.FindStringSubmatch(link)
	if m == nil || !strings.HasPrefix(m[1], api+"/") {
		return ""
	}
	return m[1]
}

// CommentFeed returns the repo's issue comments created at or after since,
// oldest first, at most limit (100 when limit <= 0). It calls
// GET /repos/{r}/issues/comments?since=&sort=created&direction=asc and follows
// Link rel="next". GitHub's since filters by update time, so an edited old
// comment comes back: one created before since is dropped. One created exactly
// at since is kept (the relay dedups by id). Comments on pull requests are
// skipped. A rate limit is a *tracker.RateLimitError.
func (c *Client) CommentFeed(ctx context.Context, since time.Time, limit int) ([]tracker.FeedComment, error) {
	if limit <= 0 {
		limit = 100
	}
	q := url.Values{"since": {since.UTC().Format(time.RFC3339)}, "sort": {"created"}, "direction": {"asc"}, "per_page": {"100"}}
	next := api + "/repos/" + c.repo + "/issues/comments?" + q.Encode()
	var out []tracker.FeedComment
	for next != "" && len(out) < limit {
		code, h, raw, err := c.send(ctx, http.MethodGet, next, nil)
		if err != nil {
			return nil, err
		}
		if rl := rateLimit(code, h, time.Now()); rl != nil {
			return nil, rl
		}
		if code != http.StatusOK {
			return nil, fmt.Errorf("githubissues: GET %s: http %d: %s", next, code, c.bodyText(raw))
		}
		var page []ghComment
		if err := json.Unmarshal(raw, &page); err != nil {
			return nil, err
		}
		for _, n := range page {
			if fc, ok := c.feedComment(n, since); ok && len(out) < limit {
				out = append(out, fc)
			}
		}
		next = nextPage(h.Get("Link"))
	}
	return out, nil
}

// feedComment maps a repo comment, or reports false for one the feed skips.
func (c *Client) feedComment(n ghComment, since time.Time) (tracker.FeedComment, bool) {
	if strings.Contains(n.HTMLURL, "/pull/") {
		return tracker.FeedComment{}, false // a pull request's conversation, not an issue's
	}
	at, err := time.Parse(time.RFC3339, n.CreatedAt)
	if err != nil || at.Before(since) {
		return tracker.FeedComment{}, false
	}
	num, err := strconv.Atoi(n.IssueURL[strings.LastIndex(n.IssueURL, "/")+1:])
	if err != nil || num < 1 {
		return tracker.FeedComment{}, false
	}
	return tracker.FeedComment{
		ID: strconv.FormatInt(n.ID, 10), Body: n.Body, CreatedAt: at,
		Author: n.User.Login, AuthorID: strconv.FormatInt(n.User.ID, 10), AuthorBot: n.User.Type == "Bot",
		IssueIdentifier: c.key(num),
	}, true
}

// Viewer returns the login the token authenticates as (GET /user) — the
// relay's self-filter. A GitHub App installation token cannot call it; the
// relay then relies on dropping Bot authors.
func (c *Client) Viewer(ctx context.Context) (string, error) {
	var out struct {
		Login string `json:"login"`
	}
	if err := c.getJSON(ctx, api+"/user", &out); err != nil {
		return "", err
	}
	return out.Login, nil
}
```

4. At the end of the file, next to the `scheduler.Tracker` assertion:

```go
// Jam's contract (via NewJam; at-cove's New satisfies it too, with "#N" keys).
var _ tracker.Tracker = (*Client)(nil)
```

- [ ] **Step 5: Run the package tests**

Run: `gofmt -l internal/dispatch && go test ./internal/dispatch/githubissues ./cmd/at-cove`
Expected: no gofmt output; PASS. The existing write-method tests still pass through `do`.

- [ ] **Step 6: Commit**

```bash
git add internal/dispatch/githubissues
git commit -m "feat(githubissues): comment feed, viewer, rate limits; tokens never in errors

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: Both trackers on the shared interface — fakes, conformance, Requisitioner end to end

**Files:**
- Create: `internal/tracker/trackertest/trackertest.go` (Token, Rewrite, Case, Both)
- Create: `internal/tracker/trackertest/github.go` (the fake GitHub)
- Create: `internal/tracker/trackertest/linear.go` (the fake Linear)
- Create: `internal/tracker/trackertest/conformance_test.go`
- Create: `internal/dispatcher/trackers_test.go`
- Modify: `internal/dispatcher/dispatcher.go:32` (doc comment only: "a subset of tracker.Tracker")

**Interfaces:**
- Consumes:
  - `githubissues.NewJam` (Task 2)
  - `linear.New`
  - `tracker.Tracker` (Task 1)
  - `dispatcher.New` and the `tick` method
  - The dispatcher test fakes `fakeRaiser`, `fakeRegistry`, `fakeAdmitter{grant: true}` (`internal/dispatcher/dispatcher_test.go`)
- Produces (Task 6 uses them):
  - `trackertest.Token`
  - `func trackertest.Rewrite(srv *httptest.Server) *http.Client`
  - `trackertest.NewGitHub(t, repo, login) *GitHub`, with:
    - `.HTTPClient()`, `.Client(t)`, `.AddIssue(GHIssue)`, `.AddComment(GHComment)`
    - `.State(n) string`, `.CommentsOn(n) []GHComment`
  - `trackertest.NewLinear(t, team, viewer) *Linear`, with:
    - `.HTTPClient()`, `.Client(t)`, `.AddIssue(LinIssue)`, `.AddComment(LinComment)`
    - `.IssueState(id) string`, `.CommentsOn(id) []LinComment`
  - `trackertest.GitHubStates() kit.StateMap`, `trackertest.LinearStates() kit.StateMap`
  - `trackertest.Both(t) []Case`

  `Case` has these fields:

  | Field | Type |
  |---|---|
  | `Kind`, `Key`, `Self` | `string` |
  | `Tracker` | `tracker.Tracker` |
  | `State` | `func() string` |
  | `Posted` | `func() []string` |
  | `Comment` | `func(author, body string, at time.Time)` |
  | `GitHub` | `*GitHub` |
  | `Linear` | `*Linear` |

- [ ] **Step 1: Write the failing conformance test**

`internal/tracker/trackertest/conformance_test.go`:

```go
package trackertest

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/dispatch/scheduler"
	"github.com/aethons-tools/cove/internal/tracker"
)

// TestTrackersShareTheInterface drives Linear and GitHub through the same
// tracker.Tracker calls Jam makes, against their httptest fakes.
func TestTrackersShareTheInterface(t *testing.T) {
	ctx := context.Background()
	for _, c := range Both(t) {
		t.Run(c.Kind, func(t *testing.T) {
			ready, err := c.Tracker.ListReady(ctx)
			if err != nil || len(ready) != 2 { // GitHub's PR #3 is never listed
				t.Fatalf("ListReady = %+v, %v; want the two open ready issues", ready, err)
			}
			i := slices.IndexFunc(ready, func(is scheduler.Issue) bool { return is.Identifier == c.Key })
			if i < 0 || !ready[i].DispatchLabeled || ready[i].Class != "impl" || ready[i].Title != "Fix the build" {
				t.Fatalf("ready = %+v; want %s dispatch-labeled, class impl", ready, c.Key)
			}
			if other := ready[1-i]; other.DispatchLabeled {
				t.Fatalf("unlabeled issue = %+v", other)
			}
			id, err := c.Tracker.IssueByIdentifier(ctx, c.Key)
			if err != nil || id != ready[i].ID {
				t.Fatalf("IssueByIdentifier(%s) = %q, %v; want %q", c.Key, id, err, ready[i].ID)
			}
			for _, step := range []struct {
				role scheduler.Role
				want string
			}{{scheduler.RoleInProgress, "in-progress"}, {scheduler.RoleInReview, "in-review"}, {scheduler.RoleDone, "done"}} {
				if err := c.Tracker.Transition(ctx, id, step.role); err != nil || c.State() != step.want {
					t.Fatalf("Transition → %s: state %q, %v", step.want, c.State(), err)
				}
			}
			at := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
			c.Comment("brent", "looks good", at)
			if err := c.Tracker.PostComment(ctx, id, "on it"); err != nil || !slices.Contains(c.Posted(), "on it") {
				t.Fatalf("PostComment: posted %q, %v", c.Posted(), err)
			}
			cs, err := c.Tracker.Comments(ctx, id)
			if err != nil || !slices.Contains(cs, scheduler.Comment{Author: "brent", Body: "looks good"}) {
				t.Fatalf("Comments = %+v, %v", cs, err)
			}
			feed, err := c.Tracker.CommentFeed(ctx, at.Add(-time.Second), 100)
			if err != nil {
				t.Fatalf("CommentFeed: %v", err)
			}
			j := slices.IndexFunc(feed, func(f tracker.FeedComment) bool { return f.Body == "looks good" })
			if j < 0 || feed[j].IssueIdentifier != c.Key || feed[j].Author != "brent" || feed[j].AuthorID == "" || !feed[j].CreatedAt.Equal(at) {
				t.Fatalf("feed = %+v", feed)
			}
			if later, _ := c.Tracker.CommentFeed(ctx, at.Add(time.Second), 100); slices.ContainsFunc(later, func(f tracker.FeedComment) bool { return f.Body == "looks good" }) {
				t.Fatal("CommentFeed returned a comment created before since")
			}
			if self, err := c.Tracker.Viewer(ctx); err != nil || self != c.Self {
				t.Fatalf("Viewer = %q, %v; want %q", self, err, c.Self)
			}
		})
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/tracker/trackertest`
Expected: FAIL to build (`undefined: Both`).

- [ ] **Step 3: Implement the fakes**

`internal/tracker/trackertest/trackertest.go`:

```go
// Package trackertest holds hermetic httptest fakes of the GitHub REST and
// Linear GraphQL APIs. They cover enough of each for Jam's tracker.Tracker,
// plus Both, which seeds one Case per tracker for shared-interface tests.
// Tests only.
package trackertest

import (
	"encoding/json"
	"hash/crc32"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/tracker"
)

// Token is the credential both fakes accept.
const Token = "trackertest-token"

// api is the GitHub API root the client is hardwired to.
const api = "https://api.github.com"

// Rewrite is an *http.Client that sends every request to srv, whatever its
// URL's host — so a client with a fixed API root reaches the fake. The
// client resolves redirects and Link URLs against the real host first, and
// this transport rewrites them in turn.
func Rewrite(srv *httptest.Server) *http.Client {
	u, _ := url.Parse(srv.URL)
	return &http.Client{Transport: rewrite{u}}
}

type rewrite struct{ to *url.URL }

func (r rewrite) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme, req.URL.Host, req.Host = r.to.Scheme, r.to.Host, r.to.Host
	return http.DefaultTransport.RoundTrip(req)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// Case is one tracker under a shared-interface test.
type Case struct {
	Kind    string          // tracker.KindLinear | tracker.KindGitHub
	Tracker tracker.Tracker // the real client, against the fake
	Key     string          // ticket key of the seeded dispatch-labeled ready issue
	Self    string          // what Viewer returns
	State   func() string   // the seeded issue's role: "ready", "in-progress", …, "done"
	Posted  func() []string // bodies the client posted on the seeded issue
	// Comment adds a person's comment on the seeded issue, created at at.
	Comment func(author, body string, at time.Time)
	GitHub  *GitHub // set for the github case
	Linear  *Linear // set for the linear case
}

// Both seeds and returns a Linear case (team ACME, viewer "Jam") and a
// GitHub case (repo acme/board, login "jam-bot"). Each holds:
//   - a ready issue tagged dispatch:go and class:impl, "Fix the build" (Key)
//   - a ready issue with no dispatch label, "Someday"
//   - on GitHub only, a pull request #3 labelled like the first, which must
//     never surface
func Both(t testing.TB) []Case {
	t.Helper()
	lin := NewLinear(t, "ACME", "Jam")
	lin.AddIssue(LinIssue{ID: "lin-1", Identifier: "ACME-1", Title: "Fix the build", Description: "It is red.", State: "Ready", Labels: []string{"dispatch:go", "class:impl"}})
	lin.AddIssue(LinIssue{ID: "lin-2", Identifier: "ACME-2", Title: "Someday", State: "Ready"})
	gh := NewGitHub(t, "acme/board", "jam-bot")
	gh.AddIssue(GHIssue{Number: 1, Title: "Fix the build", Body: "It is red.", Labels: []string{"status:ready", "dispatch:go", "class:impl"}})
	gh.AddIssue(GHIssue{Number: 2, Title: "Someday", Labels: []string{"status:ready"}})
	gh.AddIssue(GHIssue{Number: 3, Title: "A pull request", Labels: []string{"status:ready", "dispatch:go"}, PullRequest: true})
	linRole := map[string]string{"Ready": "ready", "In Progress": "in-progress", "In Review": "in-review", "Done": "done", "Needs Input": "needs-input", "Blocked": "blocked"}
	return []Case{{
		Kind: tracker.KindLinear, Tracker: lin.Client(t), Key: "ACME-1", Self: "Jam", Linear: lin,
		State: func() string { return linRole[lin.IssueState("lin-1")] },
		Posted: func() []string {
			var out []string
			for _, c := range lin.CommentsOn("lin-1") {
				if c.Author == lin.Viewer {
					out = append(out, c.Body)
				}
			}
			return out
		},
		Comment: func(author, body string, at time.Time) {
			lin.AddComment(LinComment{IssueID: "lin-1", Body: body, Author: author, AuthorID: "u-" + author, CreatedAt: at})
		},
	}, {
		Kind: tracker.KindGitHub, Tracker: gh.Client(t), Key: "acme/board#1", Self: "jam-bot", GitHub: gh,
		State: func() string { return gh.State(1) },
		Posted: func() []string {
			var out []string
			for _, c := range gh.CommentsOn(1) {
				if c.Login == gh.Login {
					out = append(out, c.Body)
				}
			}
			return out
		},
		Comment: func(author, body string, at time.Time) {
			gh.AddComment(GHComment{Issue: 1, Body: body, Login: author, UserID: int64(crc32.ChecksumIEEE([]byte(author))), CreatedAt: at})
		},
	}}
}

// trimStatus is a GitHub status label's role ("status:in-review" → "in-review").
func trimStatus(label string) (string, bool) { return strings.CutPrefix(label, "status:") }
```

`internal/tracker/trackertest/github.go`:

```go
package trackertest

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/dispatch/githubissues"
	"github.com/aethons-tools/cove/internal/kit"
)

// GHIssue is one issue (or pull request) in the fake repo.
type GHIssue struct {
	Number      int
	Title, Body string
	Closed      bool
	Labels      []string
	PullRequest bool
}

// GHComment is one comment in the fake repo.
type GHComment struct {
	ID        int64 // 0: assigned
	Issue     int
	Body      string
	Login     string
	UserID    int64
	Bot       bool // user type "Bot"
	OnPull    bool // a pull request's conversation (html_url …/pull/N)
	CreatedAt time.Time
}

// GitHub is a fake GitHub REST API for one repo. It covers search, issues,
// labels, comments, the repo comment feed (paginated) and /user.
type GitHub struct {
	Repo, Login string
	PerPage     int // feed page size override (0: the request's per_page)

	srv      *httptest.Server
	mu       sync.Mutex
	issues   map[int]*GHIssue
	comments []GHComment
	seq      int64
}

// GitHubStates is the status-label map the fake's client is built with.
func GitHubStates() kit.StateMap {
	return kit.StateMap{Ready: "status:ready", InProgress: "status:in-progress", InReview: "status:in-review",
		NeedsInput: "status:needs-input", Blocked: "status:blocked"}
}

func NewGitHub(t testing.TB, repo, login string) *GitHub {
	g := &GitHub{Repo: repo, Login: login, issues: map[int]*GHIssue{}, seq: 1000}
	g.srv = httptest.NewServer(http.HandlerFunc(g.serve))
	t.Cleanup(g.srv.Close)
	return g
}

// HTTPClient reaches the fake in place of api.github.com.
func (g *GitHub) HTTPClient() *http.Client { return Rewrite(g.srv) }

// Client is Jam's GitHub client against the fake.
func (g *GitHub) Client(t testing.TB) *githubissues.Client {
	t.Helper()
	c, err := githubissues.NewJam(kit.GitHubTracker{Repo: g.Repo, ClassLabelPrefix: "class:", DispatchLabelPrefix: "dispatch:", States: GitHubStates()}, Token, g.HTTPClient())
	if err != nil {
		t.Fatalf("githubissues.NewJam: %v", err)
	}
	return c
}

func (g *GitHub) AddIssue(i GHIssue) {
	g.mu.Lock()
	defer g.mu.Unlock()
	i.Labels = slices.Clone(i.Labels)
	g.issues[i.Number] = &i
}

func (g *GitHub) AddComment(c GHComment) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if c.ID == 0 {
		g.seq++
		c.ID = g.seq
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now().UTC().Truncate(time.Second)
	}
	g.comments = append(g.comments, c)
}

// State is issue n's role: "done" when closed, else its status label's.
func (g *GitHub) State(n int) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	i := g.issues[n]
	if i.Closed {
		return "done"
	}
	for _, l := range i.Labels {
		if role, ok := trimStatus(l); ok {
			return role
		}
	}
	return ""
}

func (g *GitHub) CommentsOn(n int) []GHComment {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []GHComment
	for _, c := range g.comments {
		if c.Issue == n {
			out = append(out, c)
		}
	}
	return out
}

func msg(s string) map[string]string { return map[string]string{"message": s} }

func (g *GitHub) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+Token {
		writeJSON(w, http.StatusUnauthorized, msg("Bad credentials"))
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	prefix := "/repos/" + g.Repo
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/user":
		writeJSON(w, http.StatusOK, map[string]any{"login": g.Login, "id": 1, "type": "User"})
	case r.Method == http.MethodGet && r.URL.Path == "/search/issues":
		g.search(w, r.URL.Query().Get("q"))
	case r.Method == http.MethodGet && r.URL.Path == prefix+"/issues/comments":
		g.feed(w, r)
	case strings.HasPrefix(r.URL.Path, prefix+"/issues/"):
		g.issue(w, r, strings.Split(strings.TrimPrefix(r.URL.Path, prefix+"/issues/"), "/"))
	default:
		writeJSON(w, http.StatusNotFound, msg("Not Found"))
	}
}

var searchLabel = regexp.MustCompile(`label:"([^"]*)"`)

// search honours repo:, is:open, is:issue and one quoted label:.
func (g *GitHub) search(w http.ResponseWriter, q string) {
	m := searchLabel.FindStringSubmatch(q)
	items := []map[string]any{}
	for _, n := range slices.Sorted(maps.Keys(g.issues)) {
		i := g.issues[n]
		switch {
		case !strings.Contains(q, "repo:"+g.Repo), m == nil, !slices.Contains(i.Labels, m[1]):
		case strings.Contains(q, "is:open") && i.Closed:
		case strings.Contains(q, "is:issue") && i.PullRequest:
		default:
			items = append(items, g.issueJSON(i))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (g *GitHub) issueJSON(i *GHIssue) map[string]any {
	labels := []map[string]string{}
	for _, l := range i.Labels {
		labels = append(labels, map[string]string{"name": l})
	}
	state := "open"
	if i.Closed {
		state = "closed"
	}
	out := map[string]any{"number": i.Number, "title": i.Title, "body": i.Body, "state": state, "labels": labels}
	if i.PullRequest {
		out["pull_request"] = map[string]string{"url": api + "/repos/" + g.Repo + "/pulls/" + strconv.Itoa(i.Number)}
	}
	return out
}

func (g *GitHub) commentJSON(c GHComment) map[string]any {
	kind, typ := "issues", "User"
	if c.OnPull {
		kind = "pull"
	}
	if c.Bot {
		typ = "Bot"
	}
	n, id := strconv.Itoa(c.Issue), strconv.FormatInt(c.ID, 10)
	at := c.CreatedAt.UTC().Format(time.RFC3339)
	return map[string]any{"id": c.ID, "body": c.Body, "created_at": at, "updated_at": at,
		"html_url":  "https://github.com/" + g.Repo + "/" + kind + "/" + n + "#issuecomment-" + id,
		"issue_url": api + "/repos/" + g.Repo + "/issues/" + n,
		"user":      map[string]any{"login": c.Login, "id": c.UserID, "type": typ}}
}

func (g *GitHub) issue(w http.ResponseWriter, r *http.Request, seg []string) {
	n, err := strconv.Atoi(seg[0])
	i, ok := g.issues[n]
	if err != nil || !ok {
		writeJSON(w, http.StatusNotFound, msg("Not Found"))
		return
	}
	switch {
	case len(seg) == 1 && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, g.issueJSON(i))
	case len(seg) == 1 && r.Method == http.MethodPatch:
		var b struct {
			State string `json:"state"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		i.Closed = b.State == "closed"
		writeJSON(w, http.StatusOK, g.issueJSON(i))
	case len(seg) == 2 && seg[1] == "labels" && r.Method == http.MethodPost:
		var b struct {
			Labels []string `json:"labels"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		for _, l := range b.Labels {
			if !slices.Contains(i.Labels, l) {
				i.Labels = append(i.Labels, l)
			}
		}
		writeJSON(w, http.StatusOK, g.issueJSON(i)["labels"])
	case len(seg) == 3 && seg[1] == "labels" && r.Method == http.MethodDelete:
		k := slices.Index(i.Labels, seg[2])
		if k < 0 {
			writeJSON(w, http.StatusNotFound, msg("Label does not exist"))
			return
		}
		i.Labels = slices.Delete(i.Labels, k, k+1)
		writeJSON(w, http.StatusOK, g.issueJSON(i)["labels"])
	case len(seg) == 2 && seg[1] == "comments" && r.Method == http.MethodPost:
		var b struct {
			Body string `json:"body"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		g.seq++
		c := GHComment{ID: g.seq, Issue: n, Body: b.Body, Login: g.Login, UserID: 1, CreatedAt: time.Now().UTC().Truncate(time.Second)}
		g.comments = append(g.comments, c)
		writeJSON(w, http.StatusCreated, g.commentJSON(c))
	case len(seg) == 2 && seg[1] == "comments" && r.Method == http.MethodGet:
		out := []map[string]any{}
		for _, c := range g.comments {
			if c.Issue == n {
				out = append(out, g.commentJSON(c))
			}
		}
		writeJSON(w, http.StatusOK, out)
	default:
		writeJSON(w, http.StatusNotFound, msg("Not Found"))
	}
}

// feed serves GET /repos/{r}/issues/comments: created at or after since,
// oldest first, paged with an absolute api.github.com Link rel="next".
func (g *GitHub) feed(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	since, _ := time.Parse(time.RFC3339, q.Get("since"))
	per, _ := strconv.Atoi(q.Get("per_page"))
	if g.PerPage > 0 {
		per = g.PerPage
	}
	if per <= 0 {
		per = 30
	}
	page, _ := strconv.Atoi(q.Get("page"))
	page = max(page, 1)
	var all []GHComment
	for _, c := range g.comments {
		if !c.CreatedAt.Before(since) {
			all = append(all, c)
		}
	}
	slices.SortStableFunc(all, func(a, b GHComment) int { return a.CreatedAt.Compare(b.CreatedAt) })
	lo := min((page-1)*per, len(all))
	hi := min(lo+per, len(all))
	if hi < len(all) {
		q.Set("page", strconv.Itoa(page+1))
		w.Header().Set("Link", "<"+api+r.URL.Path+"?"+q.Encode()+`>; rel="next"`)
	}
	out := []map[string]any{}
	for _, c := range all[lo:hi] {
		out = append(out, g.commentJSON(c))
	}
	writeJSON(w, http.StatusOK, out)
}
```

`internal/tracker/trackertest/linear.go`:

```go
package trackertest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/dispatch/linear"
	"github.com/aethons-tools/cove/internal/kit"
)

// LinIssue is one issue on the fake team.
type LinIssue struct {
	ID, Identifier, Title, Description string
	State                              string // workflow state name (LinearStates)
	Labels                             []string
}

// LinComment is one comment on the fake team.
type LinComment struct {
	ID, IssueID, Body, Author, AuthorID string // ID "" : assigned
	CreatedAt                           time.Time
}

// Linear is a fake Linear GraphQL API for one team. It answers the queries
// *linear.Client sends, matched by distinctive substrings: workflow states,
// the ready list, blocker lookup, issue lookup, updates, comments, the team
// comment feed and the viewer.
type Linear struct {
	Team, Viewer string

	srv      *httptest.Server
	mu       sync.Mutex
	issues   []*LinIssue
	comments []LinComment
	seq      int
}

var linearStateType = map[string]string{"Ready": "unstarted", "In Progress": "started", "In Review": "started",
	"Done": "completed", "Needs Input": "unstarted", "Blocked": "backlog"}

// LinearStates is the state map the fake's client is built with.
func LinearStates() kit.StateMap {
	return kit.StateMap{Ready: "Ready", InProgress: "In Progress", InReview: "In Review", Done: "Done", NeedsInput: "Needs Input", Blocked: "Blocked"}
}

func NewLinear(t testing.TB, team, viewer string) *Linear {
	l := &Linear{Team: team, Viewer: viewer}
	l.srv = httptest.NewServer(http.HandlerFunc(l.serve))
	t.Cleanup(l.srv.Close)
	return l
}

// HTTPClient reaches the fake in place of api.linear.app.
func (l *Linear) HTTPClient() *http.Client { return Rewrite(l.srv) }

// Client is Jam's Linear client against the fake.
func (l *Linear) Client(t testing.TB) *linear.Client {
	t.Helper()
	c, err := linear.New(kit.Config{Tracker: &kit.Tracker{Linear: &kit.LinearTracker{
		Team: l.Team, ClassLabelPrefix: "class:", DispatchLabelPrefix: "dispatch:", States: LinearStates()}}}, Token, l.HTTPClient())
	if err != nil {
		t.Fatalf("linear.New: %v", err)
	}
	return c
}

func (l *Linear) AddIssue(i LinIssue) {
	l.mu.Lock()
	defer l.mu.Unlock()
	i.Labels = slices.Clone(i.Labels)
	l.issues = append(l.issues, &i)
}

func (l *Linear) AddComment(c LinComment) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.addComment(c)
}

func (l *Linear) addComment(c LinComment) {
	if c.ID == "" {
		l.seq++
		c.ID = fmt.Sprintf("c-%d", l.seq)
	}
	l.comments = append(l.comments, c)
}

func (l *Linear) IssueState(id string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if i := l.find(id); i != nil {
		return i.State
	}
	return ""
}

func (l *Linear) CommentsOn(id string) []LinComment {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []LinComment
	for _, c := range l.comments {
		if c.IssueID == id {
			out = append(out, c)
		}
	}
	return out
}

// find looks an issue up by id or identifier (Linear's issue(id:) takes both).
func (l *Linear) find(ref string) *LinIssue {
	for _, i := range l.issues {
		if i.ID == ref || i.Identifier == ref {
			return i
		}
	}
	return nil
}

func gqlError(w http.ResponseWriter, code int, m string) {
	writeJSON(w, code, map[string]any{"errors": []map[string]string{{"message": m}}})
}

func (l *Linear) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != Token {
		gqlError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	var req struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		gqlError(w, http.StatusBadRequest, "bad request")
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	q := req.Query
	str := func(k string) string { s, _ := req.Variables[k].(string); return s }
	var data any
	switch {
	case strings.Contains(q, "workflowStates"):
		nodes := []map[string]string{}
		for name, typ := range linearStateType {
			nodes = append(nodes, map[string]string{"id": "st-" + name, "name": name, "type": typ})
		}
		data = map[string]any{"workflowStates": map[string]any{"nodes": nodes}}
	case strings.Contains(q, "issueUpdate"):
		i := l.find(str("id"))
		name, ok := strings.CutPrefix(str("stateId"), "st-")
		if ok = ok && i != nil; ok {
			i.State = name
		}
		data = map[string]any{"issueUpdate": map[string]bool{"success": ok}}
	case strings.Contains(q, "commentCreate"):
		i := l.find(str("issueId"))
		if i != nil {
			l.addComment(LinComment{IssueID: i.ID, Body: str("body"), Author: l.Viewer, AuthorID: "u-self", CreatedAt: time.Now().UTC()})
		}
		data = map[string]any{"commentCreate": map[string]bool{"success": i != nil}}
	case strings.Contains(q, "viewer"):
		data = map[string]any{"viewer": map[string]string{"displayName": l.Viewer}}
	case strings.Contains(q, "comments(filter"):
		since, _ := time.Parse(time.RFC3339Nano, str("since"))
		first, _ := req.Variables["first"].(float64)
		cs := slices.Clone(l.comments)
		slices.SortStableFunc(cs, func(a, b LinComment) int { return a.CreatedAt.Compare(b.CreatedAt) })
		nodes := []map[string]any{}
		for _, c := range cs {
			if !c.CreatedAt.After(since) || len(nodes) >= int(first) {
				continue
			}
			nodes = append(nodes, map[string]any{"id": c.ID, "body": c.Body, "createdAt": c.CreatedAt.UTC().Format(time.RFC3339Nano),
				"user":  map[string]string{"id": c.AuthorID, "displayName": c.Author},
				"issue": map[string]string{"identifier": l.find(c.IssueID).Identifier}, "parent": nil})
		}
		data = map[string]any{"comments": map[string]any{"nodes": nodes}}
	case strings.Contains(q, "issue(id:$id){id}"):
		if i := l.find(str("id")); i != nil {
			data = map[string]any{"issue": map[string]string{"id": i.ID}}
		} else {
			data = map[string]any{"issue": nil}
		}
	case strings.Contains(q, "issue(id:$id){comments"):
		nodes := []map[string]any{}
		for _, c := range l.comments {
			if c.IssueID == str("id") {
				nodes = append(nodes, map[string]any{"body": c.Body, "user": map[string]string{"displayName": c.Author}})
			}
		}
		data = map[string]any{"issue": map[string]any{"comments": map[string]any{"nodes": nodes}}}
	case strings.Contains(q, "issues(filter:{id:{in"):
		data = map[string]any{"issues": map[string]any{"nodes": []any{}}} // fake issues have no blockers
	case strings.Contains(q, "issues(filter:{team"):
		nodes := []map[string]any{}
		for _, i := range l.issues {
			if i.State != str("state") {
				continue
			}
			labels := []map[string]string{}
			for _, n := range i.Labels {
				labels = append(labels, map[string]string{"name": n})
			}
			nodes = append(nodes, map[string]any{"id": i.ID, "identifier": i.Identifier, "title": i.Title, "description": i.Description,
				"labels": map[string]any{"nodes": labels}, "inverseRelations": map[string]any{"nodes": []any{}}})
		}
		data = map[string]any{"issues": map[string]any{"nodes": nodes}}
	default:
		gqlError(w, http.StatusOK, "trackertest: unsupported query")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
}
```

- [ ] **Step 4: Run the conformance test**

Run: `gofmt -l internal/tracker && go test ./internal/tracker/...`
Expected: no gofmt output; PASS for both `TestTrackersShareTheInterface/linear` and `/github`.

- [ ] **Step 5: Write the Requisitioner end-to-end test**

`internal/dispatcher/trackers_test.go`:

```go
package dispatcher

import (
	"context"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/tracker/trackertest"
)

// TestRequisitionerOnBothTrackers runs one real poll pass of the
// Requisitioner against each tracker's fake, through the shared interface:
// only the dispatch-labeled issue is claimed and raised, keyed by its
// ticket key.
func TestRequisitionerOnBothTrackers(t *testing.T) {
	for _, c := range trackertest.Both(t) {
		t.Run(c.Kind, func(t *testing.T) {
			raiser := &fakeRaiser{}
			d := New(c.Tracker, raiser, &fakeRegistry{}, &fakeAdmitter{grant: true}, Config{Role: "worker", Project: "acme"}, nil)
			d.tick(context.Background())
			if len(raiser.specs) != 1 {
				t.Fatalf("raised %d; want 1: %+v", len(raiser.specs), raiser.specs)
			}
			s := raiser.specs[0]
			if s.Unit != c.Key || s.Mission != "Fix the build" || !strings.Contains(s.Prompt, c.Key) || !strings.Contains(s.Prompt, "It is red.") {
				t.Fatalf("spec = %+v", s)
			}
			if got := c.State(); got != "in-progress" {
				t.Fatalf("claimed issue state = %q; want in-progress", got)
			}
		})
	}
}
```

Change the `Tracker` doc comment in `internal/dispatcher/dispatcher.go:32` to: `// Tracker is the subset of tracker.Tracker the Requisitioner needs (satisfied by *linear.Client and *githubissues.Client).`

- [ ] **Step 6: Run it**

Run: `go test ./internal/dispatcher`
Expected: PASS. The Requisitioner needs no code change for GitHub; this test pins that.

- [ ] **Step 7: Commit**

```bash
git add internal/tracker/trackertest internal/dispatcher
git commit -m "test(tracker): Linear and GitHub fakes; both trackers on the shared interface and the Requisitioner

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: Ticket keys and the `github` connection inside Jam

**Files:**
- Modify: `internal/jam/registry.go:43-44` (`ConnectionKinds` gains `github`; add `TrackerKinds`, `TrackerConnection`)
- Modify: `internal/jam/decide.go:102-118,175` (`globMatch`; `ListTargets` uses `TrackerConnection`)
- Modify: `internal/jam/intercom_post.go:333-349,722-737` (`ticketChannel` validates GitHub keys on create; `resolveTicket` splits GitHub keys)
- Modify: `internal/jam/rooms.go:25-45` and `internal/jam/rooms_admin.go:63-76` (a GitHub ref must be a ticket key)
- Modify: `internal/jam/people.go:61-63` (a member's handle comes from the linear account, else the github one)
- Create: `internal/jam/github_tickets_test.go`
- Modify: `docs/usage/jam/comms-addressing.md`

**Interfaces:**
- Consumes: `ticketkey.IsGitHub`, `ticketkey.ErrGitHub` (Task 1)
- Produces:
  - `jam.ConnectionKinds = []string{"linear", "discord", "github"}`
  - `jam.TrackerKinds = []string{"linear", "github"}`
  - `func jam.TrackerConnection(store Store) (Connection, bool)`
  - A ticket channel is keyed `"<connection id>/owner/repo#<n>"`, labelled with the key, and bound to `Ref` = the key.
  - Task 6 relies on `ticket:<key>` and `ticket:<connection>/<key>` resolving for GitHub keys.

- [ ] **Step 1: Write the failing tests**

`internal/jam/github_tickets_test.go`:

```go
package jam

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/ticketkey"
)

// newGitHubICFixture is newICFixture with its tracker on a github
// connection "gh-acme" and the ticket session on acme/board#7.
func newGitHubICFixture(t *testing.T, addressing ...string) *icFixture {
	t.Helper()
	f := newICFixture(t, addressing...)
	gh, err := f.store.CreateConnection(Connection{Kind: "github", Name: "gh-acme"})
	if err != nil {
		t.Fatal(err)
	}
	f.tracker = gh
	f.ticket.Unit = "acme/board#7"
	if err := f.store.PutInstance(f.ticket); err != nil {
		t.Fatal(err)
	}
	f.ic = NewIntercom(f.store, func() (ident.ID, bool) { return f.tracker.ID, true }, f.log, func() int64 { return f.tail }, nil)
	return f
}

func TestGitHubTicketChannel(t *testing.T) {
	f := newGitHubICFixture(t)
	if err := f.ic.SetUp(f.ticket); err != nil {
		t.Fatalf("SetUp: %v", err)
	}
	ch, ok := f.store.ChannelByKey(f.project.ID, SourceTicket, string(f.tracker.ID)+"/acme/board#7")
	if !ok || ch.Label != "acme/board#7" || len(ch.Bindings) != 1 || ch.Bindings[0] != (Binding{ConnectionID: f.tracker.ID, Ref: "acme/board#7", Mode: BindBoth}) {
		t.Fatalf("ticket channel = %+v, %v", ch, ok)
	}
	bad := f.ticket
	bad.ActorID, bad.Unit = string(ident.New(ident.Session)), "#7"
	if err := f.ic.SetUp(bad); !errors.Is(err, ticketkey.ErrGitHub) {
		t.Fatalf("SetUp(unit #7 on a github tracker) = %v; want ErrGitHub", err)
	}
}

func TestPlanGitHubTicketTargets(t *testing.T) {
	f := newGitHubICFixture(t, "ticket:*")
	if err := f.ic.SetUp(f.ticket); err != nil {
		t.Fatal(err)
	}
	own := f.mustPlan(f.ticket, "ticket:acme/board#7")
	for _, addr := range []string{"ticket:acme/board#7", "ticket:gh-acme/acme/board#7", "ticket:" + string(f.tracker.ID) + "/acme/board#7"} {
		if got := f.mustPlan(f.standing, addr); got.Channel.ID != own.Channel.ID {
			t.Fatalf("%s → %s; want %s", addr, got.Channel.ID, own.Channel.ID)
		}
	}
	if _, err := f.plan(f.standing, "ticket:acme/board#9"); !errors.Is(err, ErrSendUnresolved) {
		t.Fatalf("a ticket with no channel: %v", err)
	}
	_, err := f.plan(f.standing, "ticket:acme/board#x")
	if !errors.Is(err, ErrSendUnresolved) || !strings.Contains(err.Error(), "github ticket key must be owner/repo#<n>") {
		t.Fatalf("malformed key: %v", err)
	}

	repo := newGitHubICFixture(t, "ticket:acme/*")
	if err := repo.ic.SetUp(repo.ticket); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.plan(repo.standing, "ticket:acme/board#7"); err != nil {
		t.Fatalf("ticket:acme/* reaches acme/board#7: %v", err)
	}
	other := newGitHubICFixture(t, "ticket:other/*")
	if err := other.ic.SetUp(other.ticket); err != nil {
		t.Fatal(err)
	}
	if _, err := other.plan(other.standing, "ticket:acme/board#7"); !errors.Is(err, ErrSendDenied) {
		t.Fatalf("ticket:other/* must not reach acme/board#7: %v", err)
	}
}

func TestGlobMatchGitHubTickets(t *testing.T) {
	for _, c := range []struct {
		glob, form string
		want       bool
	}{
		{"ticket:*", "ticket:acme/board#7", true},
		{"*", "ticket:acme/board#7", true},
		{"ticket:acme/*", "ticket:acme/board#7", true},
		{"ticket:acme/board#*", "ticket:acme/board#7", true},
		{"ticket:gh-acme/*", "ticket:gh-acme/acme/board#7", true},
		{"ticket:other/*", "ticket:acme/board#7", false},
		{"user:*", "ticket:acme/board#7", false},
		// Linear globs keep path.Match semantics exactly:
		{"ticket:*", "ticket:ACME-7", true},
		{"ticket:lin*", "ticket:linear/ACME-7", false},
	} {
		if got := globMatch(c.glob, c.form); got != c.want {
			t.Errorf("globMatch(%q, %q) = %v; want %v", c.glob, c.form, got, c.want)
		}
	}
}

func TestListTargetsGitHubTicketSessions(t *testing.T) {
	for _, c := range []struct {
		addressing []string
		listed     bool
	}{
		{[]string{"session:*"}, false}, // another's ticket session needs ticket: addressing too
		{[]string{"session:*", "ticket:acme/*"}, true},
	} {
		f := newGitHubICFixture(t, c.addressing...)
		var got []string
		for _, tg := range ListTargets(f.store, f.actor, time.Now()) {
			got = append(got, tg.Kind+":"+tg.Name)
		}
		if slices.Contains(got, "session:"+f.ticket.ActorID) != c.listed {
			t.Errorf("addressing %v: targets %v; want ticket session listed=%v", c.addressing, got, c.listed)
		}
	}
}

func TestGitHubConnectionKindAndRooms(t *testing.T) {
	s := NewMemStore()
	if err := s.CreateProject("acme"); err != nil {
		t.Fatal(err)
	}
	if _, ok := TrackerConnection(s); ok {
		t.Fatal("no tracker connection yet")
	}
	gh, err := s.CreateConnection(Connection{Kind: "github", Name: "gh-acme"})
	if err != nil {
		t.Fatalf("a github connection: %v", err)
	}
	if c, ok := TrackerConnection(s); !ok || c.ID != gh.ID {
		t.Fatalf("TrackerConnection = %+v, %v", c, ok)
	}
	if _, _, err := PutRoom(s, "acme", RoomBody{Name: "eng", Connection: "gh-acme", Ref: "ACME-1"}); !errors.Is(err, ticketkey.ErrGitHub) {
		t.Fatalf("github room with a Linear ref = %v; want ErrGitHub", err)
	}
	if _, _, err := PutRoom(s, "acme", RoomBody{Name: "eng", Connection: "gh-acme", Ref: "acme/board#3"}); err != nil {
		t.Fatalf("github room: %v", err)
	}
}

func TestMemberHandleFromGitHubAccount(t *testing.T) {
	s := NewMemStore()
	if err := s.CreateProject("acme"); err != nil {
		t.Fatal(err)
	}
	p, _ := s.GetProject("acme")
	u, err := s.CreateUser(User{Name: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddMember(p.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	gh, _ := s.CreateConnection(Connection{Kind: "github", Name: "gh-acme"})
	if _, err := s.UpsertAccount(Account{ConnectionID: gh.ID, Handle: "alice-gh", UserID: u.ID, Status: StatusLive}); err != nil {
		t.Fatal(err)
	}
	if m, ok := MemberOf(s, p.ID, u.ID); !ok || m.Handle != "alice-gh" {
		t.Fatalf("member = %+v, %v; want handle alice-gh", m, ok)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/jam -run 'GitHub|GlobMatch'`
Expected: FAIL to build (`undefined: globMatch`, `undefined: TrackerConnection`).

- [ ] **Step 3: Implement**

`internal/jam/registry.go`, replacing lines 43-44:

```go
// ConnectionKinds are the services a connection may be.
var ConnectionKinds = []string{"linear", "discord", "github"}

// TrackerKinds are the connection kinds a Requisitioner's tracker may be.
var TrackerKinds = []string{"linear", "github"}

// TrackerConnection is the tracker connection ticket bindings default to
// when serve has not resolved the Requisitioner's: the linear one, else the
// github one.
func TrackerConnection(store Store) (Connection, bool) {
	for _, k := range TrackerKinds {
		if c, ok := store.ConnectionOfKind(k); ok {
			return c, true
		}
	}
	return Connection{}, false
}
```

`internal/jam/decide.go`: in `anyAllowed`, replace `if ok, _ := path.Match(g, f); ok {` with `if globMatch(g, f) {`. Add after `anyAllowed`:

```go
// globMatch matches a target form against an addressing glob (path.Match).
// A GitHub ticket key holds a '/' (owner/repo#<n>) that path.Match's '*'
// never crosses. So a GitHub ticket form also matches with every '/' read
// as an ordinary character: ticket:* and * reach every ticket, and
// ticket:acme/* reaches every ticket of acme's repos. Other forms, Linear
// tickets included, match exactly as before.
func globMatch(g, f string) bool {
	if ok, _ := path.Match(g, f); ok {
		return true
	}
	if !isGitHubTicketForm(f) {
		return false
	}
	ok, _ := path.Match(strings.ReplaceAll(g, "/", "\x00"), strings.ReplaceAll(f, "/", "\x00"))
	return ok
}

// isGitHubTicketForm reports a "ticket:owner/repo#<n>" form, bare or scoped
// to a connection ("ticket:<conn>/owner/repo#<n>").
func isGitHubTicketForm(f string) bool {
	rest, ok := strings.CutPrefix(f, "ticket:")
	if !ok {
		return false
	}
	if ticketkey.IsGitHub(rest) {
		return true
	}
	_, key, scoped := strings.Cut(rest, "/")
	return scoped && ticketkey.IsGitHub(key)
}
```

Replace `tracker, hasTracker := store.ConnectionOfKind("linear")` (line 175) with `tracker, hasTracker := TrackerConnection(store)`. Add `"github.com/aethons-tools/cove/internal/ticketkey"` to decide.go's imports.

`internal/jam/intercom_post.go`, `ticketChannel`: immediately after the early return `if ch, ok := ic.store.ChannelByKey(p.ID, SourceTicket, key); ok || !create { return ch, ok, nil }`, add:

```go
	if ic.connKind(conn) == "github" && !ticketkey.IsGitHub(inst.Unit) {
		return Channel{}, false, fmt.Errorf("ticket %q: %w", inst.Unit, ticketkey.ErrGitHub)
	}
```

Replace the head of `resolveTicket`, up to and including `if key == "" { return Channel{}, ErrSendDenied }`:

```go
// resolveTicket finds a ticket's channel: "<key>" on the tracker connection,
// or "<connection name|id>/<key>". A GitHub key (owner/repo#<n>) is itself
// split by '/', so it is matched whole first. A session's own ticket needs
// no addressing; any other needs ticket:<glob>.
func (ic *Intercom) resolveTicket(p Poster, project Project, ref string, globs []string) (Channel, error) {
	tracker, hasTracker := ic.tracker()
	conn, key, scoped := strings.Cut(ref, "/")
	if ticketkey.IsGitHub(ref) {
		conn, key, scoped = "", ref, false
	}
	connID, connOK := tracker, hasTracker
	if !scoped {
		key = ref
	} else if id, err := ResolveRegistryRef(ic.store, ident.Connection, conn); err == nil {
		connID, connOK = id, true
	} else if hasTracker && ic.connKind(tracker) == "github" {
		key = ref // owner/repo#… on the tracker, malformed: refused by the key check below
	} else {
		connOK = false
	}
	if key == "" {
		return Channel{}, ErrSendDenied
	}
	if connOK && ic.connKind(connID) == "github" && !ticketkey.IsGitHub(key) {
		return Channel{}, fmt.Errorf("%w: %w", ErrSendUnresolved, ticketkey.ErrGitHub)
	}
```

(The rest of the function, from `own := …`, is unchanged.) Add the helper next to `ticketChannel`:

```go
// connKind is connection id's kind ("" when unknown).
func (ic *Intercom) connKind(id ident.ID) string {
	c, _ := ic.store.GetConnection(id)
	return c.Kind
}
```

Add `"github.com/aethons-tools/cove/internal/ticketkey"` to intercom_post.go's imports.

`internal/jam/rooms_admin.go`: add the helper, and call it in `PutRoom` right after `conn, err := roomConnection(store, b.Connection)` and its error check:

```go
	if err := validRef(conn.Kind, b.Ref); err != nil {
		return RoomView{}, false, fmt.Errorf("room %q: %w", b.Name, err)
	}
```

```go
// validRef refuses a binding ref its connection's service cannot name: on
// github it must be a ticket key (owner/repo#<n>).
func validRef(kind, ref string) error {
	if kind == "github" && !ticketkey.IsGitHub(ref) {
		return ticketkey.ErrGitHub
	}
	return nil
}
```

Add `"github.com/aethons-tools/cove/internal/ticketkey"` to rooms_admin.go's imports.

`internal/jam/rooms.go`, `planRoom`: at the top of `if rc.Ref != "" {`, add:

```go
		if err := validRef(service, rc.Ref); err != nil {
			return fmt.Errorf("channel %q: %w", rc.Name, err)
		}
```

`internal/jam/people.go:61-63` becomes:

```go
	for _, kind := range TrackerKinds { // the tracker @-handle: linear's, else github's (a login)
		if a, ok := linkedAccount(store, kind, user, func(a Account) bool { return a.Handle != "" }); ok {
			m.Handle = a.Handle
			break
		}
	}
```

Leave `humans_migration.go:361` alone. It migrates legacy Linear/Discord humans, and no legacy data holds GitHub accounts.

- [ ] **Step 4: Run the jam suite**

Run: `gofmt -l internal/jam && go test ./internal/jam/...`
Expected: no gofmt output; PASS. That includes the existing `TestIntercomPlanRoomsAndTickets` (Linear keys unchanged) and the `storetest` suites.

- [ ] **Step 5: Document the addressing (docs-author skill)**

In `docs/usage/jam/comms-addressing.md`:
- Target table row for `ticket:`: append ` A GitHub key is \`owner/repo#<n>\` (\`ticket:acme/board#7\`, or scoped \`ticket:<connection>/acme/board#7\`); a malformed one is refused with "github ticket key must be owner/repo#<n>".`
- The globs paragraph, after "write each prefix you mean.": add `For a GitHub ticket key, \`*\` also crosses its \`/\`: \`ticket:*\` and \`*\` reach every ticket, \`ticket:acme/*\` every ticket of acme's repos.`
- Members bullet: "Their tracker `@`-handle is the **account** linked to them on the `linear` connection, or, on a GitHub-tracked Jam, their login on the `github` connection."
- CLI block: change `--kind linear|discord` to `--kind linear|discord|github`.
- The rooms paragraph: "...or a kind (`linear`, `discord`, `github`: …); default `linear`. A `github` room's ref is a ticket key (`owner/repo#<n>`)."
- Set frontmatter `updated: 2026-10-26`.

- [ ] **Step 6: Commit**

```bash
git add internal/jam docs/usage/jam/comms-addressing.md
git commit -m "feat(jam): github connections and owner/repo#N ticket keys in channels, targets and globs

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: Serve — `requisitioner.github`, one tracker for the Requisitioner, ticketer and relay

**Files:**
- Modify: `cmd/at-jam/config.go:650-677,769-790` (the `GitHub` block, `trackerKind`, `pollInterval`, validation)
- Create: `cmd/at-jam/tracker.go` (`openTracker`)
- Modify: `cmd/at-jam/ticket.go` (rename `linearTicketer` → `trackerTicketer`)
- Rename: `cmd/at-jam/relay_linear.go` → `cmd/at-jam/relay_tracker.go`, and `relay_linear_test.go` → `relay_tracker_test.go`. In them, `linearSurface` becomes a generic `trackerSurface` with a rate-limit wait.
- Modify: `cmd/at-jam/relay_dir.go` (`trackerKind`; tracker service in place of the literal `"linear"`; drop bot authors)
- Modify: `cmd/at-jam/main.go:2235-2244,2517-2581` (wiring; remove the now-unused `linear` and `kit` imports)
- Create: `cmd/at-jam/tracker_test.go`
- Modify: `cmd/at-jam/config_test.go`, `cmd/at-jam/ticket_test.go`, `cmd/at-jam/connections_test.go`
- Modify: `docs/usage/jam/requisitioner.md`, `docs/usage/jam/serve.md:78,115`, `docs/usage/jam/INDEX.md:72`

**Interfaces:**
- Consumes:
  - `tracker.Tracker`, `tracker.RateLimitError`, `tracker.KindGitHub` (Task 1)
  - `githubissues.NewJam` (Task 2)
  - `trackertest.Both`, `trackertest.NewGitHub`, `trackertest.NewLinear`, `trackertest.GHComment`, `trackertest.Token`, `trackertest.GitHubStates`, `trackertest.LinearStates` (Task 4)
  - `jam.TrackerConnection` and GitHub ticket channels (Task 5)
- Produces:
  - `requisitionerConfig.GitHub *kit.GitHubTracker` (yaml `github`)
  - `func (d *requisitionerConfig) trackerKind() string`
  - `func (d *requisitionerConfig) pollInterval() string`
  - `func openTracker(d *requisitionerConfig, token string, httpc *http.Client) (tracker.Tracker, error)`
  - `trackerTicketer{t ticketTracker}`
  - `trackerSurface{service string; feed commentFeeder; poster commentPoster; started time.Time; now func() time.Time}`
  - `directory.trackerKind string`

- [ ] **Step 1: Write the failing tests**

Add to `cmd/at-jam/config_test.go`:

```go
func ghBlock() *kit.GitHubTracker {
	return &kit.GitHubTracker{Repo: "acme/board", States: kit.StateMap{Ready: "status:ready", InProgress: "status:in-progress",
		InReview: "status:in-review", NeedsInput: "status:needs-input", Blocked: "status:blocked"}}
}

func TestValidateRequisitionerTrackerChoice(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(*requisitionerConfig)
		want   string // "" = valid
	}{
		"github":          {func(d *requisitionerConfig) { d.GitHub = ghBlock() }, ""},
		"linear":          {func(d *requisitionerConfig) { d.Linear = &kit.LinearTracker{Team: "T"} }, ""},
		"neither":         {func(d *requisitionerConfig) {}, "requisitioner: set exactly one of linear or github"},
		"both":            {func(d *requisitionerConfig) { d.Linear, d.GitHub = &kit.LinearTracker{Team: "T"}, ghBlock() }, "requisitioner: set exactly one of linear or github"},
		"bad repo":        {func(d *requisitionerConfig) { d.GitHub = ghBlock(); d.GitHub.Repo = "acme" }, "runtime.requisitioner.github.repo: github repo must be owner/name"},
		"dot-dot repo":    {func(d *requisitionerConfig) { d.GitHub = ghBlock(); d.GitHub.Repo = "acme/.." }, "github repo must be owner/name"},
		"missing state":   {func(d *requisitionerConfig) { d.GitHub = ghBlock(); d.GitHub.States.Blocked = "" }, "runtime.requisitioner.github.states.blocked is required"},
		"github + legacy": {func(d *requisitionerConfig) { d.GitHub, d.Connection, d.TrackerTokenCred = ghBlock(), "", "gh-bot" }, "tracker-token-cred is Linear-only"},
	} {
		t.Run(name, func(t *testing.T) {
			c := serveConfig{Credentials: map[string]credSpec{"gh-bot": {}}}
			c.Runtime.Requisitioner = &requisitionerConfig{Role: "w", MaxConcurrent: 1, Connection: "gh-acme"}
			tc.mutate(c.Runtime.Requisitioner)
			err := c.validateRequisitioner()
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("validate = %v; want %q", err, tc.want)
			}
		})
	}
}

func TestParseRequisitionerGitHubBlock(t *testing.T) {
	cfg, err := parseServeConfig([]byte(`
runtime:
  requisitioner:
    role: worker
    max-concurrent: 1
    connection: gh-acme
    github:
      repo: acme/board
      poll-interval: 2m
      dispatch-label-prefix: "go:"
      states: { ready: "status:ready", in-progress: "status:in-progress", in-review: "status:in-review", needs-input: "status:needs-input", blocked: "status:blocked" }
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	d := cfg.Runtime.Requisitioner
	if d.GitHub == nil || d.GitHub.Repo != "acme/board" || d.GitHub.DispatchLabelPrefix != "go:" || d.trackerKind() != "github" || d.pollInterval() != "2m" {
		t.Fatalf("github block = %+v", d.GitHub)
	}
	if err := cfg.validateRequisitioner(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	d.PollInterval = "45s"
	if d.pollInterval() != "45s" {
		t.Fatal("requisitioner poll-interval wins over the tracker block's")
	}
}
```

Add `"strings"` to the imports of `config_test.go` if missing.

`cmd/at-jam/tracker_test.go`:

```go
package main

import (
	"context"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/dispatch/githubissues"
	"github.com/aethons-tools/cove/internal/dispatch/linear"
	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/kit"
	"github.com/aethons-tools/cove/internal/tracker"
	"github.com/aethons-tools/cove/internal/tracker/trackertest"
)

func TestOpenTrackerPicksTheBlock(t *testing.T) {
	gh := trackertest.NewGitHub(t, "acme/board", "jam-bot")
	trk, err := openTracker(&requisitionerConfig{GitHub: &kit.GitHubTracker{Repo: "acme/board", States: trackertest.GitHubStates()}}, trackertest.Token, gh.HTTPClient())
	if _, ok := trk.(*githubissues.Client); err != nil || !ok {
		t.Fatalf("github block → %T, %v", trk, err)
	}
	lin := trackertest.NewLinear(t, "ACME", "Jam")
	trk, err = openTracker(&requisitionerConfig{Linear: &kit.LinearTracker{Team: "ACME", States: trackertest.LinearStates()}}, trackertest.Token, lin.HTTPClient())
	if _, ok := trk.(*linear.Client); err != nil || !ok {
		t.Fatalf("linear block → %T, %v", trk, err)
	}
	if trk, err := openTracker(&requisitionerConfig{GitHub: &kit.GitHubTracker{Repo: "bad"}}, "tok", nil); err == nil || trk != nil {
		t.Fatalf("a failed open = %v (%T); want a nil interface and an error", err, trk)
	}
}

func TestTicketerOnBothTrackers(t *testing.T) {
	ctx := context.Background()
	for _, c := range trackertest.Both(t) {
		t.Run(c.Kind, func(t *testing.T) {
			err := trackerTicketer{c.Tracker}.Report(ctx, jam.Instance{Unit: c.Key},
				jam.TicketReport{State: "in-review", Summary: "PR is up", PR: "https://github.com/acme/board/pull/9"})
			if err != nil || c.State() != "in-review" || !slices.Contains(c.Posted(), "**in-review** — PR is up\n\nPR: https://github.com/acme/board/pull/9") {
				t.Fatalf("report: state %q, posted %q, %v", c.State(), c.Posted(), err)
			}
			if err := (trackerTicketer{c.Tracker}).BlockUnfinished(ctx, jam.Instance{Unit: c.Key}, "wrapped up"); err != nil || c.State() != "blocked" {
				t.Fatalf("block: state %q, %v", c.State(), err)
			}
		})
	}
	gh := trackertest.Both(t)[1]
	if err := (trackerTicketer{gh.Tracker}).Report(ctx, jam.Instance{Unit: "#1"}, jam.TicketReport{State: "done"}); err == nil || !strings.Contains(err.Error(), "github ticket key must be owner/repo#<n>") {
		t.Fatalf("report on a bare #1 = %v", err)
	}
}

type limitedFeed struct {
	calls int
	err   error
}

func (f *limitedFeed) CommentFeed(context.Context, time.Time, int) ([]tracker.FeedComment, error) {
	f.calls++
	return nil, f.err
}

// Review Focus 2: after a rate limit the surface makes no call until
// Retry-After has passed, and its cursor holds.
func TestTrackerSurfaceWaitsOutRateLimit(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	f := &limitedFeed{err: &tracker.RateLimitError{Service: "github", RetryAfter: time.Minute}}
	s := &trackerSurface{service: "github", feed: f, started: now, now: func() time.Time { return now }}
	cur := now.Format(time.RFC3339Nano)
	if _, next, err := s.Poll(ctx, "acme", cur); err == nil || next != cur {
		t.Fatalf("rate-limited poll = %q, %v; want the cursor and the error", next, err)
	}
	now = now.Add(30 * time.Second)
	if evs, next, err := s.Poll(ctx, "acme", cur); err != nil || len(evs) != 0 || next != cur || f.calls != 1 {
		t.Fatalf("poll inside the wait = %v %q %v, calls %d; want a quiet no-op", evs, next, err, f.calls)
	}
	now, f.err = now.Add(31*time.Second), nil
	if _, _, err := s.Poll(ctx, "acme", cur); err != nil || f.calls != 2 {
		t.Fatalf("poll after the wait: %v, calls %d", err, f.calls)
	}
	if s.Service() != "github" || (&trackerSurface{}).Service() != "linear" {
		t.Fatal("Service() is the tracker kind, linear by default")
	}
}

func TestTrackerSurfaceCursorNeverMovesBack(t *testing.T) {
	since := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	ff := &fakeFeeder{cs: []linear.FeedComment{
		{ID: "1", CreatedAt: since, IssueIdentifier: "acme/board#1", AuthorBot: true}, // GitHub's since is inclusive
		{ID: "0", CreatedAt: since.Add(-time.Hour), IssueIdentifier: "acme/board#1"},
	}}
	s := &trackerSurface{service: "github", feed: ff}
	cur := since.Format(time.RFC3339Nano)
	evs, next, err := s.Poll(context.Background(), "acme", cur)
	if err != nil || next != cur || len(evs) != 2 || !evs[0].AuthorBot {
		t.Fatalf("poll = %+v %q %v; want both events, cursor %q, bot flagged", evs, next, err, cur)
	}
}

// Review Focus 5 and the spec's relay end-to-end: on each tracker a person's
// reply lands in the ticket channel. Jam's own comment and (on GitHub) a bot's
// are dropped. A session's message goes back onto the issue.
func TestRelayEndToEndBothTrackers(t *testing.T) {
	ctx := context.Background()
	for _, c := range trackertest.Both(t) {
		t.Run(c.Kind, func(t *testing.T) {
			st := jam.NewMemStore()
			mustCreateProject(t, st, "acme")
			p, _ := st.GetProject("acme")
			conn, err := st.CreateConnection(jam.Connection{Kind: c.Kind, Name: c.Kind})
			if err != nil {
				t.Fatal(err)
			}
			if err := st.PutRole("acme", jam.Role{Name: "impl"}); err != nil {
				t.Fatal(err)
			}
			inst := jam.Instance{ActorID: string(ident.New(ident.Session)), Project: "acme", Role: "impl", Unit: c.Key, Phase: jam.PhaseLive}
			if err := st.PutInstance(inst); err != nil {
				t.Fatal(err)
			}
			trackerFn := func() (ident.ID, bool) { return conn.ID, true }
			lg := intercom.NewMemLog(nil)
			ic := jam.NewIntercom(st, trackerFn, lg, nil, nil)
			if err := ic.SetUp(inst); err != nil {
				t.Fatal(err)
			}
			self, err := c.Tracker.Viewer(ctx)
			if err != nil {
				t.Fatal(err)
			}
			dir := &directory{store: st, ic: ic, project: string(p.ID), selfIdentity: self, tracker: trackerFn, trackerKind: c.Kind}
			surf := &trackerSurface{service: c.Kind, feed: c.Tracker, poster: c.Tracker, started: time.Now().Add(-2 * time.Minute)}

			now := time.Now().UTC().Truncate(time.Second)
			c.Comment("brent", "please rebase", now.Add(-60*time.Second))
			c.Comment(c.Self, "status from jam", now.Add(-50*time.Second))
			if c.GitHub != nil {
				c.GitHub.AddComment(trackertest.GHComment{Issue: 1, Body: "ci passed", Login: "ci[bot]", UserID: 99, Bot: true, CreatedAt: now.Add(-40 * time.Second)})
			}
			evs, _, err := surf.Poll(ctx, string(p.ID), "")
			if err != nil {
				t.Fatalf("poll: %v", err)
			}
			for _, ev := range evs {
				r, ok := dir.Route(c.Kind, string(p.ID), ev)
				if !ok {
					continue
				}
				m := intercom.Squawk{ID: "in:" + c.Kind + ":" + ev.ForeignID, From: r.From, Body: ev.Body, At: ev.At, Origin: r.Origin, OriginRef: r.OriginRef}
				if err := dir.Post(r, m); err != nil {
					t.Fatalf("post %q: %v", ev.Body, err)
				}
			}
			ch, ok := st.ChannelByKey(p.ID, jam.SourceTicket, string(conn.ID)+"/"+c.Key)
			if !ok {
				t.Fatal("no ticket channel")
			}
			var bodies []string
			for _, m := range lg.ChannelBefore(ch.ID, math.MaxInt64, 50) {
				bodies = append(bodies, m.Body)
			}
			if !slices.Contains(bodies, "please rebase") || slices.Contains(bodies, "status from jam") || slices.Contains(bodies, "ci passed") {
				t.Fatalf("ticket channel = %q; want only the person's reply", bodies)
			}

			pl, err := ic.Plan(jam.Poster{ID: ident.ID(inst.ActorID), Session: &inst, Actor: &jam.Actor{ID: inst.ActorID, Grants: []jam.Grant{{Project: "acme", Role: "impl"}}}}, "", time.Now())
			if err != nil {
				t.Fatal(err)
			}
			m, err := ic.Post(pl, intercom.Squawk{From: ident.ID(inst.ActorID), Body: "rebased"})
			if err != nil {
				t.Fatal(err)
			}
			ds := dir.Surfaces(c.Kind, m)
			if len(ds) != 1 || ds[0].Service != c.Kind || ds[0].Address != c.Key {
				t.Fatalf("surfaces = %+v; want one %s delivery to %s", ds, c.Kind, c.Key)
			}
			if _, err := surf.Deliver(ctx, ds[0], m); err != nil || !slices.Contains(c.Posted(), "rebased") {
				t.Fatalf("deliver: posted %q, %v", c.Posted(), err)
			}
			if got := dir.Surfaces("linear", m); c.Kind == "github" && len(got) != 0 {
				t.Fatalf("a github ticket is not delivered to linear: %+v", got)
			}
		})
	}
}
```

Add a case to `TestResolveServeConnection` in `cmd/at-jam/connections_test.go`. Before the table loop:

```go
	if _, err := st.CreateConnection(jam.Connection{Kind: "github", Name: "gh-acme", CredName: "lin-tok"}); err != nil {
		t.Fatal(err)
	}
	if c, _, err := resolveServeConnection(st, "runtime.requisitioner", "github", "gh-acme", "", demanded); err != nil || c.Kind != "github" {
		t.Fatalf("github connection = %+v %v", c, err)
	}
```

Add a row to its table: `"linear as github": {"github", "linear-acme", "is a linear connection, want github"},`.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmd/at-jam -run 'TrackerChoice|GitHubBlock|OpenTracker|Ticketer|TrackerSurface|RelayEndToEnd|ResolveServeConnection'`
Expected: FAIL to build (`undefined: openTracker`, `unknown field GitHub`, `undefined: trackerSurface`).

- [ ] **Step 3: Rename the Linear-specific names**

```bash
git mv cmd/at-jam/relay_linear.go cmd/at-jam/relay_tracker.go
git mv cmd/at-jam/relay_linear_test.go cmd/at-jam/relay_tracker_test.go
sed -i 's/linearSurface/trackerSurface/g; s/linearTicketer/trackerTicketer/g' cmd/at-jam/*.go
```

- [ ] **Step 4: Implement config and `openTracker`**

`cmd/at-jam/config.go`, `requisitionerConfig`. Change the `Connection` comment and add the block after `Linear`:

```go
	// Connection names the tracker connection the Requisitioner, escalation
	// and the tracker relay use: kind linear or github, matching the block
	// set below. Exclusive with the deprecated TrackerTokenCred (Linear only),
	// which binds the implicit connection named "linear".
	Connection       string `yaml:"connection"`
	TrackerTokenCred string `yaml:"tracker-token-cred"`
	// DeprecatedTrackerToken detects the removed inline form (see discordConfig).
	DeprecatedTrackerToken *credSpec          `yaml:"tracker-token"`
	Linear                 *kit.LinearTracker `yaml:"linear"`
	// GitHub dispatches from one repo's Issues instead (exactly one of
	// linear or github): status labels for states, done = closed.
	GitHub *kit.GitHubTracker `yaml:"github"`
```

Add the two accessors below the struct:

```go
// trackerKind is the kind of the Requisitioner's tracker (and of its
// connection and relay service).
func (d *requisitionerConfig) trackerKind() string {
	if d.GitHub != nil {
		return tracker.KindGitHub
	}
	return tracker.KindLinear
}

// pollInterval is the Requisitioner's own poll-interval, else its tracker
// block's ("" ⇒ the Requisitioner's 30s default).
func (d *requisitionerConfig) pollInterval() string {
	switch {
	case d.PollInterval != "":
		return d.PollInterval
	case d.GitHub != nil:
		return d.GitHub.PollInterval
	case d.Linear != nil:
		return d.Linear.PollInterval
	}
	return ""
}
```

In `validateRequisitioner`, replace the `if d.Linear == nil { … }` check with:

```go
	if (d.Linear == nil) == (d.GitHub == nil) {
		return errors.New("requisitioner: set exactly one of linear or github")
	}
```

Then, after the `DeprecatedTrackerToken` check and before `return checkConnectionOrCred(…)`, add:

```go
	if g := d.GitHub; g != nil {
		if err := ticketkey.ValidRepo(g.Repo); err != nil {
			return fmt.Errorf("runtime.requisitioner.github.repo: %w", err)
		}
		for _, s := range []struct{ name, label string }{
			{"ready", g.States.Ready}, {"in-progress", g.States.InProgress}, {"in-review", g.States.InReview},
			{"needs-input", g.States.NeedsInput}, {"blocked", g.States.Blocked},
		} { // done = the issue is closed: no label
			if strings.TrimSpace(s.label) == "" {
				return fmt.Errorf("runtime.requisitioner.github.states.%s is required", s.name)
			}
		}
		if d.TrackerTokenCred != "" {
			return fmt.Errorf("runtime.requisitioner.tracker-token-cred is Linear-only; set runtime.requisitioner.connection to a github connection")
		}
	}
```

Update the function's doc comment to say "required: role, max-concurrent > 0, exactly one of linear or github". Add the imports `errors`, `strings` (if missing), `github.com/aethons-tools/cove/internal/ticketkey` and `github.com/aethons-tools/cove/internal/tracker`.

`cmd/at-jam/tracker.go`:

```go
package main

import (
	"net/http"

	"github.com/aethons-tools/cove/internal/dispatch/githubissues"
	"github.com/aethons-tools/cove/internal/dispatch/linear"
	"github.com/aethons-tools/cove/internal/kit"
	"github.com/aethons-tools/cove/internal/tracker"
)

// openTracker builds the Requisitioner's tracker from whichever block is
// set (validateRequisitioner guarantees exactly one). The same value
// serves the Requisitioner, ticket reports, wake-on close and the relay.
// A failure is a nil interface, never a typed nil.
func openTracker(d *requisitionerConfig, token string, httpc *http.Client) (tracker.Tracker, error) {
	if d.GitHub != nil {
		c, err := githubissues.NewJam(*d.GitHub, token, httpc)
		if err != nil {
			return nil, err
		}
		return c, nil
	}
	// linear.New wants a full kit.Config; wrap the configured LinearTracker.
	c, err := linear.New(kit.Config{Tracker: &kit.Tracker{Linear: d.Linear}}, token, httpc)
	if err != nil {
		return nil, err
	}
	return c, nil
}
```

- [ ] **Step 5: Implement the generic ticketer, surface and directory**

`cmd/at-jam/ticket.go`: the `ticketTracker` comment becomes `// ticketTracker is the slice of tracker.Tracker a ticket session's reports need.`, and the `trackerTicketer` comment reads "…moves a ticket session's ticket on the Requisitioner's tracker (Linear or GitHub)…". There is no logic change. The GitHub client's `IssueByIdentifier` already refuses a malformed key with the fixed text.

`cmd/at-jam/relay_tracker.go`:
- Replace the `internal/dispatch/linear` import with `internal/tracker`, and add `"cmp"` and `"errors"`.
- `commentFeeder` becomes:

```go
// commentFeeder is the narrow slice of tracker.Tracker that trackerSurface
// polls, kept as an interface so Poll is testable without a live client.
type commentFeeder interface {
	CommentFeed(ctx context.Context, since time.Time, limit int) ([]tracker.FeedComment, error)
}
```

- The surface type and its `Service` and `Poll`:

```go
// trackerSurface is the relay.Surface over the Requisitioner's tracker's
// comment feed (Linear's team feed, GitHub's repo feed). After a rate limit
// it makes no calls until the tracker's Retry-After has passed.
type trackerSurface struct {
	service   string // tracker kind; "" = linear
	feed      commentFeeder
	poster    commentPoster
	started   time.Time        // baseline for an empty cursor (shadow forward, not all history)
	now       func() time.Time // nil: time.Now
	notBefore time.Time        // a rate limit's end: Poll is a no-op until then
}

func (s *trackerSurface) Service() string { return cmp.Or(s.service, tracker.KindLinear) }

func (s *trackerSurface) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// Poll fetches comments from `since` (or s.started when since is empty),
// mapping each into a relay.Event. next is the latest CreatedAt seen when
// it is after the cursor, else the incoming since: the cursor never moves
// back.
func (s *trackerSurface) Poll(ctx context.Context, project, since string) (events []relay.Event, next string, err error) {
	if s.clock().Before(s.notBefore) {
		return nil, since, nil // waiting out a rate limit
	}
	from := s.started
	if since != "" {
		from, err = time.Parse(time.RFC3339Nano, since)
		if err != nil {
			return nil, since, fmt.Errorf("%s surface: parse cursor %q: %w", s.Service(), since, err)
		}
	}
	cs, err := s.feed.CommentFeed(ctx, from, 100)
	if err != nil {
		var rl *tracker.RateLimitError
		if errors.As(err, &rl) {
			s.notBefore = s.clock().Add(rl.RetryAfter)
		}
		return nil, since, err
	}
	next = since
	latest := from
	events = make([]relay.Event, 0, len(cs))
	for _, c := range cs {
		events = append(events, relay.Event{
			ForeignID:      c.ID,
			Surface:        c.IssueIdentifier,
			Author:         c.Author,
			AuthorID:       c.AuthorID,
			AuthorBot:      c.AuthorBot,
			Body:           c.Body,
			ReplyToForeign: c.ParentID,
			At:             c.CreatedAt,
			ContentType:    intercom.ContentMarkdown, // Linear and GitHub comment bodies are markdown
		})
		if c.CreatedAt.After(latest) {
			latest = c.CreatedAt
		}
	}
	if latest.After(from) {
		next = latest.Format(time.RFC3339Nano)
	}
	return events, next, nil
}
```

- In `Deliver`, change the three `"linear deliver: …"` messages to `fmt.Errorf("%s deliver: …", s.Service(), …)`.
- In `relay_tracker_test.go`, the existing `TestLinearSurface*` tests keep passing unchanged. For an empty cursor, `from` is `started` and the comments come after it.

`cmd/at-jam/relay_dir.go`:
- Add the field `trackerKind string // the tracker's kind (relay service); "" = linear` and update the `selfIdentity` comment to "Jam's tracker identity (Linear displayName, GitHub login): the self-post filter". The `tracker` comment becomes "the tracker connection ticket bindings are on".
- Add the helper:

```go
// trackerService is the relay service ticket bindings are delivered on.
func (d *directory) trackerService() string { return cmp.Or(d.trackerKind, "linear") }
```

- In `Surfaces`:
  - Change `if service == "linear" && len(escalated.handles) > 0 {` to `if service == d.trackerService() && len(escalated.handles) > 0 {`.
  - Change `if service == "linear" && ch.Kind == jam.SourceTicket && …` to `if service == d.trackerService() && ch.Kind == jam.SourceTicket && …`. Inside it, `relay.Delivery{Service: "linear", …, prefix("linear")}` becomes `relay.Delivery{Service: service, …, prefix(service)}`.
  - The chat fallback: `service == "linear" && ch.Kind == jam.SourceChat` becomes `service == d.trackerService() && ch.Kind == jam.SourceChat`, with `Service: service` and `prefix(service)`.
- `Route`'s tail and the router become:

```go
	if service == d.trackerService() {
		return d.routeTracker(service, e)
	}
	return relay.Routed{}, false
}

// routeTracker drops Jam's own comments (its posts, echoed back on the feed)
// and any bot's, then maps the issue to the channel holding its binding: a
// ticket's, or a room bound to it. A comment on an issue no channel holds is
// unroutable.
func (d *directory) routeTracker(service string, e relay.Event) (relay.Routed, bool) {
	if e.AuthorBot || (d.selfIdentity != "" && e.Author == d.selfIdentity) {
		return relay.Routed{}, false
	}
	conn, ok := d.tracker()
	if !ok {
		return relay.Routed{}, false
	}
	ch, ok := d.store.ChannelByBinding(conn, e.Surface)
	if !ok {
		return relay.Routed{}, false
	}
	from, ok := d.author(service, ch, e.Surface, e.AuthorID, e.Author)
	if !ok {
		return relay.Routed{}, false
	}
	r := relay.Routed{Channel: ch.ID, From: from, Origin: conn, OriginRef: e.Surface}
	if e.ReplyToForeign != "" {
		r.ReplyTo = "in:" + service + ":" + e.ReplyToForeign
	}
	return r, true
}
```

(The `d.selfIdentity != ""` guard keeps an unknown Viewer from dropping every comment whose author is empty. The existing `TestRoute*` cases use `selfIdentity: "Jam"`, so they are unaffected.) Add `"cmp"` to the file's imports.

- [ ] **Step 6: Wire serve**

`cmd/at-jam/main.go:2235-2244`: change the comment's last clause to "(until then, or with no requisitioner, the tracker connection if any)", and `c, ok := st.ConnectionOfKind("linear")` to `c, ok := jam.TrackerConnection(st)`.

Replace `main.go:2517-2581`, from `if dc != nil {` through `log.Info("Jam relay (linear): resident, egress ON", "self", self != "")`, with:

```go
	if dc != nil {
		kind := dc.trackerKind()
		// Resolve Jam's own tracker token (never injected into a cove, never logged).
		tconn, tcred, err := resolveServeConnection(st, "runtime.requisitioner", kind, dc.Connection, dc.TrackerTokenCred, cfg.Credentials)
		if err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		trackerConn.Store(tconn.ID)
		tokEnv, err := secret.Resolve(runner.OS{}, nil, []secret.Spec{specs[tcred]})
		if err != nil {
			fmt.Fprintln(stderr, "at-jam: requisitioner connection credential:", err)
			return 1
		}
		log.Info("requisitioner: connection", "kind", kind, "connection", tconn.Name, "id", tconn.ID)
		trk, err := openTracker(dc, tokEnv[tcred], nil)
		if err != nil {
			fmt.Fprintln(stderr, "at-jam: requisitioner tracker:", err)
			return 1
		}
		poll, _ := time.ParseDuration(dc.pollInterval()) // "" or invalid → 0 → Requisitioner default
		// The Requisitioner's (project, role), normalized the same way as the
		// Allocator's fallback (see requisitionerProject).
		project := reqProject
		if r, ok := st.GetRole(project, dc.Role); ok && r.Allocation.MaxEphemeral > 0 && r.Allocation.MaxEphemeral != dc.MaxConcurrent {
			log.Info("Jam allocator: roster max-ephemeral overrides Requisitioner max-concurrent",
				"project", project, "role", dc.Role, "max-ephemeral", r.Allocation.MaxEphemeral, "max-concurrent", dc.MaxConcurrent)
		}
		tickets.set(trackerTicketer{trk})
		disp := dispatcher.New(trk, sup, st, alloc, dispatcher.Config{
			Role: dc.Role, Project: project, PollInterval: poll,
		}, log)
		go disp.Run(context.Background())
		log.Info("requisitioner: resident", "kind", kind, "role", dc.Role, "max-concurrent", dc.MaxConcurrent)

		// The tracker relay engine polls the tracker's comment feed and posts
		// inbound replies into the channel log opened above (ingress). It also
		// delivers outbound Log messages to the tracker (egress). The Log is
		// the single source of truth for both directions.
		self, err := trk.Viewer(context.Background())
		if err != nil {
			log.Warn("Jam relay: viewer lookup failed; self-post filter disabled (bot authors are still dropped)", "kind", kind, "error", err.Error())
		}
		dir.project = reqProject // by id: the relays' cursors and polling outlive a rename
		dir.selfIdentity = self
		dir.trackerKind = kind
		surf := &trackerSurface{service: kind, feed: trk, poster: trk, started: time.Now()}
		// Seed once: skip everything the 1a dual-write already delivered live,
		// so turning egress on never re-posts the Log's shadow history.
		// Persisted with a nonzero LastSeq → never re-seeds (a re-seed to a
		// newer tail would drop messages appended-but-not-yet-delivered since
		// the first cutover). needsSeed also re-seeds a marker whose LastSeq
		// is zero, which covers a pre-COV-184 marker file (persisted the
		// low-water as LastMsg, a string) upgrading in place — see
		// fileMarkers.needsSeed. A new github service seeds at the tail.
		if relayMarkers.needsSeed(kind) {
			if err := relayMarkers.SetEgress(kind, relay.EgressMark{LastSeq: logTailSeq(chlog)}); err != nil {
				fmt.Fprintln(stderr, "at-jam: relay egress seed:", err)
				return 1
			}
		}
		eng := relay.New(surf, chlog, relayMarkers, relayCursors, dir, relay.Config{EgressEnabled: true}, log)
		go eng.Run(context.Background())
		log.Info("Jam relay ("+kind+"): resident, egress ON", "self", self != "")
```

(The closing brace that follows is unchanged.) Remove the `internal/dispatch/linear` and `internal/kit` imports from `main.go`. Nothing else in the file uses them; `go build ./cmd/at-jam` confirms it.

- [ ] **Step 7: Run the serve package and the whole suite**

Run: `gofmt -l cmd internal && go test ./cmd/at-jam ./cmd/at-cove ./internal/...`
Expected: no gofmt output; PASS. The renamed tests (`relay_tracker_test.go`, `ticket_test.go`) and the existing `relay_dir_test.go` pass unchanged with `trackerKind` unset.

- [ ] **Step 8: Document the GitHub tracker (docs-author skill)**

`docs/usage/jam/requisitioner.md` owns the requisitioner block. Make these edits:
- Frontmatter:
  - `summary`: "…ready tracker tickets (Linear or GitHub Issues)…"
  - `read_when`: "…polling a tracker (Linear or GitHub Issues)…"
  - `updated: 2026-10-26`
- Flow step 1: "poll the tracker for issues in its READY state (GitHub: open issues with the ready status label; pull requests never count)".
- Flow step 6: "`unit = <ticket key>` (`AET-42`; GitHub `owner/repo#<n>`)".
- Config section:
  - Change the YAML comment on `connection:` to "a connection of the tracker's kind (`linear` or `github`)…".
  - Add the line "Set **exactly one** of `linear:` or `github:`; serve refuses to start otherwise (`requisitioner: set exactly one of linear or github`). `poll-interval` falls back to the tracker block's when unset."
- A new subsection `### GitHub Issues (\`github:\`)` with this example:

```yaml
runtime:
  requisitioner:
    role: worker
    max-concurrent: 5
    connection: gh-acme       # at-jam connection add --kind github --name gh-acme --cred github-bot
    github:
      repo: acme/board        # owner/name
      class-label-prefix: "class:"
      dispatch-label-prefix: "dispatch:"   # default dispatch:
      states: { ready: "status:ready", in-progress: "status:in-progress", in-review: "status:in-review", needs-input: "status:needs-input", blocked: "status:blocked" }
```

  The subsection's bullets:
  - **States are status labels.** Jam keeps exactly one on an issue, and done closes the issue (`states.done` is unused).
  - **Blockers.** Blockers are `Depends on #N` / `Blocked by #N` in the body, in the same repo; a cross-repo blocker is ignored with a warning.
  - **Ticket keys and the token.** Ticket keys are `owner/repo#<n>`. The token needs Issues read/write and Metadata read on the repo (a fine-grained PAT). `tracker-token-cred` is Linear-only.
  - **The relay.** The relay reads the repo's issue comments: it drops Jam's own login and any Bot account, skips pull-request comments, and waits out a rate limit's `Retry-After`.
- "Not yet": delete the "GitHub-issues tracker … not wired" half of the last bullet. In the paragraph about "the Linear relay", say "the tracker relay (Linear or GitHub comments)".

`docs/usage/jam/serve.md`:
- Line 78 comment: `# an \`at-jam connection\` of kind linear or github (the tracker block's)`.
- Row 115: replace "a `linear` block, and `connection`: a **connection** of kind `linear` (`at-jam connection add --kind linear …`)" with "exactly one of a `linear` or `github` block, and `connection`: a **connection** of that kind". Also replace "The deprecated `tracker-token-cred`…" with "The deprecated, Linear-only `tracker-token-cred`…". The rest of the row stays.

`docs/usage/jam/INDEX.md:72`: "polling a tracker (Linear or GitHub Issues)".

Then run the audit and compare it with the baseline:

Run: `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs 2>&1 | tail -1`
Expected: the error count equals the pre-branch baseline (755) plus this plan file's own findings. No new dangling links or oversize docs.

- [ ] **Step 9: Commit**

```bash
git add cmd/at-jam docs/usage/jam/requisitioner.md docs/usage/jam/serve.md docs/usage/jam/INDEX.md
git commit -m "feat(jam): runtime.requisitioner.github — one tracker for the Requisitioner, reports and relay

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7: Branch verification

**Files:** none (verification only; fix forward in the owning task's files if anything fails)

**Interfaces:**
- Consumes: everything above.
- Produces: a green branch.

- [ ] **Step 1: Run the full gates**

Run: `GOPROXY=https://proxy.golang.org,direct just test; echo "test exit $?"; just lint; echo "lint exit $?"; go vet -tags integration ./...; echo "vet exit $?"`
Expected: all three exit 0.

- [ ] **Step 2: Prove at-cove is untouched in behaviour**

Run: `git diff main --stat -- cmd/at-cove internal/dispatch/scheduler/engine.go`
Expected: no changes to `cmd/at-cove` or the engine. `go test ./cmd/at-cove ./internal/dispatch/...` passed in Step 1.

- [ ] **Step 3: Check that no token reaches a log line**

Run: `grep -rn 'token' cmd/at-jam/main.go | grep -i 'log\.' || true`
Expected: no log call carries the token. It is only passed to `openTracker`.

- [ ] **Step 4: Docs audit**

Run: `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs 2>&1 | tail -1`
Expected: the baseline plus only this plan file's own errors.

---

## Self-Review

- **Spec §1 coverage.** Each item maps to a task:
  - Config, exactly one of the two blocks → Task 6
  - `github:` fields (`dispatch-label-prefix` new) → Tasks 2 and 6
  - Connection kind `github` → Tasks 5 and 6
  - One interface → Tasks 1 and 4
  - `NewJam` gains the gate, `IssueByIdentifier`, `CommentFeed` and `Viewer` → Tasks 2 and 3
  - Blockers stay in the body: unchanged code, documented in Task 6
  - Keys in channels, targets, escalation and reports → Tasks 5 and 6
    - Escalation delivers through `Surfaces`' tracker branch, now kind-generic.
- **"The package moves…" ruling.** `githubissues` already sits beside `internal/dispatch/linear`, so it stays put; no shim is needed. `kit.GitHubTracker` serves as Jam's config type via `requisitioner.github`. Both leave `internal/kit` / `internal/dispatch` in later slices.
- **§5.** `requisitioner: set exactly one of linear or github` → Task 6. `github ticket key must be owner/repo#<n>` → Task 1 (`ticketkey.ErrGitHub`), surfaced in Tasks 2, 5 and 6. Tokens are never echoed → Task 3 `TestErrorsNeverCarryTheToken`.
- **§6.** The httptest GitHub, the ready listing with dispatch and class labels, comments, transitions by label and close, `IssueByIdentifier`, the `CommentFeed` since-cursor and `Viewer` → Tasks 2-4. The Requisitioner and relay end to end on both trackers → Task 4 `TestRequisitionerOnBothTrackers` and Task 6 `TestRelayEndToEndBothTrackers`.
- **Type consistency.** These names are used identically in every task:
  - `tracker.FeedComment` (aliased as `linear.FeedComment`), `tracker.RateLimitError`
  - `githubissues.NewJam(kit.GitHubTracker, string, *http.Client)`
  - `trackertest.Case.{Kind, Tracker, Key, Self, State, Posted, Comment, GitHub, Linear}`
  - `trackerSurface{service, feed, poster, started, now}`, `directory.trackerKind`
  - `jam.TrackerConnection`, `requisitionerConfig.{GitHub, trackerKind(), pollInterval()}`
- **Placeholders.** None. Every code step carries its code, and every doc step names its exact edits.
