package jam

import (
	"errors"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam/condition"
)

type flakyCreds struct{ err error }

func (f *flakyCreds) Resolve(name string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return "VAL", nil
}

func TestWatchedResolverRaisesAfterThresholdAndClears(t *testing.T) {
	tr := condition.New(condition.Options{})
	base := &flakyCreds{err: errors.New("boom")}
	w := NewWatchedResolver(base, tr, func(n string) string { return "check " + n })
	key := condition.Key("cred.unavailable", "vertex-gcp")
	for i := 0; i < CredFailThreshold-1; i++ {
		_, _ = w.Resolve("vertex-gcp")
	}
	if tr.IsOpen(key) {
		t.Fatal("raised below threshold")
	}
	_, _ = w.ResolveFor("vertex-gcp", "idhash") // ResolveFor counts too
	c, ok := tr.Get(key)
	if !ok || c.Severity != condition.Critical || c.Fix != "check vertex-gcp" {
		t.Fatalf("condition = %+v, ok=%v", c, ok)
	}
	base.err = nil
	if v, err := w.Resolve("vertex-gcp"); err != nil || v != "VAL" {
		t.Fatalf("passthrough: %q %v", v, err)
	}
	if tr.IsOpen(key) {
		t.Fatal("success did not clear")
	}
}

func TestWatchedResolverNeverCopiesErrorText(t *testing.T) {
	tr := condition.New(condition.Options{})
	w := NewWatchedResolver(&flakyCreds{err: errors.New("resolver printed sekrit-token-123")}, tr, func(string) string { return "" })
	for i := 0; i < CredFailThreshold; i++ {
		_, _ = w.Resolve("c")
	}
	c, ok := tr.Get(condition.Key("cred.unavailable", "c"))
	if !ok {
		t.Fatal("not raised")
	}
	if strings.Contains(c.Summary+c.Detail+c.Fix, "sekrit") {
		t.Fatalf("secret copied into condition: %+v", c)
	}
}

func TestWatchedResolverKeepsPoolAndIdentity(t *testing.T) {
	pool := &ChainResolver{base: &flakyCreds{}, poolCred: "anthropic-sub"}
	w := NewWatchedResolver(pool, condition.New(condition.Options{}), func(string) string { return "" })
	if !w.PoolCredential("anthropic-sub") || w.PoolCredential("other") {
		t.Fatal("PoolCredential not delegated")
	}
	if w2 := NewWatchedResolver(&flakyCreds{}, nil, nil); w2.PoolCredential("x") {
		t.Fatal("non-pool base reported a pool credential")
	}
	var _ IdentityCredResolver = w
	var _ PoolCredResolver = w
}
