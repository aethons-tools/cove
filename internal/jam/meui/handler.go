package meui

import (
	"embed"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/uiassets"
)

//go:embed templates/*.html
var files embed.FS

var pages = map[string]*template.Template{
	"inbox": mustParse("inbox.html"),
}

func mustParse(names ...string) *template.Template {
	paths := make([]string, 0, len(names)+1)
	paths = append(paths, "templates/layout.html")
	for _, n := range names {
		paths = append(paths, "templates/"+n)
	}
	return template.Must(template.New("").Funcs(template.FuncMap{
		"stylesheet": func() string { return uiassets.StylesheetHref("/me/static/") },
	}).ParseFS(files, paths...))
}

// inboxPage is the full data for the two-pane inbox.
type inboxPage struct {
	Me         string
	Groups     []RailGroup
	Conv       *Conversation
	Recipients []NewMessageOption
}

// Handler serves the participant intercom inbox under /me. It reads identity per
// request from jam.ParticipantFrom (the /me gate injects it) — never a
// constructor argument — so one handler serves every participant. lg may be nil.
func Handler(d Deps, lg *slog.Logger, opts ...Option) http.Handler {
	if lg == nil {
		lg = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	h := &handler{d: d, lg: lg}
	for _, opt := range opts {
		opt(h)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /me/{$}", h.full)
	mux.HandleFunc("GET /me/rail", h.rail)
	mux.HandleFunc("GET /me/stream", h.stream)
	mux.HandleFunc("POST /me/read", h.markRead)
	mux.HandleFunc("GET /me/presence", h.presenceStrip)
	mux.HandleFunc("GET /me/events", h.events)
	// The shared assets (jam.css, htmx) — templates are never reachable here.
	mux.Handle("GET /me/static/", uiassets.Handler("/me/static/"))
	return mux
}

type handler struct {
	d        Deps
	lg       *slog.Logger
	changes  Changes  // nil = no `changed` push
	presence Presence // nil = no `presence` push; live sessions read "working"
	// Without either, GET /me/events answers 204 and the page polls.
}

// build assembles the page for the request's participant and ?c selection.
func (h *handler) build(r *http.Request) (inboxPage, bool) {
	p, ok := jam.ParticipantFrom(r)
	if !ok {
		return inboxPage{}, false
	}
	sel := r.URL.Query().Get("c")
	page := inboxPage{
		Me:         p.Name,
		Groups:     Rail(p, h.d, sel),
		Recipients: newMessageOptions(p, h.d),
	}
	if sel != "" {
		if conv, ok := conversation(p, h.d, sel); ok {
			conv.Sessions = sessionRows(conv.SessionIDs, h.d.Store.ListInstances(), h.presence)
			page.Conv = &conv
		}
	}
	return page, true
}

func (h *handler) render(w http.ResponseWriter, tmpl string, page inboxPage) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	t := pages["inbox"]
	var err error
	if tmpl == "" {
		err = t.ExecuteTemplate(w, "layout", page)
	} else {
		err = t.ExecuteTemplate(w, tmpl, page)
	}
	if err != nil {
		h.lg.Error("meui render failed", "tmpl", tmpl, "error", err.Error())
	}
}

func (h *handler) full(w http.ResponseWriter, r *http.Request) {
	page, ok := h.build(r)
	if !ok {
		http.Error(w, "no participant", http.StatusUnauthorized)
		return
	}
	h.render(w, "", page)
}

func (h *handler) rail(w http.ResponseWriter, r *http.Request) {
	page, ok := h.build(r)
	if !ok {
		http.Error(w, "no participant", http.StatusUnauthorized)
		return
	}
	h.render(w, "rail", page)
}

// stream renders just the open conversation's message list (the #stream poll
// target). It never re-renders the composer, so a half-typed reply survives.
func (h *handler) stream(w http.ResponseWriter, r *http.Request) {
	page, ok := h.build(r)
	if !ok {
		http.Error(w, "no participant", http.StatusUnauthorized)
		return
	}
	h.render(w, "messages", page)
}

// presenceStrip renders just the open conversation's session statuses (the
// #presence refresh target).
func (h *handler) presenceStrip(w http.ResponseWriter, r *http.Request) {
	page, ok := h.build(r)
	if !ok {
		http.Error(w, "no participant", http.StatusUnauthorized)
		return
	}
	h.render(w, "sessions", page)
}

// markRead advances the participant's read cursor on a channel to seq (a
// History channel has none: it is all read).
func (h *handler) markRead(w http.ResponseWriter, r *http.Request) {
	p, ok := jam.ParticipantFrom(r)
	if !ok {
		http.Error(w, "no participant", http.StatusUnauthorized)
		return
	}
	channel := r.FormValue("channel")
	seq, _ := strconv.ParseInt(r.FormValue("seq"), 10, 64)
	if channel == "" {
		http.Error(w, "empty channel", http.StatusBadRequest)
		return
	}
	if strings.HasPrefix(channel, legacyPrefix) || h.d.Log == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if _, ok := jam.UserChannel(h.d.Store, h.d.Intercom, h.d.Log, p.UserID, ident.ID(channel)); ok {
		if err := h.d.Store.CommitChannelRead(p.UserID, ident.ID(channel), seq); err != nil {
			h.lg.Warn("meui mark-read failed", "channel", channel, "error", err.Error())
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
