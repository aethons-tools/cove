package switchboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRESTClient_PollReversesAndAdvancesCursor(t *testing.T) {
	var gotAuth, gotAfter string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAfter = r.URL.Query().Get("after")
		// Discord returns newest-first
		json.NewEncoder(w).Encode([]map[string]any{
			{"id": "20", "content": "second", "author": map[string]any{"username": "u", "global_name": "Sam"}},
			{"id": "10", "content": "first", "author": map[string]any{"username": "u", "global_name": "Sam"}},
		})
	}))
	defer srv.Close()

	c := NewRESTClient("secrettoken", []string{"chan1"}, WithBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	msgs, cursors, err := c.Poll(context.Background(), map[string]string{"chan1": "5"})
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bot secrettoken" {
		t.Fatalf("auth header = %q", gotAuth)
	}
	if gotAfter != "5" {
		t.Fatalf("after param = %q", gotAfter)
	}
	if len(msgs) != 2 || msgs[0].Content != "first" || msgs[1].Content != "second" {
		t.Fatalf("messages not chronological: %+v", msgs)
	}
	if msgs[0].Author != "Sam" || msgs[0].Channel != "chan1" {
		t.Fatalf("msg fields = %+v", msgs[0])
	}
	if cursors["chan1"] != "20" {
		t.Fatalf("cursor = %q, want 20", cursors["chan1"])
	}
}

func TestRESTClient_Post(t *testing.T) {
	var body map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/channels/chan1/messages") || r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c := NewRESTClient("tok", []string{"chan1"}, WithBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	if err := c.Post(context.Background(), "chan1", "hello world"); err != nil {
		t.Fatal(err)
	}
	if body["content"] != "hello world" {
		t.Fatalf("posted body = %+v", body)
	}
}
