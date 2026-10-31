package gittest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServeTakesPushes(t *testing.T) {
	url, first, _ := Serve(t, map[string]string{"a.txt": "one\n"}, WithPush())
	if Head(t, url, "main") != first {
		t.Fatal("head is not the first commit")
	}
	if got, ok := Show(t, url, "main", "a.txt"); !ok || got != "one\n" {
		t.Fatalf("show = %q %v", got, ok)
	}
	work := filepath.Join(t.TempDir(), "w")
	run(t, "", "git", "clone", "-q", url, work)
	if err := os.WriteFile(filepath.Join(work, "b.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, work, "git", "add", "-A")
	run(t, work, "git", "commit", "-q", "-m", "b")
	run(t, work, "git", "push", "-q", "origin", "HEAD:refs/heads/topic")
	sha := strings.TrimSpace(run(t, work, "git", "rev-parse", "HEAD"))
	if Head(t, url, "topic") != sha {
		t.Fatal("push not seen")
	}
	if _, ok := Show(t, url, "main", "b.txt"); ok {
		t.Fatal("b.txt on main")
	}
	if Head(t, url, "nope") != "" {
		t.Fatal("unknown branch has a head")
	}
}
