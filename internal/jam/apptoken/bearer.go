package apptoken

import (
	"net/http"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/appui"
)

// Bearer authenticates an /app/user request by app token: the request acts
// as the token's user (a participant when they are in a project) with the
// operator standing recorded at consent, and is tracked so revoking the token
// ends it.
func (s *Service) Bearer() appui.BearerFunc {
	return func(r *http.Request, raw string) (*http.Request, func(), error) {
		t, u, err := jam.ResolveAppToken(s.c.Store, raw, s.c.Now(), s.c.Log)
		if err != nil {
			return nil, nil, err
		}
		p := appui.Principal{Admin: t.Operator(), Operator: jam.Operator{ID: t.OperatorID}}
		if part, ok := jam.ParticipantByUser(s.c.Store, u.ID); ok {
			p.User = &part
		}
		r = appui.WithPrincipal(r, p)
		ctx, done := s.live.Track(r.Context(), t.ID)
		return r.WithContext(ctx), done, nil
	}
}

// valid reports whether token id may still be used: present, unexpired, and
// its user live.
func (s *Service) valid(id ident.ID) bool {
	t, ok := s.c.Store.AppTokenByID(id)
	if !ok || t.Expired(s.c.Now()) {
		return false
	}
	u, ok := s.c.Store.GetUser(t.UserID)
	return ok && u.Status == jam.StatusLive
}

// sweep ends the open requests of tokens no longer valid.
func (s *Service) sweep() { s.live.Sweep(s.valid) }
