package jam

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// buildsHandler is the admin handler over a supervisor whose kit draft
// builds (SetKitDraftBuilds, the one wiring) are draftBuildsFor's, the
// builds, the draft's ETag, and the audit log (JSON lines).
func buildsHandler(t *testing.T, fb *fakeDraftBuilder) (http.Handler, *KitDraftBuilds, string, *bytes.Buffer) {
	t.Helper()
	b, st, etag, _ := draftBuildsFor(t, fb)
	sup := NewSupervisor(st, &fakeLauncher{}, "holder-builds", time.Minute, time.Minute, time.Now, slog.New(slog.DiscardHandler))
	sup.SetKitDraftBuilds(b)
	var logs bytes.Buffer
	h := NewAdminHandler(st, sup, nil, LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewJSONHandler(&logs, nil)), nil)
	return h, b, etag, &logs
}

// auditRecords are the log's records whose msg is msg, each as its fields.
func auditRecords(t *testing.T, logs *bytes.Buffer, msg string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(logs.String()), "\n") {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		if rec["msg"] == msg {
			out = append(out, rec)
		}
	}
	return out
}

func TestDraftBuildRoutes(t *testing.T) {
	fb := &fakeDraftBuilder{started: make(chan string, 1), release: make(chan struct{}), write: "#1 \x1b[31mred\x1b[0m\n"}
	h, b, etag, logs := buildsHandler(t, fb)
	const path = "/admin/kits/app/draft/build"
	if w := draftDo(t, h, "POST", path, "text/plain", etag, nil); w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("text/plain = %d", w.Code)
	}
	if w := draftDo(t, h, "POST", path, "application/json", "", nil); w.Code != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match = %d", w.Code)
	}
	if w := draftDo(t, h, "POST", path, "application/json", `"`+strings.Repeat("0", 64)+`"`, nil); w.Code != http.StatusPreconditionFailed || body(w) != "draft app changed since you loaded it (reload)" {
		t.Fatalf("stale = %d %s", w.Code, w.Body)
	}
	w := draftDo(t, h, "POST", path, "application/json", etag, nil)
	var v KitDraftBuild
	if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &v) != nil || v.State != DraftBuildRunning || DraftETag(v.Tree) != etag || v.ID == "" {
		t.Fatalf("start = %d %s", w.Code, w.Body)
	}
	// The API started it on the supervisor's builds: the one registry.
	if s, err := b.Status("app", "", 0); err != nil || s.ID != v.ID {
		t.Fatalf("supervisor's builds = %+v %v, want build %s", s, err, v.ID)
	}
	if w := draftDo(t, h, "POST", path, "application/json", etag, nil); w.Code != http.StatusConflict || body(w) != "a draft build of app is already running on local" {
		t.Fatalf("second start = %d %s", w.Code, w.Body)
	}
	<-fb.started // the fake wrote its log line before signalling
	g := draftDo(t, h, "GET", path, "", "", nil)
	var gv KitDraftBuild
	if g.Code != http.StatusOK || json.Unmarshal(g.Body.Bytes(), &gv) != nil || gv.ID != v.ID || !strings.Contains(gv.Log, "\x1b[31mred") || g.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status = %d %s", g.Code, g.Body)
	}
	f := draftDo(t, h, "GET", path+"?since="+jsonInt(gv.LogEnd), "", "", nil)
	var fv KitDraftBuild
	if f.Code != http.StatusOK || json.Unmarshal(f.Body.Bytes(), &fv) != nil || fv.Log != "" || fv.LogFrom != gv.LogEnd {
		t.Fatalf("follow = %d %s", f.Code, f.Body)
	}
	// Past the end is clamped to it; past int64 is refused.
	l := draftDo(t, h, "GET", path+"?since=9223372036854775807", "", "", nil)
	var lv KitDraftBuild
	if l.Code != http.StatusOK || json.Unmarshal(l.Body.Bytes(), &lv) != nil || lv.Log != "" || lv.LogFrom != gv.LogEnd || lv.LogEnd != gv.LogEnd {
		t.Fatalf("since past the end = %d %s", l.Code, l.Body)
	}
	for _, q := range []string{"-1", "x", "9223372036854775808", "99999999999999999999"} {
		if w := draftDo(t, h, "GET", path+"?since="+q, "", "", nil); w.Code != http.StatusBadRequest || body(w) != "since must be a log offset (0 or more)" {
			t.Fatalf("since=%s = %d %s", q, w.Code, w.Body)
		}
	}
	d := draftDo(t, h, "DELETE", path, "", "", nil)
	var dv KitDraftBuild
	if d.Code != http.StatusOK || json.Unmarshal(d.Body.Bytes(), &dv) != nil || dv.State != DraftBuildFailed || dv.Error != "cancelled" {
		t.Fatalf("cancel = %d %s", d.Code, d.Body)
	}
	if w := draftDo(t, h, "DELETE", path, "", "", nil); w.Code != http.StatusNotFound || body(w) != "kit app has no draft build running" {
		t.Fatalf("second cancel = %d %s", w.Code, w.Body)
	}
	// The audit lines carry exactly these fields, never log content.
	for msg, want := range map[string][]string{
		"kit draft build requested": {"build", "kit", "level", "manager", "msg", "operator", "time", "tree"},
		"kit draft build cancelled": {"build", "kit", "level", "manager", "msg", "operator", "time"},
	} {
		recs := auditRecords(t, logs, msg)
		if len(recs) != 1 {
			t.Fatalf("%d %q lines:\n%s", len(recs), msg, logs)
		}
		rec := recs[0]
		if keys := slices.Sorted(maps.Keys(rec)); !slices.Equal(keys, want) {
			t.Errorf("%q fields = %v, want %v", msg, keys, want)
		}
		if rec["kit"] != "app" || rec["build"] != v.ID || rec["operator"] == "" {
			t.Errorf("%q = %v", msg, rec)
		}
	}
	if rec := auditRecords(t, logs, "kit draft build requested")[0]; rec["tree"] != shortTree(v.Tree) {
		t.Errorf("requested tree = %v, want %s", rec["tree"], shortTree(v.Tree))
	}
}

