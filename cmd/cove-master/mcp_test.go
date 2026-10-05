package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeJam records what the mcp subcommand sent it and lets tests script a
// canned inbox or a failure status.
type fakeJam struct {
	gotAuth   string
	gotMethod string
	gotBody   map[string]any

	failStatus int         // if non-zero, ServeHTTP replies with this status instead
	inbox      []squawkOut // canned GET response
}

func (f *fakeJam) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.gotAuth = r.Header.Get("Authorization")
		f.gotMethod = r.Method
		if f.failStatus != 0 {
			w.WriteHeader(f.failStatus)
			return
		}
		switch r.Method {
		case http.MethodPost:
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			f.gotBody = body
			w.WriteHeader(http.StatusNoContent)
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"squawks": f.inbox})
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
}

// connectMCP wires the messaging server (built against the fake Jam via
// getenv) to a client over in-memory transports, per go-sdk v1.7.0's pattern:
// the server side must connect before the client initializes its session.
func connectMCP(t *testing.T, getenv func(string) string) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()

	srv := newMessagingServer(getenv)
	clientTransport, serverTransport := mcp.NewInMemoryTransports()

	serverSession, err := srv.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server Connect: %v", err)
	}
	t.Cleanup(func() { serverSession.Close() })

	cli := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0.0.1"}, nil)
	sess, err := cli.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client Connect: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess
}

func TestMCPListsReadAndSend(t *testing.T) {
	fh := &fakeJam{}
	backend := httptest.NewServer(fh.handler())
	defer backend.Close()

	env := map[string]string{
		"AT_JAM_RUNTIME_ADDR":   backend.URL,
		"AT_JAM_IDENTITY_TOKEN": "tok-A",
	}
	getenv := func(k string) string { return env[k] }

	sess := connectMCP(t, getenv)
	res, err := sess.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	names := map[string]bool{}
	for _, tool := range res.Tools {
		names[tool.Name] = true
	}
	if !names["read"] || !names["send"] || !names["list_targets"] || !names["commit"] || !names["escalate"] || !names["end"] || !names["idle_timeout"] || !names["alarm_set"] || !names["alarm_clear"] || !names["alarm_list"] {
		t.Fatalf("want read+send+list_targets+commit+escalate+end+idle_timeout+alarm_* tools, got %v", names)
	}
}

func TestMCPSendForwardsToJam(t *testing.T) {
	fh := &fakeJam{}
	backend := httptest.NewServer(fh.handler())
	defer backend.Close()

	env := map[string]string{
		"AT_JAM_RUNTIME_ADDR":   backend.URL,
		"AT_JAM_IDENTITY_TOKEN": "tok-A",
	}
	getenv := func(k string) string { return env[k] }

	sess := connectMCP(t, getenv)
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "send",
		Arguments: map[string]any{"text": "hi"},
	})
	if err != nil {
		t.Fatalf("CallTool(send): %v", err)
	}
	if res.IsError {
		t.Fatalf("send tool reported error: %+v", res.Content)
	}
	if fh.gotMethod != http.MethodPost {
		t.Fatalf("Jam saw method %q, want POST", fh.gotMethod)
	}
	if fh.gotAuth != "Bearer tok-A" {
		t.Fatalf("Jam saw Authorization %q, want Bearer tok-A", fh.gotAuth)
	}
	if fh.gotBody["body"] != "hi" {
		t.Fatalf("Jam saw body %v, want {body: hi}", fh.gotBody)
	}
}

