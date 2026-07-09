package worker

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type fmtSample struct {
	Name  string `json:"name" yaml:"name"`
	Count int    `json:"count" yaml:"count"`
}

func writeAtWork(t *testing.T, dir, file, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".at-work"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".at-work", file), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestResolveContract(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := resolveContract(dir, "task"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("absent: want ErrNotExist, got %v", err)
	}
	writeAtWork(t, dir, "task.json", "{}")
	p, ext, err := resolveContract(dir, "task")
	if err != nil || ext != ".json" || p != filepath.Join(dir, ".at-work", "task.json") {
		t.Fatalf("json: %q %q %v", p, ext, err)
	}
	writeAtWork(t, dir, "task.yml", "{}")
	if _, _, err := resolveContract(dir, "task"); err == nil {
		t.Fatal("both .json and .yml present: want an error")
	}
}

func TestDecodeStrictAndLenient(t *testing.T) {
	dir := t.TempDir()
	// JSON with an unknown field
	writeAtWork(t, dir, "j.json", `{"name":"a","count":1,"extra":true}`)
	var s fmtSample
	if err := decodeFile(filepath.Join(dir, ".at-work", "j.json"), true, &s); err == nil {
		t.Fatal("strict JSON: unknown field must error")
	}
	if err := decodeFile(filepath.Join(dir, ".at-work", "j.json"), false, &s); err != nil || s.Name != "a" || s.Count != 1 {
		t.Fatalf("lenient JSON: %+v %v", s, err)
	}
	// YAML with an unknown field
	writeAtWork(t, dir, "y.yml", "name: b\ncount: 2\nextra: true\n")
	var s2 fmtSample
	if err := decodeFile(filepath.Join(dir, ".at-work", "y.yml"), true, &s2); err == nil {
		t.Fatal("strict YAML: unknown field must error")
	}
	if err := decodeFile(filepath.Join(dir, ".at-work", "y.yml"), false, &s2); err != nil || s2.Name != "b" {
		t.Fatalf("lenient YAML: %+v %v", s2, err)
	}
}

func TestEncodeFileMirrorsExtension(t *testing.T) {
	dir := t.TempDir()
	jp := filepath.Join(dir, "out.json")
	if err := encodeFile(jp, fmtSample{Name: "x", Count: 3}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(jp); string(b) == "" || b[0] != '{' {
		t.Fatalf("json output not JSON: %q", b)
	}
	yp := filepath.Join(dir, "out.yml")
	if err := encodeFile(yp, fmtSample{Name: "x", Count: 3}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(yp); string(b) == "" || b[0] == '{' {
		t.Fatalf("yaml output looks like JSON: %q", b)
	}
}
