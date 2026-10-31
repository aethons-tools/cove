package jam

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

func TestRaiseAssignsDistinctColors(t *testing.T) {
	sup, store, _ := supTestKit(t, &fakeLauncher{liveness: LivenessAlive})
	for _, id := range []string{"w1", "w2"} {
		if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: id, Role: "guest"}); err != nil {
			t.Fatal(err)
		}
	}
	c1, ok1 := store.SessionColor("w1")
	c2, ok2 := store.SessionColor("w2")
	if !ok1 || !ok2 || c1 == c2 {
		t.Fatalf("colors = %q/%v, %q/%v; want two different ones", c1, ok1, c2, ok2)
	}
}

func TestReRaiseKeepsTheColor(t *testing.T) {
	sup, store, _ := supTestKit(t, &fakeLauncher{liveness: LivenessAlive})
	ctx := context.Background()
	if _, _, _, err := sup.Raise(ctx, RaiseSpec{ActorID: "w1", Role: "guest"}); err != nil {
		t.Fatal(err)
	}
	before, _ := store.SessionColor("w1")
	// another live session, so a fresh pick would differ from the first color
	if _, _, _, err := sup.Raise(ctx, RaiseSpec{ActorID: "w2", Role: "guest"}); err != nil {
		t.Fatal(err)
	}
	if err := sup.Teardown(ctx, "w1"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := sup.Raise(ctx, RaiseSpec{ActorID: "w1", Role: "guest"}); err != nil {
		t.Fatal(err)
	}
	if after, _ := store.SessionColor("w1"); after != before {
		t.Fatalf("color after re-raise = %q, want %q", after, before)
	}
}

type colorFailStore struct{ Store }

func (colorFailStore) PutSessionColor(string, string) (string, error) {
	return "", errors.New("boom")
}

func TestRaiseSurvivesAColorStoreError(t *testing.T) {
	mem := NewMemStore()
	if err := mem.PutRole("default", Role{Name: "guest", Scope: Scope{Destinations: []string{"anthropic"}, TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	store := colorFailStore{mem}
	sup := NewSupervisor(store, &fakeLauncher{liveness: LivenessAlive}, "holder-A", 60*time.Second, 30*time.Second,
		func() time.Time { return time.Unix(1000, 0).UTC() }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest"}); err != nil {
		t.Fatalf("Raise: %v", err)
	}
	if _, ok := store.GetInstance("w1"); !ok {
		t.Fatal("no instance")
	}
	if _, ok := store.SessionColor("w1"); ok {
		t.Fatal("a color was stored")
	}
}
