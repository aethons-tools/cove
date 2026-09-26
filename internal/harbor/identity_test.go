package harbor

import (
	"path/filepath"
	"testing"
)

func newFileStoreT(t *testing.T) *FileStore {
	t.Helper()
	st, err := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestMintTokenIsUniqueAndHashable(t *testing.T) {
	a, err := MintToken()
	if err != nil {
		t.Fatalf("MintToken: %v", err)
	}
	b, _ := MintToken()
	if a == b {
		t.Fatal("two mints returned the same token")
	}
	if len(a) < 40 {
		t.Fatalf("token too short: %d chars", len(a))
	}
	if HashToken(a) == a {
		t.Fatal("hash equals raw token")
	}
	if HashToken(a) != HashToken(a) {
		t.Fatal("hash not stable for same input")
	}
}

func TestHumanDeliveryFor(t *testing.T) {
	h := Human{Name: "alice", Handle: "@alice", Delivery: []DeliveryProfile{{Service: "discord", Address: "chan-1"}}}
	if d, ok := h.DeliveryFor("discord"); !ok || d.Address != "chan-1" {
		t.Fatalf("DeliveryFor(discord) = %+v,%v", d, ok)
	}
	if _, ok := h.DeliveryFor("slack"); ok {
		t.Fatal("DeliveryFor(slack) should miss")
	}
}

func TestHumanByLogin(t *testing.T) {
	store := newFileStoreT(t)
	if err := store.AddHuman("acme", Human{Name: "alice", Handle: "@alice", Login: "auth0|abc"}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddHuman("acme", Human{Name: "bob", Handle: "@bob"}); err != nil { // unlinked
		t.Fatal(err)
	}
	if h, ok := HumanByLogin(store, "acme", "auth0|abc"); !ok || h.Name != "alice" {
		t.Fatalf("HumanByLogin(auth0|abc) = %+v,%v; want alice", h, ok)
	}
	if _, ok := HumanByLogin(store, "acme", "auth0|zzz"); ok {
		t.Fatal("an unlinked login must not match")
	}
	if _, ok := HumanByLogin(store, "acme", ""); ok {
		t.Fatal("the empty login must never match (bob has no login)")
	}
	if _, ok := HumanByLogin(store, "other", "auth0|abc"); ok {
		t.Fatal("a login links a human only within its own project")
	}
}
