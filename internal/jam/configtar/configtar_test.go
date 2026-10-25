package configtar

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func mustWrite(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil { // past the umask
		t.Fatal(err)
	}
}

// tarEntry is one hand-built archive entry; typ defaults to a regular file.
type tarEntry struct {
	name, body, link string
	typ              byte
}

func tarGz(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, e := range entries {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		h := &tar.Header{Name: e.name, Typeflag: typ, Linkname: e.link, Mode: 0o644}
		if typ == tar.TypeReg {
			h.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if typ == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
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

func manyEntries(n int) []tarEntry {
	out := make([]tarEntry, n)
	for i := range out {
		out[i] = tarEntry{name: fmt.Sprintf("users/u%d.yml", i), body: "x"}
	}
	return out
}

func TestPackUnpackRoundTrip(t *testing.T) {
	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "users/bob.yml"), "kind: user\n", 0o644)
	mustWrite(t, filepath.Join(src, "kits/web/ctx/run.sh"), "#!/bin/sh\n", 0o755)
	mustWrite(t, filepath.Join(src, ".git/HEAD"), "ref: x\n", 0o644)
	a, err := Pack(src)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Pack(src)
	if !bytes.Equal(a, b) {
		t.Fatal("Pack is not deterministic")
	}
	dst := t.TempDir()
	if err := Unpack(bytes.NewReader(a), dst); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(dst, "kits/web/ctx/run.sh")); err != nil || fi.Mode().Perm()&0o100 == 0 {
		t.Fatalf("exec bit lost: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dst, "users/bob.yml")); err != nil || string(got) != "kind: user\n" {
		t.Fatalf("users/bob.yml = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dst, ".git")); !os.IsNotExist(err) {
		t.Fatal(".git packed")
	}
}

func TestPackRefusesSymlinks(t *testing.T) {
	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "users/bob.yml"), "kind: user\n", 0o644)
	if err := os.Symlink("/etc/passwd", filepath.Join(src, "users/eve.yml")); err != nil {
		t.Fatal(err)
	}
	_, err := Pack(src)
	if err == nil || !strings.Contains(err.Error(), "config tree: users/eve.yml is a symlink or not a regular file") {
		t.Fatalf("err = %v", err)
	}
}

func TestUnpackRefuses(t *testing.T) {
	for name, entries := range map[string][]tarEntry{
		"abs":       {{name: "/etc/x", body: "x"}},
		"dotdot":    {{name: "a/../../x", body: "x"}},
		"dotdotdir": {{name: "../x", typ: tar.TypeDir}},
		"symlink":   {{name: "l", link: "/etc/passwd", typ: tar.TypeSymlink}},
		"hardlink":  {{name: "l", link: "users/bob.yml", typ: tar.TypeLink}},
		"device":    {{name: "d", typ: tar.TypeChar}},
		"fifo":      {{name: "f", typ: tar.TypeFifo}},
		"bomb":      {{name: "big", body: strings.Repeat("0", MaxUnpacked+1)}},
		"spread":    {{name: "a", body: strings.Repeat("0", MaxUnpacked/2)}, {name: "b", body: strings.Repeat("0", MaxUnpacked/2+1)}},
		"entries":   manyEntries(MaxEntries + 1),
	} {
		t.Run(name, func(t *testing.T) {
			dest := t.TempDir()
			if err := Unpack(bytes.NewReader(tarGz(t, entries)), dest); err == nil {
				t.Fatal("accepted")
			} else if (name == "bomb" || name == "spread" || name == "entries") && !errors.Is(err, ErrTooLarge) {
				t.Fatalf("want ErrTooLarge, got %v", err)
			}
			if _, err := os.Lstat(filepath.Join(dest, "l")); err == nil && name == "symlink" {
				t.Fatal("symlink created")
			}
		})
	}
}

func TestUnpackRefusesNotGzip(t *testing.T) {
	if err := Unpack(strings.NewReader("not gzip"), t.TempDir()); err == nil || errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v", err)
	}
}

func TestUnpackAcceptsMaxEntries(t *testing.T) {
	if err := Unpack(bytes.NewReader(tarGz(t, manyEntries(MaxEntries))), t.TempDir()); err != nil {
		t.Fatal(err)
	}
}

// Special headers (PAX records, GNU long names) are not entries, so a chain
// of them could decompress far past the caps; the decompressed stream itself
// is bounded.
func TestUnpackBoundsDecompressedHeaders(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	pad := strings.Repeat("x", 100<<10)
	for i := range 300 {
		h := &tar.Header{Name: fmt.Sprintf("users/u%d", i), Typeflag: tar.TypeDir, Mode: 0o755, Format: tar.FormatPAX,
			PAXRecords: map[string]string{"comment": pad}}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if buf.Len() > MaxUpload {
		t.Fatalf("fixture %d bytes", buf.Len())
	}
	if err := Unpack(&buf, t.TempDir()); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
}

// The decompressed bound leaves room for a full archive: MaxUnpacked file
// bytes across MaxEntries entries with long (PAX) names.
func TestUnpackAcceptsFullArchiveWithLongNames(t *testing.T) {
	long := strings.Repeat("d", 150)
	entries := make([]tarEntry, MaxEntries)
	per := MaxUnpacked / MaxEntries
	for i := range entries {
		entries[i] = tarEntry{name: fmt.Sprintf("users/%s%d", long, i), body: strings.Repeat("0", per)}
	}
	if err := Unpack(bytes.NewReader(tarGz(t, entries)), t.TempDir()); err != nil {
		t.Fatal(err)
	}
}

// Unpack sets each file's mode past the serve's umask, so a kit context's
// 0644 and 0755 files read back as the same kit tree.
func TestUnpackModesPastUmask(t *testing.T) {
	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "a"), "a", 0o644)
	mustWrite(t, filepath.Join(src, "run.sh"), "#!/bin/sh\n", 0o755)
	tarball, err := Pack(src)
	if err != nil {
		t.Fatal(err)
	}
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	dst := t.TempDir()
	if err := Unpack(bytes.NewReader(tarball), dst); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]os.FileMode{"a": 0o644, "run.sh": 0o755} {
		if fi, err := os.Stat(filepath.Join(dst, name)); err != nil || fi.Mode().Perm() != want {
			t.Errorf("%s: %v %v, want %o", name, fi, err, want)
		}
	}
}
