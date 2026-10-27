package main

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/cli"
	"github.com/aethons-tools/cove/internal/jam"
)

func TestKitPushDirectory(t *testing.T) {
	store := jam.NewMemStore()
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	ts := httptest.NewServer(h)
	defer ts.Close()
	dir := filepath.Join(t.TempDir(), "web")
	for p, body := range map[string]string{"kit.yml": "kind: studio\n", "Dockerfile": "ARG JAM_STUDIO_BASE_IMAGE\nFROM ${JAM_STUDIO_BASE_IMAGE}\n", "run.sh": "echo\n"} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, p), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(filepath.Join(dir, "run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	getenv := func(string) string { return "" }
	if code := run([]string{"kit", "push", "--admin-url", ts.URL, dir}, getenv, &out, &errb); code != 0 || out.String() != "pushed web v1\n" {
		t.Fatalf("push: exit=%d out=%q err=%q", code, out.String(), errb.String())
	}
	out.Reset()
	if code := run([]string{"kit", "push", "--admin-url", ts.URL, dir}, getenv, &out, &errb); code != 0 || out.String() != "web unchanged (current v1)\n" {
		t.Fatalf("repush: %q %q", out.String(), errb.String())
	}
	out.Reset()
	if code := run([]string{"kit", "show", "--admin-url", ts.URL, "--own", "web"}, getenv, &out, &errb); code != 0 || !strings.Contains(out.String(), "#   0755") || !strings.Contains(out.String(), "run.sh") {
		t.Fatalf("show:\n%s", out.String())
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(dir, "leak")); err != nil {
		t.Fatal(err)
	}
	errb.Reset()
	if code := run([]string{"kit", "push", "--admin-url", ts.URL, dir}, getenv, &out, &errb); code == 0 || !strings.Contains(errb.String(), "leak: symlinks are not allowed") {
		t.Fatalf("symlink: exit=%d err=%q", code, errb.String())
	}
}

func TestKitPushArgs(t *testing.T) {
	var out, errb bytes.Buffer
	if code := cmdKit([]string{"push"}, cli.Globals{}, &out, &errb); code != 2 || !strings.Contains(errb.String(), "expected <dir> or --config <file>") {
		t.Fatalf("no args: %d %q", code, errb.String())
	}
	if code := cmdKit([]string{"push", "--config", "-"}, cli.Globals{}, &out, &errb); code != 2 || !strings.Contains(errb.String(), "--name is required with --config") {
		t.Fatalf("stdin without name: %d %q", code, errb.String())
	}
}

// Without --name a directory push goes under kit.yml's legacy name:, else
// the directory's name; --config needs --name or a legacy name:.
func TestKitPushNames(t *testing.T) {
	store := jam.NewMemStore()
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	ts := httptest.NewServer(h)
	defer ts.Close()
	getenv := func(string) string { return "" }
	dir := filepath.Join(t.TempDir(), "web")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	kitFile := filepath.Join(dir, "kit.yml")
	if err := os.WriteFile(kitFile, []byte("kind: studio\nname: api\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args []string
		want string
		code int
	}{
		{[]string{dir}, "pushed api v1\n", 0},
		{[]string{"--config", kitFile}, "api unchanged (current v1)\n", 0},
		{[]string{"--name", "api", "--config", kitFile, dir}, "", 2},
	} {
		var out, errb bytes.Buffer
		if code := run(append([]string{"kit", "push", "--admin-url", ts.URL}, tc.args...), getenv, &out, &errb); code != tc.code || out.String() != tc.want {
			t.Fatalf("%v: exit=%d out=%q err=%q", tc.args, code, out.String(), errb.String())
		}
	}
	if err := os.WriteFile(kitFile, []byte("kind: studio\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := run([]string{"kit", "push", "--admin-url", ts.URL, dir}, getenv, &out, &errb); code != 0 || out.String() != "pushed web v1\n" {
		t.Fatalf("dir name: exit=%d out=%q err=%q", code, out.String(), errb.String())
	}
	errb.Reset()
	if code := run([]string{"kit", "push", "--admin-url", ts.URL, "--config", kitFile}, getenv, &out, &errb); code == 0 || !strings.Contains(errb.String(), "--name is required with --config") {
		t.Fatalf("no name: exit=%d err=%q", code, errb.String())
	}
}

// A <dir> that cannot be opened says why, without the OS error.
func TestKitPushDirErrors(t *testing.T) {
	tmp := t.TempDir()
	file := filepath.Join(tmp, "kit.yml")
	if err := os.WriteFile(file, []byte("kind: studio\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for dir, want := range map[string]string{
		filepath.Join(tmp, "nope"): ": no such directory",
		file:                       ": not a directory",
	} {
		_, _, err := kitPushFiles([]string{dir}, "", "")
		if err == nil || err.Error() != dir+want {
			t.Fatalf("%s: err = %v, want %q", dir, err, dir+want)
		}
	}
	locked := filepath.Join(tmp, "locked")
	if err := os.Mkdir(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	if os.Geteuid() != 0 {
		if _, _, err := kitPushFiles([]string{filepath.Join(locked, "x")}, "", ""); err == nil || err.Error() != filepath.Join(locked, "x")+": cannot open the directory" {
			t.Fatalf("unreadable: err = %v", err)
		}
	}
}

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
