//go:build integration

package allocpg

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aethons-tools/cove/internal/allocator"
)

// row is the projection the events test helper returns.
type row struct {
	revision      int64
	kind          string
	reservationID string
}

// events reads a stream ordered by revision (test-only helper).
func (s *Store) events(ctx context.Context, streamID string) ([]row, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT stream_revision, kind, reservation_id FROM alloc_events
		 WHERE stream_id = $1 ORDER BY stream_revision`, streamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.revision, &r.kind, &r.reservationID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// newTestStore opens a pool from HARBOR_TEST_POSTGRES_DSN (skipping when unset),
// applies migrations via New, and truncates alloc_events for a clean case.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("HARBOR_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set HARBOR_TEST_POSTGRES_DSN to run the allocpg event-store integration tests")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	s, err := New(context.Background(), pool, nil)
	if err != nil {
		t.Fatalf("allocpg.New: %v", err)
	}
	if _, err := pool.Exec(context.Background(), `TRUNCATE alloc_events`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return s
}

// New applies migrations; the table exists and Record appends sequential revisions.
func TestAllocpg_RecordAppendsSequentialRevisions(t *testing.T) {
	st := newTestStore(t)
	ev := allocator.Event{Category: "acme", Project: "acme", Role: "worker", Kind: allocator.KindReservationGranted, ReservationID: "cove-AET-1"}
	for i := 0; i < 3; i++ {
		if err := st.Record(context.Background(), ev); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := st.events(context.Background(), "acme/worker")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0].revision != 1 || rows[2].revision != 3 {
		t.Fatalf("revisions not 1..3: %+v", rows)
	}
	if rows[0].kind != string(allocator.KindReservationGranted) || rows[0].reservationID != "cove-AET-1" {
		t.Fatalf("unexpected first row: %+v", rows[0])
	}
}

// The UNIQUE(stream_id, stream_revision) gate rejects a duplicate revision — the
// OCC primitive. A raw duplicate INSERT must fail with a unique violation.
func TestAllocpg_DuplicateRevisionRejected(t *testing.T) {
	st := newTestStore(t)
	ins := func(rev int64) error {
		_, err := st.pool.Exec(context.Background(),
			`INSERT INTO alloc_events (category, stream_id, stream_revision, kind) VALUES ($1,$2,$3,$4)`,
			"acme", "acme/worker", rev, "reservation_granted")
		return err
	}
	if err := ins(1); err != nil {
		t.Fatal(err)
	}
	if err := ins(1); !isUniqueViolation(err) {
		t.Fatalf("expected unique violation on duplicate revision, got %v", err)
	}
}

// Outstanding folds a (project, role) stream to granted − released — the ledger
// count the Slice-4 cutover will make authoritative.
func TestAllocpg_OutstandingCountsGrantsMinusReleases(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	grant := allocator.Event{Category: "acme", Project: "acme", Role: "worker", Kind: allocator.KindReservationGranted}
	rel := allocator.Event{Category: "acme", Project: "acme", Role: "worker", Kind: allocator.KindReservationReleased}
	for i := 0; i < 3; i++ {
		if err := st.Record(ctx, grant); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Record(ctx, rel); err != nil {
		t.Fatal(err)
	}
	got, err := st.Outstanding(ctx, "acme", "worker")
	if err != nil {
		t.Fatal(err)
	}
	if got != 2 { // 3 granted − 1 released
		t.Fatalf("Outstanding = %d, want 2", got)
	}
}
