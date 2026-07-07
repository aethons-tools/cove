package github

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

func TestOpenPRCreates(t *testing.T) {
	var body map[string]any
	var auth string
	c := New("tok", &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		auth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		return resp(201, `{"html_url":"https://github.com/o/r/pull/7"}`), nil
	})})
	url, err := c.OpenPR(context.Background(), "o/r", "main", "implement/AET-1", "AET-1: T", "the body")
	if err != nil {
		t.Fatalf("OpenPR: %v", err)
	}
	if url != "https://github.com/o/r/pull/7" {
		t.Fatalf("url = %q", url)
	}
	if auth != "Bearer tok" {
		t.Fatalf("auth = %q; want Bearer tok", auth)
	}
	if body["head"] != "implement/AET-1" || body["base"] != "main" || body["title"] != "AET-1: T" {
		t.Fatalf("request body wrong: %v", body)
	}
}

func TestOpenPRReturnsExistingOn422(t *testing.T) {
	calls := 0
	c := New("tok", &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 { // create → already exists
			return resp(422, `{"message":"Validation Failed","errors":[{"message":"A pull request already exists"}]}`), nil
		}
		// lookup existing open PR for the head
		return resp(200, `[{"html_url":"https://github.com/o/r/pull/3"}]`), nil
	})})
	url, err := c.OpenPR(context.Background(), "o/r", "main", "implement/AET-1", "t", "b")
	if err != nil {
		t.Fatalf("OpenPR: %v", err)
	}
	if url != "https://github.com/o/r/pull/3" {
		t.Fatalf("url = %q; want the existing PR", url)
	}
}
