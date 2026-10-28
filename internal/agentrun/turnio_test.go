package agentrun

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type seenReq struct {
	method, path, query, auth string
	body                      map[string]any
}

func turnIOServer(t *testing.T, status map[string]int, reply map[string]string) (*httptest.Server, *[]seenReq) {
	t.Helper()
	var seen []seenReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		if len(b) > 0 {
			if err := json.Unmarshal(b, &body); err != nil {
				t.Errorf("%s %s: body not JSON: %s", r.Method, r.URL.Path, b)
			}
		}
		seen = append(seen, seenReq{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), body})
		if s, ok := status[r.URL.Path]; ok {
			w.WriteHeader(s)
			return
		}
		if out, ok := reply[r.URL.Path]; ok {
			_, _ = io.WriteString(w, out)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func TestHTTPTurnIO(t *testing.T) {
	srv, seen := turnIOServer(t, nil, map[string]string{
		"/squawks": `{"squawks":[{"id":"m1","author":"you","body":"hi","at":"2026-10-28T00:00:00Z","channel":{"id":"chn_1","kind":"session","label":"me"}},` +
			`{"id":"m2","author":"you","body":"again","at":"2026-10-28T00:01:00Z","channel":{"id":"chn_1","kind":"session","label":"me"}}]}`,
	})
	io := HTTPTurnIO(srv.URL, "tok")
	ctx := context.Background()

	es, more, err := io.Inbox(ctx, 1)
	if err != nil || len(es) != 1 || !more || es[0].ID != "m1" || es[0].Channel.Label != "me" {
		t.Fatalf("Inbox(1) = %+v, %v, %v", es, more, err)
	}
	for _, step := range []func() error{
		func() error { return io.Post(ctx, "Done.", "") },
		func() error { return io.Post(ctx, "Elsewhere", "user:x") },
		func() error { return io.Commit(ctx, "m1") },
		func() error { return io.End(ctx, "finished") },
		func() error { return io.SetAlarm(ctx, "turn-end", "2026-10-28T01:00:00Z", "ask about the preview") },
		func() error { return io.SetIdle(ctx, "2h") },
		func() error { return io.Mute(ctx, true) },
		func() error { return io.SetKept(ctx, 2) },
	} {
		if err := step(); err != nil {
			t.Fatal(err)
		}
	}
	want := []struct {
		method, path string
		body         map[string]any
	}{
		{"GET", "/squawks", nil},
		{"POST", "/squawks", map[string]any{"body": "Done.", "auto": true}},
		{"POST", "/squawks", map[string]any{"body": "Elsewhere", "to": "user:x", "auto": true}},
		{"POST", "/squawks/commit", map[string]any{"up_to": "m1"}},
		{"POST", "/end", map[string]any{"reason": "finished"}},
		{"PUT", "/alarms/turn-end", map[string]any{"schedule": "2026-10-28T01:00:00Z", "note": "ask about the preview"}},
		{"PUT", "/idle", map[string]any{"duration": "2h", "scope": "next"}},
		{"PUT", "/mute", map[string]any{"on": true}},
		{"PUT", "/kept", map[string]any{"count": float64(2)}},
	}
	if len(*seen) != len(want) {
		t.Fatalf("requests = %+v", *seen)
	}
	for i, w := range want {
		g := (*seen)[i]
		if g.method != w.method || g.path != w.path || g.auth != "Bearer tok" {
			t.Errorf("request %d = %s %s (auth %q), want %s %s", i, g.method, g.path, g.auth, w.method, w.path)
		}
		for k, v := range w.body {
			if g.body[k] != v {
				t.Errorf("request %d body[%s] = %v, want %v", i, k, g.body[k], v)
			}
		}
	}
	if q := (*seen)[0].query; !strings.Contains(q, "limit=2") {
		t.Errorf("Inbox query = %q, want limit=2 (one past the cap, to learn whether more wait)", q)
	}
}

func TestHTTPTurnIOMuteUnsupported(t *testing.T) {
	srv, _ := turnIOServer(t, map[string]int{"/mute": http.StatusNotFound, "/squawks/commit": http.StatusBadRequest}, nil)
	io := HTTPTurnIO(srv.URL, "tok")
	if err := io.Mute(context.Background(), true); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Mute on an older Jam = %v, want ErrUnsupported", err)
	}
	if err := io.Commit(context.Background(), "m1"); err == nil || errors.Is(err, ErrUnsupported) {
		t.Fatalf("Commit 400 = %v, want a plain error", err)
	}
}
