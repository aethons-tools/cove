package jam

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

// NewContextHandler serves GET /context on the cove-facing listener: the calling
// identity's session context, recompiled from the current config (ContextFor),
// so jam-studio-agent can refresh a running session. The identity arrives as a
// bearer (or gh's "token" scheme); unknown or expired → 401, no registered
// instance → 404. ?turn-protocol=2 asks for the structured turn end's guidance
// (the bundle's TurnProtocol confirms it). Logs the actor id only. Records the
// delivered part as a setup snapshot (on change).
func NewContextHandler(store Store, sup *Supervisor, now func() time.Time, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(sessionctx.EndpointHeader, "1")
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		tok, ok := presentedToken(r, bearerIn)
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
		// Only the structured turn end ("2") is recognized; anything else
		// gets the legacy guidance.
		turnProtocol := 0
		if r.URL.Query().Get("turn-protocol") == strconv.Itoa(sessionctx.TurnProtocolStructured) {
			turnProtocol = sessionctx.TurnProtocolStructured
		}
		if err := sup.SetTurnProtocol(a.ID, turnProtocol); err != nil && !errors.Is(err, ErrNoInstance) {
			log.Warn("context: turn protocol not recorded", "actor", a.ID, "err", err.Error())
		}
		b, err := sup.ContextForTurns(a, turnProtocol)
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
		// after delivery: a write on change must not delay the response
		RecordSetupPart(store, log, a.ID, SetupPartContext, b.Fingerprint, func() string { return contextSnapshotBody(b) }, now())
	})
}
