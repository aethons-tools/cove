package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	if !names["read"] || !names["send"] {
		t.Fatalf("want read+send tools, got %v", names)
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
