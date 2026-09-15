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

// fakeHarbor records what the mcp subcommand sent it and lets tests script a
// canned inbox or a failure status.
type fakeHarbor struct {
	gotAuth   string
	gotMethod string
	gotBody   map[string]any

	failStatus int          // if non-zero, ServeHTTP replies with this status instead
	inbox      []messageOut // canned GET response
}

func (f *fakeHarbor) handler() http.HandlerFunc {
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
			_ = json.NewEncoder(w).Encode(map[string]any{"messages": f.inbox})
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
}

// connectMCP wires the messaging server (built against the fake harbor via
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
	fh := &fakeHarbor{}
	backend := httptest.NewServer(fh.handler())
	defer backend.Close()

	env := map[string]string{
		"AT_HARBOR_RUNTIME_ADDR":   backend.URL,
		"AT_HARBOR_IDENTITY_TOKEN": "tok-A",
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
	if !names["read"] || !names["send"] || !names["list_targets"] || !names["commit"] {
		t.Fatalf("want read+send+list_targets+commit tools, got %v", names)
	}
}

func TestMCPSendForwardsToHarbor(t *testing.T) {
	fh := &fakeHarbor{}
	backend := httptest.NewServer(fh.handler())
	defer backend.Close()

	env := map[string]string{
		"AT_HARBOR_RUNTIME_ADDR":   backend.URL,
		"AT_HARBOR_IDENTITY_TOKEN": "tok-A",
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
		t.Fatalf("harbor saw method %q, want POST", fh.gotMethod)
	}
	if fh.gotAuth != "Bearer tok-A" {
		t.Fatalf("harbor saw Authorization %q, want Bearer tok-A", fh.gotAuth)
	}
	if fh.gotBody["body"] != "hi" {
		t.Fatalf("harbor saw body %v, want {body: hi}", fh.gotBody)
	}
}

func TestMCPReadReturnsInbox(t *testing.T) {
	fh := &fakeHarbor{inbox: []messageOut{
		{ID: "m1", Author: "brent", Body: "hello", At: "2026-09-13T00:00:00Z"},
	}}
	backend := httptest.NewServer(fh.handler())
	defer backend.Close()

	env := map[string]string{
		"AT_HARBOR_RUNTIME_ADDR":   backend.URL,
		"AT_HARBOR_IDENTITY_TOKEN": "tok-B",
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
		t.Fatalf("harbor saw method %q, want GET", fh.gotMethod)
	}
	if fh.gotAuth != "Bearer tok-B" {
		t.Fatalf("harbor saw Authorization %q, want Bearer tok-B", fh.gotAuth)
	}

	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	var out readOut
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal structured content: %v", err)
	}
	if len(out.Messages) != 1 || out.Messages[0].Body != "hello" {
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
		case "AT_HARBOR_RUNTIME_ADDR":
			return srv.URL
		case "AT_HARBOR_IDENTITY_TOKEN":
			return "tok"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.send(context.Background(), "hi", "human:alice"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/messages" || !strings.Contains(gotBody, `"to":"human:alice"`) || !strings.Contains(gotBody, `"body":"hi"`) {
		t.Fatalf("path=%q body=%q", gotPath, gotBody)
	}
}

func TestMCPListTargets(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages/targets" || r.Method != http.MethodGet {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"targets":[{"target":"human:alice","kind":"human","name":"alice"}]}`))
	}))
	defer srv.Close()
	c, _ := newMessagingClient(func(k string) string {
		switch k {
		case "AT_HARBOR_RUNTIME_ADDR":
			return srv.URL
		case "AT_HARBOR_IDENTITY_TOKEN":
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
		_, _ = w.Write([]byte(`{"messages":[]}`))
	}))
	defer srv.Close()
	c, err := newMessagingClient(func(k string) string {
		switch k {
		case "AT_HARBOR_RUNTIME_ADDR":
			return srv.URL
		case "AT_HARBOR_IDENTITY_TOKEN":
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
	if gotURL != "/messages" {
		t.Fatalf("url=%q, want /messages with no query string", gotURL)
	}
}

func TestMCPReadWithSeekParamsSendsQuery(t *testing.T) {
	var gotURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.String()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"messages":[],"committed_cursor":"m9","page_first":"m1","page_last":"m2"}`))
	}))
	defer srv.Close()
	c, err := newMessagingClient(func(k string) string {
		switch k {
		case "AT_HARBOR_RUNTIME_ADDR":
			return srv.URL
		case "AT_HARBOR_IDENTITY_TOKEN":
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
	if u.Path != "/messages" {
		t.Fatalf("path=%q, want /messages", u.Path)
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
		_, _ = w.Write([]byte(`{"messages":[]}`))
	}))
	defer srv.Close()
	c, err := newMessagingClient(func(k string) string {
		switch k {
		case "AT_HARBOR_RUNTIME_ADDR":
			return srv.URL
		case "AT_HARBOR_IDENTITY_TOKEN":
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
		case "AT_HARBOR_RUNTIME_ADDR":
			return srv.URL
		case "AT_HARBOR_IDENTITY_TOKEN":
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
	if gotPath != "/messages/commit" || !strings.Contains(gotBody, `"up_to":"m5"`) {
		t.Fatalf("path=%q body=%q", gotPath, gotBody)
	}
	if out.CommittedCursor != "m5" {
		t.Fatalf("out=%+v", out)
	}
}

func TestMCPCommitToolForwardsToHarbor(t *testing.T) {
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
		"AT_HARBOR_RUNTIME_ADDR":   srv.URL,
		"AT_HARBOR_IDENTITY_TOKEN": "tok-C",
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
	if gotPath != "/messages/commit" || !strings.Contains(gotBody, `"up_to":"m7"`) {
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
		case "AT_HARBOR_RUNTIME_ADDR":
			return srv.URL
		case "AT_HARBOR_IDENTITY_TOKEN":
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

func TestMCPNonTwoXXIsToolErrorWithoutToken(t *testing.T) {
	fh := &fakeHarbor{failStatus: http.StatusInternalServerError}
	backend := httptest.NewServer(fh.handler())
	defer backend.Close()

	const secretToken = "super-secret-token-value"
	env := map[string]string{
		"AT_HARBOR_RUNTIME_ADDR":   backend.URL,
		"AT_HARBOR_IDENTITY_TOKEN": secretToken,
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
		t.Fatal("want a tool-level error on a non-2xx harbor response")
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
