package userui

//go:generate sh -c "cd ../../.. && go tool tygo generate"

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/appui"
	"github.com/aethons-tools/cove/internal/jam/uiassets"
	"github.com/aethons-tools/cove/internal/jam/webapp"
)

// apiRoutes registers the JSON API the Jam app (web/app) reads: the user
// area's view models, behind its participant checks
// (docs/superpowers/specs/2026-10-26-jam-spa-design.md §2).
func (h *handler) apiRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /app/user/api/bootstrap", h.apiBootstrap)
	mux.HandleFunc("GET /app/user/api/rail", h.apiRail)
	h.conversationRoutes(mux)
	h.composeRoutes(mux)
	h.askRoutes(mux)
	h.sessionRoutes(mux)
	h.newSessionRoutes(mux)
	h.boardRoutes(mux)
	mux.HandleFunc("/app/user/api/", func(w http.ResponseWriter, r *http.Request) {
		writeAPIError(w, http.StatusNotFound, "not_found", "no such API route")
	})
}

// app serves the Jam app (web/app) for the user area's page paths: webapp,
// with this participant's bootstrap and the app's CSP.
func (h *handler) app() http.Handler {
	return webapp.Handler(webapp.Config{
		Prefix: "/app/user/", FS: uiassets.AppFS(), FrameSrc: h.frameSrc, Name: "the Jam app", Head: appui.HeadMeta,
		Bootstrap: func(r *http.Request) (any, error) {
			b, ok := h.bootstrap(r)
			if !ok {
				return nil, errors.New("userui: no participant on the request (mount the handler behind the user gate)")
			}
			return b, nil
		},
	})
}

// WithFrameSrc lets the app's pages frame origins (CSP frame-src): the
// preview hosts, when the session pane may frame them. None = 'none'.
func WithFrameSrc(origins []string) Option {
	return func(h *handler) { h.frameSrc = origins }
}

func (h *handler) bootstrap(r *http.Request) (Bootstrap, bool) {
	p, ok := jam.ParticipantFrom(r)
	if !ok {
		return Bootstrap{}, false
	}
	tb := appui.TitlebarFor(r, "inbox")
	b := Bootstrap{
		Brand: tb.Brand, Roles: tb.Roles, SignOut: tb.SignOut, Tabs: []NavTab{},
		User:       BootUser{ID: string(p.UserID), Name: p.Name, Initial: initial(p.Name)},
		AttachMax:  h.d.Attachments.MaxBytes(),
		PushKey:    h.pushKey,
		Boards:     h.d.Boards != nil, // the app bundles its own editor
		NewSession: h.d.Personal != nil && h.d.Intercom != nil && h.d.Send != nil,
		PushFocus:  h.focusOn(),
	}
	if pr, ok := appui.PrincipalFrom(r); ok {
		b.Admin = pr.Admin
	}
	for _, t := range tb.Tabs {
		b.Tabs = append(b.Tabs, NavTab{Key: t.Key, Label: t.Label, Href: t.Href, Badge: t.Badge.N, BadgeRed: t.Badge.Red, BadgeTitle: t.Badge.Title})
	}
	return b, true
}

// initial is the avatar letter: the name's first rune, upper-cased; "?" when empty.
func initial(name string) string {
	if c, _ := utf8.DecodeRuneInString(name); c != utf8.RuneError {
		return strings.ToUpper(string(c))
	}
	return "?"
}

func (h *handler) apiBootstrap(w http.ResponseWriter, r *http.Request) {
	b, ok := h.bootstrap(r)
	if !ok {
		writeAPIError(w, http.StatusForbidden, "forbidden", "sign in as a project member")
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (h *handler) apiRail(w http.ResponseWriter, r *http.Request) {
	p, ok := jam.ParticipantFrom(r)
	if !ok {
		writeAPIError(w, http.StatusForbidden, "forbidden", "sign in as a project member")
		return
	}
	// ?c= is the open conversation, counted as read (it is being read).
	groups := Rail(p, h.d, r.URL.Query().Get("c"))
	if groups == nil {
		groups = []RailGroup{}
	}
	writeJSON(w, http.StatusOK, RailResponse{Groups: groups, UnreadTotal: unreadTotal(groups)})
}

// writeJSON writes v as an API response (never cached, never sniffed).
func writeJSON(w http.ResponseWriter, status int, v any) {
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeAPIError writes the API's error envelope.
func writeAPIError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, APIError{Error: msg, Code: code})
}