func jsonInt(n int64) string { b, _ := json.Marshal(n); return string(b) }

func TestDraftBuildRoutesNeedALauncher(t *testing.T) {
	st := NewMemStore()
	pushApp(t, st, "kit.yml", "kind: studio\n")
	// No supervisor, and a supervisor never given builds: both answer 503.
	sup := NewSupervisor(st, &fakeLauncher{}, "holder-nobuilds", time.Minute, time.Minute, time.Now, slog.New(slog.DiscardHandler))
	for _, s := range []*Supervisor{nil, sup} {
		h := NewAdminHandler(st, s, nil, LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.DiscardHandler), nil)
		for _, m := range []string{"GET", "DELETE"} {
			if w := draftDo(t, h, m, "/admin/kits/app/draft/build", "", "", nil); w.Code != http.StatusServiceUnavailable || body(w) != kitDraftBuildsOff {
				t.Fatalf("%s = %d %s", m, w.Code, w.Body)
			}
		}
		if w := draftDo(t, h, "POST", "/admin/kits/app/draft/build", "application/json", `"x"`, nil); w.Code != http.StatusServiceUnavailable || body(w) != kitDraftBuildsOff {
			t.Fatalf("POST = %d %s", w.Code, w.Body)
		}
	}
}

// A DELETE whose request goes away while the build is still stopping
// answers the fixed 503 text, not the context's error; the build is still
// cancelled and ends once its builder returns.
func TestDraftBuildCancelRequestGone(t *testing.T) {
	hold := make(chan struct{})
	fb := &fakeDraftBuilder{during: func() { <-hold }, release: make(chan struct{})} // ignores its ctx until hold closes
	h, b, etag, logs := buildsHandler(t, fb)
	const path = "/admin/kits/app/draft/build"
	sw := draftDo(t, h, "POST", path, "application/json", etag, nil)
	var sv KitDraftBuild
	if sw.Code != http.StatusAccepted || json.Unmarshal(sw.Body.Bytes(), &sv) != nil || sv.ID == "" {
		t.Fatalf("start = %d %s", sw.Code, sw.Body)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := httptest.NewRequestWithContext(ctx, "DELETE", path, nil)
	r.RemoteAddr = "127.0.0.1:5000"
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); h.ServeHTTP(w, r) }()
	cancel()
	<-done
	if w.Code != http.StatusServiceUnavailable || body(w) != "kit app draft build is still stopping" {
		t.Fatalf("gone = %d %s", w.Code, w.Body)
	}
	if recs := auditRecords(t, logs, "kit draft build cancelled"); len(recs) != 1 || recs[0]["kit"] != "app" || recs[0]["build"] != sv.ID {
		t.Fatalf("audit = %v\n%s", recs, logs)
	}
	close(hold)
	if v := finished(t, b); v.State != DraftBuildFailed || v.Error != "cancelled" {
		t.Fatalf("build = %+v", v)
	}
}

