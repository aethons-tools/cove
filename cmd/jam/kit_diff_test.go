package main

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
)

func TestKitDiffCLI(t *testing.T) {
	store := jam.NewMemStore()
	for _, p := range []string{"prompt: a\n", "prompt: b\n"} {
		if _, err := store.PushKit("app", "kind: studio\n"+p); err != nil {
			t.Fatal(err)
		}
	}
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	ts := httptest.NewServer(h)
	defer ts.Close()
	var out, errb bytes.Buffer
	if code := run([]string{"kit", "diff", "--admin-url", ts.URL, "app"}, func(string) string { return "" }, &out, &errb); code != 0 || !strings.Contains(out.String(), "+prompt: b") || !strings.HasPrefix(out.String(), "app v1 → v2\n") {
		t.Fatalf("exit=%d out=%q err=%q", code, out.String(), errb.String())
	}
	out.Reset()
	errb.Reset()
	if code := run([]string{"kit", "diff", "--admin-url", ts.URL, "--from", "2", "--to", "2", "app"}, func(string) string { return "" }, &out, &errb); code != 1 || !strings.Contains(errb.String(), "pick two different versions") {
		t.Fatalf("same version: exit=%d out=%q err=%q", code, out.String(), errb.String())
	}
	if code := run([]string{"kit", "diff", "--admin-url", ts.URL}, func(string) string { return "" }, &out, &errb); code != 2 {
		t.Fatalf("no name: exit=%d", code)
	}
}

// Negative versions are refused client-side; a hostile server's name and
// text reach the terminal escaped.
func TestKitDiffCLIRefusesNegativesAndEscapes(t *testing.T) {
	for _, args := range [][]string{{"--from", "-1"}, {"--to", "-1"}} {
		var out, errb bytes.Buffer
		key := strings.TrimPrefix(args[0], "--")
		if code := run(append([]string{"kit", "diff", "--admin-url", "http://127.0.0.1:1"}, append(args, "app")...), func(string) string { return "" }, &out, &errb); code != 2 || !strings.Contains(errb.String(), key+" must be a version number") {
			t.Errorf("%v: exit=%d err=%q", args, code, errb.String())
		}
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"name":"a\u001b[31mpp","from":1,"to":2,"text":"kit.yml:\n  +x\u001b]0;pwn\u0007\n"}`)
	}))
	defer ts.Close()
	var out, errb bytes.Buffer
	if code := run([]string{"kit", "diff", "--admin-url", ts.URL, "app"}, func(string) string { return "" }, &out, &errb); code != 0 || strings.ContainsAny(out.String(), "\x1b\x07") || !strings.Contains(out.String(), `a\x1b[31mpp v1 → v2`) {
		t.Fatalf("exit=%d out=%q err=%q", code, out.String(), errb.String())
	}
}

// A version published from a draft shows its origin in `kit versions`.
func TestKitVersionsShowsDraftOrigin(t *testing.T) {
	store := jam.NewMemStore()
	if _, err := store.PushKit("app", "kind: studio\n"); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	d, err := jam.OpenKitDraft(store, "app", 0, "op", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	save, err := jam.PutKitDraftFile(store, log, "app", "n.md", 0, []byte("n\n"), jam.DraftETag(d.Tree), "op", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jam.PublishKitDraft(store, log, "app", false, save.ETag); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, log, nil))
	defer ts.Close()
	var out, errb bytes.Buffer
	if code := run([]string{"kit", "versions", "--admin-url", ts.URL, "app"}, func(string) string { return "" }, &out, &errb); code != 0 {
		t.Fatal(errb.String())
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[2], "v2") || !strings.Contains(lines[2], "draft") || strings.Contains(lines[1], "draft") {
		t.Fatalf("versions:\n%s", out.String())
	}
}
