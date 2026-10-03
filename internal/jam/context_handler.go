package jam

import (
	"errors"
	"log/slog"
	"net/http"
	"time"
)

// NewContextHandler serves GET /context on the cove-facing listener: the calling
// identity's session context, recompiled from the current config (ContextFor),
// so cove-master can refresh a running session. The identity arrives as a
// bearer (or gh's "token" scheme); unknown or expired → 401, no registered
// instance → 404. Logs the actor id only.
func NewContextHandler(store Store, sup *Supervisor, now func() time.Time, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		tok, ok := presentedToken(r, ApplyBearer)
		if !ok {
			http.Error(w, "missing identity", http.StatusUnauthorized)
			return
		}
		a, ok := store.Lookup(HashToken(tok))
		if !ok || (!a.Expiry.IsZero() && now().After(a.Expiry)) {
			http.Error(w, "unknown identity", http.StatusUnauthorized)
			return
		}
		if sup == nil {
			http.Error(w, "no session context on this Jam", http.StatusNotFound)
			return
		}
		b, err := sup.ContextForActor(a)
		switch {
		case errors.Is(err, ErrNoInstance):
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		case err != nil:
			log.Warn("context compile failed", "actor", a.ID, "err", err.Error())
			http.Error(w, "context unavailable", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, b)
	})
}
