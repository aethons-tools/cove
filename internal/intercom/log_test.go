package intercom

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestAppendAssignsIDAndAt(t *testing.T) {
	l, err := Open(filepath.Join(t.TempDir(), "m.jsonl"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	got, err := l.Append(Squawk{From: Target{Kind: "actor", Ref: "a"}, To: []Target{{Kind: "human", Ref: "b"}}, Body: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID == "" || got.At.IsZero() {
		t.Fatalf("Append must assign ID+At: %+v", got)
	}
	// caller-supplied ID/At preserved
	at := time.Unix(5, 0)
	got2, _ := l.Append(Squawk{ID: "fixed", At: at, From: Target{Kind: "actor", Ref: "a"}, To: []Target{{Kind: "human", Ref: "b"}}, Body: "yo"})
	if got2.ID != "fixed" || !got2.At.Equal(at) {
		t.Fatalf("Append must preserve supplied ID/At: %+v", got2)
	}
}

func TestAppendRejectsInvalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.jsonl")
	l, _ := Open(path, nil)
	defer l.Close()
	if _, err := l.Append(Squawk{From: Target{Kind: "actor", Ref: "a"}, Body: ""}); err == nil {
		t.Fatal("expected validation error")
	}
	// nothing written to disk
	if b, _ := os.ReadFile(path); len(b) != 0 {
		t.Fatalf("invalid append must not write: %q", b)
	}
}

func TestPersistenceReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.jsonl")
	l, _ := Open(path, nil)
	for i := 0; i < 3; i++ {
		_, _ = l.Append(Squawk{From: Target{Kind: "actor", Ref: "a"}, To: []Target{{Kind: "human", Ref: "b"}}, Body: "m"})
	}
	l.Close()
	l2, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	if n := len(l2.snapshot()); n != 3 {
		t.Fatalf("reload: got %d messages want 3", n)
	}
}

func TestOpenToleratesTornLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.jsonl")
	l, _ := Open(path, nil)
	_, _ = l.Append(Squawk{From: Target{Kind: "actor", Ref: "a"}, To: []Target{{Kind: "human", Ref: "b"}}, Body: "good"})
	l.Close()
	// simulate a torn final append
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.WriteString(`{"id":"x","body":"trunca`)
	f.Close()
	l2, err := Open(path, nil)
	if err != nil {
		t.Fatalf("Open must tolerate a torn line: %v", err)
	}
	defer l2.Close()
	if n := len(l2.snapshot()); n != 1 {
		t.Fatalf("torn line: want the 1 good message, got %d", n)
	}
	// a subsequent append still yields valid lines
	if _, err := l2.Append(Squawk{From: Target{Kind: "actor", Ref: "a"}, To: []Target{{Kind: "human", Ref: "b"}}, Body: "after"}); err != nil {
		t.Fatal(err)
	}
}

func TestSeqResumesAcrossOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.jsonl")
	l, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	m1, err := l.Append(Squawk{From: Target{Kind: "actor", Ref: "a"}, To: []Target{{Kind: "human", Ref: "b"}}, Body: "one"})
	if err != nil {
		t.Fatal(err)
	}
	m2, err := l.Append(Squawk{From: Target{Kind: "actor", Ref: "a"}, To: []Target{{Kind: "human", Ref: "b"}}, Body: "two"})
	if err != nil {
		t.Fatal(err)
	}
	if m1.Seq <= 0 || m2.Seq != m1.Seq+1 {
		t.Fatalf("Seq not monotonic before reopen: m1=%d m2=%d", m1.Seq, m2.Seq)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	l2, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	m3, err := l2.Append(Squawk{From: Target{Kind: "actor", Ref: "a"}, To: []Target{{Kind: "human", Ref: "b"}}, Body: "three"})
	if err != nil {
		t.Fatal(err)
	}
	if m3.Seq != m2.Seq+1 {
		t.Fatalf("Seq after reopen = %d, want %d (prior max %d + 1)", m3.Seq, m2.Seq+1, m2.Seq)
	}
}

func TestConcurrentAppend(t *testing.T) {
	l, _ := Open(filepath.Join(t.TempDir(), "m.jsonl"), nil)
	defer l.Close()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = l.Append(Squawk{From: Target{Kind: "actor", Ref: "a"}, To: []Target{{Kind: "human", Ref: "b"}}, Body: "c"})
		}()
	}
	wg.Wait()
	if n := len(l.snapshot()); n != 50 {
		t.Fatalf("concurrent append: got %d want 50", n)
	}
}