func TestMCPReadReturnsInbox(t *testing.T) {
	fh := &fakeJam{inbox: []squawkOut{
		{ID: "m1", Author: "brent", Body: "hello", At: "2026-09-13T00:00:00Z"},
	}}
	backend := httptest.NewServer(fh.handler())
	defer backend.Close()

	env := map[string]string{
		"AT_JAM_RUNTIME_ADDR":   backend.URL,
		"AT_JAM_IDENTITY_TOKEN": "tok-B",
	}
	getenv := func(k string) string { return env[k] }

	sess := connectMCP(t, getenv)
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "read",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool(read): %v", err)
	}
	if res.IsError {
		t.Fatalf("read tool reported error: %+v", res.Content)
	}
	if fh.gotMethod != http.MethodGet {
		t.Fatalf("Jam saw method %q, want GET", fh.gotMethod)
	}
	if fh.gotAuth != "Bearer tok-B" {
		t.Fatalf("Jam saw Authorization %q, want Bearer tok-B", fh.gotAuth)
	}

	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	var out readOut
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal structured content: %v", err)
	}
	if len(out.Squawks) != 1 || out.Squawks[0].Body != "hello" {
		t.Fatalf("read result = %+v", out)
	}
}

func TestMCPSendForwardsTo(t *testing.T) {
	var gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	c, err := newMessagingClient(func(k string) string {
		switch k {
		case "AT_JAM_RUNTIME_ADDR":
			return srv.URL
		case "AT_JAM_IDENTITY_TOKEN":
			return "tok"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.send(context.Background(), "hi", "human:alice", ""); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/squawks" || !strings.Contains(gotBody, `"to":"human:alice"`) || !strings.Contains(gotBody, `"body":"hi"`) {
		t.Fatalf("path=%q body=%q", gotPath, gotBody)
	}
}

func TestMCPListTargets(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/squawks/targets" || r.Method != http.MethodGet {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"targets":[{"target":"human:alice","kind":"human","name":"alice"}]}`))
	}))
	defer srv.Close()
	c, _ := newMessagingClient(func(k string) string {
		switch k {
		case "AT_JAM_RUNTIME_ADDR":
			return srv.URL
		case "AT_JAM_IDENTITY_TOKEN":
			return "tok"
		}
		return ""
	})
	out, err := c.listTargets(context.Background())
	if err != nil || len(out.Targets) != 1 || out.Targets[0].Target != "human:alice" {
		t.Fatalf("out=%+v err=%v", out, err)
	}
}

