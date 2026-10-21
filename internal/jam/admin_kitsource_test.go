package jam

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam/gitsource"
)

const fakeToken = "ghp_SECRETTOKEN123"

func kitSourceAdmin(t *testing.T) (*httptest.Server, *MemStore, *fakeFetcher) {
	t.Helper()
	st2 := NewMemStore()
	f := newFakeFetcher(t, map[string]string{"main": "c1"}, map[string]map[string]string{"c1": {"kit.yml": webKit}})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := NewAdminHandler(st2, nil, nil, LoopbackAuthenticator{}, func(string) bool { return true }, nil, log, nil, nil,
		WithKitFetcher(st2, f, log))
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts, st2, f
}

func call(t *testing.T, method, url, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

const goodSrc = `{"source":"https://git.example.com/acme/kits@main"}`

func TestKitSourceAdminLifecycle(t *testing.T) {
	ts, st, _ := kitSourceAdmin(t)

	code, body := call(t, "POST", ts.URL+"/admin/kits/import", goodSrc)
	if code != 200 || !strings.Contains(body, `"name":"web-dev"`) || !strings.Contains(body, `"commit":"c1"`) {
		t.Fatalf("import = %d %s", code, body)
	}
	if k, _ := st.GetKit("web-dev"); k.Source != nil {
		t.Fatal("import must not track")
	}

	code, body = call(t, "PUT", ts.URL+"/admin/kits/web-dev/source", goodSrc)
	if code != 200 || !strings.Contains(body, `"unchanged":true`) {
		t.Fatalf("track = %d %s", code, body)
	}
	if k, _ := st.GetKit("web-dev"); k.Source == nil || k.Source.Commit != "c1" {
		t.Fatalf("tracked source = %+v", k.Source)
	}

	code, body = call(t, "POST", ts.URL+"/admin/kits/web-dev/sync", "")
	if code != 200 || !strings.Contains(body, `"unchanged":true`) {
		t.Fatalf("sync = %d %s", code, body)
	}

	code, body = call(t, "GET", ts.URL+"/admin/kits/web-dev/history", "")
	if code != 200 || !strings.Contains(body, `"version":1`) || !strings.Contains(body, `"current":true`) ||
		!strings.Contains(body, `"repo":"https://git.example.com/acme/kits"`) {
		t.Fatalf("history = %d %s", code, body)
	}
	if code, body = call(t, "GET", ts.URL+"/admin/kits/web-dev/versions", ""); code != 200 || body != "[1]\n" {
		t.Fatalf("versions = %d %q", code, body)
	}

	if code, _ = call(t, "DELETE", ts.URL+"/admin/kits/web-dev/source", ""); code != 204 {
		t.Fatalf("untrack = %d", code)
	}
	if k, _ := st.GetKit("web-dev"); k.Source != nil {
		t.Fatal("still tracked")
	}
	if code, _ = call(t, "POST", ts.URL+"/admin/kits/web-dev/sync", ""); code != 409 {
		t.Fatalf("sync untracked = %d, want 409", code)
	}
}

func TestKitSourceAdminErrors(t *testing.T) {
	ts, _, f := kitSourceAdmin(t)

	for _, src := range []string{`{"source":"nonsense"}`, `{"source":"https://git.example.com/a/b"}`, `{}`} {
		if code, _ := call(t, "POST", ts.URL+"/admin/kits/import", src); code != 400 {
			t.Errorf("import %s = %d, want 400", src, code)
		}
	}
	if code, _ := call(t, "POST", ts.URL+"/admin/kits/import", "{"); code != 400 {
		t.Errorf("bad json = %d", code)
	}

	// Parse errors never echo the input.
	_, body := call(t, "POST", ts.URL+"/admin/kits/import", `{"source":"https://u:`+fakeToken+`@git.example.com/a@main"}`)
	if strings.Contains(body, fakeToken) {
		t.Fatalf("parse error echoed the credential: %s", body)
	}

	// A fetch failure is 502 carrying the fetcher's (redacted) message only.
	f.failNext = fmt.Errorf("%w", &fetchErrForTest{"auth failed for https://git.example.com/ (check kits.git cred gh)"})
	code, body := call(t, "POST", ts.URL+"/admin/kits/import", goodSrc)
	if code != 502 || !strings.Contains(body, "auth failed") || strings.Contains(body, fakeToken) {
		t.Fatalf("fetch failure = %d %s", code, body)
	}

	// An unusable kit.yml is the operator's input: 400.
	f.commits["c1"] = map[string]string{"kit.yml": "kind: nope\n"}
	if code, _ = call(t, "POST", ts.URL+"/admin/kits/import", goodSrc); code != 400 {
		t.Fatalf("bad kit.yml = %d, want 400", code)
	}

	// Unknown kit: 404 on sync and untrack.
	for _, c := range [][2]string{{"POST", "/admin/kits/ghost/sync"}, {"DELETE", "/admin/kits/ghost/source"}, {"GET", "/admin/kits/ghost/history"}} {
		if code, _ := call(t, c[0], ts.URL+c[1], ""); code != 404 {
			t.Errorf("%s %s = %d, want 404", c[0], c[1], code)
		}
	}
}

func TestWriteStatusMapsErrNotFound(t *testing.T) {
	err := errors.Join(errors.New("kit x"), ErrNotFound)
	if got := WriteStatus(err, 500); got != http.StatusNotFound {
		t.Fatalf("WriteStatus(ErrNotFound) = %d, want 404", got)
	}
	if got := projectErrStatus(ErrNotFound, 500); got != http.StatusNotFound {
		t.Fatalf("projectErrStatus(ErrNotFound) = %d, want 404", got)
	}
}

// fetchErrForTest is an error a Fetcher would produce: its message, matching
// gitsource.ErrFetch.
type fetchErrForTest struct{ msg string }

func (e *fetchErrForTest) Error() string        { return e.msg }
func (e *fetchErrForTest) Is(target error) bool { return target == gitsource.ErrFetch }

// A store failure is 500 with a generic body; the detail is only logged.
func TestKitSourceAdminStoreFailure(t *testing.T) {
	f := newFakeFetcher(t, map[string]string{"main": "c1"}, map[string]map[string]string{"c1": {"kit.yml": webKit}})
	st := &failStore{Store: NewMemStore(), failPush: true}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ts := httptest.NewServer(NewAdminHandler(st, nil, nil, LoopbackAuthenticator{}, func(string) bool { return true }, nil, log, nil, nil,
		WithKitFetcher(st, f, log)))
	defer ts.Close()
	code, body := call(t, "POST", ts.URL+"/admin/kits/import", goodSrc)
	if code != 500 || strings.Contains(body, "push failed") || !strings.Contains(body, "kit store error") {
		t.Fatalf("store failure = %d %q", code, body)
	}
}

// The history lists versions ascending.
func TestKitHistoryOrder(t *testing.T) {
	ts, st, _ := kitSourceAdmin(t)
	for _, c := range []string{"kind: studio\nname: k\nprompt: a\n", "kind: studio\nname: k\nprompt: b\n", "kind: studio\nname: k\nprompt: c\n"} {
		if _, _, err := PushStudioKit(st, "k", c); err != nil {
			t.Fatal(err)
		}
	}
	_, body := call(t, "GET", ts.URL+"/admin/kits/k/history", "")
	i1, i2, i3 := strings.Index(body, `"version":1`), strings.Index(body, `"version":2`), strings.Index(body, `"version":3`)
	if !(0 <= i1 && i1 < i2 && i2 < i3) {
		t.Fatalf("history order: %s", body)
	}
}

func TestKitShowCarriesSources(t *testing.T) {
	ts, _, _ := kitSourceAdmin(t)
	if code, body := call(t, "PUT", ts.URL+"/admin/kits/web-dev/source", goodSrc); code != 200 {
		t.Fatalf("track = %d %s", code, body)
	}
	code, body := call(t, "GET", ts.URL+"/admin/kits/web-dev", "")
	if code != 200 || !strings.Contains(body, `"source":{"repo":"https://git.example.com/acme/kits"`) ||
		!strings.Contains(body, `"tracking":{"repo":"https://git.example.com/acme/kits"`) {
		t.Fatalf("show = %d %s", code, body)
	}
	call(t, "DELETE", ts.URL+"/admin/kits/web-dev/source", "")
	_, body = call(t, "GET", ts.URL+"/admin/kits/web-dev", "")
	if strings.Contains(body, `"tracking"`) || !strings.Contains(body, `"source"`) {
		t.Fatalf("after untrack: %s", body)
	}
}

// A set import answers with the kit and its ancestors in "also".
func TestKitSourceAdminSetAlso(t *testing.T) {
	st := NewMemStore()
	f := newFakeFetcher(t, map[string]string{"main": "c1"}, map[string]map[string]string{"c1": {"web/kit.yml": webKit, "app/kit.yml": appKit}})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ts := httptest.NewServer(NewAdminHandler(st, nil, nil, LoopbackAuthenticator{}, func(string) bool { return true }, nil, log, nil, nil,
		WithKitFetcher(st, f, log)))
	t.Cleanup(ts.Close)
	code, body := call(t, "POST", ts.URL+"/admin/kits/import", `{"source":"https://git.example.com/acme/kits//app@main"}`)
	if code != 200 || !strings.Contains(body, `"name":"app"`) || !strings.Contains(body, `"also":[{"name":"web-dev","version":1`) {
		t.Fatalf("import = %d %s", code, body)
	}
}
