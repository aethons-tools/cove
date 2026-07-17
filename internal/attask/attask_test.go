package attask

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestLookupReturnsBinaryForArch(t *testing.T) {
	fsys := fstest.MapFS{
		"bin/at-task-linux-amd64": {Data: []byte("amd64-binary")},
		"bin/at-task-linux-arm64": {Data: []byte("arm64-binary")},
		"bin/README":              {Data: []byte("placeholder")},
	}
	for _, arch := range []string{"amd64", "arm64"} {
		got, err := lookup(fsys, arch)
		if err != nil {
			t.Fatalf("lookup(%q): unexpected error: %v", arch, err)
		}
		if want := arch + "-binary"; string(got) != want {
			t.Fatalf("lookup(%q) = %q, want %q", arch, got, want)
		}
	}
}

func TestLookupErrorsWhenNotStaged(t *testing.T) {
	// A fresh checkout embeds only the placeholder; the binaries aren't staged.
	fsys := fstest.MapFS{"bin/README": {Data: []byte("placeholder")}}
	_, err := lookup(fsys, "arm64")
	if err == nil {
		t.Fatal("expected an error when the binary is not staged, got nil")
	}
	// The message must be actionable: name the arch and point at the fix.
	for _, want := range []string{"arm64", "build.sh"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q must mention %q", err.Error(), want)
		}
	}
}

func TestLookupErrorsOnEmptyBinary(t *testing.T) {
	// A zero-byte staged file (e.g. a failed build) must not pass as a binary.
	fsys := fstest.MapFS{"bin/at-task-linux-amd64": {Data: []byte{}}}
	if _, err := lookup(fsys, "amd64"); err == nil {
		t.Fatal("expected an error for an empty binary, got nil")
	}
}
