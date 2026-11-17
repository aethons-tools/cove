package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/aethons-tools/cove/internal/jam"
)

// observeJam is a fake Jam's observe routes: it records the last request
// and answers each view with a recognizable body; session "denied" gets 403
// and "nobody" 404.
type observeJam struct{ method, uri string }

func (j *observeJam) env(t *testing.T, observe bool) func(string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		j.method, j.uri = r.Method, r.URL.RequestURI()
		switch {
		case strings.Contains(r.URL.Path, "/denied/"):
			http.Error(w, "the role does not observe sessions (scope.observe)", http.StatusForbidden)
		case strings.Contains(r.URL.Path, "/nobody/"):
			http.Error(w, "unknown session", http.StatusNotFound)
		case r.URL.Query().Get("dir") == "sideways":
			http.Error(w, "dir: forward or backward", http.StatusBadRequest)
		case r.URL.Path == "/squawks/observe/sessions":
			_, _ = io.WriteString(w, `{"sessions":[{"id":"ses_1","label":"impl-7"}]}`)
		case strings.HasSuffix(r.URL.Path, "/activity"):
			_, _ = io.WriteString(w, `{"messages":[{"body":"hi","out":true}]}`)
		case strings.HasSuffix(r.URL.Path, "/streams"):
			_, _ = io.WriteString(w, `{"streams":[{"stream_id":"s1"}]}`)
		case strings.HasSuffix(r.URL.Path, "/events"):
			_, _ = io.WriteString(w, `{"stream_id":"s1","events":[{"type":"result"}],"next_after_seq":9,"more":false}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	env := map[string]string{"JAM_RUNTIME_ADDR": srv.URL, "JAM_IDENTITY_TOKEN": "tok-A", "HOME": t.TempDir()}
	if observe {
		env["JAM_OBSERVE"] = "1"
	}
	return func(k string) string { return env[k] }
}

func toolNames(t *testing.T, sess *mcp.ClientSession) []string {
	t.Helper()
	res, err := sess.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, tool := range res.Tools {
		got = append(got, tool.Name)
	}
	slices.Sort(got)
	return got
}

// The observe tools are offered only with JAM_OBSERVE=1 (a role with
// scope.observe), and are exactly jam.ObserveTools.
func TestObserveToolsMatchJam(t *testing.T) {
	without := toolNames(t, connectMCP(t, (&observeJam{}).env(t, false)))
	for _, n := range jam.ObserveTools {
		if slices.Contains(without, n) {
			t.Fatalf("%s offered without the grant", n)
		}
	}
	with := toolNames(t, connectMCP(t, (&observeJam{}).env(t, true)))
	var added []string
	for _, n := range with {
		if !slices.Contains(without, n) {
			added = append(added, n)
		}
	}
	if !slices.Equal(added, jam.ObserveTools) {
		t.Fatalf("added tools = %v, want %v", added, jam.ObserveTools)
	}
}

func toolText(t *testing.T, sess *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String(), res.IsError
}

func TestObserveToolsCallJam(t *testing.T) {
	j := &observeJam{}
	sess := connectMCP(t, j.env(t, true))
	for _, c := range []struct {
		tool    string
		args    map[string]any
		uri     string
		answers string
	}{
		{"observe_sessions", map[string]any{}, "/squawks/observe/sessions", `"label":"impl-7"`},
		{"observe_sessions", map[string]any{"since": "2026-11-15T00:00:00Z", "until": "2026-11-16T00:00:00Z"},
			"/squawks/observe/sessions?since=2026-11-15T00%3A00%3A00Z&until=2026-11-16T00%3A00%3A00Z", `"sessions"`},
		{"observe_activity", map[string]any{"session": "ses_1"}, "/squawks/observe/sessions/ses_1/activity", `"out":true`},
		{"observe_activity", map[string]any{"session": "ses_1", "anchor": "id", "id": "m1", "dir": "backward", "limit": 5},
			"/squawks/observe/sessions/ses_1/activity?anchor=id&dir=backward&id=m1&limit=5", `"messages"`},
		{"observe_streams", map[string]any{"session": "ses_1"}, "/squawks/observe/sessions/ses_1/streams", `"stream_id":"s1"`},
		{"observe_events", map[string]any{"session": "ses_1", "stream": "s1", "after_seq": 4, "limit": 10, "types": []string{"result", "assistant"}},
			"/squawks/observe/sessions/ses_1/events?after_seq=4&limit=10&stream=s1&types=result%2Cassistant", `"next_after_seq":9`},
	} {
		text, isErr := toolText(t, sess, c.tool, c.args)
		if isErr || !strings.Contains(text, c.answers) || j.method != http.MethodGet || j.uri != c.uri {
			t.Errorf("%s %v: %s %s err=%v → %s", c.tool, c.args, j.method, j.uri, isErr, text)
		}
	}
	for _, c := range []struct {
		tool, session string
		args          map[string]any
		want          string
	}{
		{"observe_streams", "denied", nil, "scope.observe"},
		{"observe_streams", "nobody", nil, "unknown session"},
		{"observe_activity", "ses_1", map[string]any{"dir": "sideways"}, "dir: forward or backward"},
		{"observe_streams", "../x", nil, "not a session id"},
		{"observe_streams", "", nil, "session is required"},
	} {
		args := map[string]any{"session": c.session}
		for k, v := range c.args {
			args[k] = v
		}
		if text, isErr := toolText(t, sess, c.tool, args); !isErr || !strings.Contains(text, c.want) {
			t.Errorf("%s %v: err=%v %s, want %q", c.tool, args, isErr, text, c.want)
		}
	}
}
