package harbor

import (
	"path/filepath"
	"testing"
)

func TestFileStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ids.json")
	s, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	id := Identity{ID: "spider-18", TokenHash: HashToken("tok"), Project: "ACME", Role: "guest", Destinations: []string{"anthropic"}}
	if err := s.Add(id); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// Reload from disk to prove persistence.
	s2, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got, ok := s2.Lookup(HashToken("tok"))
	if !ok || got.ID != "spider-18" {
		t.Fatalf("Lookup after reload = %+v, %v", got, ok)
	}
	if err := s2.Remove("spider-18"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, ok := s2.Lookup(HashToken("tok")); ok {
		t.Fatal("identity still present after Remove")
	}
}
