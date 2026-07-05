package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersionPrintsStampedValue(t *testing.T) {
	old := version
	defer func() { version = old }()
	version = "1.2.3"

	var out, errOut bytes.Buffer
	code := run([]string{"version"}, &out, &errOut)

	if code != 0 {
		t.Fatalf("exit = %d; want 0 (stderr: %q)", code, errOut.String())
	}
	if got := strings.TrimSpace(out.String()); got != "1.2.3" {
		t.Fatalf("stdout = %q; want %q", got, "1.2.3")
	}
}

func TestServeReportsNotImplemented(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"serve"}, &out, &errOut)

	if code != 1 {
		t.Fatalf("exit = %d; want 1", code)
	}
	if !strings.Contains(errOut.String(), "not implemented") {
		t.Fatalf("stderr = %q; want it to mention 'not implemented'", errOut.String())
	}
}

func TestUnknownCommandPrintsUsage(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"bogus"}, &out, &errOut)

	if code != 2 {
		t.Fatalf("exit = %d; want 2", code)
	}
	if !strings.Contains(errOut.String(), "unknown command") ||
		!strings.Contains(errOut.String(), "Usage:") {
		t.Fatalf("stderr = %q; want 'unknown command' and 'Usage:'", errOut.String())
	}
}

func TestNoArgsPrintsUsage(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run(nil, &out, &errOut)

	if code != 2 {
		t.Fatalf("exit = %d; want 2", code)
	}
	if !strings.Contains(errOut.String(), "Usage:") {
		t.Fatalf("stderr = %q; want usage text", errOut.String())
	}
}
