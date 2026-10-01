package studio

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// tfile is one extracted tar entry: its content and unix permission bits.
type tfile struct {
	body string
	mode os.FileMode
}

// tarGzEntry is one file to pack into a test tar.gz (mode 0 → 0644).
type tarGzEntry struct {
	name string
	body string
	mode os.FileMode
}

// tarGzBytes builds a gzip-compressed tar from entries.
func tarGzBytes(t *testing.T, entries []tarGzEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for _, e := range entries {
		mode := e.mode
		if mode == 0 {
			mode = 0o644
		}
		if err := tw.WriteHeader(&tar.Header{
			Name: e.name, Mode: int64(mode), Size: int64(len(e.body)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// tarB64 builds a tar.gz from name→content entries (0644) and base64-encodes it.
func tarB64(t *testing.T, entries map[string]string) string {
	t.Helper()
	var es []tarGzEntry
	for name, body := range entries {
		es = append(es, tarGzEntry{name: name, body: body})
	}
	return base64.StdEncoding.EncodeToString(tarGzBytes(t, es))
}

// untarGz reads a tar.gz into a name→tfile map (content + mode).
func untarGz(t *testing.T, raw []byte) map[string]tfile {
	t.Helper()
	gr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	out := map[string]tfile{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar: %v", err)
		}
		var body bytes.Buffer
		if _, err := io.Copy(&body, tr); err != nil { //nolint:gosec // test input is bounded
			t.Fatalf("read %s: %v", hdr.Name, err)
		}
		out[hdr.Name] = tfile{body: body.String(), mode: os.FileMode(hdr.Mode).Perm()}
	}
	return out
}

// extractB64ToDir untars a base64 tar.gz into a fresh dir and returns it (the
// test-side analogue of docker's context extraction).
func extractB64ToDir(t *testing.T, b64 string) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("base64: %v", err)
	}
	dir := filepath.Join(t.TempDir(), "ctx")
	for name, f := range untarGz(t, raw) {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(f.body), f.mode); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}
