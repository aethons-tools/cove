package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestJamVersionAndMintVersion(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"version"}, func(string) string { return "" }, &out, &errb); code != 0 || !strings.HasPrefix(out.String(), "jam ") {
		t.Fatalf("jam version: code %d out %q err %q", code, out.String(), errb.String())
	}
	out.Reset()
	if code := run([]string{"mint", "version"}, func(string) string { return "" }, &out, &errb); code != 0 || !strings.HasPrefix(out.String(), "jam mint ") {
		t.Fatalf("jam mint version: code %d out %q err %q", code, out.String(), errb.String())
	}
}
