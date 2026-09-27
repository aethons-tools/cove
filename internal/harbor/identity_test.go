package harbor

import (
	"path/filepath"
	"testing"
	"time"
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

// PersonalIdle applies the idle-ladder defaults for unset (zero) settings:
// idle-after 4h, nag-every 24h, reclaim-after never (0).
func TestRoleAllocationPersonalIdleDefaults(t *testing.T) {
	idle, nag, reclaim := RoleAllocation{}.PersonalIdle()
	if idle != 4*time.Hour || nag != 24*time.Hour || reclaim != 0 {
		t.Fatalf("defaults = %v, %v, %v; want 4h, 24h, 0", idle, nag, reclaim)
	}
	idle, nag, reclaim = RoleAllocation{IdleAfter: time.Hour, NagEvery: 2 * time.Hour, ReclaimAfter: 72 * time.Hour}.PersonalIdle()
	if idle != time.Hour || nag != 2*time.Hour || reclaim != 72*time.Hour {
		t.Fatalf("set = %v, %v, %v; want 1h, 2h, 72h", idle, nag, reclaim)
	}
}

func TestDiscordInboxOwner(t *testing.T) {
	disc := func(addr string) []DeliveryProfile { return []DeliveryProfile{{Service: "discord", Address: addr}} }
	r := Roster{
		Humans: []Human{
			{Name: "alice", Delivery: disc("inbox-A")},
			{Name: "bob", Delivery: disc("shared")},
			{Name: "carol", Delivery: disc("shared")},
			{Name: "dave", Delivery: []DeliveryProfile{{Service: "slack", Address: "inbox-D"}}},
			{Name: "erin", Delivery: disc("inbox-E")},
			{Name: "noaddr", Delivery: disc("")},
		},
		Channels: []Channel{{Name: "team", Service: "discord", Ref: "inbox-E"}},
	}
	for _, tc := range []struct {
		channel, want string
		ok            bool
	}{
		{"inbox-A", "alice", true}, // unique owner
		{"nobody", "", false},      // no owner
		{"shared", "", false},      // two humans share it
		{"inbox-D", "", false},     // only a non-discord profile uses it
		{"inbox-E", "", false},     // also a roster channel: shared, not an inbox
		{"", "", false},            // the empty channel never matches
	} {
		got, ok := DiscordInboxOwner(r, tc.channel)
		if got != tc.want || ok != tc.ok {
			t.Errorf("DiscordInboxOwner(%q) = %q,%v, want %q,%v", tc.channel, got, ok, tc.want, tc.ok)
		}
	}
}
