package adminui

import (
	"errors"
	"fmt"
	"net/http"

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
