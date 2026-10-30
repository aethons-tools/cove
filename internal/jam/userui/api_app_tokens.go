package userui

import (
	"cmp"
	"net/http"
	"slices"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
)

// WithAppTokenRevoke is called with each app token the user revokes, to end
// its open requests (apptoken.Live.Revoke).
func WithAppTokenRevoke(revoke func(ident.ID)) Option {
	return func(h *handler) { h.appTokenRevoke = revoke }
}

// appTokenRoutes serves the viewer's app tokens (Connected apps):
// docs/usage/jam/app-tokens.md.
func (h *handler) appTokenRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /app/user/api/app-tokens", h.apiAppTokens)
	mux.HandleFunc("DELETE /app/user/api/app-tokens/{id}", h.apiAppTokenDelete)
}

func (h *handler) apiAppTokens(w http.ResponseWriter, r *http.Request) {
	p, ok := jam.ParticipantFrom(r)
	if !ok {
		writeAPIError(w, http.StatusForbidden, "forbidden", "sign in as a project member")
		return
	}
	toks := h.d.Store.AppTokens(p.UserID)
	slices.SortFunc(toks, func(a, b jam.AppToken) int {
		return cmp.Or(b.CreatedAt.Compare(a.CreatedAt), cmp.Compare(b.ID, a.ID))
	})
	out := AppTokensResponse{Tokens: []AppTokenRow{}}
	for _, t := range toks {
		out.Tokens = append(out.Tokens, AppTokenRow{ID: string(t.ID), Label: t.Label, Created: t.CreatedAt,
			LastUsed: t.LastUsedAt, Expires: t.ExpiresAt, Operator: t.Operator()})
	}
	writeJSON(w, http.StatusOK, out)
}

// apiAppTokenDelete revokes one of the viewer's tokens. It answers 204
// whether or not the token was theirs (or existed), like push unsubscribe.
func (h *handler) apiAppTokenDelete(w http.ResponseWriter, r *http.Request) {
	p, ok := jam.ParticipantFrom(r)
	if !ok {
		writeAPIError(w, http.StatusForbidden, "forbidden", "sign in as a project member")
		return
	}
	id, err := ident.Parse(r.PathValue("id"))
	if err == nil && id.Kind() == ident.AppToken {
		deleted, err := h.d.Store.DeleteUserAppToken(p.UserID, id)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "internal", "could not revoke the app token")
			return
		}
		if deleted {
			h.lg.Info("app token revoked", "user", p.UserID, "token", id)
			if h.appTokenRevoke != nil {
				h.appTokenRevoke(id)
			}
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
