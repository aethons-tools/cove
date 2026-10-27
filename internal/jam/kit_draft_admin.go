package jam

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/kittree"
	"github.com/aethons-tools/cove/internal/studio"
	"github.com/aethons-tools/cove/internal/textesc"
)

// registerKitDrafts mounts the kit draft API (docs/usage/jam/kits-drafts.md)
// on the admin mux, behind the same operator gate:
//
//	POST   /admin/kits/{name}/draft   {from?}  open (201, the status)
//	GET    /admin/kits/{name}/draft            status (ETag header)
//	DELETE /admin/kits/{name}/draft            discard (If-Match; 204)
//	GET    /admin/kits/{name}/draft/files/{path...}          one file's bytes
//	PUT    /admin/kits/{name}/draft   replace the tree (x-tar, If-Match): kit draft sync
//	PUT    /admin/kits/{name}/draft/files/{path...}[?mode=]  write one file (octet-stream, If-Match)
//	DELETE /admin/kits/{name}/draft/files/{path...}          remove one file (If-Match)
//	POST   /admin/kits/{name}/draft/publish  {pin?}  publish (If-Match)
//
// Every write but open needs If-Match (the draft's ETag: 428 without, 412
// stale). Open and publish take JSON only, a file write octet-stream only,
// a tree replace a tar only (415 otherwise, before any body is read), so no
// browser no-cors request can write. Audit logs carry the kit, the operator
// and tree prefixes, never content.
func registerKitDrafts(mux *http.ServeMux, store Store, log *slog.Logger) {
	mux.HandleFunc("POST /admin/kits/{name}/draft", func(w http.ResponseWriter, r *http.Request) {
		if !requireContentType(w, r, "application/json") {
			return
		}
		var b KitDraftOpenBody
		if !decodeOptional(w, r, &b) {
			return
		}
		name := r.PathValue("name")
		d, err := OpenKitDraft(store, name, b.From, OperatorID(r), time.Now().UTC())
		if err != nil {
			draftFail(w, r, log, err)
			return
		}
		log.Info("kit draft opened", "operator", OperatorID(r), "kit", name, "base", d.Base, "tree", shortTree(d.Tree))
		writeDraftStatus(w, r, store, log, name, http.StatusCreated)
	})
	mux.HandleFunc("GET /admin/kits/{name}/draft", func(w http.ResponseWriter, r *http.Request) {
		writeDraftStatus(w, r, store, log, r.PathValue("name"), http.StatusOK)
	})
	mux.HandleFunc("DELETE /admin/kits/{name}/draft", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		d, err := DiscardKitDraft(store, log, name, r.Header.Get("If-Match"))
		if err != nil {
			draftFail(w, r, log, err)
			return
		}
		log.Info("kit draft discarded", "operator", OperatorID(r), "kit", name, "tree", shortTree(d.Tree))
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /admin/kits/{name}/draft/publish", func(w http.ResponseWriter, r *http.Request) {
		if !requireContentType(w, r, "application/json") {
			return
		}
		var b KitDraftPublishBody
		if !decodeOptional(w, r, &b) {
			return
		}
		name := r.PathValue("name")
		res, err := PublishKitDraft(store, log, name, b.Pin, r.Header.Get("If-Match"))
		if err != nil {
			draftFail(w, r, log, err)
			return
		}
		log.Info("kit draft published", "operator", OperatorID(r), "kit", name, "version", res.Version, "base", res.Base, "tree", shortTree(res.Tree), "pinned", len(res.Pinned), "drift", res.Drift)
		writeJSON(w, http.StatusOK, res)
	})
	// The whole tree, replaced by an uncompressed tar of files (kit draft
	// sync): read within the tree limits as it streams, the body capped at
	// kittree.MaxUpload (413 past it), as POST …/versions reads a push.
	mux.HandleFunc("PUT /admin/kits/{name}/draft", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if !requireContentType(w, r, "application/x-tar") || !draftPrecheck(w, r, store, log, name) {
			return
		}
		files, ok := readKitTar(w, r, "draft", name)
		if !ok {
			return
		}
		s, err := ReplaceKitDraft(store, log, name, files, r.Header.Get("If-Match"), OperatorID(r), time.Now().UTC())
		if err != nil {
			draftFail(w, r, log, err)
			return
		}
		logDraftSaved(log, r, name, s)
		writeDraftSave(w, s)
	})
	// One file of the draft. {path...} is the file's path, percent-decoded;
	// it is CheckPath'd before any text names it.
	mux.HandleFunc("GET /admin/kits/{name}/draft/files/{path...}", func(w http.ResponseWriter, r *http.Request) {
		data, e, etag, err := KitDraftFile(store, r.PathValue("name"), r.PathValue("path"))
		if err != nil {
			draftFail(w, r, log, err)
			return
		}
		h := w.Header()
		h.Set("Content-Type", "application/octet-stream")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("ETag", etag)
		h.Set("X-Kit-File-Mode", fmt.Sprintf("%04o", e.Mode))
		h.Set("Content-Disposition", "attachment")
		h.Set("Cache-Control", "no-store")
		w.Write(data)
	})
	mux.HandleFunc("PUT /admin/kits/{name}/draft/files/{path...}", func(w http.ResponseWriter, r *http.Request) {
		name, p := r.PathValue("name"), r.PathValue("path")
		if !requireContentType(w, r, "application/octet-stream") || !draftPrecheck(w, r, store, log, name) {
			return
		}
		if err := kittree.CheckPath(p); err != nil {
			draftFail(w, r, log, draftPathErr(name, err))
			return
		}
		mode, err := draftMode(name, r.URL.Query().Get("mode"))
		if err != nil {
			draftFail(w, r, log, err)
			return
		}
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, kittree.MaxFile))
		if err != nil {
			if bodyTooLarge(err) {
				draftFail(w, r, log, draftFileTooLarge(name, p))
				return
			}
			http.Error(w, "kit draft: the body could not be read", http.StatusBadRequest)
			return
		}
		s, err := PutKitDraftFile(store, log, name, p, mode, data, r.Header.Get("If-Match"), OperatorID(r), time.Now().UTC())
		if err != nil {
			draftFail(w, r, log, err)
			return
		}
		logDraftSaved(log, r, name, s)
		writeDraftSave(w, s)
	})
	mux.HandleFunc("DELETE /admin/kits/{name}/draft/files/{path...}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		s, err := RemoveKitDraftFile(store, log, name, r.PathValue("path"), r.Header.Get("If-Match"), OperatorID(r), time.Now().UTC())
		if err != nil {
			draftFail(w, r, log, err)
			return
		}
		logDraftSaved(log, r, name, s)
		writeDraftSave(w, s)
	})
}

