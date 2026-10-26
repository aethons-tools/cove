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

// ghCommentJSON is one repo issue comment as GitHub serves it. issue is
// "issues/N" or "pull/N" (a PR conversation's comment).
func ghCommentJSON(id int64, issue string, login, typ, created, updated string) string {
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
			ghCommentJSON(1001, "issues/7", "brent", "User", "2026-10-26T12:00:00Z", "2026-10-26T12:00:00Z"),  // exactly at since: kept
			ghCommentJSON(1002, "pull/8", "brent", "User", "2026-10-26T12:01:00Z", "2026-10-26T12:01:00Z"),    // a PR conversation: skipped
			ghCommentJSON(1003, "issues/7", "ci[bot]", "Bot", "2026-10-26T12:02:00Z", "2026-10-26T12:02:00Z"), // a bot: kept, flagged
			ghCommentJSON(1004, "issues/7", "brent", "User", "2026-10-26T11:00:00Z", "2026-10-26T12:03:00Z"),  // edited old comment: skipped
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
			io.WriteString(w, "["+ghCommentJSON(1, "issues/1", "a", "User", "2026-10-26T12:00:01Z", "2026-10-26T12:00:01Z")+","+
				ghCommentJSON(2, "issues/1", "a", "User", "2026-10-26T12:00:02Z", "2026-10-26T12:00:02Z")+"]")
		case "2":
			w.Header().Set("Link", `<https://evil.example/steal?page=3>; rel="next"`) // never followed: not the API host
			io.WriteString(w, "["+ghCommentJSON(3, "issues/1", "a", "User", "2026-10-26T12:00:03Z", "2026-10-26T12:00:03Z")+"]")
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

// prHeavyRepo serves n pages of two PR-conversation comments each, one
// minute apart from base, linked by rel="next"; it counts the pages served.
func prHeavyRepo(t *testing.T, n int, base time.Time, served *int) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/acme/board/issues/comments", func(w http.ResponseWriter, r *http.Request) {
		*served++
		p, _ := strconv.Atoi(r.URL.Query().Get("page"))
		p = max(p, 1)
		if p < n {
			w.Header().Set("Link", fmt.Sprintf(`<https://api.github.com/repos/acme/board/issues/comments?page=%d>; rel="next"`, p+1))
		}
		at := func(i int) string { return base.Add(time.Duration(2*(p-1)+i) * time.Minute).Format(time.RFC3339) }
		io.WriteString(w, "["+ghCommentJSON(int64(2*p), "pull/9", "a", "User", at(0), at(0))+","+
			ghCommentJSON(int64(2*p+1), "pull/9", "a", "User", at(1), at(1))+"]")
	})
	return mux
}

// A PR-heavy repo: one call reads at most maxFeedPages pages, and the cursor
// moves past every comment seen — the skipped PR ones too — so the next poll
// starts where this one stopped instead of re-paging the same PR comments.
func TestCommentFeedCursorPassesSkippedPRComments(t *testing.T) {
	since := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	served := 0
	c := newJamClient(t, prHeavyRepo(t, maxFeedPages+5, since, &served))
	got, next, err := c.CommentFeedCursor(context.Background(), since, 100)
	if err != nil || len(got) != 0 {
		t.Fatalf("PR-heavy feed = %+v, %v; want no comments", got, err)
	}
	if served != maxFeedPages {
		t.Fatalf("pages fetched = %d; want the cap %d", served, maxFeedPages)
	}
	// The last comment seen is page maxFeedPages's second: minute 2*maxFeedPages-1.
	if want := since.Add(time.Duration(2*maxFeedPages-1) * time.Minute); !next.Equal(want) {
		t.Fatalf("next = %v; want %v (the last PR comment seen)", next, want)
	}
}

// The cursor never moves before since (an edited old comment's created_at
// is earlier), and a limit stops it at the last comment returned: a comment
// read but not returned is read again next time.
func TestCommentFeedCursorNeverBackNeverPastLimit(t *testing.T) {
	since := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/acme/board/issues/comments", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "["+strings.Join([]string{
			ghCommentJSON(1, "issues/7", "a", "User", "2026-10-26T11:00:00Z", "2026-10-26T12:05:00Z"), // edited old: skipped
			ghCommentJSON(2, "issues/7", "a", "User", "2026-10-26T12:01:00Z", "2026-10-26T12:01:00Z"),
			ghCommentJSON(3, "pull/8", "a", "User", "2026-10-26T12:02:00Z", "2026-10-26T12:02:00Z"), // PR: skipped, cursor passes it
			ghCommentJSON(4, "issues/7", "a", "User", "2026-10-26T12:03:00Z", "2026-10-26T12:03:00Z"),
			ghCommentJSON(5, "issues/7", "a", "User", "2026-10-26T12:04:00Z", "2026-10-26T12:04:00Z"),
		}, ",")+"]")
	})
	c := newJamClient(t, mux)
	ctx := context.Background()
	got, next, err := c.CommentFeedCursor(ctx, since, 2)
	if err != nil || len(got) != 2 || got[0].ID != "2" || got[1].ID != "4" {
		t.Fatalf("limit 2 = %+v, %v; want 2, 4", got, err)
	}
	if want := since.Add(3 * time.Minute); !next.Equal(want) {
		t.Fatalf("next = %v; want %v (comment 4, not 5)", next, want)
	}
	late := since.Add(time.Hour) // every comment is older than since
	if got, next, err := c.CommentFeedCursor(ctx, late, 100); err != nil || len(got) != 0 || !next.Equal(late) {
		t.Fatalf("all-old feed = %+v, %v, %v; want none, cursor held at since", got, next, err)
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

// Jam's reads (getJSON) surface a rate limit as a RateLimitError; at-cove's
// New keeps its old error for the same answer (F8: at-cove unchanged).
func TestRateLimitOnlyForJam(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /search/issues", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"message":"secondary rate limit"}`)
	})
	var rl *tracker.RateLimitError
	if _, err := newJamClient(t, mux).ListReady(context.Background()); !errors.As(err, &rl) || rl.RetryAfter != 30*time.Second {
		t.Fatalf("Jam ListReady = %v; want RateLimitError 30s", err)
	}
	cove := newJamClient(t, mux)
	cove.jam = false // what New builds
	_, err := cove.ListReady(context.Background())
	if errors.As(err, &rl) || err == nil || !strings.Contains(err.Error(), "githubissues: GET ") || !strings.Contains(err.Error(), `http 403: {"message":"secondary rate limit"}`) {
		t.Fatalf("at-cove ListReady = %v; want its old http 403 error", err)
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
