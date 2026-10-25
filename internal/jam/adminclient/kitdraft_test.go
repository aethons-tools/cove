package adminclient

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/kittree"
	"github.com/aethons-tools/cove/internal/kittree/kittreetest"
)

func TestKitDraftClient(t *testing.T) {
	store := jam.NewMemStore()
	if _, err := store.PushKit("app", "kind: studio\n"); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.DiscardHandler), nil))
	defer ts.Close()
	c := New(ts.URL, "")
	st, err := c.OpenKitDraft("app", 0)
	if err != nil || st.Base != 1 || st.ETag == "" {
		t.Fatalf("open = %+v %v", st, err)
	}
	uni := "docs/ünï dé.md"
	s, err := c.PutKitDraftFile("app", uni, []byte("hi\n"), "0755", st.ETag)
	if err != nil || len(s.Changed) != 1 || s.Changed[0].Path != uni {
		t.Fatalf("put = %+v %v", s, err)
	}
	if b, mode, err := c.KitDraftFile("app", uni); err != nil || string(b) != "hi\n" || mode != "0755" {
		t.Fatalf("get = %q %q %v", b, mode, err)
	}
	// A stale ETag is a 412 with the fixed text (Review Focus 1).
	_, err = c.PutKitDraftFile("app", "x", []byte("x"), "", st.ETag)
	se, ok := errors.AsType[*StatusError](err)
	if !ok || se.Code != 412 || se.Body != "draft app changed since you loaded it (reload)" {
		t.Fatalf("stale put = %v", err)
	}
	if s, err = c.RemoveKitDraftFile("app", uni, s.ETag); err != nil || s.Changed[0].Kind != "removed" {
		t.Fatalf("rm = %+v %v", s, err)
	}
	var body bytes.Buffer
	if err := kittree.WriteTar(&body, kittreetest.Files("kit.yml", "kind: studio\nprompt: synced\n")); err != nil {
		t.Fatal(err)
	}
	if s, err = c.ReplaceKitDraft("app", body.Bytes(), s.ETag); err != nil || len(s.Changed) != 1 || s.Changed[0].Kind != "modified" {
		t.Fatalf("replace = %+v %v", s, err)
	}
	if st, err = c.KitDraftStatus("app"); err != nil || st.ETag != s.ETag {
		t.Fatalf("status = %+v %v", st, err)
	}
	res, err := c.PublishKitDraft("app", false, s.ETag)
	if err != nil || res.Version != 2 {
		t.Fatalf("publish = %+v %v", res, err)
	}
	if _, err := c.KitDraftStatus("app"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("status after publish = %v, want ErrNotFound", err)
	}
	st, err = c.OpenKitDraft("app", 1)
	if err != nil || st.Base != 1 || st.Current != 2 {
		t.Fatalf("open --from 1 = %+v %v", st, err)
	}
	if err := c.DiscardKitDraft("app", st.ETag); err != nil {
		t.Fatal(err)
	}
}

// Headers and bodies on the wire are exact, and a redirect is answered as is
// (never followed with the bearer and body).
func TestKitDraftClientWire(t *testing.T) {
	type seen struct{ method, path, ct, ifMatch, auth string }
	var got []seen
	var body []byte
	hits := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/elsewhere" {
			hits++
			return
		}
		got = append(got, seen{r.Method, r.URL.RequestURI(), r.Header.Get("Content-Type"), r.Header.Get("If-Match"), r.Header.Get("Authorization")})
		body, _ = io.ReadAll(r.Body)
		http.Redirect(w, r, "/elsewhere", http.StatusTemporaryRedirect)
	}))
	defer ts.Close()
	c := New(ts.URL, "tok")
	check := func(name string, err error, want seen) {
		t.Helper()
		se, ok := errors.AsType[*StatusError](err)
		if !ok || se.Code != http.StatusTemporaryRedirect {
			t.Fatalf("%s: err = %v, want the 307 as a StatusError", name, err)
		}
		if g := got[len(got)-1]; g != want {
			t.Fatalf("%s: wire = %+v, want %+v", name, g, want)
		}
	}
	_, err := c.PutKitDraftFile("app", "a b/c.sh", []byte{0, 1}, "0755", `"E"`)
	check("put", err, seen{"PUT", "/admin/kits/app/draft/files/a%20b/c.sh?mode=0755", "application/octet-stream", `"E"`, "Bearer tok"})
	if !bytes.Equal(body, []byte{0, 1}) {
		t.Fatalf("put body = %v", body)
	}
	_, err = c.ReplaceKitDraft("app", []byte("tar"), `"E"`)
	check("replace", err, seen{"PUT", "/admin/kits/app/draft", "application/x-tar", `"E"`, "Bearer tok"})
	_, err = c.PublishKitDraft("app", true, `"E"`)
	check("publish", err, seen{"POST", "/admin/kits/app/draft/publish", "application/json", `"E"`, "Bearer tok"})
	if string(body) != `{"pin":true}` {
		t.Fatalf("publish body = %s", body)
	}
	_, err = c.OpenKitDraft("app", 2)
	check("open", err, seen{"POST", "/admin/kits/app/draft", "application/json", "", "Bearer tok"})
	err = c.DiscardKitDraft("app", `"E"`)
	check("discard", err, seen{"DELETE", "/admin/kits/app/draft", "", `"E"`, "Bearer tok"})
	// '#' and '?' in a path are escaped, never read as a fragment or a query.
	_, err = c.RemoveKitDraftFile("app", "a#b/c?d.sh", `"E"`)
	check("rm", err, seen{"DELETE", "/admin/kits/app/draft/files/a%23b/c%3Fd.sh", "", `"E"`, "Bearer tok"})
	_, _, err = c.KitDraftFile("app", "a#b/c?d.sh")
	check("get", err, seen{"GET", "/admin/kits/app/draft/files/a%23b/c%3Fd.sh", "", "", "Bearer tok"})
	_, err = c.KitDraftStatus("app")
	check("status", err, seen{"GET", "/admin/kits/app/draft", "", "", "Bearer tok"})
	if hits != 0 {
		t.Fatalf("redirect was followed %d times", hits)
	}
}
