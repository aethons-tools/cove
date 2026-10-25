package adminui

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/kittree"
)

// draftFormLimit caps a Draft-tab form body: the editor's text up to the
// per-file limit, percent-encoded (at most 3 bytes a byte), plus the fields.
func draftFormLimit() int64 { return 3*kittree.MaxFile + 64<<10 }

// draftUploadLimit caps a Draft-tab upload body: one file up to the per-file
// limit plus the multipart framing and the fields.
func draftUploadLimit() int64 { return kittree.MaxFile + 1<<20 }

// draftTooLarge is the 413 text for a Draft-tab body past its cap; name is
// a stored kit's (tag-safe).
func draftTooLarge(name string) string {
	return fmt.Sprintf("draft %s: the file is over %s (limit %s per file)", name, kittree.SizeText(kittree.MaxFile), kittree.SizeText(kittree.MaxFile))
}

// registerKitDraft mounts the kit page's Draft tab
// (docs/usage/jam/kits-drafts.md#in-the-admin-ui) and its open and discard
// writes. Every write is a form POST through guardWrite that calls the jam
// draft functions directly, sending its form's etag as If-Match.
func registerKitDraft(mux *http.ServeMux, store jam.Store, log *slog.Logger, guardWrite func(http.ResponseWriter, *http.Request) bool) {
	mux.HandleFunc("GET /app/admin/kits/{name}/draft", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		q := draftQuery{File: r.URL.Query().Get("file"), Diff: r.URL.Query().Get("diff") == "1"}
		p, ok, err := buildDraftPage(store, name, q)
		switch {
		case !ok:
			renderStatus(w, r, http.StatusNotFound, "kit-draft", draftPage{Title: "Kits", NotFound: true, NotFoundFor: name})
		case err != nil:
			draftUIFail(w, r, log, err)
		default:
			renderStatus(w, r, p.status(), "kit-draft", p)
		}
	})

	// Open, from the form's version (empty: current). The answer redirects
	// the page to the Draft tab.
	mux.HandleFunc("POST /app/admin/kits/{name}/draft", func(w http.ResponseWriter, r *http.Request) {
		name, ok := draftForm(w, r, store, guardWrite, false)
		if !ok {
			return
		}
		from := 0
		if s := strings.TrimSpace(r.FormValue("from")); s != "" {
			n, err := strconv.Atoi(s)
			if err != nil {
				renderError(w, http.StatusBadRequest, "from must be a version number")
				return
			}
			from = n
		}
		d, err := jam.OpenKitDraft(store, name, from, jam.OperatorID(r), time.Now().UTC())
		if err != nil {
			draftUIFail(w, r, log, err)
			return
		}
		log.Info("ui kit draft opened", "operator", jam.OperatorID(r), "kit", name, "base", d.Base, "tree", shortDigest(d.Tree))
		w.Header().Set("HX-Redirect", draftURL(name))
		renderDraftBody(w, r, store, log, name, draftQuery{}, fmt.Sprintf("opened a draft of %s from v%d", name, d.Base))
	})

	mux.HandleFunc("POST /app/admin/kits/{name}/draft/discard", func(w http.ResponseWriter, r *http.Request) {
		name, ok := draftForm(w, r, store, guardWrite, false)
		if !ok {
			return
		}
		d, err := jam.DiscardKitDraft(store, log, name, r.FormValue("etag"))
		if err != nil {
			draftUIFail(w, r, log, err)
			return
		}
		log.Info("ui kit draft discarded", "operator", jam.OperatorID(r), "kit", name, "tree", shortDigest(d.Tree))
		renderDraftBody(w, r, store, log, name, draftQuery{}, "discarded the draft of "+name)
	})
}

// draftForm runs a Draft-tab write's preamble: the origin guard, the kit's
// existence (404, before the body is read), the body cap (413, from
// Content-Length, else as it reads) and the form parse (multipart for an
// upload). It returns the kit name, or false once it has answered.
func draftForm(w http.ResponseWriter, r *http.Request, store jam.Store, guardWrite func(http.ResponseWriter, *http.Request) bool, multipart bool) (string, bool) {
	if !guardWrite(w, r) {
		return "", false
	}
	name := r.PathValue("name")
	if _, ok := store.GetKit(name); !ok {
		renderError(w, http.StatusNotFound, fmt.Sprintf("kit %q does not exist", name))
		return "", false
	}
	limit := draftFormLimit()
	if multipart {
		limit = draftUploadLimit()
	}
	if r.ContentLength > limit {
		renderError(w, http.StatusRequestEntityTooLarge, draftTooLarge(name))
		return "", false
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	var err error
	if multipart {
		err = r.ParseMultipartForm(limit) // in memory: the body is capped at limit
	} else {
		err = r.ParseForm()
	}
	if err != nil {
		if _, big := errors.AsType[*http.MaxBytesError](err); big {
			renderError(w, http.StatusRequestEntityTooLarge, draftTooLarge(name))
			return "", false
		}
		renderError(w, http.StatusBadRequest, "invalid form")
		return "", false
	}
	return name, true
}

// draftUIFail answers a draft call's error: a jam.WriteError's own status and
// fixed text as an error fragment, else a fixed 500, the error logged (it
// names a kit or a tree prefix, never content).
func draftUIFail(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	if we, ok := errors.AsType[*jam.WriteError](err); ok {
		renderError(w, we.Status, we.Msg)
		return
	}
	log.Error("ui kit draft: store error", "operator", jam.OperatorID(r), "method", r.Method, "route", r.Pattern, "err", err.Error())
	renderError(w, http.StatusInternalServerError, "internal error")
}

// loadDraftPage is buildDraftPage for a write's answer: false once it has
// answered a kit gone meanwhile (404) or a store fault.
func loadDraftPage(w http.ResponseWriter, r *http.Request, store jam.Store, log *slog.Logger, name string, q draftQuery) (draftPage, bool) {
	p, ok, err := buildDraftPage(store, name, q)
	if !ok {
		renderError(w, http.StatusNotFound, "kit no longer exists")
		return draftPage{}, false
	}
	if err != nil {
		draftUIFail(w, r, log, err)
		return draftPage{}, false
	}
	return p, true
}

// writeDraftBody renders p's #draft body, plus msg as the #flash message.
func writeDraftBody(w http.ResponseWriter, r *http.Request, p draftPage, msg string) {
	renderFragment(w, r, "kit-draft", "draft-body", p)
	writeFlash(w, msg)
}

// renderDraftBody answers a Draft-tab write: the #draft body showing q, and msg.
func renderDraftBody(w http.ResponseWriter, r *http.Request, store jam.Store, log *slog.Logger, name string, q draftQuery, msg string) {
	if p, ok := loadDraftPage(w, r, store, log, name, q); ok {
		writeDraftBody(w, r, p, msg)
	}
}
