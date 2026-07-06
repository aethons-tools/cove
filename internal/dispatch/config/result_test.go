package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "result.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReadResultOK(t *testing.T) {
	p := writeTemp(t, `{"status":"ok","artifacts":{"prUrl":"https://x/pr/1"},"summary":"done"}`)
	r := ReadResult(p)
	if r.Status != StatusOK {
		t.Fatalf("Status = %q; want ok", r.Status)
	}
	if r.Artifacts.PRURL != "https://x/pr/1" {
		t.Fatalf("PRURL = %q", r.Artifacts.PRURL)
	}
}

func TestReadResultNeedsInput(t *testing.T) {
	p := writeTemp(t, `{"status":"needs_input","needsInput":{"blocker":"ambiguous","need":"pick A or B"}}`)
	r := ReadResult(p)
	if r.Status != StatusNeedsInput {
		t.Fatalf("Status = %q; want needs_input", r.Status)
	}
	if r.NeedsInput == nil || r.NeedsInput.Need != "pick A or B" {
		t.Fatalf("NeedsInput parsed wrong: %+v", r.NeedsInput)
	}
}

func TestReadResultMissingFileIsError(t *testing.T) {
	r := ReadResult(filepath.Join(t.TempDir(), "nope.json"))
	if r.Status != StatusError {
		t.Fatalf("Status = %q; want error for missing file", r.Status)
	}
}

func TestReadResultMalformedIsError(t *testing.T) {
	p := writeTemp(t, `{not json`)
	if r := ReadResult(p); r.Status != StatusError {
		t.Fatalf("Status = %q; want error for malformed json", r.Status)
	}
}

func TestReadResultUnknownStatusIsError(t *testing.T) {
	p := writeTemp(t, `{"status":"weird"}`)
	if r := ReadResult(p); r.Status != StatusError {
		t.Fatalf("Status = %q; want error for unknown status", r.Status)
	}
}
