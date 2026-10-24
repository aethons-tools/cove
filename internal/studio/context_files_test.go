package studio

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"testing"
)

// ContextFiles lists every regular file of a stored context — path, mode,
// streamed size and content sum — reading the tar with the scan's checks but
// without requiring a reproducible packing (unsorted names, odd modes and
// directory entries are fine). Only the wanted file's leading bytes are kept.
func TestContextFilesListsAndKeepsWanted(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString(tarGzBytes(t, []tarGzEntry{
		{name: "run.sh", body: "#!/bin/sh\necho hi\n", mode: 0o755},
		{name: "Dockerfile", body: "FROM x\n"},
		{name: "odd", body: "0123456789", mode: 0o664},
	}))
	files, err := ContextFiles(b64, "odd", 4)
	if err != nil {
		t.Fatalf("ContextFiles: %v", err)
	}
	if len(files) != 3 {
		t.Fatalf("files %+v", files)
	}
	by := map[string]ContextEntry{}
	for _, f := range files {
		by[f.Path] = f
	}
	if f := by["run.sh"]; f.Mode != 0o755 || f.Size != 18 || f.Data != nil || f.Sum != sha256.Sum256([]byte("#!/bin/sh\necho hi\n")) {
		t.Errorf("run.sh %+v", f)
	}
	if f := by["odd"]; f.Mode != 0o664 || f.Size != 10 || !bytes.Equal(f.Data, []byte("0123")) || f.Sum != sha256.Sum256([]byte("0123456789")) {
		t.Errorf("odd %+v", f)
	}
	if _, err := ContextFiles(dirEntryContext(t), "", 0); err != nil {
		t.Errorf("a directory entry is not reproducible but is readable: %v", err)
	}
}

func TestContextFilesRefusesUnsafe(t *testing.T) {
	if _, err := ContextFiles("not base64!", "", 0); !errors.Is(err, ErrContextBase64) {
		t.Errorf("bad base64: %v, want ErrContextBase64", err)
	}
	dotdot := base64.StdEncoding.EncodeToString(tarGzBytes(t, []tarGzEntry{{name: "Dockerfile", body: "x"}, {name: "../x", body: "x"}}))
	if _, err := ContextFiles(dotdot, "", 0); err == nil || errors.Is(err, ErrContextBase64) {
		t.Errorf("dotdot: %v", err)
	}
	notGzip := base64.StdEncoding.EncodeToString([]byte("plain"))
	if _, err := ContextFiles(notGzip, "", 0); err == nil {
		t.Error("a non-gzip context must be refused")
	}
}
