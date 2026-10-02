package jam

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

func TestValidateIdentity(t *testing.T) {
	ok := []OIDCIdentity{{Issuer: "https://accounts.google.com", Subject: "alice-sub"}}
	if err := ValidateIdentity(ok); err != nil {
		t.Fatalf("ValidateIdentity(%+v) = %v, want nil", ok, err)
	}
	for _, bad := range [][]OIDCIdentity{
		{{Issuer: "", Subject: "x"}},
		{{Issuer: "x", Subject: ""}},
		{{Issuer: "", Subject: ""}},
	} {
		if err := ValidateIdentity(bad); err == nil {
			t.Fatalf("ValidateIdentity(%+v) = nil, want error", bad)
		}
	}
}

func TestHumanByLogin(t *testing.T) {
	store := newFileStoreT(t)
	mustCreateProject(t, store, "acme")
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

func TestHumanByDiscordUser(t *testing.T) {
	r := Roster{Humans: []Human{
		{Name: "alice", Delivery: []DeliveryProfile{{Service: "discord", Address: "inbox-a", UserID: "111"}}},
		{Name: "bob", Delivery: []DeliveryProfile{{Service: "discord", Address: "inbox-b"}}}, // unbound
		{Name: "carol", Delivery: []DeliveryProfile{{Service: "slack", Address: "x", UserID: "333"}}},
	}}
	if h, ok := HumanByDiscordUser(r, "111"); !ok || h.Name != "alice" {
		t.Fatalf("HumanByDiscordUser(111) = %+v,%v; want alice", h, ok)
	}
	if _, ok := HumanByDiscordUser(r, "999"); ok {
		t.Fatal("an unbound id must not match")
	}
	if _, ok := HumanByDiscordUser(r, ""); ok {
		t.Fatal("the empty id must never match (bob is unbound)")
	}
	if _, ok := HumanByDiscordUser(r, "333"); ok {
		t.Fatal("only a discord profile's user id binds")
	}
	// Two humans holding one id (never admitted by the admin route, but fail
	// closed if a roster somehow carries it): nobody matches.
	r.Humans = append(r.Humans, Human{Name: "mallory", Delivery: []DeliveryProfile{{Service: "discord", Address: "inbox-m", UserID: "111"}}})
	if _, ok := HumanByDiscordUser(r, "111"); ok {
		t.Fatal("an id bound to two humans must match nobody")
	}
}

// DiscordAuthor attributes a Discord message to a roster human: never a bot;
// a bound author id in any channel; else a unique inbox whose owner is not
// bound; else nobody (the caller falls back to the display name).
func TestDiscordAuthor(t *testing.T) {
	disc := func(addr, uid string) []DeliveryProfile {
		return []DeliveryProfile{{Service: "discord", Address: addr, UserID: uid}}
	}
	r := Roster{
		Humans: []Human{
			{Name: "alice", Delivery: disc("inbox-A", "111")}, // bound, own inbox
			{Name: "bob", Delivery: disc("inbox-B", "")},      // unbound, own inbox
			{Name: "carol", Delivery: disc("shared", "333")},  // bound, shared inbox
			{Name: "dave", Delivery: disc("shared", "")},      // unbound, shared inbox
		},
		Channels: []Channel{{Name: "team", Service: "discord", Ref: "team-ch"}},
	}
	for _, tc := range []struct {
		name, channel, authorID string
		bot                     bool
		want, by                string
		ok                      bool
	}{
		{"bot with a bound id", "inbox-A", "111", true, "", "", false},
		{"bot in an unbound owner's inbox", "inbox-B", "", true, "", "", false},
		{"bound id in own inbox", "inbox-A", "111", false, "alice", "id", true},
		{"bound id in a shared inbox", "shared", "333", false, "carol", "id", true},
		{"bound id in another human's inbox", "inbox-B", "111", false, "alice", "id", true},
		{"bound id in a roster channel", "team-ch", "333", false, "carol", "id", true},
		{"unique inbox, unbound owner", "inbox-B", "999", false, "bob", "channel", true},
		{"unique inbox, bound owner, other author", "inbox-A", "999", false, "", "", false},
		{"unique inbox, bound owner, empty author id", "inbox-A", "", false, "", "", false},
		{"empty author id, unbound owner (older events)", "inbox-B", "", false, "bob", "channel", true},
		{"unbound author in a shared inbox", "shared", "999", false, "", "", false},
		{"id bound in another project only", "shared", "555", false, "", "", false},
		{"unknown channel, unknown id", "nowhere", "999", false, "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, by, ok := DiscordAuthor(r, tc.channel, tc.authorID, tc.bot)
			if got != tc.want || by != tc.by || ok != tc.ok {
				t.Fatalf("DiscordAuthor(%q, %q, bot=%v) = %q,%q,%v; want %q,%q,%v", tc.channel, tc.authorID, tc.bot, got, by, ok, tc.want, tc.by, tc.ok)
			}
		})
	}
}
