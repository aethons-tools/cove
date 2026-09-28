package meui

import (
	"embed"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/aethons-tools/cove/internal/jam"
)

//go:embed templates/*.html static/htmx.min.js
var files embed.FS

// staticFS scopes the static route to static/ only — templates must never be
// fetchable via /me/static/.
var staticFS = func() fs.FS {
	sub, err := fs.Sub(files, "static")
	if err != nil {
		panic(err)
	}
	return sub
}()

var pages = map[string]*template.Template{
	"inbox": mustParse("inbox.html"),
}

func mustParse(names ...string) *template.Template {
	paths := make([]string, 0, len(names)+1)
	paths = append(paths, "templates/layout.html")
	for _, n := range names {
		paths = append(paths, "templates/"+n)
	}
	return template.Must(template.ParseFS(files, paths...))
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
func Handler(store Store, log jam.LogReader, lg *slog.Logger) http.Handler {
	if lg == nil {
		lg = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	h := &handler{store: store, log: log, lg: lg}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /me/{$}", h.full)
	mux.HandleFunc("GET /me/rail", h.rail)
	mux.HandleFunc("GET /me/pane", h.pane)
	mux.HandleFunc("POST /me/read", h.markRead)
	mux.Handle("GET /me/static/", http.StripPrefix("/me/static/", http.FileServer(http.FS(staticFS))))
	return mux
}

type handler struct {
	store Store
	log   jam.LogReader
	lg    *slog.Logger
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
		Groups:     Rail(p, h.store, h.log, sel),
		Recipients: newMessageOptions(p, h.store),
	}
	if sel != "" {
		if conv, ok := conversation(p, h.store, h.log, sel); ok {
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

func (h *handler) pane(w http.ResponseWriter, r *http.Request) {
	page, ok := h.build(r)
	if !ok {
		http.Error(w, "no participant", http.StatusUnauthorized)
		return
	}
	h.render(w, "pane", page)
}

// markRead advances the participant's unread cursor for a channel to seq, under
// the self ref of that channel's project (matching how the rail queried it).
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
	if conv, ok := conversation(p, h.store, h.log, channel); ok {
		if ref := selfRefForProject(p, h.store, conv.Project); ref != "" {
			if err := h.store.CommitUnread(ref, channel, seq); err != nil {
				h.lg.Warn("meui mark-read failed", "channel", channel, "error", err.Error())
			}
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
