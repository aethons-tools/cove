package userui

import (
	"embed"
	"errors"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

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
		"stylesheet": func() string { return uiassets.StylesheetHref("/app/static/") },
		"mb":         func(n int64) int64 { return n >> 20 },
	}).ParseFS(files, paths...))
}

// inboxPage is the full data for the two-pane inbox.
type inboxPage struct {
	Me         string
	Groups     []RailGroup
	Conv       *Conversation
	Recipients []NewMessageOption
	AttachMax  int64 // per-file cap; 0 = attachments off
}

// Handler serves the participant intercom inbox under /app/user. It reads identity per
// request from jam.ParticipantFrom (the /app/user gate injects it) — never a
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
	mux.HandleFunc("GET /app/user/{$}", h.full)
	mux.HandleFunc("GET /app/user/rail", h.rail)
	mux.HandleFunc("GET /app/user/stream", h.stream)
	mux.HandleFunc("POST /app/user/read", h.markRead)
	mux.HandleFunc("POST /app/user/join", h.join)
	mux.HandleFunc("POST /app/user/leave", h.leave)
	mux.HandleFunc("POST /app/user/call-in", h.callIn)
	mux.HandleFunc("POST /app/user/asks/{id}/answer", h.answerAsk)
	mux.HandleFunc("POST /app/user/uploads", h.upload)
	mux.HandleFunc("GET /app/user/blobs/{id}", h.blob)
	mux.HandleFunc("POST /app/user/boards", h.boardCreate)
	mux.HandleFunc("/app/user/boards/{id}/elements", h.boardElements)
	mux.HandleFunc("GET /app/user/boards/{id}/thumb.png", h.boardThumb)
	mux.HandleFunc("GET /app/user/presence", h.presenceStrip)
	mux.HandleFunc("GET /app/user/asks", h.askPanel)
	mux.HandleFunc("GET /app/user/events", h.events)
	return mux
}

type handler struct {
	d        Deps
	lg       *slog.Logger
	changes  Changes  // nil = no `changed` push
	presence Presence // nil = no `presence` push; live sessions read "working"
	// Without either, GET /app/user/events answers 204 and the page polls.
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
		AttachMax:  h.d.Attachments.MaxBytes(),
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
		h.lg.Error("userui render failed", "tmpl", tmpl, "error", err.Error())
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

// upload stores the viewer's file as a pending attachment (see
// jam.Attachments.HandleUpload).
func (h *handler) upload(w http.ResponseWriter, r *http.Request) {
	p, ok := jam.ParticipantFrom(r)
	if !ok {
		http.Error(w, "no participant", http.StatusUnauthorized)
		return
	}
	h.d.Attachments.HandleUpload(w, r, p.UserID)
}

// blob serves an attachment the viewer may read (404 alike otherwise).
func (h *handler) blob(w http.ResponseWriter, r *http.Request) {
	p, ok := jam.ParticipantFrom(r)
	if !ok {
		http.Error(w, "no participant", http.StatusUnauthorized)
		return
	}
	h.d.Attachments.Serve(w, r, p.UserID, ident.ID(r.PathValue("id")))
}

// boardCreate starts a whiteboard in a channel the viewer may post to.
func (h *handler) boardCreate(w http.ResponseWriter, r *http.Request) {
	p, ok := jam.ParticipantFrom(r)
	if !ok || h.d.Boards == nil {
		http.Error(w, "whiteboards unavailable", http.StatusNotFound)
		return
	}
	h.d.Boards.HandleCreate(w, r, jam.Poster{ID: p.UserID}, time.Now())
}

// boardElements reads (GET ?since=) or merges (POST) a board's elements.
func (h *handler) boardElements(w http.ResponseWriter, r *http.Request) {
	p, ok := jam.ParticipantFrom(r)
	if !ok || h.d.Boards == nil {
		http.Error(w, "whiteboards unavailable", http.StatusNotFound)
		return
	}
	h.d.Boards.HandleElements(w, r, p.UserID, ident.ID(r.PathValue("id")))
}

// boardThumb is a board's server-rendered thumbnail (the card's).
func (h *handler) boardThumb(w http.ResponseWriter, r *http.Request) {
	p, ok := jam.ParticipantFrom(r)
	if !ok || h.d.Boards == nil {
		http.Error(w, "whiteboards unavailable", http.StatusNotFound)
		return
	}
	if r.URL.Query().Get("size") == "" {
		q := r.URL.Query()
		q.Set("size", "480")
		r.URL.RawQuery = q.Encode()
	}
	h.d.Boards.ServeSnapshot(w, r, p.UserID, ident.ID(r.PathValue("id")))
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
			h.lg.Warn("userui mark-read failed", "channel", channel, "error", err.Error())
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// join, leave and callIn are the conversation's membership controls: 204,
// then the page reloads (HX-Refresh), so the header shows the new state. A refusal never says
// whether the channel exists.
func (h *handler) join(w http.ResponseWriter, r *http.Request) {
	h.membership(w, r, func(p jam.Participant, ch ident.ID) error { return h.d.Intercom.JoinChannel(p.UserID, ch) })
}

func (h *handler) leave(w http.ResponseWriter, r *http.Request) {
	h.membership(w, r, func(p jam.Participant, ch ident.ID) error { return h.d.Intercom.LeaveChannel(p.UserID, ch) })
}

func (h *handler) callIn(w http.ResponseWriter, r *http.Request) {
	h.membership(w, r, func(p jam.Participant, ch ident.ID) error {
		_, _, err := h.d.Intercom.CallIn(jam.Poster{ID: p.UserID}, ch, r.FormValue("who"))
		return err
	})
}

func (h *handler) membership(w http.ResponseWriter, r *http.Request, act func(jam.Participant, ident.ID) error) {
	p, ok := jam.ParticipantFrom(r)
	if !ok {
		http.Error(w, "no participant", http.StatusUnauthorized)
		return
	}
	channel := r.FormValue("channel")
	if channel == "" || h.d.Intercom == nil {
		http.Error(w, "channel required", http.StatusBadRequest)
		return
	}
	err := act(p, ident.ID(channel))
	switch {
	case err == nil:
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, jam.ErrSendDenied):
		http.Error(w, "not allowed", http.StatusForbidden)
	case errors.Is(err, jam.ErrSendUnresolved), errors.Is(err, jam.ErrRemoved):
		http.Error(w, "not found", http.StatusNotFound)
	case errors.Is(err, jam.ErrFixedMembers):
		http.Error(w, "this channel's members are fixed", http.StatusConflict)
	default:
		h.lg.Error("userui membership change failed", "channel", channel, "error", err.Error())
		http.Error(w, "failed", http.StatusInternalServerError)
	}
}