// Review Focus 3, over HTTP: two concurrent POSTs, one 202 and one 409.
func TestDraftBuildTwoConcurrentPosts(t *testing.T) {
	fb := &fakeDraftBuilder{release: make(chan struct{})}
	h, _, etag, _ := buildsHandler(t, fb)
	codes := make(chan int, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes <- draftDo(t, h, "POST", "/admin/kits/app/draft/build", "application/json", etag, nil).Code
		}()
	}
	wg.Wait()
	close(codes)
	got := map[int]int{}
	for c := range codes {
		got[c]++
	}
	if got[http.StatusAccepted] != 1 || got[http.StatusConflict] != 1 {
		t.Fatalf("codes = %v, want one 202 and one 409", got)
	}
	close(fb.release)
}

// managedBuildsHandler is buildsHandler over fm's managers.
func managedBuildsHandler(t *testing.T, fm *fakeManagedDraft) (http.Handler, *KitDraftBuilds, string) {
	t.Helper()
	h, b, etag, _ := buildsHandler(t, &fakeDraftBuilder{})
	b.builder = fm
	return h, b, etag
}

func TestDraftBuildStartUnknownManager404(t *testing.T) {
	fm := newFakeManagedDraft("local", "m1")
	h, b, etag := managedBuildsHandler(t, fm)
	const path = "/admin/kits/app/draft/build"
	if w := draftDo(t, h, "POST", path, "application/json", etag, []byte(`{"manager":"nope"}`)); w.Code != http.StatusNotFound || !strings.Contains(body(w), "unknown manager: nope") {
		t.Fatalf("unknown = %d %s", w.Code, w.Body)
	}
	fm.setAway("m1", true)
	if w := draftDo(t, h, "POST", path, "application/json", etag, []byte(`{"manager":"m1"}`)); w.Code != http.StatusServiceUnavailable || !strings.Contains(body(w), "manager unavailable: m1") {
		t.Fatalf("unavailable = %d %s", w.Code, w.Body)
	}
	fm.setAway("m1", false)
	w := draftDo(t, h, "POST", path, "application/json", etag, []byte(`{"manager":"m1"}`))
	var v KitDraftBuild
	if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &v) != nil || v.Manager != "m1" {
		t.Fatalf("start m1 = %d %s", w.Code, w.Body)
	}
	waitJob(t, b, "app", "m1")
	g := draftDo(t, h, "GET", path+"?manager=m1", "", "", nil)
	var gv KitDraftBuild
	if g.Code != http.StatusOK || json.Unmarshal(g.Body.Bytes(), &gv) != nil || gv.ID != v.ID || gv.Manager != "m1" {
		t.Fatalf("status m1 = %d %s", g.Code, g.Body)
	}
	if g := draftDo(t, h, "GET", path+"?manager=local", "", "", nil); g.Code != http.StatusOK || !strings.Contains(g.Body.String(), `"state":"unknown"`) {
		t.Fatalf("status local = %d %s", g.Code, g.Body)
	}
	if g := draftDo(t, h, "GET", path+"?manager=nope", "", "", nil); g.Code != http.StatusNotFound {
		t.Fatalf("status nope = %d %s", g.Code, g.Body)
	}
	if d := draftDo(t, h, "DELETE", path+"?manager=m1", "", "", nil); d.Code != http.StatusNotFound || body(d) != "kit app has no draft build running on m1" {
		t.Fatalf("cancel m1 = %d %s", d.Code, d.Body)
	}
	if d := draftDo(t, h, "DELETE", path+"?manager=nope", "", "", nil); d.Code != http.StatusNotFound || body(d) != "unknown manager: nope" {
		t.Fatalf("cancel nope = %d %s", d.Code, d.Body)
	}
}
