//go:build integration

package sessionpg_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aethons-tools/cove/internal/jam/sessionevents"
	"github.com/aethons-tools/cove/internal/jam/sessionevents/sessioneventstest"
	"github.com/aethons-tools/cove/internal/jam/sessionevents/sessionpg"
)

func TestPostgresSessionEventsConformance(t *testing.T) {
	dsn := os.Getenv("JAM_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set JAM_TEST_POSTGRES_DSN to run the Postgres session-events integration tests")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	sessioneventstest.RunConformance(t, func(t *testing.T) sessionevents.Store {
		s, err := sessionpg.New(context.Background(), pool, nil)
		if err != nil {
			t.Fatalf("sessionpg.New: %v", err)
		}
		if _, err := pool.Exec(context.Background(), `TRUNCATE session_events`); err != nil {
			t.Fatalf("truncate: %v", err)
		}
		return s
	})
}

// TestPostgresAppendSanitizesRejectedJSON covers lines Go's json.Valid accepts
// but Postgres rejects (data-exception class 22): they must still be stored.
func TestPostgresAppendSanitizesRejectedJSON(t *testing.T) {
	dsn := os.Getenv("JAM_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set JAM_TEST_POSTGRES_DSN to run the Postgres session-events integration tests")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	s, err := sessionpg.New(ctx, pool, nil)
	if err != nil {
		t.Fatalf("sessionpg.New: %v", err)
	}
	if _, err := pool.Exec(ctx, `TRUNCATE session_events`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	const stream = "0123456789abcdef0123456789abcdef"
	var enc string
	if err := pool.QueryRow(ctx, `SHOW server_encoding`).Scan(&enc); err != nil {
		t.Fatalf("server_encoding: %v", err)
	}
	cases := []struct {
		name string
		raw  []byte
		want string // expected stored text
	}{
		{"lone surrogate escape", []byte(`{"a":"\ud800"}`), `{"a":"\ud800"}`},
		{"invalid utf8 in json string", []byte("{\"a\":\"\xff\"}"), "{\"a\":\"\uFFFD\"}"},
		{"invalid utf8 non-json", []byte("not json \xff\xfe tail"), "not json \uFFFD tail"},
	}
	at := time.Unix(1700000000, 0).UTC()
	for i, c := range cases {
		seq := uint64(i + 1)
		t.Run(c.name, func(t *testing.T) {
			if enc != "UTF8" && strings.Contains(c.name, "invalid utf8") {
				t.Skipf("server_encoding is %s; invalid UTF-8 is only rejected under UTF8", enc)
			}
			e := sessionevents.Event{ActorID: "w1", StreamID: stream, Seq: seq, Kind: sessionevents.KindEvent,
				Turn: 1, ObservedAt: at, ReceivedAt: at, Raw: c.raw,
				Stamp: sessionevents.Stamp{Project: "default", Role: "guest", RaisedAt: at}}
			if err := s.Append(e); err != nil {
				t.Fatalf("Append: %v", err)
			}
			got, err := s.List(sessionevents.Filter{ActorID: "w1", StreamID: stream, AfterSeq: seq - 1, Limit: 1})
			if err != nil || len(got) != 1 || got[0].Seq != seq {
				t.Fatalf("List = %v, %v", got, err)
			}
			if string(got[0].Raw) != c.want || strings.ContainsRune(string(got[0].Raw), 0) {
				t.Fatalf("raw = %q, want %q", got[0].Raw, c.want)
			}
		})
	}
}

// TestPostgresAppendSanitizesIndexText: text columns derived from the event
// (type, tool name, ...) must not hold NUL or invalid UTF-8, or the insert —
// and its raw_text fallback — fails and wedges the stream.
func TestPostgresAppendSanitizesIndexText(t *testing.T) {
	dsn := os.Getenv("JAM_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set JAM_TEST_POSTGRES_DSN to run the Postgres session-events integration tests")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	s, err := sessionpg.New(ctx, pool, nil)
	if err != nil {
		t.Fatalf("sessionpg.New: %v", err)
	}
	if _, err := pool.Exec(ctx, `TRUNCATE session_events`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	var enc string
	if err := pool.QueryRow(ctx, `SHOW server_encoding`).Scan(&enc); err != nil {
		t.Fatalf("server_encoding: %v", err)
	}
	const stream = "0123456789abcdef0123456789abcdef"
	at := time.Unix(1700000000, 0).UTC()
	mk := func(seq uint64, raw string, idx sessionevents.Index) sessionevents.Event {
		return sessionevents.Event{ActorID: "w1", StreamID: stream, Seq: seq, Kind: sessionevents.KindEvent,
			Turn: 1, ObservedAt: at, ReceivedAt: at, Raw: []byte(raw), Index: idx,
			Stamp: sessionevents.Stamp{Project: "default", Role: "guest", RaisedAt: at}}
	}
	t.Run("NUL from JSON escapes", func(t *testing.T) {
		raw := `{"type":"assistant\u0000x","message":{"content":[{"type":"tool_use","name":"Ba\u0000sh","input":{}}]}}`
		if err := s.Append(mk(1, raw, sessionevents.DeriveIndex([]byte(raw)))); err != nil {
			t.Fatalf("Append: %v", err)
		}
		got, err := s.List(sessionevents.Filter{ActorID: "w1", StreamID: stream, AfterSeq: 0, Limit: 1})
		if err != nil || len(got) != 1 {
			t.Fatalf("List = %v, %v", got, err)
		}
		if got[0].Index.Type != "assistant�x" || got[0].Index.ToolName != "Ba�sh" {
			t.Fatalf("index = %+v", got[0].Index)
		}
	})
	t.Run("invalid utf8 tool name", func(t *testing.T) {
		if enc != "UTF8" {
			t.Skipf("server_encoding is %s; invalid UTF-8 is only rejected under UTF8", enc)
		}
		if err := s.Append(mk(2, `{"type":"assistant"}`, sessionevents.Index{Type: "assistant", ToolName: "Ba\xffsh"})); err != nil {
			t.Fatalf("Append: %v", err)
		}
		got, err := s.List(sessionevents.Filter{ActorID: "w1", StreamID: stream, AfterSeq: 1, Limit: 1})
		if err != nil || len(got) != 1 || got[0].Index.ToolName != "Ba�sh" {
			t.Fatalf("List = %+v, %v", got, err)
		}
	})
}

// Events carry project and owner ids; BackfillIDs fills those recorded
// before from their name labels, once.
func TestSessionpgProjectOwnerIDs(t *testing.T) {
	s := newIntegrationStore(t)
	at := time.Unix(1000, 0).UTC()
	withID := sessionevents.Event{ActorID: "a1", StreamID: "s1", Seq: 1, Kind: "line", ObservedAt: at, ReceivedAt: at,
		Stamp: sessionevents.Stamp{Project: "acme", ProjectID: "prj_a", Owner: "alice", OwnerID: "usr_a", RaisedAt: at}}
	old := withID
	old.Seq, old.Stamp.ProjectID, old.Stamp.OwnerID = 2, "", ""
	for _, ev := range []sessionevents.Event{withID, old} {
		if err := s.Append(ev); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.BackfillIDs(context.Background(), map[string]string{"acme": "prj_a"}, map[string]string{"alice": "usr_a"})
	if err != nil || n != 2 {
		t.Fatalf("BackfillIDs = %d, %v; want 2 (one row, two columns)", n, err)
	}
	got, err := s.List(sessionevents.Filter{ActorID: "a1", StreamID: "s1"})
	if err != nil || len(got) != 2 {
		t.Fatalf("List = %+v, %v", got, err)
	}
	for _, e := range got {
		if e.Stamp.ProjectID != "prj_a" || e.Stamp.OwnerID != "usr_a" {
			t.Fatalf("event %d stamp = %+v", e.Seq, e.Stamp)
		}
	}
	if n, _ := s.BackfillIDs(context.Background(), map[string]string{"acme": "prj_a"}, nil); n != 0 {
		t.Fatalf("again = %d", n)
	}
}

func newIntegrationStore(t *testing.T) *sessionpg.Store {
	t.Helper()
	dsn := os.Getenv("JAM_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set JAM_TEST_POSTGRES_DSN to run the session-events Postgres integration tests")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	s, err := sessionpg.New(ctx, pool, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `TRUNCATE session_events`); err != nil {
		t.Fatal(err)
	}
	return s
}
