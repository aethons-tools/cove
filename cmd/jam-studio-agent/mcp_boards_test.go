package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

type boardJam struct{ method, path, body string }

func (j *boardJam) env(t *testing.T) func(string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		j.method, j.path, j.body = r.Method, r.URL.RequestURI(), string(b)
		switch {
		case r.URL.Path == "/squawks/boards" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"board_id":"brd_01j9q3aaaaaaaaaaaaaaaaaaaa","channel":"chn_x"}`)
		case r.URL.Path == "/squawks/boards":
			_, _ = io.WriteString(w, `{"boards":[{"board_id":"brd_01j9q3aaaaaaaaaaaaaaaaaaaa","channel":{"id":"chn_x"},"title":"Arch","seq":3}]}`)
		case strings.HasSuffix(r.URL.Path, "/read"):
			_, _ = io.WriteString(w, `{"seq":3,"elements":[{"id":"api","type":"rectangle","x":0,"y":0,"w":120,"h":60,"text":"API"}]}`)
		case strings.HasSuffix(r.URL.Path, "/apply"):
			_, _ = io.WriteString(w, `{"seq":4,"added":["db"]}`)
		case strings.HasSuffix(r.URL.Path, "/snapshot.png"):
			w.Header().Set("Content-Type", "image/png")
			w.Header().Set("X-Board-Seq", "4")
			_, _ = io.WriteString(w, "\x89PNG fake")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	env := map[string]string{"JAM_RUNTIME_ADDR": srv.URL, "JAM_IDENTITY_TOKEN": "tok-A", "HOME": t.TempDir()}
	return func(k string) string { return env[k] }
}

func TestMCPBoardTools(t *testing.T) {
	j := &boardJam{}
	getenv := j.env(t)
	sess := connectMCP(t, getenv)
	const id = "brd_01j9q3aaaaaaaaaaaaaaaaaaaa"
	if out := callTool(t, sess, "board_create", map[string]any{"title": "Arch"}); out["board_id"] != id || !strings.Contains(j.body, `"title":"Arch"`) {
		t.Fatalf("create = %v (%s)", out, j.body)
	}
	if out := callTool(t, sess, "boards", map[string]any{}); !strings.Contains(mustJSON(out), `"title":"Arch"`) {
		t.Fatalf("boards = %v", out)
	}
	if out := callTool(t, sess, "board_read", map[string]any{"board_id": id}); !strings.Contains(mustJSON(out), `"text":"API"`) || j.path != "/squawks/boards/"+id+"/read" {
		t.Fatalf("read = %v %s", out, j.path)
	}
	out := callTool(t, sess, "board_apply", map[string]any{"board_id": id, "ops": []any{map[string]any{"op": "add", "type": "rectangle", "id": "db", "x": 300, "y": 0, "text": "DB"}}})
	if j.method != http.MethodPost || !strings.Contains(j.body, `"ops":[{"op":"add"`) || !strings.Contains(mustJSON(out), `"added":["db"]`) {
		t.Fatalf("apply = %v %s", out, j.body)
	}
	out = callTool(t, sess, "board_snapshot", map[string]any{"board_id": id})
	p, _ := out["path"].(string)
	if want := filepath.Join(getenv("HOME"), "boards", id+"-4.png"); p != want {
		t.Fatalf("snapshot path %q, want %q", p, want)
	}
	if b, _ := os.ReadFile(p); !strings.HasPrefix(string(b), "\x89PNG") {
		t.Fatal("snapshot bytes")
	}
	if res, err := sess.CallTool(context.Background(), mcpParams("board_snapshot", map[string]any{"board_id": id})); err == nil && !res.IsError {
		t.Fatal("a second snapshot at the same seq must not overwrite")
	}
	if res, err := sess.CallTool(context.Background(), mcpParams("board_read", map[string]any{"board_id": "../x"})); err == nil && !res.IsError {
		t.Fatal("a bad board id should fail")
	}
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestTrimReasonAndSeq(t *testing.T) {
	if s := trimReason([]byte(strings.Repeat("é", 200))); !utf8.ValidString(s) || len(s) > 300 {
		t.Fatalf("trimReason cut a character: %q", s)
	}
	if digits.MatchString("") || !digits.MatchString("42") {
		t.Fatal("seq check")
	}
}
