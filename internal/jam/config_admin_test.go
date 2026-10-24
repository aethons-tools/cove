package jam

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
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

	"github.com/aethons-tools/cove/internal/ident"
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
	t.Helper()
	log := slog.New(slog.DiscardHandler)
	ts := httptest.NewServer(NewAdminHandler(st, nil, nil, LoopbackAuthenticator{}, allCreds.CredExists, nil, log, nil,
		WithConfig(st, f, allCreds, log)))
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
	postTar(t, ts.URL+"/admin/config/apply?plan=stale", body, http.StatusConflict, nil)
	var res ConfigApplyResult
	postTar(t, ts.URL+"/admin/config/apply?plan="+diff.Plan.Digest, body, http.StatusOK, &res)
	if !res.Applied || len(st.ListUsers()) != 1 {
		t.Fatalf("%+v", res)
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

	refused := packTree(t, map[string]string{"projects/app/roles/ic.yml": "kind: role\nversion: 1\nkit: missing\n"})
	if msg := postTar(t, ts.URL+"/admin/config/apply", refused, http.StatusConflict, nil); !strings.Contains(msg, "config plan has refusals") {
		t.Fatalf("refused apply: %s", msg)
	}
	if msg := sendJSON(t, "POST", ts.URL+"/admin/config/diff", `{}`, http.StatusConflict, nil); !strings.Contains(msg, "no config source is tracked") {
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
			for _, route := range []string{"diff", "apply"} {
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
		for _, route := range []string{"diff", "apply"} {
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

func TestConfigSourceRoutes(t *testing.T) {
	st := NewMemStore()
	f, cfg := cfgFetcher(t, cfgFiles)
	ts := configServerWith(t, st, f)
	const src = `{"source":"https://github.com/acme/jam//cfg@main"}`
	var tracked ConfigApplyResult
	sendJSON(t, "PUT", ts.URL+"/admin/config/source", src, http.StatusOK, &tracked)
	if tracked.Commit != "c1" || tracked.Applied || opOf(tracked.Plan, ConfigKindUser, "bob").Action != ConfigCreate || len(st.ListUsers()) != 0 {
		t.Fatalf("track: %+v", tracked)
	}
	var diff ConfigApplyResult
	sendJSON(t, "POST", ts.URL+"/admin/config/diff", `{}`, http.StatusOK, &diff)
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
	sendJSON(t, "POST", ts.URL+"/admin/config/apply", src, http.StatusOK, &res)
	if res.Applied {
		t.Fatalf("re-apply wrote: %+v", res)
	}
	sendJSON(t, "DELETE", ts.URL+"/admin/config/source", "", http.StatusNoContent, nil)
	if _, ok := st.GetConfigSource(); ok || len(st.ListConfigMarks()) == 0 {
		t.Fatal("untrack kept the source or dropped the marks")
	}
	sendJSON(t, "POST", ts.URL+"/admin/config/sync", `{}`, http.StatusConflict, nil)
	sendJSON(t, "PUT", ts.URL+"/admin/config/source", `{}`, http.StatusBadRequest, nil)
	cfg.failNext = fmt.Errorf("%w: repository https://github.com/acme/jam not found", gitsource.ErrFetch)
	if msg := sendJSON(t, "POST", ts.URL+"/admin/config/diff", src, http.StatusBadGateway, nil); !strings.Contains(msg, "not found") {
		t.Fatalf("fetch failure: %s", msg)
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
	for _, route := range []struct{ method, path string }{{"PUT", "/admin/config/source"}, {"POST", "/admin/config/diff"}, {"POST", "/admin/config/apply"}} {
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

// A role's name keeps its slash in the route.
func TestConfigReleaseRoute(t *testing.T) {
	st := appliedFull(t)
	ts := configServer(t, st)
	ic := ConfigKey{ConfigKindRole, "app/ic"}
	before := len(st.ListConfigMarks())
	sendJSON(t, "DELETE", ts.URL+"/admin/config/managed/role/app/ic", "", http.StatusNoContent, nil)
	for _, mk := range st.ListConfigMarks() {
		if mk.Key() == ic {
			t.Fatal("role app/ic still managed")
		}
	}
	if len(st.ListConfigMarks()) != before-1 {
		t.Fatalf("release removed other marks: %v", st.ListConfigMarks())
	}
	sendJSON(t, "DELETE", ts.URL+"/admin/config/managed/role/app/ic", "", http.StatusNotFound, nil)
	sendJSON(t, "DELETE", ts.URL+"/admin/config/managed/widget/x", "", http.StatusBadRequest, nil)
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
