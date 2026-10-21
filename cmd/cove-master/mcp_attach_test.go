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
)

// attachJam is a fake Jam for uploads, sends and blob downloads.
type attachJam struct {
	uploads []string // X-Filename of each upload
	bodies  []string
	send    map[string]any
}

func (j *attachJam) env(t *testing.T) func(string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/squawks/uploads":
			b, _ := io.ReadAll(r.Body)
			j.uploads, j.bodies = append(j.uploads, r.Header.Get("X-Filename")), append(j.bodies, string(b))
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"ref":"blb_`+strings.Repeat("0", 25)+string(rune('a'+len(j.uploads)))+`","size":3}`)
		case r.URL.Path == "/squawks" && r.Method == http.MethodPost:
			_ = json.NewDecoder(r.Body).Decode(&j.send)
			_, _ = io.WriteString(w, `{"id":"s1","channel":{"id":"chn_x"}}`)
		case strings.HasPrefix(r.URL.Path, "/squawks/blobs/"):
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Content-Disposition", `attachment; filename="../build.log"`)
			_, _ = io.WriteString(w, "log lines\n")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	env := map[string]string{"AT_JAM_RUNTIME_ADDR": srv.URL, "AT_JAM_IDENTITY_TOKEN": "tok-A", "HOME": t.TempDir()}
	return func(k string) string { return env[k] }
}

func TestMCPSendAttachUploadsThenSends(t *testing.T) {
	j := &attachJam{}
	getenv := j.env(t)
	dir := t.TempDir()
	a, b := filepath.Join(dir, "shot.png"), filepath.Join(dir, "notes.txt")
	_ = os.WriteFile(a, []byte("PNG"), 0o600)
	_ = os.WriteFile(b, []byte("hi"), 0o600)
	sess := connectMCP(t, getenv)
	callTool(t, sess, "send", map[string]any{"text": "see attached", "attach": []any{a, b}})
	if len(j.uploads) != 2 || j.uploads[0] != "shot.png" || j.bodies[0] != "PNG" || j.uploads[1] != "notes.txt" {
		t.Fatalf("uploads = %v %v", j.uploads, j.bodies)
	}
	refs, _ := j.send["attachments"].([]any)
	if len(refs) != 2 || j.send["body"] != "see attached" {
		t.Fatalf("send = %v", j.send)
	}
	res, err := sess.CallTool(context.Background(), mcpParams("send", map[string]any{"text": "x", "attach": []any{filepath.Join(dir, "missing")}}))
	if err == nil && !res.IsError {
		t.Fatal("a missing file should fail the send")
	}
}

func TestMCPFetchWritesOnce(t *testing.T) {
	j := &attachJam{}
	getenv := j.env(t)
	sess := connectMCP(t, getenv)
	ref := "blb_01j9q3aaaaaaaaaaaaaaaaaaaa"
	out := callTool(t, sess, "fetch", map[string]any{"ref": ref})
	p, _ := out["path"].(string)
	if want := filepath.Join(getenv("HOME"), "attachments", ref+"-build.log"); p != want {
		t.Fatalf("path = %q, want %q", p, want)
	}
	if b, _ := os.ReadFile(p); string(b) != "log lines\n" {
		t.Fatalf("content = %q", b)
	}
	if st, _ := os.Stat(filepath.Dir(p)); st.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode = %v", st.Mode())
	}
	res, err := sess.CallTool(context.Background(), mcpParams("fetch", map[string]any{"ref": ref}))
	if err == nil && !res.IsError {
		t.Fatal("fetching over an existing file should fail")
	}
	if res, err := sess.CallTool(context.Background(), mcpParams("fetch", map[string]any{"ref": "../etc"})); err == nil && !res.IsError {
		t.Fatal("a non-blob ref should fail")
	}
}
