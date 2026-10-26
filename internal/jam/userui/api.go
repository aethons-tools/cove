package userui

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/appui"
	"github.com/aethons-tools/cove/internal/jam/uiassets"
	"github.com/aethons-tools/cove/internal/jam/webapp"
)

// apiRoutes registers the JSON API the Jam app (web/app) reads: the same view
// models the templates render, behind the same participant checks
// (docs/superpowers/specs/2026-10-26-jam-spa-design.md §2).
func (h *handler) apiRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /app/user/api/bootstrap", h.apiBootstrap)
	mux.HandleFunc("GET /app/user/api/rail", h.apiRail)
	mux.HandleFunc("/app/user/api/", func(w http.ResponseWriter, r *http.Request) {
		writeAPIError(w, http.StatusNotFound, "not_found", "no such API route")
	})
}

// Next serves the Jam app at /app/next/ (mount it behind the user gate):
// webapp with this participant's bootstrap. frameSrc are the origins its
// panes may frame (none yet: the Studio slice passes the preview origins).
func Next(d Deps, lg *slog.Logger, frameSrc []string, opts ...Option) http.Handler {
	h := newHandler(d, lg, opts...)
	return webapp.Handler(webapp.Config{
		Prefix: "/app/next/", FS: uiassets.AppFS(), FrameSrc: frameSrc, Name: "the Jam app",
		Bootstrap: func(r *http.Request) (any, error) {
			b, ok := h.bootstrap(r)
			if !ok {
				return nil, errors.New("userui: no participant on the request (mount Next behind the user gate)")
			}
			return b, nil
		},
	})
}

func (h *handler) bootstrap(r *http.Request) (Bootstrap, bool) {
	p, ok := jam.ParticipantFrom(r)
	if !ok {
		return Bootstrap{}, false
	}
	tb := appui.TitlebarFor(r, "inbox")
	b := Bootstrap{
		Brand: tb.Brand, Roles: tb.Roles, SignOut: tb.SignOut, Tabs: []NavTab{},
		User:      BootUser{ID: string(p.UserID), Name: p.Name, Initial: initial(p.Name)},
		AttachMax: h.d.Attachments.MaxBytes(),
		PushKey:   h.pushKey,
		Boards:    h.d.Boards != nil && uiassets.WhiteboardBuilt(),
	}
	if pr, ok := appui.PrincipalFrom(r); ok {
		b.Admin = pr.Admin
	}
	for _, t := range tb.Tabs {
		href := t.Href
		if t.Key == "inbox" {
			href = "/app/next/" // the app's own Inbox while it lives here
		}
		b.Tabs = append(b.Tabs, NavTab{Key: t.Key, Label: t.Label, Href: href, Badge: t.Badge.N, BadgeRed: t.Badge.Red, BadgeTitle: t.Badge.Title})
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
	groups := Rail(p, h.d, "")
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
