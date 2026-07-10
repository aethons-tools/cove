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
	writeAtTask(t, dir, "worker-result.json",
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

func TestWriteTaskResultCreatesDir(t *testing.T) {
	dir := t.TempDir() // no .at-task/ yet
	tr := ErrorResult("boom", "detail")
	if err := WriteTaskResult(dir, ".json", tr); err != nil {
		t.Fatalf("WriteTaskResult: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".at-task", "task-result.json")); err != nil {
		t.Fatalf("task-result not written: %v", err)
	}
}

func TestErrorResult(t *testing.T) {
	tr := ErrorResult("msg", "det")
	v, err := tr.Status.ActiveTask()
	if err != nil || v != "error" {
		t.Fatalf("variant = %q err=%v; want error", v, err)
	}
	if tr.Status.Error.Message != "msg" || tr.Status.Error.Detail != "det" {
		t.Fatalf("error = %+v", tr.Status.Error)
	}
	if tr.WorkerResult != nil {
		t.Fatalf("ErrorResult must not echo a worker-result: %v", tr.WorkerResult)
	}
}

func TestWorkerResultFrom(t *testing.T) {
	// nil → not ok
	if _, ok := WorkerResultFrom(nil); ok {
		t.Fatal("nil raw should be not-ok")
	}
	// a decoded needs-input echo (as it arrives from json.Unmarshal into `any`)
	raw := map[string]any{"status": map[string]any{"needs-input": map[string]any{
		"doing": "d", "blocker": "b", "need": "n", "tried": "tr",
	}}}
	wr, ok := WorkerResultFrom(raw)
	if !ok || wr.Status.NeedsInput == nil || wr.Status.NeedsInput.Blocker != "b" {
		t.Fatalf("decode: ok=%v wr=%+v", ok, wr.Status)
	}
}

func TestWriteTaskResultMirrorsExtension(t *testing.T) {
	tr := TaskResult{
		Status:       TaskStatus{OK: &TaskOK{Message: "opened", PRURL: "https://x/pull/1"}},
		WorkerResult: map[string]any{"status": map[string]any{"ok": map[string]any{}}, "note": "kept"},
	}
	for _, ext := range []string{".json", ".yml"} {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, ".at-task"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := WriteTaskResult(dir, ext, tr); err != nil {
			t.Fatalf("%s: %v", ext, err)
		}
		p := filepath.Join(dir, ".at-task", "task-result"+ext)
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
