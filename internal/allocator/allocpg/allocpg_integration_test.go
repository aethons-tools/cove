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

// ephemeral builds an ephemeral acme/worker grant request for id.
func ephemeral(id string) allocator.Request {
	return allocator.Request{Project: "acme", Role: "worker", ReservationID: id, Kind: allocator.SessionEphemeral}
}

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
		got, err := st.Grant(ctx, ephemeral("cove-"+strconv.Itoa(i)), 2)
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
	got, err := st.Grant(ctx, ephemeral("cove-after-release"), 2)
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
		if _, err := st.Grant(ctx, ephemeral(id), 100); err != nil {
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
	if _, err := st.Grant(ctx, ephemeral("fresh"), 100); err != nil {
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
	got, err := st.Grant(ctx, ephemeral("cove-AET-7"), 3)
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

// Existing rows (and a Record that names no session kind) are ephemeral — the
// 0002 migration's column default — so today's counts are unchanged.
func TestAllocpg_DefaultsToEphemeral(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if err := st.Record(ctx, allocator.Event{Category: "acme", Project: "acme", Role: "worker", Kind: allocator.KindReservationGranted, ReservationID: "r"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx,
		`INSERT INTO alloc_events (category, stream_id, stream_revision, kind, reservation_id) VALUES ('acme','acme/worker',2,'reservation_granted','raw')`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := st.pool.QueryRow(ctx, `SELECT COUNT(*) FROM alloc_events WHERE session_kind = 'ephemeral'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("ephemeral rows = %d, want 2", n)
	}
}

// A cap applies to its own kind only: personal grants do not consume ephemeral
// capacity.
func TestAllocpg_Grant_CapsPerSessionKind(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	req := func(id string, k allocator.SessionKind) allocator.Request {
		return allocator.Request{Project: "acme", Role: "worker", ReservationID: id, Kind: k, Owner: "brent"}
	}
	for _, id := range []string{"p1", "p2"} {
		if ok, err := st.Grant(ctx, req(id, allocator.SessionPersonal), 5); err != nil || !ok {
			t.Fatalf("personal %s: %v,%v", id, ok, err)
		}
	}
	if ok, err := st.Grant(ctx, req("e1", allocator.SessionEphemeral), 1); err != nil || !ok {
		t.Fatalf("e1 should grant (personals don't count): %v,%v", ok, err)
	}
	if ok, err := st.Grant(ctx, req("e2", allocator.SessionEphemeral), 1); err != nil || ok {
		t.Fatalf("e2 should be denied: ephemeral cap 1 reached: %v,%v", ok, err)
	}
	// ... while the personal kind still has its own headroom.
	if ok, err := st.Grant(ctx, req("p3", allocator.SessionPersonal), 3); err != nil || !ok {
		t.Fatalf("p3 should grant (2 personal < 3): %v,%v", ok, err)
	}
}

// A release inherits the kind of the reservation's latest grant, so per-kind
// counts net correctly without release callers knowing the kind.
func TestAllocpg_Release_InheritsSessionKind(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	r := allocator.Request{Project: "acme", Role: "worker", ReservationID: "p1", Kind: allocator.SessionPersonal, Owner: "brent"}
	if ok, err := st.Grant(ctx, r, 1); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err := st.Record(ctx, allocator.Event{Category: "acme", Project: "acme", Role: "worker", Kind: allocator.KindReservationReleased, ReservationID: "p1"}); err != nil {
		t.Fatal(err)
	}
	var kind, owner string
	if err := st.pool.QueryRow(ctx,
		`SELECT session_kind, session_owner FROM alloc_events WHERE kind = $1 AND reservation_id = 'p1'`,
		string(allocator.KindReservationReleased)).Scan(&kind, &owner); err != nil {
		t.Fatal(err)
	}
	if kind != string(allocator.SessionPersonal) || owner != "brent" {
		t.Fatalf("release recorded kind=%q owner=%q, want personal/brent", kind, owner)
	}
	if ok, err := st.Grant(ctx, allocator.Request{Project: "acme", Role: "worker", ReservationID: "p2", Kind: allocator.SessionPersonal}, 1); err != nil || !ok {
		t.Fatalf("p2 should grant once p1's personal slot is released: %v,%v", ok, err)
	}
}

// OutstandingReservations reports each reservation's session kind, name, and
// owner from its latest grant.
func TestAllocpg_OutstandingReservations_ReportsSessionFields(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if ok, err := st.Grant(ctx, allocator.Request{Project: "acme", Role: "worker", ReservationID: "p1", Kind: allocator.SessionPersonal, Owner: "brent"}, 1); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if ok, err := st.Grant(ctx, allocator.Request{Project: "acme", Role: "worker", ReservationID: "s1", Kind: allocator.SessionStanding, Name: "triage"}, 1); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if ok, err := st.Grant(ctx, ephemeral("e1"), 1); err != nil || !ok {
		t.Fatal(ok, err)
	}
	got, err := st.OutstandingReservations(ctx, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]allocator.Reservation{}
	for _, r := range got {
		by[r.ReservationID] = r
	}
	if r := by["p1"]; r.SessionKind != allocator.SessionPersonal || r.Owner != "brent" {
		t.Fatalf("p1 = %+v, want personal owned by brent", r)
	}
	if r := by["s1"]; r.SessionKind != allocator.SessionStanding || r.Name != "triage" {
		t.Fatalf("s1 = %+v, want standing named triage", r)
	}
	if r := by["e1"]; r.SessionKind != allocator.SessionEphemeral || r.Project != "acme" || r.Role != "worker" {
		t.Fatalf("e1 = %+v, want ephemeral acme/worker", r)
	}
}
