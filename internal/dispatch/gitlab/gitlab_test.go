package gitlab

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func resp(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func TestOpenMRCreates(t *testing.T) {
	var gotURL, gotTok string
	var body map[string]any
	c := New("tok", "gitlab.example.com", &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		gotURL = r.URL.String()
		gotTok = r.Header.Get("PRIVATE-TOKEN")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		return resp(201, `{"web_url":"https://gitlab.example.com/g/app/-/merge_requests/5"}`), nil
	})})
	url, err := c.OpenPR(context.Background(), "g/sub/app", "main", "implement/AET-1", "AET-1: T", "the body")
	if err != nil {
		t.Fatalf("OpenPR: %v", err)
	}
	if url != "https://gitlab.example.com/g/app/-/merge_requests/5" {
		t.Fatalf("url = %q", url)
	}
	if gotTok != "tok" {
		t.Fatalf("PRIVATE-TOKEN = %q", gotTok)
	}
	if !strings.Contains(gotURL, "https://gitlab.example.com/api/v4/projects/g%2Fsub%2Fapp/merge_requests") {
		t.Fatalf("request URL = %q", gotURL)
	}
	if body["source_branch"] != "implement/AET-1" || body["target_branch"] != "main" || body["title"] != "AET-1: T" || body["description"] != "the body" {
		t.Fatalf("body = %v", body)
	}
}

func TestOpenMRReturnsExistingOn409(t *testing.T) {
	calls := 0
	c := New("tok", "gitlab.com", &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return resp(409, `{"message":["Another open merge request already exists for this source branch"]}`), nil
		}
		return resp(200, `[{"web_url":"https://gitlab.com/g/app/-/merge_requests/3"}]`), nil
	})})
	url, err := c.OpenPR(context.Background(), "g/app", "main", "implement/AET-1", "t", "b")
	if err != nil {
		t.Fatalf("OpenPR: %v", err)
	}
	if url != "https://gitlab.com/g/app/-/merge_requests/3" {
		t.Fatalf("url = %q", url)
	}
	if calls != 2 {
		t.Fatalf("expected create+lookup, got %d calls", calls)
	}
}

func TestOpenMRErrorSurfaces(t *testing.T) {
	c := New("tok", "gitlab.com", &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		return resp(400, `{"message":"bad"}`), nil
	})})
	if _, err := c.OpenPR(context.Background(), "g/app", "main", "h", "t", "b"); err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("want http 400 error, got %v", err)
	}
}
