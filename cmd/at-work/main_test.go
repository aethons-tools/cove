package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersion(t *testing.T) {
	old := version
	defer func() { version = old }()
	version = "1.2.3"
	var out, errOut bytes.Buffer
	if code := run([]string{"version"}, &out, &errOut); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if strings.TrimSpace(out.String()) != "at-work 1.2.3" {
		t.Fatalf("stdout = %q", out.String())
	}
}

func TestUnknownCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"bogus"}, &out, &errOut); code != 2 {
		t.Fatalf("exit = %d; want 2", code)
	}
	if !strings.Contains(errOut.String(), "usage:") {
		t.Fatalf("stderr = %q", errOut.String())
	}
}

func TestPrepareRejectsExtraArgs(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"prepare", "x"}, &out, &errOut); code != 2 {
		t.Fatalf("exit = %d; want 2 (prepare takes no arguments)", code)
	}
}

func TestCompleteRejectsExtraArgs(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"complete", "x"}, &out, &errOut); code != 2 {
		t.Fatalf("exit = %d; want 2 (complete takes no arguments)", code)
	}
}
