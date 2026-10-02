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