func TestMCPReadNoParamsSendsNoQuery(t *testing.T) {
	var gotURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.String()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"squawks":[]}`))
	}))
	defer srv.Close()
	c, err := newMessagingClient(func(k string) string {
		switch k {
		case "AT_JAM_RUNTIME_ADDR":
			return srv.URL
		case "AT_JAM_IDENTITY_TOKEN":
			return "tok"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.read(context.Background(), readIn{}); err != nil {
		t.Fatal(err)
	}
	if gotURL != "/squawks" {
		t.Fatalf("url=%q, want /squawks with no query string", gotURL)
	}
}

func TestMCPReadWithSeekParamsSendsQuery(t *testing.T) {
	var gotURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.String()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"squawks":[],"committed_cursor":"m9","page_first":"m1","page_last":"m2"}`))
	}))
	defer srv.Close()
	c, err := newMessagingClient(func(k string) string {
		switch k {
		case "AT_JAM_RUNTIME_ADDR":
			return srv.URL
		case "AT_JAM_IDENTITY_TOKEN":
			return "tok"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.read(context.Background(), readIn{Anchor: "end", Dir: "backward", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(gotURL)
	if err != nil {
		t.Fatalf("parse gotURL: %v", err)
	}
	if u.Path != "/squawks" {
		t.Fatalf("path=%q, want /squawks", u.Path)
	}
	q := u.Query()
	if q.Get("anchor") != "end" || q.Get("dir") != "backward" || q.Get("limit") != "10" {
		t.Fatalf("query=%q, want anchor=end dir=backward limit=10", u.RawQuery)
	}
	if out.CommittedCursor != "m9" || out.PageFirst != "m1" || out.PageLast != "m2" {
		t.Fatalf("out=%+v", out)
	}
}

func TestMCPReadWithIDAnchorSendsQuery(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"squawks":[]}`))
	}))
	defer srv.Close()
	c, err := newMessagingClient(func(k string) string {
		switch k {
		case "AT_JAM_RUNTIME_ADDR":
			return srv.URL
		case "AT_JAM_IDENTITY_TOKEN":
			return "tok"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.read(context.Background(), readIn{Anchor: "id", ID: "m5"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotQuery, "anchor=id") || !strings.Contains(gotQuery, "id=m5") {
		t.Fatalf("query=%q, want anchor=id and id=m5", gotQuery)
	}
}

func TestMCPCommitPostsUpTo(t *testing.T) {
	var gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"committed_cursor":"m5"}`))
	}))
	defer srv.Close()
	c, err := newMessagingClient(func(k string) string {
		switch k {
		case "AT_JAM_RUNTIME_ADDR":
			return srv.URL
		case "AT_JAM_IDENTITY_TOKEN":
			return "tok"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.commit(context.Background(), "m5")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/squawks/commit" || !strings.Contains(gotBody, `"up_to":"m5"`) {
		t.Fatalf("path=%q body=%q", gotPath, gotBody)
	}
	if out.CommittedCursor != "m5" {
		t.Fatalf("out=%+v", out)
	}
}

func TestMCPCommitToolForwardsToJam(t *testing.T) {
	var gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"committed_cursor":"m7"}`))
	}))
	defer srv.Close()

	env := map[string]string{
		"AT_JAM_RUNTIME_ADDR":   srv.URL,
		"AT_JAM_IDENTITY_TOKEN": "tok-C",
	}
	getenv := func(k string) string { return env[k] }

	sess := connectMCP(t, getenv)
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "commit",
		Arguments: map[string]any{"up_to": "m7"},
	})
	if err != nil {
		t.Fatalf("CallTool(commit): %v", err)
	}
	if res.IsError {
		t.Fatalf("commit tool reported error: %+v", res.Content)
	}
	if gotPath != "/squawks/commit" || !strings.Contains(gotBody, `"up_to":"m7"`) {
		t.Fatalf("path=%q body=%q", gotPath, gotBody)
	}

	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	var out commitOut
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal structured content: %v", err)
	}
	if out.CommittedCursor != "m7" {
		t.Fatalf("commit result = %+v", out)
	}
}

func TestMCPEscalateForwardsCategory(t *testing.T) {
	var gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	c, err := newMessagingClient(func(k string) string {
		switch k {
		case "AT_JAM_RUNTIME_ADDR":
			return srv.URL
		case "AT_JAM_IDENTITY_TOKEN":
			return "tok"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.escalate(context.Background(), "infra"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/escalate" || !strings.Contains(gotBody, `"category":"infra"`) {
		t.Fatalf("path=%q body=%q", gotPath, gotBody)
	}
}

func turnEndClient(t *testing.T, gotMethod, gotPath, gotBody *string) *messagingClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*gotMethod, *gotPath = r.Method, r.URL.Path
		b, _ := io.ReadAll(r.Body)
		*gotBody = string(b)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	c, err := newMessagingClient(func(k string) string {
		switch k {
		case "AT_JAM_RUNTIME_ADDR":
			return srv.URL
		case "AT_JAM_IDENTITY_TOKEN":
			return "tok"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestMCPEndForwardsReason(t *testing.T) {
	var m, p, b string
	c := turnEndClient(t, &m, &p, &b)
	if err := c.end(context.Background(), "merged"); err != nil {
		t.Fatal(err)
	}
	if m != "POST" || p != "/end" || !strings.Contains(b, `"reason":"merged"`) {
		t.Fatalf("%s %s %s", m, p, b)
	}
}

func TestMCPIdleTimeoutForwards(t *testing.T) {
	var m, p, b string
	c := turnEndClient(t, &m, &p, &b)
	if err := c.idleTimeout(context.Background(), "45m", "next"); err != nil {
		t.Fatal(err)
	}
	if m != "PUT" || p != "/idle" || !strings.Contains(b, `"duration":"45m"`) || !strings.Contains(b, `"scope":"next"`) {
		t.Fatalf("%s %s %s", m, p, b)
	}
}

func TestMCPAlarmSetForwards(t *testing.T) {
	var m, p, b string
	c := turnEndClient(t, &m, &p, &b)
	if err := c.setAlarm(context.Background(), "pr-watch", "*/5 * * * *", "check"); err != nil {
		t.Fatal(err)
	}
	if m != "PUT" || p != "/alarms/pr-watch" || !strings.Contains(b, `"schedule":"*/5 * * * *"`) || !strings.Contains(b, `"note":"check"`) {
		t.Fatalf("%s %s %s", m, p, b)
	}
}

func TestMCPAlarmClearForwards(t *testing.T) {
	var m, p, b string
	c := turnEndClient(t, &m, &p, &b)
	if err := c.clearAlarm(context.Background(), "pr-watch"); err != nil {
		t.Fatal(err)
	}
	if m != "DELETE" || p != "/alarms/pr-watch" {
		t.Fatalf("%s %s", m, p)
	}
}

func TestMCPAlarmListDecodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"alarms":[{"name":"nightly","schedule":"0 2 * * *","note":"backup","next_at":"2026-10-06T02:00:00Z"}]}`)
	}))
	defer srv.Close()
	c, err := newMessagingClient(func(k string) string {
		return map[string]string{"AT_JAM_RUNTIME_ADDR": srv.URL, "AT_JAM_IDENTITY_TOKEN": "tok"}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.listAlarms(context.Background())
	if err != nil || len(out.Alarms) != 1 || out.Alarms[0].Name != "nightly" || out.Alarms[0].NextAt != "2026-10-06T02:00:00Z" {
		t.Fatalf("out=%+v err=%v", out, err)
	}
}

func TestMCPNonTwoXXIsToolErrorWithoutToken(t *testing.T) {
	fh := &fakeJam{failStatus: http.StatusInternalServerError}
	backend := httptest.NewServer(fh.handler())
	defer backend.Close()

	const secretToken = "super-secret-token-value"
	env := map[string]string{
		"AT_JAM_RUNTIME_ADDR":   backend.URL,
		"AT_JAM_IDENTITY_TOKEN": secretToken,
	}
	getenv := func(k string) string { return env[k] }

	sess := connectMCP(t, getenv)
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "send",
		Arguments: map[string]any{"text": "hi"},
	})
	if err != nil {
		t.Fatalf("CallTool(send) protocol error: %v", err)
	}
	if !res.IsError {
		t.Fatal("want a tool-level error on a non-2xx Jam response")
	}
	var text strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text.WriteString(tc.Text)
		}
	}
	if strings.Contains(text.String(), secretToken) {
		t.Fatalf("tool error leaked the token: %q", text.String())
	}
}