// draftPrecheck answers 400/404/428/412 before a write's body is read, so a
// stale upload is refused without reading it; the write re-checks under kitMu.
func draftPrecheck(w http.ResponseWriter, r *http.Request, store Store, log *slog.Logger, name string) bool {
	d, err := draftOf(store, name)
	if err == nil {
		err = checkDraftETag(d, r.Header.Get("If-Match"))
	}
	if err != nil {
		draftFail(w, r, log, err)
		return false
	}
	return true
}

// draftMode reads ?mode=: "" (keep), 644/0644 or 755/0755.
func draftMode(name, q string) (fs.FileMode, error) {
	switch q {
	case "":
		return 0, nil
	case "644", "0644":
		return 0o644, nil
	case "755", "0755":
		return 0o755, nil
	}
	return 0, writeErr(http.StatusBadRequest, "draft %s: mode must be 0644 or 0755", name)
}

// writeDraftSave answers a draft write with its new ETag.
func writeDraftSave(w http.ResponseWriter, s KitDraftSave) {
	w.Header().Set("ETag", s.ETag)
	writeJSON(w, http.StatusOK, s)
}

// logDraftSaved is a draft write's audit line, only when it changed the
// tree (a new tree always changes some file): never content or a path.
func logDraftSaved(log *slog.Logger, r *http.Request, name string, s KitDraftSave) {
	if len(s.Changed) == 0 {
		return
	}
	log.Info("kit draft saved", "operator", OperatorID(r), "kit", name, "tree", shortTree(s.Tree), "changed", len(s.Changed))
}

// bodyTooLarge reports whether err is a capped body's overflow
// (http.MaxBytesReader): the kit push and draft writes answer it 413.
func bodyTooLarge(err error) bool {
	_, big := errors.AsType[*http.MaxBytesError](err)
	return big
}

