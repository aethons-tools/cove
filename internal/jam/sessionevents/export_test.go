package sessionevents_test

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/sessionevents"
)

func exportServer(t *testing.T, st sessionevents.Store) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /admin/sessions/{actor_id}/events", sessionevents.ExportHandler(st))
	return mux
}

func TestExportNDJSONLatestStreamByDefault(t *testing.T) {
	st := sessionevents.NewMemStore()
	older, newer := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	t0 := time.Unix(1000, 0)
	st.Append(sessionevents.Event{ActorID: "w1", StreamID: older, Seq: 1, Kind: "event", ReceivedAt: t0, Raw: []byte(`{"n":0}`)})
	for i := uint64(1); i <= 3; i++ {
		st.Append(sessionevents.Event{ActorID: "w1", StreamID: newer, Seq: i, Kind: "event", ReceivedAt: t0.Add(time.Hour), Raw: []byte(`{"n":1}`)})
	}
	rec := httptest.NewRecorder()
	exportServer(t, st).ServeHTTP(rec, httptest.NewRequest("GET", "/admin/sessions/w1/events?after_seq=1&limit=1", nil))
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/x-ndjson" {
		t.Fatalf("%d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control = %q", cc)
	}
	sc := bufio.NewScanner(rec.Body)
	var lines []map[string]any
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, m)
	}
	if len(lines) != 1 || lines[0]["stream_id"] != newer || lines[0]["seq"].(float64) != 2 {
		t.Fatalf("lines %+v", lines)
	}
}

func TestExportErrors(t *testing.T) {
	st := sessionevents.NewMemStore()
	h := exportServer(t, st)
	for path, code := range map[string]int{
		"/admin/sessions/nobody/events":           404,
		"/admin/sessions/w1/events?after_seq=x":   400,
		"/admin/sessions/w1/events?stream=../etc": 400,
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != code {
			t.Errorf("%s: got %d want %d", path, rec.Code, code)
		}
	}
}
