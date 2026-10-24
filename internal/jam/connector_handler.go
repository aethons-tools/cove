package jam

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/aethons-tools/cove/internal/jam/snippet"
)

// NewConnectorHandler serves GET /connector on the cove-facing listener: the
// calling identity's client connector (ConnectorFor), so a host-side client
// learns what env and git routing its destinations need. The identity arrives
// as a bearer (or gh's "token" scheme); unknown or expired → 401, an env/git
// conflict among the identity's destinations → 409. Logs the actor id only.
// Records the delivered part as a setup snapshot (on change).
func NewConnectorHandler(store Store, now func() time.Time, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		c, err := ConnectorFor(store, a)
		if err != nil {
			log.Warn("connector conflict", "actor", a.ID, "reason", err.Error())
			http.Error(w, "connector conflict: "+err.Error(), http.StatusConflict)
			return
		}
		writeJSON(w, http.StatusOK, c)
		// after delivery: a write on change must not delay the response
		if _, ok := store.GetInstance(a.ID); ok { // a hand-enrolled client has no studio to record
			RecordSetupPart(store, log, a.ID, SetupPartConnector, snippet.Fingerprint(c), func() string { return connectorBody(c) }, now())
		}
	})
}
