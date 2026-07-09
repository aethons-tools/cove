package worker

import "testing"

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
