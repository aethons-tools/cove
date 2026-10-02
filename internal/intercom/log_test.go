package intercom

import (
	"sync"
	"testing"
	"time"
)

func TestAppendAssignsIDAndAt(t *testing.T) {
	l := NewMemLog()
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
	l := NewMemLog()
	if _, err := l.Append(Squawk{From: Target{Kind: "actor", Ref: "a"}, Body: ""}); err == nil {
		t.Fatal("expected validation error")
	}
	if n := len(l.snapshot()); n != 0 {
		t.Fatalf("invalid append must not store: %d messages", n)
	}
}

func TestSeqMonotonicFromOne(t *testing.T) {
	l := NewMemLog()
	for want := int64(1); want <= 3; want++ {
		m, err := l.Append(Squawk{From: Target{Kind: "actor", Ref: "a"}, To: []Target{{Kind: "human", Ref: "b"}}, Body: "m"})
		if err != nil {
			t.Fatal(err)
		}
		if m.Seq != want {
			t.Fatalf("Seq = %d, want %d", m.Seq, want)
		}
	}
}

func TestConcurrentAppend(t *testing.T) {
	l := NewMemLog()
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