// writeDraftStatus answers kit name's draft status with its ETag.
func writeDraftStatus(w http.ResponseWriter, r *http.Request, store Store, log *slog.Logger, name string, code int) {
	st, err := KitDraftStatusOf(store, name)
	if err != nil {
		draftFail(w, r, log, err)
		return
	}
	w.Header().Set("ETag", st.ETag)
	writeJSON(w, code, st)
}

// draftInternalError is the fixed answer to a draft failure that is not
// the operator's (a store error): its text goes to the log, never the client.
const draftInternalError = "internal error"

// draftFail answers err: a WriteError's own status and fixed text, else a
// fixed 500, the error logged with the method and route pattern (it names
// a kit or a tree prefix, no content).
func draftFail(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	if we, ok := errors.AsType[*WriteError](err); ok {
		http.Error(w, we.Msg, we.Status)
		return
	}
	if code, ok := ManagerErrStatus(err); ok {
		http.Error(w, textesc.Escape(err.Error(), false), code)
		return
	}
	log.Error("kit draft: store error", "operator", OperatorID(r), "method", r.Method, "route", r.Pattern, "err", err.Error())
	http.Error(w, draftInternalError, http.StatusInternalServerError)
}

// requireContentType refuses (415) a request whose media type is not want,
// before its body is read.
func requireContentType(w http.ResponseWriter, r *http.Request, want string) bool {
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != want {
		http.Error(w, "kit draft: Content-Type must be "+want, http.StatusUnsupportedMediaType)
		return false
	}
	return true
}

// decodeOptional decodes a small JSON body into v; an empty body leaves v zero.
func decodeOptional(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(v); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return false
	}
	return true
}

// shortTree is a tree hash's 12-hex prefix, for logs.
func shortTree(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// draftFilePathGuard refuses (400, the fixed unsafe-path text) a request
// under /admin/kits/{name}/draft/files/ whose raw path has an empty, . or ..
// segment after files/, before any ServeMux routes it: the mux would 307 it
// to the cleaned path, and a client following the redirect would write or
// remove a different file. It wraps the outermost admin handler.
func draftFilePathGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if name, bad := draftFileDotSegment(r.URL.EscapedPath()); bad {
			err := unsafeKitName()
			if studio.TagSafeName(name) {
				// CheckPath's own fixed refusal, the path never named.
				err = draftPathErr(name, kittree.CheckPath(".."))
			}
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// draftFileDotSegment reports whether escaped, a raw request path, is a
// draft file route whose file path has an empty, . or .. segment (raw or
// percent-encoded), and the route's kit name, unescaped.
func draftFileDotSegment(escaped string) (string, bool) {
	segs := strings.Split(escaped, "/")
	// "", "admin", "kits", {name}, "draft", "files", {path...}
	if len(segs) < 7 || segs[0] != "" || segs[1] != "admin" || segs[2] != "kits" || segs[4] != "draft" || segs[5] != "files" {
		return "", false
	}
	for _, seg := range segs[6:] {
		if u, err := url.PathUnescape(seg); err == nil {
			seg = u
		}
		if seg == "" || seg == "." || seg == ".." {
			name, _ := url.PathUnescape(segs[3])
			return name, true
		}
	}
	return "", false
}

// readKitTar reads a request's tar body into files within the tree limits,
// the body capped at kittree.MaxUpload: 413 past it, 400 for an unreadable
// or refused tar (located by noun and kit name, never echoing a bad name).
// It answers the failure itself and reports false.
func readKitTar(w http.ResponseWriter, r *http.Request, noun, name string) ([]kittree.File, bool) {
	files, err := kittree.ReadTar(http.MaxBytesReader(w, r.Body, kittree.MaxUpload()))
	if err != nil {
		if bodyTooLarge(err) {
			http.Error(w, fmt.Sprintf("kit upload over %d bytes", kittree.MaxUpload()), http.StatusRequestEntityTooLarge)
			return nil, false
		}
		http.Error(w, fmt.Sprintf("%s %s: %s", noun, name, err), http.StatusBadRequest)
		return nil, false
	}
	return files, true
}
