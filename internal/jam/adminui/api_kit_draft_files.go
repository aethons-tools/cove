package adminui

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/kittree"
)

// The kit draft file API (docs/modules/ROOT/pages/usage/jam/kits-drafts.adoc#file-api): a draft's
// files as plain bytes, for clients that edit them as files (the VS Code
// extension). Each write is conditional on the file it touches (its
// sha256), not on the whole draft, so a change to another file never
// refuses it.

// draftFileRetries bounds a write's read-check-write loop: another file
// changing between the read and the write is retried, this many times.
const draftFileRetries = 3

// maxDraftFile is a draft file's size limit.
func maxDraftFile() int64 { return kittree.MaxFile }

func draftFilesOf(st jam.KitDraftStatus) DraftFiles {
	out := DraftFiles{ETag: st.ETag, Base: st.Base, Current: st.Current, Problems: []string{}, MaxFile: maxDraftFile(), Files: []DraftFile{}}
	for _, p := range st.Problems {
		out.Problems = append(out.Problems, jam.SetupDisplayText(p))
	}
	for _, e := range st.Entries {
		out.Files = append(out.Files, DraftFile{Path: e.Path, Exec: e.Mode == "0755", Size: e.Size, SHA256: e.SHA256})
	}
	return out
}

// draftStatus is kit name's draft status; false (answered) when the kit or
// its draft is missing (404 not_found / no_draft) or the read failed.
func draftStatus(w http.ResponseWriter, r *http.Request, store jam.Store, log *slog.Logger, name string) (jam.KitDraftStatus, bool) {
	if _, ok := store.GetKit(name); !ok {
		writeAPIError(w, http.StatusNotFound, "not_found", fmt.Sprintf("kit %q does not exist", name))
		return jam.KitDraftStatus{}, false
	}
	if _, ok := store.GetKitDraft(name); !ok {
		writeAPIError(w, http.StatusNotFound, "no_draft", fmt.Sprintf("kit %s has no draft (discarded, or the Jam restarted): open it again", name))
		return jam.KitDraftStatus{}, false
	}
	st, err := jam.KitDraftStatusOf(store, name)
	if err != nil {
		draftFailJSON(w, r, log, err)
		return jam.KitDraftStatus{}, false
	}
	return st, true
}

// fileSHA is path p's sha256 in st; "" when st has no such file.
func fileSHA(st jam.KitDraftStatus, p string) string {
	for _, e := range st.Entries {
		if e.Path == p {
			return e.SHA256
		}
	}
	return ""
}

// precondition is a write's per-file condition: the sha256 the file must
// have, or create (it must not exist). false (answered 428) when neither
// If-Match nor If-None-Match: * is sent.
func precondition(w http.ResponseWriter, r *http.Request) (sha string, create, ok bool) {
	if r.Header.Get("If-None-Match") == "*" {
		return "", true, true
	}
	if m := strings.Trim(strings.TrimSpace(r.Header.Get("If-Match")), `"`); m != "" {
		return m, false, true
	}
	writeAPIError(w, http.StatusPreconditionRequired, "precondition_required", "send If-Match with the file's sha256, or If-None-Match: * for a new file")
	return "", false, false
}

func apiKitDraftFiles(api *http.ServeMux, store jam.Store, log *slog.Logger) {
	base := apiPrefix + "kits/{name}/draft/files"
	answer := func(w http.ResponseWriter, r *http.Request, name string) {
		if st, ok := draftStatus(w, r, store, log, name); ok {
			writeJSON(w, http.StatusOK, draftFilesOf(st))
		}
	}
	// write runs do against the draft's current ETag once the file's
	// precondition holds (check), retrying when another file changed between
	// the read and the write.
	write := func(w http.ResponseWriter, r *http.Request, name string, check func(jam.KitDraftStatus) bool, do func(etag string) error) {
		for i := 0; ; i++ {
			st, ok := draftStatus(w, r, store, log, name)
			if !ok {
				return
			}
			if !check(st) {
				writeAPIError(w, http.StatusPreconditionFailed, "changed", "the file changed in the draft since you read it (reload it)")
				return
			}
			err := do(st.ETag)
			if we, ok := errors.AsType[*jam.WriteError](err); ok && we.Status == http.StatusPreconditionFailed && i < draftFileRetries {
				continue
			}
			if err != nil {
				draftFailJSON(w, r, log, err)
				return
			}
			answer(w, r, name)
			return
		}
	}
	matches := func(p, sha string, create bool) func(jam.KitDraftStatus) bool {
		return func(st jam.KitDraftStatus) bool {
			cur := fileSHA(st, p)
			if create {
				return cur == ""
			}
			return cur != "" && cur == sha
		}
	}

	api.HandleFunc("GET "+base, func(w http.ResponseWriter, r *http.Request) {
		answer(w, r, r.PathValue("name"))
	})
	api.HandleFunc("GET "+base+"/{path...}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if _, ok := draftStatus(w, r, store, log, name); !ok {
			return
		}
		b, e, _, err := jam.KitDraftFile(store, name, r.PathValue("path"))
		if err != nil {
			draftFailJSON(w, r, log, err)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("ETag", `"`+e.SHA256+`"`)
		_, _ = w.Write(b)
	})
	api.HandleFunc("PUT "+base+"/{path...}", func(w http.ResponseWriter, r *http.Request) {
		name, p := r.PathValue("name"), r.PathValue("path")
		sha, create, ok := precondition(w, r)
		if !ok {
			return
		}
		var mode fs.FileMode // 0 keeps the file's mode (a new file: 0644)
		switch r.URL.Query().Get("exec") {
		case "1":
			mode = 0o755
		case "0":
			mode = 0o644
		}
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxDraftFile()+1))
		if err != nil || int64(len(data)) > maxDraftFile() {
			writeAPIError(w, http.StatusRequestEntityTooLarge, "too_large", draftTooLarge(name))
			return
		}
		write(w, r, name, matches(p, sha, create), func(etag string) error {
			if create {
				if mode == 0 {
					mode = 0o644
				}
				_, err := jam.AddKitDraftFile(store, log, name, p, data, mode, etag, jam.OperatorID(r), time.Now())
				return err
			}
			_, err := jam.PutKitDraftFile(store, log, name, p, mode, data, etag, jam.OperatorID(r), time.Now())
			return err
		})
	})
	api.HandleFunc("DELETE "+base+"/{path...}", func(w http.ResponseWriter, r *http.Request) {
		name, p := r.PathValue("name"), r.PathValue("path")
		sha, create, ok := precondition(w, r)
		if !ok {
			return
		}
		write(w, r, name, matches(p, sha, create), func(etag string) error {
			_, err := jam.RemoveKitDraftFile(store, log, name, p, etag, jam.OperatorID(r), time.Now())
			return err
		})
	})
	api.HandleFunc("POST "+apiPrefix+"kits/{name}/draft/files-rename", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); ct != "application/json" {
			writeAPIError(w, http.StatusUnsupportedMediaType, "bad_request", "send JSON (Content-Type: application/json)")
			return
		}
		var body struct {
			From   string `json:"from"`
			To     string `json:"to"`
			SHA256 string `json:"sha256"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
			writeAPIError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
			return
		}
		write(w, r, name, matches(body.From, body.SHA256, false), func(etag string) error {
			_, err := jam.RenameKitDraftFile(store, log, name, body.From, body.To, etag, jam.OperatorID(r), time.Now())
			return err
		})
	})
}
