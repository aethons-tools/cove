package sessionevents

import (
	"encoding/json"
	"net/http"
	"strconv"
)

const (
	defaultExportLimit = 1000
	maxExportLimit     = 10000
)

// ExportHandler serves one stream of an actor's session events as NDJSON (the
// Event wire form). Query: stream (default: the newest), after_seq, limit
// (default 1000, max 10000). Mount it behind the operator authenticator —
// events carry agent output.
func ExportHandler(store Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor := r.PathValue("actor_id")
		q := r.URL.Query()
		var after uint64
		if v := q.Get("after_seq"); v != "" {
			n, err := strconv.ParseUint(v, 10, 64)
			if err != nil {
				http.Error(w, "after_seq must be an unsigned integer", http.StatusBadRequest)
				return
			}
			after = n
		}
		limit := defaultExportLimit
		if v := q.Get("limit"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				http.Error(w, "limit must be a positive integer", http.StatusBadRequest)
				return
			}
			limit = min(n, maxExportLimit)
		}
		stream := q.Get("stream")
		if stream != "" && !ValidStreamID(stream) {
			http.Error(w, "invalid stream id", http.StatusBadRequest)
			return
		}
		if stream == "" {
			streams, err := store.Streams(actor)
			if err != nil {
				http.Error(w, "listing streams failed", http.StatusInternalServerError)
				return
			}
			if len(streams) == 0 {
				http.Error(w, "no session events for this actor", http.StatusNotFound)
				return
			}
			stream = streams[0].StreamID
		}
		evs, err := store.List(Filter{ActorID: actor, StreamID: stream, AfterSeq: after, Limit: limit})
		if err != nil {
			http.Error(w, "listing events failed", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Header().Set("Cache-Control", "no-store")
		enc := json.NewEncoder(w)
		for _, e := range evs {
			if err := enc.Encode(e); err != nil {
				return
			}
		}
	})
}
