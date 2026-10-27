package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// askJam records the last request and answers per path.
type askJam struct{ method, path, body string }

func (j *askJam) env(t *testing.T) func(string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		j.method, j.path, j.body = r.Method, r.URL.RequestURI(), string(b)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/squawks/ask":
			_, _ = io.WriteString(w, `{"ask_id":"a1","version":1,"channel":{"id":"chn_x","kind":"session","label":"me"}}`)
		case "/squawks/ask/amend":
			_, _ = io.WriteString(w, `{"version":2}`)
		case "/squawks/ask/withdraw":
			w.WriteHeader(http.StatusNoContent)
		case "/squawks/asks":
			_, _ = io.WriteString(w, `{"asks":[{"ask_id":"a1","channel":{"id":"chn_x"},"question":"Merge?","options":[{"id":"yes","label":"Yes"},{"id":"no","label":"No"}],"version":1,"state":"answered","answer":{"version":1,"choice":"yes","by":"usr_x","at":"2026-10-21T00:00:00Z"}}]}`)
		case "/squawks":
			_, _ = io.WriteString(w, `{"squawks":[{"id":"s1","author":"alice","body":"alice chose Yes on: Merge?","parts":[{"type":"application/vnd.jam.ask+json","data":{"event":"answer","ask_id":"a1","version":1,"choice":"yes"}}]}]}`)
		}
	}))
	t.Cleanup(srv.Close)
	env := map[string]string{"JAM_RUNTIME_ADDR": srv.URL, "JAM_IDENTITY_TOKEN": "tok-A"}
	return func(k string) string { return env[k] }
}

func callTool(t *testing.T, sess *mcp.ClientSession, name string, args map[string]any) map[string]any {
	t.Helper()
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	if res.IsError {
		t.Fatalf("%s: tool error %+v", name, res.Content)
	}
	out, _ := res.StructuredContent.(map[string]any)
	return out
}

func TestMCPAskTools(t *testing.T) {
	j := &askJam{}
	sess := connectMCP(t, j.env(t))
	tools, err := sess.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range tools.Tools {
		names[tool.Name] = true
	}
	for _, n := range []string{"ask", "amend", "withdraw", "asks"} {
		if !names[n] {
			t.Fatalf("tool %s missing: %v", n, names)
		}
	}

	out := callTool(t, sess, "ask", map[string]any{"question": "Merge?", "options": []any{map[string]any{"id": "yes", "label": "Yes"}, map[string]any{"id": "no", "label": "No"}}, "of": []any{"user:alice"}})
	if j.method != http.MethodPost || j.path != "/squawks/ask" || !strings.Contains(j.body, `"question":"Merge?"`) || !strings.Contains(j.body, `"of":["user:alice"]`) || out["ask_id"] != "a1" {
		t.Fatalf("ask: %s %s %s → %v", j.method, j.path, j.body, out)
	}
	out = callTool(t, sess, "amend", map[string]any{"ask_id": "a1", "question": "Merge now?"})
	if j.path != "/squawks/ask/amend" || !strings.Contains(j.body, `"ask_id":"a1"`) || out["version"] != float64(2) {
		t.Fatalf("amend: %s %s → %v", j.path, j.body, out)
	}
	callTool(t, sess, "withdraw", map[string]any{"ask_id": "a1", "reason": "found it"})
	if j.path != "/squawks/ask/withdraw" || !strings.Contains(j.body, `"reason":"found it"`) {
		t.Fatalf("withdraw: %s %s", j.path, j.body)
	}
	out = callTool(t, sess, "asks", map[string]any{"state": "answered"})
	if j.method != http.MethodGet || j.path != "/squawks/asks?state=answered" {
		t.Fatalf("asks: %s %s", j.method, j.path)
	}
	b, _ := json.Marshal(out)
	if !strings.Contains(string(b), `"choice":"yes"`) {
		t.Fatalf("asks out %s", b)
	}
	out = callTool(t, sess, "read", map[string]any{})
	b, _ = json.Marshal(out)
	if !strings.Contains(string(b), `"type":"application/vnd.jam.ask+json"`) || !strings.Contains(string(b), `"choice":"yes"`) {
		t.Fatalf("read out %s", b)
	}
}

func mcpParams(name string, args map[string]any) *mcp.CallToolParams {
	return &mcp.CallToolParams{Name: name, Arguments: args}
}
