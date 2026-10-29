package userui

import (
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/push"
)

// Handler serves the user area under /app/user: the Jam app (web/app) for
// every page path, its JSON API (api/), and the byte and stream routes the
// app uses beside it (events, uploads, blobs, board elements and thumbnails,
// the Thinking feed, Web Push). It reads identity per request from
// jam.ParticipantFrom (the /app/user gate injects it) — never a constructor
// argument — so one handler serves every participant. lg may be nil.
func Handler(d Deps, lg *slog.Logger, opts ...Option) http.Handler {
	h := newHandler(d, lg, opts...)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /app/user/uploads", h.upload)
	mux.HandleFunc("GET /app/user/blobs/{id}", h.blob)
	mux.HandleFunc("/app/user/boards/{id}/elements", h.boardElements)
	mux.HandleFunc("GET /app/user/boards/{id}/thumb.png", h.boardThumb)
	mux.HandleFunc("GET /app/user/events", h.events)
	mux.HandleFunc("GET /app/user/session/{id}/thinking/events", h.thinkingEvents)
	h.pushRoutes(mux)
	h.apiRoutes(mux)
	// Every other path is the Jam app: its client routes deep-link. The
	// routes above are more specific, so they win; webapp answers 405 to
	// anything but GET and HEAD. (No method in the pattern: one would
	// conflict with the method-less routes above.)
	mux.Handle("/app/user/", h.app())
	return mux
}

// newHandler builds the handler Handler serves.
func newHandler(d Deps, lg *slog.Logger, opts ...Option) *handler {
	if lg == nil {
		lg = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	h := &handler{d: d, lg: lg, streams: map[ident.ID]int{}, mints: newMintLimiter(time.Now)}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

type handler struct {
	d        Deps
	lg       *slog.Logger
	changes  Changes  // nil = no `changed` push
	presence Presence // nil = no `presence` push; live sessions read "working"
	pushKey  string   // VAPID public key; "" = Web Push off (routes 404)
	// pushFocus records the conversation each user has focused (nil: off).
	pushFocus *push.Focus
	// Without either, GET /app/user/events answers 204 and the app polls.

	// mu guards streams: each viewer's open Thinking feeds (capped at
	// maxThinkingStreams).
	mu      sync.Mutex
	streams map[ident.ID]int

	mints *mintLimiter // frame URLs minted per user (frameOutcome)

	frameSrc []string // the origins the app's pages may frame (WithFrameSrc)

	appTokenRevoke func(ident.ID) // ends a revoked app token's requests (nil: none)
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
