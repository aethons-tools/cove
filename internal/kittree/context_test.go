package kittree

import (
	"archive/tar"
	"bytes"
	"io"
	"testing"
	"time"
)

func TestContextTarIsTreeMinusKitYML(t *testing.T) {
	tr, fsys, _ := sampleFS(t)
	var a, b bytes.Buffer
	if err := ContextTar(&a, fsys); err != nil {
		t.Fatal(err)
	}
	if err := ContextTar(&b, fsys); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Fatal("not deterministic")
	}
	want := tr.Without(KitFile).Entries
	r := tar.NewReader(&a)
	for i := 0; ; i++ {
		hdr, err := r.Next()
		if err == io.EOF {
			if i != len(want) {
				t.Fatalf("%d entries, want %d", i, len(want))
			}
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(r)
		e := want[i]
		if hdr.Name != e.Path || hdr.Mode != int64(e.Mode) || hexSum(data) != e.SHA256 || !hdr.ModTime.Equal(time.Unix(0, 0)) || hdr.Uid != 0 || hdr.Gid != 0 || hdr.Uname != "" || hdr.Gname != "" {
			t.Fatalf("entry %d = %+v", i, hdr)
		}
	}
}
