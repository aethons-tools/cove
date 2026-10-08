//go:build integration

package conditionpg

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aethons-tools/cove/internal/jam/condition"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("JAM_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set JAM_TEST_POSTGRES_DSN to run the conditionpg integration tests")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	s, err := New(context.Background(), pool, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := pool.Exec(context.Background(), `TRUNCATE attention_conditions`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return s
}

func TestConditionpgRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	open := condition.Condition{Key: "cred.unavailable:x", Severity: condition.Critical, Summary: "s", Fix: "f", Since: now.Add(-time.Hour), LastSeen: now}
	if err := s.Save(ctx, open); err != nil {
		t.Fatal(err)
	}
	open.LastSeen = now.Add(time.Minute) // upsert same occurrence
	if err := s.Save(ctx, open); err != nil {
		t.Fatal(err)
	}
	r1 := now.Add(-48 * time.Hour)
	old := now.Add(-10 * 24 * time.Hour)
	for _, c := range []condition.Condition{
		{Key: "k:recent", Severity: condition.Warning, Summary: "r", Since: r1.Add(-time.Hour), LastSeen: r1, ResolvedAt: &r1},
		{Key: "k:old", Severity: condition.Warning, Summary: "o", Since: old.Add(-time.Hour), LastSeen: old, ResolvedAt: &old},
	} {
		if err := s.Save(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Load(ctx, now.Add(-7*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("Load = %d rows, want open + recent", len(got))
	}
	for _, c := range got {
		if c.Key == "cred.unavailable:x" && (!c.Since.Equal(open.Since) || !c.LastSeen.Equal(open.LastSeen)) {
			t.Fatalf("open occurrence not upserted: %+v", c)
		}
	}
	if err := s.Prune(ctx, now.Add(-7*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	all, _ := s.Load(ctx, time.Time{})
	if len(all) != 2 {
		t.Fatalf("after prune = %d rows, want 2 (old pruned)", len(all))
	}
}
