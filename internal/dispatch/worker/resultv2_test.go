package worker

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWorkerStatusActive(t *testing.T) {
	if _, err := (WorkerStatus{}).Active(); err == nil {
		t.Fatal("zero variants: want error")
	}
	pr := &PullRequest{Title: "t", Message: "m"}
	v, err := (WorkerStatus{OK: &WorkerOK{PullRequest: pr}}).Active()
	if err != nil || v != "ok" {
		t.Fatalf("ok: %q %v", v, err)
	}
	if _, err := (WorkerStatus{OK: &WorkerOK{}, Error: &WorkerError{Message: "x"}}).Active(); err == nil {
		t.Fatal("two variants: want error")
	}
}

func TestReadWorkerResultLenientAndRaw(t *testing.T) {
	dir := t.TempDir()
	// extra top-level field + extra field inside ok — both must be accepted, and
	// survive in `raw`.
	writeAtWork(t, dir, "worker-result.json",
		`{"status":{"ok":{"pull-request":{"title":"T","message":"M"}}},"note":"kept"}`)
	wr, raw, ok, err := ReadWorkerResult(dir)
	if err != nil || !ok {
		t.Fatalf("read: ok=%v err=%v", ok, err)
	}
	if v, _ := wr.Status.Active(); v != "ok" || wr.Status.OK.PullRequest.Title != "T" {
		t.Fatalf("typed: %+v", wr)
	}
	m, _ := raw.(map[string]any)
	if m["note"] != "kept" {
		t.Fatalf("raw echo dropped unknown field: %v", raw)
	}
}

func TestReadWorkerResultAbsent(t *testing.T) {
	if _, _, ok, err := ReadWorkerResult(t.TempDir()); ok || err != nil {
		t.Fatalf("absent: ok=%v err=%v (want false,nil)", ok, err)
	}
}

func TestWriteTaskResultMirrorsExtension(t *testing.T) {
	tr := TaskResult{
		Status:       TaskStatus{OK: &TaskOK{Message: "opened", PRURL: "https://x/pull/1"}},
		WorkerResult: map[string]any{"status": map[string]any{"ok": map[string]any{}}, "note": "kept"},
	}
	for _, ext := range []string{".json", ".yml"} {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, ".at-work"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := WriteTaskResult(dir, ext, tr); err != nil {
			t.Fatalf("%s: %v", ext, err)
		}
		p := filepath.Join(dir, ".at-work", "task-result"+ext)
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("%s not written: %v", ext, err)
		}
		if ext == ".json" && b[0] != '{' {
			t.Fatalf("json output not JSON: %q", b)
		}
		if ext == ".yml" && b[0] == '{' {
			t.Fatalf("yaml output looks like JSON: %q", b)
		}
		// round-trips back (leniently) with the echo intact
		var back TaskResult
		if err := decodeFile(p, false, &back); err != nil {
			t.Fatalf("%s reparse: %v", ext, err)
		}
		if v, _ := back.Status.ActiveTask(); v != "ok" || back.Status.OK.PRURL == "" {
			t.Fatalf("%s round-trip status: %+v", ext, back.Status)
		}
	}
}
