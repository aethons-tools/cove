package adminui

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/gitsource"
)

// The kits JSON API: the kits list and a kit's page with its writes — the
// JSON twins of kits.go and kit_detail.go, through the same jam calls. The
// Draft's routes are api_kit_draft.go's.

func sourceInfo(s *sourceView) *SourceInfo {
	if s == nil {
		return nil
	}
	return &SourceInfo{Repo: s.Repo, RepoLink: s.RepoLink, Dir: s.Dir, Ref: s.Ref, Commit: s.Commit, CommitLink: s.CommitLink}
}

func kvs(xs []kv) []KV {
	out := []KV{}
	for _, x := range xs {
		out = append(out, KV{Key: x.Key, Value: x.Value})
	}
	return out
}

func kitViewOf(v kitView) KitView {
	out := KitView{N: v.N, ParseErr: v.ParseErr, RawShown: jam.SetupDisplayText(v.RawShown), Masked: v.Masked, Hidden: v.Hidden, YAML: v.YAML,
		Base: v.Base, Image: v.Image, Egress: append([]string{}, v.Egress...), Ceiling: append([]string{}, v.Ceiling...), Excluded: append([]string{}, v.Excluded...),
		BuildArgs: kvs(v.BuildArgs), Secrets: kvs(v.Secrets), MCPServers: kvs(v.MCPServers), Notes: kvs(v.Notes), Prompt: v.Prompt, Digest: v.Digest}
	out.PushText = v.PushYAML
	if v.ParseErr != "" {
		out.PushText = v.PushRaw // never the hidden placeholder: "" when hidden
	}
	return out
}

func kitFileOf(f *ctxFileView) *KitFile {
	if f == nil {
		return nil
	}
	return &KitFile{Path: f.Path, Diff: f.Diff, From: f.From, To: f.To, Lines: diffLines(f.Lines), TooLarge: f.TooLarge, Content: f.Content,
		Note: f.Note, Missing: f.Missing, Masked: f.Masked, Hidden: f.Hidden}
}

func kitDetailOf(store jam.Store, items []attnItem, marks map[jam.ConfigKey]jam.ConfigMark, name string, view, diff int, file string, canSource bool) (KitDetail, bool) {
	d, ok := buildKitDetail(store, name, view, diff, file)
	if !ok {
		return KitDetail{}, false
	}
	out := KitDetail{Name: name, Managed: managedJSON(marks, jam.ConfigKindKit, name), Current: d.Current, Versions: []KitVersionRow{},
		View: kitViewOf(d.View), Uses: []KitUse{}, Chain: []KitParent{}, ChainErr: d.ChainErr, UsedByKits: []KitChild{}, IsDefault: d.IsDefault,
		Source: sourceInfo(d.Source), SetWith: d.SetWith, CanSource: canSource, File: kitFileOf(d.File), Attn: attnFlag(items, "specs", "kit:"+name)}
	for _, v := range d.Versions {
		out.Versions = append(out.Versions, KitVersionRow{N: v.N, Prev: v.Prev, Current: v.Current, Digest: v.Digest, Pushed: v.Pushed, Source: sourceInfo(v.Source)})
	}
	if x := d.Diff; x != nil {
		out.Diff = &KitDiff{From: x.From, To: x.To, Lines: diffLines(x.Lines), DigestChanged: x.DigestChanged, TooLarge: x.TooLarge}
	}
	for _, u := range d.Uses {
		out.Uses = append(out.Uses, KitUse{Project: u.Project, Role: u.Role, Implicit: u.Implicit})
	}
	for _, p := range d.Chain {
		out.Chain = append(out.Chain, KitParent{Name: p.Name, Version: p.Version, Follow: p.Follow})
	}
	if d.Effective != nil {
		e := kitViewOf(*d.Effective)
		out.Effective = &e
	}
	for _, c := range d.UsedByKits {
		out.UsedByKits = append(out.UsedByKits, KitChild{Name: c.Name, Pinned: c.Pinned})
	}
	if d.Draft != nil {
		out.Draft = &DraftInfo{Base: d.Draft.Base}
	}
	if l := d.Ctx; l != nil {
		out.Files = &FileList{N: l.N, Size: l.Size, Rows: []FileRow{}, Hidden: l.Hidden}
		for _, r := range l.Rows {
			out.Files.Rows = append(out.Files.Rows, FileRow{Path: r.Raw, Label: r.Path, Mode: r.Mode, Size: r.Size})
		}
	}
	if x := d.CtxDiff; x != nil {
		out.FilesDiff = &FilesDiff{From: x.From, To: x.To, Rows: []FileChange{}, More: x.More, Unchanged: x.Unchanged}
		for _, r := range x.Rows {
			out.FilesDiff.Rows = append(out.FilesDiff.Rows, FileChange{Change: r.Change, Path: r.Raw, Label: r.Path, Mode: r.Mode, Size: r.Size})
		}
	}
	return out, true
}

