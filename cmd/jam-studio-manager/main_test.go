package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func noEnv(string) string { return "" }

func TestRunBadConfigExits2(t *testing.T) {
	var out, errb bytes.Buffer
	missing := filepath.Join(t.TempDir(), "nope.yml")
	if code := run([]string{"run", "--config", missing}, noEnv, &out, &errb); code != 2 {
		t.Fatalf("code = %d, want 2; stderr=%q", code, errb.String())
	}
	if !strings.HasPrefix(errb.String(), "jam-studio-manager run:") {
		t.Fatalf("stderr = %q", errb.String())
	}
}

func TestVersion(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"version"}, noEnv, &out, &errb); code != 0 {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(out.String(), "jam-studio-manager dev") {
		t.Fatalf("stdout = %q", out.String())
	}
}

func TestColimaWired(t *testing.T) {
	var out, errb bytes.Buffer
	run([]string{"colima"}, noEnv, &out, &errb)
	want := "jam-studio-manager colima: expected setup-docker|check-docker"
	if !strings.Contains(out.String()+errb.String(), want) {
		t.Fatalf("out=%q err=%q", out.String(), errb.String())
	}
}
