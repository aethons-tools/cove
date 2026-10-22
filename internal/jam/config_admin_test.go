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
	log := slog.New(slog.DiscardHandler)
	ts := httptest.NewServer(NewAdminHandler(st, nil, nil, LoopbackAuthenticator{}, allCreds.CredExists, nil, log, nil, nil,
		WithConfig(st, allCreds, log)))
	t.Cleanup(ts.Close)
	return ts
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
	resp, err := http.Post(ts.URL+"/admin/config/diff", "application/json", strings.NewReader(`{"source":"github.com/acme/cfg"}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(b), "config sources (--git) are not supported yet; use --dir") {
		t.Fatalf("json body: %d %s", resp.StatusCode, b)
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