func kitPath(name string) string { return "config/kits/" + url.PathEscape(name) }

// kitSourceFailJSON is kitSourceFail for the JSON API.
func kitSourceFailJSON(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	status, msg := jam.KitSourceStatus(err)
	if status == http.StatusInternalServerError {
		log.Error("ui kit source: store error", "operator", jam.OperatorID(r), "err", err.Error())
	}
	writeAPIError(w, status, apiCode(status), msg)
}

func apiKits(api *http.ServeMux, src frameSource, f jam.KitFetcher, log *slog.Logger) {
	store := src.store
	canSource := f != nil
	done := func(w http.ResponseWriter, name, msg string) {
		d, ok := kitDetailOf(store, src.items(), src.marks(), name, 0, 0, "", canSource)
		if !ok {
			writeAPIError(w, http.StatusNotFound, "not_found", "kit no longer exists")
			return
		}
		writeJSON(w, http.StatusOK, KitResult{Kit: d, Message: msg})
	}
	api.HandleFunc("GET "+apiPrefix+"kits", func(w http.ResponseWriter, r *http.Request) {
		marks, items := src.marks(), src.items()
		out := KitsResponse{Kits: []KitRow{}, CanSource: canSource}
		for _, k := range kitListRows(store) {
			out.Kits = append(out.Kits, KitRow{Name: k.Name, Current: k.Current, Count: k.Count, Base: k.Base, Egress: k.Egress, UsedBy: k.UsedBy,
				Invalid: k.Invalid, Draft: k.Draft, Managed: managedJSON(marks, jam.ConfigKindKit, k.Name), Attn: attnFlag(items, "specs", "kit:"+k.Name)})
		}
		writeJSON(w, http.StatusOK, out)
	})
	api.HandleFunc("POST "+apiPrefix+"kits", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Name, Config string }
		if !decodeJSON(w, r, &body) {
			return
		}
		name := strings.TrimSpace(body.Name)
		if name == "" || strings.TrimSpace(body.Config) == "" {
			writeAPIError(w, http.StatusBadRequest, "bad_request", "name and config are required")
			return
		}
		if _, exists := store.GetKit(name); exists {
			writeAPIError(w, http.StatusConflict, "conflict", fmt.Sprintf("kit %q already exists; push a new version on its page", name))
			return
		}
		v, _, err := jam.PushStudioKit(store, name, body.Config)
		if err != nil {
			apiFail(w, err, http.StatusInternalServerError)
			return
		}
		log.Info("ui kit pushed", "operator", jam.OperatorID(r), "kit", name, "version", v)
		writeJSON(w, http.StatusCreated, Created{ID: name, Path: kitPath(name)})
	})
	api.HandleFunc("GET "+apiPrefix+"kits/{name}", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		view, _ := strconv.Atoi(q.Get("v"))
		diff, _ := strconv.Atoi(q.Get("diff"))
		d, ok := kitDetailOf(store, src.items(), src.marks(), r.PathValue("name"), view, diff, q.Get("file"), canSource)
		if !ok {
			writeAPIError(w, http.StatusNotFound, "not_found", "no such kit")
			return
		}
		writeJSON(w, http.StatusOK, d)
	})
	api.HandleFunc("POST "+apiPrefix+"kits/{name}/versions", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Config string }
		if !decodeJSON(w, r, &body) {
			return
		}
		name := r.PathValue("name")
		if _, ok := store.GetKit(name); !ok {
			writeAPIError(w, http.StatusNotFound, "not_found", fmt.Sprintf("kit %q does not exist", name))
			return
		}
		// The box edits kit.yml only: the version keeps the current files.
		ref, unchanged, err := jam.ReplaceKitYML(store, name, body.Config)
		if err != nil {
			apiFail(w, err, http.StatusInternalServerError)
			return
		}
		log.Info("ui kit pushed", "operator", jam.OperatorID(r), "kit", name, "version", ref.Version, "unchanged", unchanged)
		done(w, name, pushedMsg(name, ref.Version, unchanged))
	})
	api.HandleFunc("POST "+apiPrefix+"kits/{name}/pin", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Version int }
		if !decodeJSON(w, r, &body) {
			return
		}
		name := r.PathValue("name")
		if err := jam.PinKitChecked(store, name, body.Version); err != nil {
			apiFail(w, err, http.StatusNotFound)
			return
		}
		log.Info("ui kit pinned", "operator", jam.OperatorID(r), "kit", name, "version", body.Version)
		done(w, name, fmt.Sprintf("%s current is now v%d", name, body.Version))
	})
	api.HandleFunc("DELETE "+apiPrefix+"kits/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if err := jam.RemoveKitChecked(store, log, name); err != nil {
			apiFail(w, err, http.StatusNotFound)
			return
		}
		log.Info("ui kit removed", "operator", jam.OperatorID(r), "kit", name)
		w.WriteHeader(http.StatusNoContent)
	})

	// git wraps the source actions: without a fetcher they don't exist.
	git := func(h http.HandlerFunc) http.HandlerFunc {
		if canSource {
			return h
		}
		return func(w http.ResponseWriter, r *http.Request) {
			writeAPIError(w, http.StatusNotFound, "not_found", "git actions are not available on this serve")
		}
	}
	parse := func(w http.ResponseWriter, s string) (gitsource.Source, bool) {
		out, err := gitsource.Parse(strings.TrimSpace(s))
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "bad_request", err.Error())
			return gitsource.Source{}, false
		}
		return out, true
	}
	api.HandleFunc("POST "+apiPrefix+"kits/import", git(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Source, Name string
			Track        bool
		}
		if !decodeJSON(w, r, &body) {
			return
		}
		s, ok := parse(w, body.Source)
		if !ok {
			return
		}
		importFn, verb, what := jam.ImportKit, "ui kit imported", "imported "+s.String()
		if body.Track {
			importFn, verb, what = jam.TrackKit, "ui kit tracked", "tracking "+s.String()
		}
		res, err := importFn(r.Context(), store, f, log, strings.TrimSpace(body.Name), s, time.Now())
		if err != nil {
			kitSourceFailJSON(w, r, log, err)
			return
		}
		log.Info(verb, "operator", jam.OperatorID(r), "kit", res.Name, "source", s.String(), "version", res.Version)
		writeJSON(w, http.StatusCreated, Created{ID: res.Name, Path: kitPath(res.Name), Message: sourceMsg(what, res)})
	}))
	api.HandleFunc("POST "+apiPrefix+"kits/{name}/source", git(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Source string }
		if !decodeJSON(w, r, &body) {
			return
		}
		s, ok := parse(w, body.Source)
		if !ok {
			return
		}
		name := r.PathValue("name")
		res, err := jam.TrackKit(r.Context(), store, f, log, name, s, time.Now())
		if err != nil {
			kitSourceFailJSON(w, r, log, err)
			return
		}
		log.Info("ui kit tracked", "operator", jam.OperatorID(r), "kit", name, "source", s.String(), "version", res.Version)
		done(w, name, sourceMsg("tracking "+s.String(), res))
	}))
	api.HandleFunc("DELETE "+apiPrefix+"kits/{name}/source", git(func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if err := jam.UntrackKit(store, log, name); err != nil {
			kitSourceFailJSON(w, r, log, err)
			return
		}
		log.Info("ui kit untracked", "operator", jam.OperatorID(r), "kit", name)
		done(w, name, name+" no longer tracks a source")
	}))
	api.HandleFunc("POST "+apiPrefix+"kits/{name}/sync", git(func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		res, err := jam.SyncKit(r.Context(), store, f, log, name, time.Now())
		if err != nil {
			kitSourceFailJSON(w, r, log, err)
			return
		}
		log.Info("ui kit synced", "operator", jam.OperatorID(r), "kit", name, "version", res.Version, "unchanged", res.Unchanged)
		done(w, name, sourceMsg("synced", res))
	}))
}
