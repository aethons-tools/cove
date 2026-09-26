//go:build integration

package allocpg

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

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

// Grant is the atomic OCC admission gate: it appends a ReservationGranted iff
// Outstanding < budget. With budget 2 the first two grant and the third is denied;
// releasing one frees a slot so the next grant succeeds again.
func TestAllocpg_GrantHonorsBudget(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	for i, want := range []bool{true, true, false} {
		got, err := st.Grant(ctx, "acme", "worker", "cove-"+strconv.Itoa(i), 2)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("grant %d = %v, want %v", i, got, want)
		}
	}
	// release one → a slot frees → next grant succeeds
	if err := st.Record(ctx, allocator.Event{Category: "acme", Project: "acme", Role: "worker", Kind: allocator.KindReservationReleased}); err != nil {
		t.Fatal(err)
	}
	got, err := st.Grant(ctx, "acme", "worker", "cove-after-release", 2)
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Fatal("expected grant after a release freed a slot")
	}
}

// idset projects a reservation slice to a presence set keyed by reservation id.
func idset(rs []allocator.Reservation) map[string]bool {
	m := make(map[string]bool, len(rs))
	for _, r := range rs {
		m[r.ReservationID] = true
	}
	return m
}

// OutstandingReservations returns the net-outstanding reservations (granted −
// released > 0 per reservation id) whose latest grant predates the cutoff.
// Reservation ids recur across dispatch cycles, so this must be a net count, not a
// "has no released row" test: a reservation released then re-granted is outstanding
// again. A future cutoff qualifies every age.
func TestAllocpg_OutstandingReservations_NetCountAndAge(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	g := func(id string) {
		if _, err := st.Grant(ctx, "acme", "worker", id, 100); err != nil {
			t.Fatal(err)
		}
	}
	rel := func(id string) {
		if err := st.Record(ctx, allocator.Event{Category: "acme", Project: "acme", Role: "worker", Kind: allocator.KindReservationReleased, ReservationID: id}); err != nil {
			t.Fatal(err)
		}
	}
	g("a")
	g("b")
	g("c")
	rel("b")                                                               // a,c outstanding; b released
	rel("c")                                                               // c released ...
	g("c")                                                                 // ... then re-granted ⇒ outstanding again (net 1)
	got, err := st.OutstandingReservations(ctx, time.Now().Add(time.Hour)) // future cutoff ⇒ all ages qualify
	if err != nil {
		t.Fatal(err)
	}
	ids := idset(got)
	if !ids["a"] || !ids["c"] || ids["b"] || len(got) != 2 {
		t.Fatalf("outstanding = %v, want {a,c}", got)
	}
	for _, r := range got {
		if r.Project != "acme" || r.Role != "worker" {
			t.Fatalf("unexpected (project,role): %+v", r)
		}
	}
}

// A grace-window cutoff in the past excludes reservations whose latest grant is
// newer than the cutoff — the in-flight-raise guard.
func TestAllocpg_OutstandingReservations_GraceWindowExcludesRecent(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if _, err := st.Grant(ctx, "acme", "worker", "fresh", 100); err != nil {
		t.Fatal(err)
	}
	got, err := st.OutstandingReservations(ctx, time.Now().Add(-time.Hour)) // cutoff an hour ago
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("fresh grant should be inside the grace window, got %v", got)
	}
}

// A granted reservation lands as a ReservationGranted event at head+1 carrying the
// reservation id — so the append is on the same stream Outstanding folds.
func TestAllocpg_GrantAppendsGrantedEvent(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	got, err := st.Grant(ctx, "acme", "worker", "cove-AET-7", 3)
	if err != nil || !got {
		t.Fatalf("Grant = %v, %v; want true, nil", got, err)
	}
	rows, err := st.events(ctx, "acme/worker")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].revision != 1 ||
		rows[0].kind != string(allocator.KindReservationGranted) || rows[0].reservationID != "cove-AET-7" {
		t.Fatalf("unexpected rows after grant: %+v", rows)
	}
}