func TestMCPSendForwardsContentTypeAndReadReturnsIt(t *testing.T) {
	fh := &fakeJam{inbox: []squawkOut{
		{ID: "m1", Author: "brent", Body: "a_b_c", At: "2026-09-13T00:00:00Z", ContentType: "text/plain"},
	}}
	backend := httptest.NewServer(fh.handler())
	defer backend.Close()
	env := map[string]string{"AT_JAM_RUNTIME_ADDR": backend.URL, "AT_JAM_IDENTITY_TOKEN": "tok-A"}
	sess := connectMCP(t, func(k string) string { return env[k] })

	// Default: no content_type sent (Jam defaults to markdown).
	if _, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "send", Arguments: map[string]any{"text": "**hi**"}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := fh.gotBody["content_type"]; ok {
		t.Fatalf("default send should omit content_type; Jam saw %v", fh.gotBody)
	}
	// Opt-out: forwarded as-is.
	if _, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "send", Arguments: map[string]any{"text": "2 * 3", "content_type": "text/plain"}}); err != nil {
		t.Fatal(err)
	}
	if fh.gotBody["content_type"] != "text/plain" {
		t.Fatalf("Jam saw %v, want content_type text/plain", fh.gotBody)
	}
	// read surfaces each message's content type.
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "read", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(res.StructuredContent)
	if !strings.Contains(string(raw), `"content_type":"text/plain"`) {
		t.Fatalf("read output %s should carry content_type", raw)
	}
}
