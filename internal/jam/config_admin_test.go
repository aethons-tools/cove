package jam

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam/condition"
	"github.com/aethons-tools/cove/internal/jam/configtar"
	"github.com/aethons-tools/cove/internal/jam/gitsource"
)

// packTree writes files to a temp dir and packs it.
func packTree(t *testing.T, files map[string]string) []byte {
	t.Helper()
	b, err := configtar.Pack(writeTree(t, files))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// postTar POSTs body as application/gzip, checks the status, and decodes the
// JSON answer into out when out is non-nil. It returns the body text.
func postTar(t *testing.T, url string, body []byte, want int, out any) string {
	t.Helper()
	resp, err := http.Post(url, "application/gzip", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		t.Fatalf("POST %s = %d, want %d: %s", url, resp.StatusCode, want, b)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("POST %s JSON: %v (%s)", url, err, b)
		}
	}
	return string(b)
}

// getURLJSON GETs url, wants 200, and decodes the JSON answer into out.
func getURLJSON(t *testing.T, url string, out any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", url, resp.StatusCode, b)
	}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("GET %s JSON: %v (%s)", url, err, b)
	}
}

// rawTarGz builds a tar.gz header by header: each header's body is a run of
// zeros of its Size.
func rawTarGz(t *testing.T, hdrs ...*tar.Header) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, h := range hdrs {
		if h.Mode == 0 {
			h.Mode = 0o644
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg && h.Size > 0 {
			if _, err := io.CopyN(tw, zeros{}, h.Size); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

func tarWithSymlink(t *testing.T) []byte {
	return rawTarGz(t, &tar.Header{Name: "users/bob.yml", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"})
}

func configServer(t *testing.T, st *MemStore) *httptest.Server {
	t.Helper()
	return configServerWith(t, st, nil)
}

func configServerWith(t *testing.T, st *MemStore, f KitFetcher) *httptest.Server {
	return configServerEnv(t, st, f, allCreds)
}

func configServerEnv(t *testing.T, st *MemStore, f KitFetcher, env ConfigEnv) *httptest.Server {
	t.Helper()
	log := slog.New(slog.DiscardHandler)
	ts := httptest.NewServer(NewAdminHandler(st, nil, nil, LoopbackAuthenticator{}, allCreds.CredExists, nil, log, nil,
		WithConfig(st, f, env, log)))
	t.Cleanup(ts.Close)
	return ts
}

// sendJSON sends body (as application/json unless empty), checks the status,
// decodes the answer into out when non-nil, and returns the body text.
func sendJSON(t *testing.T, method, url, body string, want int, out any) string {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		t.Fatalf("%s %s = %d, want %d: %s", method, url, resp.StatusCode, want, b)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("%s %s JSON: %v (%s)", method, url, err, b)
		}
	}
	return string(b)
}

func TestConfigRoutes(t *testing.T) {
	st := NewMemStore()
	ts := configServer(t, st)
	body := packTree(t, map[string]string{"users/bob.yml": "kind: user\nversion: 1\n"})
	var diff ConfigApplyResult
	postTar(t, ts.URL+"/admin/config/diff", body, http.StatusOK, &diff)
	if diff.Applied || len(st.ListUsers()) != 0 || diff.Plan.Ops[0].Action != ConfigCreate {
		t.Fatalf("diff wrote or misplanned: %+v", diff)
	}
	if res, err := ApplyConfigTree(context.Background(), st, nil, treeOf(t, map[string]string{"users/bob.yml": "kind: user\nversion: 1\n"}),
		allCreds, dirOpts(), nil, "local"); err != nil || !res.Applied || len(st.ListUsers()) != 1 {
		t.Fatalf("seed: %+v, %v", res, err)
	}
	var exp ConfigTreeExport
	getURLJSON(t, ts.URL+"/admin/config/export-tree", &exp)
	if _, ok := exp.Files["users/bob.yml"]; !ok {
		t.Fatalf("export-tree %+v", exp)
	}
	if _, err := st.CreateUser(User{Name: "carol", Status: StatusLive, ID: ident.New(ident.User)}); err != nil {
		t.Fatal(err)
	}
	getURLJSON(t, ts.URL+"/admin/config/export-tree", &exp)
	if _, ok := exp.Files["users/carol.yml"]; ok {
		t.Fatal("export-tree without all= renders an unmanaged user")
	}
	getURLJSON(t, ts.URL+"/admin/config/export-tree?all=1", &exp)
	if _, ok := exp.Files["users/carol.yml"]; !ok {
		t.Fatalf("export-tree?all=1 %+v", exp)
	}

	if msg := sendJSON(t, "POST", ts.URL+"/admin/config/diff", `{}`, http.StatusConflict, nil); !strings.Contains(msg, "no config repo is configured") {
		t.Fatalf("json body without a tracked source: %s", msg)
	}
}

// TestConfigRoutesRejectBadUpload: a hostile upload is answered 400 or 413
// before any tree is loaded or planned — nothing is written, and the unpack
// dir is removed.
func TestConfigRoutesRejectBadUpload(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	st := NewMemStore()
	ts := configServer(t, st)
	many := make([]*tar.Header, configtar.MaxEntries+1)
	for i := range many {
		many[i] = &tar.Header{Name: fmt.Sprintf("users/u%d.yml", i), Typeflag: tar.TypeReg}
	}
	for name, c := range map[string]struct {
		body []byte
		want int
	}{
		"not gzip": {[]byte("not gzip"), http.StatusBadRequest},
		"oversize": {bytes.Repeat([]byte{0}, configtar.MaxUpload+1), http.StatusRequestEntityTooLarge},
		"symlink":  {tarWithSymlink(t), http.StatusBadRequest},
		"dotdot":   {rawTarGz(t, &tar.Header{Name: "../x", Typeflag: tar.TypeReg}), http.StatusBadRequest},
		"absolute": {rawTarGz(t, &tar.Header{Name: "/etc/x", Typeflag: tar.TypeReg}), http.StatusBadRequest},
		"device":   {rawTarGz(t, &tar.Header{Name: "users/d", Typeflag: tar.TypeChar}), http.StatusBadRequest},
		"bomb":     {rawTarGz(t, &tar.Header{Name: "users/big.yml", Typeflag: tar.TypeReg, Size: configtar.MaxUnpacked + 1}), http.StatusRequestEntityTooLarge},
		"entries":  {rawTarGz(t, many...), http.StatusRequestEntityTooLarge},
	} {
		t.Run(name, func(t *testing.T) {
			if len(c.body) > configtar.MaxUpload && name != "oversize" {
				t.Fatalf("fixture is %d bytes, over the upload cap", len(c.body))
			}
			for _, route := range []string{"diff"} {
				msg := postTar(t, ts.URL+"/admin/config/"+route, c.body, c.want, nil)
				if c.want == http.StatusRequestEntityTooLarge && !strings.Contains(msg, "config tree too large (limit 16 MiB)") {
					t.Errorf("%s: %s", route, msg)
				}
			}
		})
	}
	// Only application/gzip is a tree: a browser's no-cors text/plain POST
	// (or any other type) is 415 before the body is read.
	good := packTree(t, map[string]string{"users/bob.yml": "kind: user\nversion: 1\n"})
	for _, ct := range []string{"text/plain", "", "application/x-www-form-urlencoded", "multipart/form-data; boundary=x"} {
		for _, route := range []string{"diff"} {
			req, _ := http.NewRequest("POST", ts.URL+"/admin/config/"+route, bytes.NewReader(good))
			if ct != "" {
				req.Header.Set("Content-Type", ct)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusUnsupportedMediaType {
				t.Errorf("%s %q = %d, want 415", route, ct, resp.StatusCode)
			}
		}
	}
	if code := postTar(t, ts.URL+"/admin/config/diff", good, http.StatusOK, nil); code == "" {
		t.Fatal("empty diff answer")
	}
	if len(st.ListUsers()) != 0 || len(st.ListConfigMarks()) != 0 {
		t.Fatal("a bad upload wrote")
	}
	left, _ := filepath.Glob(filepath.Join(tmp, "jam-config-*"))
	if len(left) != 0 {
		t.Fatalf("unpack dirs left behind: %v", left)
	}
	if _, err := os.Stat(filepath.Join(tmp, "x")); err == nil {
		t.Fatal("an entry escaped the unpack dir")
	}
}

// The follow route answers the follower's status, and a sync (the confirm)
// refreshes it.
func TestConfigFollowRoute(t *testing.T) {
	st := NewMemStore()
	f, _ := cfgFetcher(t, cfgFiles)
	env := followEnv
	env.Follower = &ConfigFollower{Store: st, Fetcher: f, Env: followEnv, Now: func() time.Time { return planNow }}
	env.Follower.Tick(context.Background())
	ts := configServerEnv(t, st, f, env)
	var s ConfigFollowStatus
	sendJSON(t, "GET", ts.URL+"/admin/config/follow", "", http.StatusOK, &s)
	if s.State != ConfigFollowAwaiting || s.Head != "c1" {
		t.Fatalf("before confirm: %+v", s)
	}
	var diff ConfigApplyResult
	sendJSON(t, "POST", ts.URL+"/admin/config/diff", `{}`, http.StatusOK, &diff)
	sendJSON(t, "POST", ts.URL+"/admin/config/sync?plan="+diff.Plan.Digest, `{}`, http.StatusOK, nil)
	sendJSON(t, "GET", ts.URL+"/admin/config/follow", "", http.StatusOK, &s)
	if s.State != ConfigFollowInSync || s.Applied != "c1" {
		t.Fatalf("after confirm: %+v", s)
	}
}

func TestConfigSyncRoutes(t *testing.T) {
	st := NewMemStore()
	f, cfg := cfgFetcher(t, cfgFiles)
	ts := configServerEnv(t, st, f, followEnv)
	const src = `{"source":"https://github.com/acme/jam//cfg@main"}`
	for _, m := range []string{"PUT", "DELETE"} {
		if msg := sendJSON(t, m, ts.URL+"/admin/config/source", src, http.StatusNotFound, nil); msg == "" {
			t.Fatalf("%s source answered nothing", m)
		}
	}
	var diff ConfigApplyResult
	sendJSON(t, "POST", ts.URL+"/admin/config/diff", `{}`, http.StatusOK, &diff)
	if diff.Commit != "c1" || diff.Applied || opOf(diff.Plan, ConfigKindUser, "bob").Action != ConfigCreate || len(st.ListUsers()) != 0 {
		t.Fatalf("diff: %+v", diff)
	}
	sendJSON(t, "POST", ts.URL+"/admin/config/sync?plan=stale", `{}`, http.StatusConflict, nil)
	var res ConfigApplyResult
	sendJSON(t, "POST", ts.URL+"/admin/config/sync?plan="+diff.Plan.Digest, `{}`, http.StatusOK, &res)
	if !res.Applied || len(st.ListUsers()) != 1 || res.Source != "https://github.com/acme/jam//cfg@main" {
		t.Fatalf("sync: %+v", res)
	}
	if got, _ := st.GetConfigSource(); got.Commit != "c1" {
		t.Fatalf("record %+v", got)
	}
	// sync needs a JSON body: a browser's no-cors POST cannot send one.
	resp, err := http.Post(ts.URL+"/admin/config/sync", "text/plain", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("text/plain sync = %d", resp.StatusCode)
	}
	cfg.failNext = fmt.Errorf("%w: repository https://github.com/acme/jam not found", gitsource.ErrFetch)
	if msg := sendJSON(t, "POST", ts.URL+"/admin/config/diff", src, http.StatusBadGateway, nil); !strings.Contains(msg, "not found") {
		t.Fatalf("fetch failure: %s", msg)
	}
	none := configServerWith(t, NewMemStore(), f)
	var off ConfigFollowStatus
	sendJSON(t, "GET", none.URL+"/admin/config/follow", "", http.StatusOK, &off)
	if off.State != ConfigFollowOff {
		t.Fatalf("follow without a follower: %+v", off)
	}
	if msg := sendJSON(t, "POST", none.URL+"/admin/config/sync", `{}`, http.StatusConflict, nil); !strings.Contains(msg, "no config repo is configured") {
		t.Fatalf("sync without a followed repo: %s", msg)
	}
}

// A pasted clone URL with a token is refused on every route that takes a
// source, and the token reaches neither an answer nor a log line.
func TestConfigSourceRoutesRefuseCredentialURL(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	st := NewMemStore()
	ts := httptest.NewServer(NewAdminHandler(st, nil, nil, LoopbackAuthenticator{}, allCreds.CredExists, nil, log, nil,
		WithConfig(st, repoFetcher{}, allCreds, log)))
	defer ts.Close()
	const token = "ghp_s3cretTOKEN"
	body := `{"source":"https://x-access-token:` + token + `@github.com/acme/jam@main"}`
	for _, route := range []struct{ method, path string }{{"POST", "/admin/config/diff"}} {
		if msg := sendJSON(t, route.method, ts.URL+route.path, body, http.StatusBadRequest, nil); strings.Contains(msg, token) {
			t.Errorf("%s %s echoes the token: %s", route.method, route.path, msg)
		}
	}
	if strings.Contains(logs.String(), token) {
		t.Fatalf("a log line carries the token:\n%s", logs.String())
	}
	if _, ok := st.GetConfigSource(); ok {
		t.Fatal("a refused source was tracked")
	}
}

// Releasing a mark is gone: the config changes only through git.
func TestConfigManagedRouteGone(t *testing.T) {
	ts := configServer(t, appliedFull(t))
	req, err := http.NewRequest("DELETE", ts.URL+"/admin/config/managed/user/bob", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE config/managed = %d, want 404 or 405", resp.StatusCode)
	}
}

// export-tree renders each kit as its directory: base64 data and modes in
// "kits", none of its files in "files".
func TestConfigExportTreeKitDirs(t *testing.T) {
	st := kitsJam(t)
	ts := configServer(t, st)
	resp, err := http.Get(ts.URL + "/admin/config/export-tree?all=1")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var exp ConfigTreeExport
	if err := json.Unmarshal(raw, &exp); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"kits"`) || strings.Contains(string(raw), `"binary"`) {
		t.Fatalf("export: %s", raw)
	}
	var run ConfigKitFile
	for _, f := range exp.Kits["kits/web"] {
		if f.Path == "run.sh" {
			run = f
		}
	}
	if run.Mode != 0o755 || string(run.Data) != "echo\n" {
		t.Fatalf("run.sh %+v", run)
	}
	if _, ok := exp.Files["kits/web/kit.yml"]; ok {
		t.Fatal("kit.yml is in files")
	}
}

func TestConfigImportMigratesAPreTreeBackup(t *testing.T) {
	st := NewMemStore()
	h := NewAdminHandler(st, nil, nil, LoopbackAuthenticator{}, allCreds.CredExists, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	snap := ConfigSnapshot{Version: 4, Kits: []Kit{{Name: "app", Current: 1, Versions: map[int]string{1: `{"kind":"studio"}`}}}}
	body, _ := json.Marshal(snap)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/admin/config", bytes.NewReader(body))
	req.RemoteAddr = "127.0.0.1:1234" // LoopbackAuthenticator
	h.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("import = %d %s", w.Code, w.Body)
	}
	if k, _ := st.GetKit("app"); k.Trees[1] == "" || len(k.Versions) != 0 {
		t.Fatalf("the imported kit was not migrated: %+v", k)
	}
}

// A broken kit in an imported pre-tree backup opens kit.migrate at once, on
// the tracker the config routes were given (WithConfig's env.Conditions).
func TestConfigImportRaisesKitMigrate(t *testing.T) {
	st := NewMemStore()
	tr := condition.New(condition.Options{})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := NewAdminHandler(st, nil, nil, LoopbackAuthenticator{}, allCreds.CredExists, nil, log, nil,
		WithConfig(st, nil, withConds(allCreds, tr), log))
	snap := ConfigSnapshot{Version: 4, Kits: []Kit{{Name: "app", Current: 1, Versions: map[int]string{1: `{"kind":"studio","base":{"context":"!!not base64"}}`}}}}
	body, _ := json.Marshal(snap)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/admin/config", bytes.NewReader(body))
	req.RemoteAddr = "127.0.0.1:1234" // LoopbackAuthenticator
	h.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("import = %d %s", w.Code, w.Body)
	}
	if !tr.IsOpen(condition.Key(KitMigrateKind, "app")) {
		t.Fatal("kit.migrate:app must be open after the import")
	}
}

func withConds(env ConfigEnv, t *condition.Tracker) ConfigEnv {
	env.Conditions = t
	return env
}

// Applying an arbitrary tree or source is gone: the config changes only
// through git (the followed repo, by sync). Diff, sync and follow stay.
func TestConfigApplyGone(t *testing.T) {
	st := NewMemStore()
	ts := configServer(t, st)
	body := packTree(t, map[string]string{"users/bob.yml": "kind: user\nversion: 1\n"})
	for _, ct := range []string{"application/gzip", "application/json"} {
		req, err := http.NewRequest("POST", ts.URL+"/admin/config/apply", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", ct)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("POST /admin/config/apply (%s) = %d, want 404 or 405", ct, resp.StatusCode)
		}
	}
	if len(st.ListUsers()) != 0 {
		t.Fatal("a removed route wrote")
	}
	var diff ConfigApplyResult
	postTar(t, ts.URL+"/admin/config/diff", body, http.StatusOK, &diff) // a --dir plan stays
	if diff.Applied || len(diff.Plan.Ops) != 1 {
		t.Fatalf("diff: %+v", diff)
	}
	if msg := sendJSON(t, "POST", ts.URL+"/admin/config/sync", `{}`, http.StatusConflict, nil); !strings.Contains(msg, "no config repo is configured") {
		t.Fatalf("sync without a followed repo: %s", msg)
	}
}

// With config in git, the snapshot import (POST /admin/config, jam import) is
// refused: it would be a live edit. diff, sync and follow are not.
func TestConfigImportRefusedWhenInGit(t *testing.T) {
	st := NewMemStore()
	f, _ := cfgFetcher(t, cfgFiles)
	ts := configServerEnv(t, st, f, followEnv)
	if msg := sendJSON(t, "POST", ts.URL+"/admin/config", `{}`, http.StatusConflict, nil); !strings.Contains(msg, "config is in git") {
		t.Fatalf("import = %s", msg)
	}
	sendJSON(t, "POST", ts.URL+"/admin/config/diff", `{}`, http.StatusOK, nil)
	sendJSON(t, "GET", ts.URL+"/admin/config/follow", "", http.StatusOK, nil)
}
