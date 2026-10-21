package adminui

import (
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/gitsource"
)

// kitURL is the page path for a kit.
func kitURL(name string) string { return "/ui/kits/" + url.PathEscape(name) }

func kitTableData(store jam.Store) map[string]any {
	return map[string]any{"Kits": kitListRows(store)}
}

// renderKitBody answers a kit-page write with the re-rendered kit body (the
// current version), plus an out-of-band #flash message when msg is set.
func renderKitBody(w http.ResponseWriter, r *http.Request, store jam.Store, canSource bool, name, msg string) {
	d, ok := buildKitDetail(store, name, 0, 0)
	if !ok {
		renderError(w, http.StatusNotFound, "kit no longer exists")
		return
	}
	d.CanSource = canSource
	renderFragment(w, r, "kit", "kit-body", d)
	if msg != "" {
		_, _ = w.Write([]byte(`<div id="flash" hx-swap-oob="innerHTML"><p class="ok">` + template.HTMLEscapeString(msg) + `</p></div>`))
	}
}

func pushedMsg(name string, v int, unchanged bool) string {
	if unchanged {
		return fmt.Sprintf("%s unchanged (current v%d)", name, v)
	}
	return fmt.Sprintf("pushed %s v%d", name, v)
}

func registerKits(mux *http.ServeMux, store jam.Store, f jam.KitFetcher, log *slog.Logger, guardWrite func(http.ResponseWriter, *http.Request) bool) {
	mux.HandleFunc("GET /ui/kits", func(w http.ResponseWriter, r *http.Request) {
		data := kitTableData(store)
		data["Title"], data["CanSource"] = "Kits", f != nil
		render(w, r, "kits", data)
	})

	mux.HandleFunc("GET /ui/kits/{name}", func(w http.ResponseWriter, r *http.Request) {
		view, _ := strconv.Atoi(r.URL.Query().Get("v"))
		diff, _ := strconv.Atoi(r.URL.Query().Get("diff"))
		d, ok := buildKitDetail(store, r.PathValue("name"), view, diff)
		if !ok {
			renderStatus(w, r, http.StatusNotFound, "kit", kitDetail{Title: "Kits", NotFound: true, NotFoundFor: r.PathValue("name")})
			return
		}
		d.CanSource = f != nil
		render(w, r, "kit", d)
	})

	// New kit: create only — further versions are pushed on the kit's page.
	mux.HandleFunc("POST /ui/kits", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		if err := r.ParseForm(); err != nil {
			renderError(w, http.StatusBadRequest, "invalid form")
			return
		}
		name, config := strings.TrimSpace(r.FormValue("name")), r.FormValue("config")
		if name == "" || strings.TrimSpace(config) == "" {
			renderError(w, http.StatusBadRequest, "name and config are required")
			return
		}
		if _, exists := store.GetKit(name); exists {
			renderError(w, http.StatusConflict, fmt.Sprintf("kit %q already exists; push a new version on its page", name))
			return
		}
		v, _, err := jam.PushStudioKit(store, name, config)
		if err != nil {
			renderError(w, jam.WriteStatus(err, http.StatusInternalServerError), err.Error())
			return
		}
		log.Info("ui kit pushed", "operator", jam.OperatorID(r), "kit", name, "version", v)
		w.Header().Set("HX-Redirect", kitURL(name))
		renderFragment(w, r, "kits", "kits-table", kitTableData(store))
	})

	mux.HandleFunc("POST /ui/kits/{name}/versions", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		if err := r.ParseForm(); err != nil {
			renderError(w, http.StatusBadRequest, "invalid form")
			return
		}
		name := r.PathValue("name")
		if _, ok := store.GetKit(name); !ok {
			renderError(w, http.StatusNotFound, fmt.Sprintf("kit %q does not exist", name))
			return
		}
		v, unchanged, err := jam.PushStudioKit(store, name, r.FormValue("config"))
		if err != nil {
			renderError(w, jam.WriteStatus(err, http.StatusInternalServerError), err.Error())
			return
		}
		log.Info("ui kit pushed", "operator", jam.OperatorID(r), "kit", name, "version", v, "unchanged", unchanged)
		renderKitBody(w, r, store, f != nil, name, pushedMsg(name, v, unchanged))
	})

	mux.HandleFunc("POST /ui/kits/{name}/pin", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		if err := r.ParseForm(); err != nil {
			renderError(w, http.StatusBadRequest, "invalid form")
			return
		}
		v, err := strconv.Atoi(strings.TrimSpace(r.FormValue("version")))
		if err != nil {
			renderError(w, http.StatusBadRequest, "version must be an integer")
			return
		}
		name := r.PathValue("name")
		if err := jam.PinKitChecked(store, name, v); err != nil {
			renderError(w, jam.WriteStatus(err, http.StatusNotFound), err.Error())
			return
		}
		log.Info("ui kit pinned", "operator", jam.OperatorID(r), "kit", name, "version", v)
		if r.Header.Get("HX-Target") == "kit" { // from the kit's own page
			renderKitBody(w, r, store, f != nil, name, fmt.Sprintf("%s current is now v%d", name, v))
			return
		}
		renderFragment(w, r, "kits", "kits-table", kitTableData(store))
	})

	mux.HandleFunc("DELETE /ui/kits/{name}", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		name := r.PathValue("name")
		if err := jam.RemoveKitChecked(store, name); err != nil {
			renderError(w, jam.WriteStatus(err, http.StatusNotFound), err.Error())
			return
		}
		log.Info("ui kit removed", "operator", jam.OperatorID(r), "kit", name)
		renderFragment(w, r, "kits", "kits-table", kitTableData(store))
	})

	if f != nil {
		registerKitSources(mux, store, f, log, guardWrite)
	}
}

// kitSourceFail answers a source operation's error: the admin API's mapping
// (jam.KitSourceStatus) as an error fragment, logging store failures.
func kitSourceFail(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	status, msg := jam.KitSourceStatus(err)
	if status == http.StatusInternalServerError {
		log.Error("ui kit source: store error", "operator", jam.OperatorID(r), "err", err.Error())
	}
	renderError(w, status, msg)
}

// formSource reads and parses the form's source field, answering 400 itself.
func formSource(w http.ResponseWriter, r *http.Request) (gitsource.Source, bool) {
	if err := r.ParseForm(); err != nil {
		renderError(w, http.StatusBadRequest, "invalid form")
		return gitsource.Source{}, false
	}
	src, err := gitsource.Parse(strings.TrimSpace(r.FormValue("source")))
	if err != nil {
		renderError(w, http.StatusBadRequest, err.Error())
		return gitsource.Source{}, false
	}
	return src, true
}

func registerKitSources(mux *http.ServeMux, store jam.Store, f jam.KitFetcher, log *slog.Logger, guardWrite func(http.ResponseWriter, *http.Request) bool) {
	// Import from git: a new version of the named (else the kit's own) kit;
	// with the "track" box checked the kit also tracks the source (TrackKit).
	mux.HandleFunc("POST /ui/kits/import", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		src, ok := formSource(w, r)
		if !ok {
			return
		}
		name, track := strings.TrimSpace(r.FormValue("name")), r.FormValue("track") != ""
		importFn, verb := jam.ImportKit, "ui kit imported"
		if track {
			importFn, verb = jam.TrackKit, "ui kit tracked"
		}
		res, err := importFn(r.Context(), store, f, log, name, src, time.Now())
		if err != nil {
			kitSourceFail(w, r, log, err)
			return
		}
		log.Info(verb, "operator", jam.OperatorID(r), "kit", res.Name, "source", src.String(), "version", res.Version)
		w.Header().Set("HX-Redirect", kitURL(res.Name))
		renderFragment(w, r, "kits", "kits-table", kitTableData(store))
	})

	mux.HandleFunc("POST /ui/kits/{name}/source", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		src, ok := formSource(w, r)
		if !ok {
			return
		}
		name := r.PathValue("name")
		res, err := jam.TrackKit(r.Context(), store, f, log, name, src, time.Now())
		if err != nil {
			kitSourceFail(w, r, log, err)
			return
		}
		log.Info("ui kit tracked", "operator", jam.OperatorID(r), "kit", name, "source", src.String(), "version", res.Version)
		renderKitBody(w, r, store, true, name, sourceMsg("tracking "+src.String(), res))
	})

	mux.HandleFunc("DELETE /ui/kits/{name}/source", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		name := r.PathValue("name")
		if err := jam.UntrackKit(store, log, name); err != nil {
			kitSourceFail(w, r, log, err)
			return
		}
		log.Info("ui kit untracked", "operator", jam.OperatorID(r), "kit", name)
		renderKitBody(w, r, store, true, name, name+" no longer tracks a source")
	})

	mux.HandleFunc("POST /ui/kits/{name}/sync", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		name := r.PathValue("name")
		res, err := jam.SyncKit(r.Context(), store, f, log, name, time.Now())
		if err != nil {
			kitSourceFail(w, r, log, err)
			return
		}
		log.Info("ui kit synced", "operator", jam.OperatorID(r), "kit", name, "version", res.Version, "unchanged", res.Unchanged)
		renderKitBody(w, r, store, true, name, sourceMsg("synced", res))
	})
}

func sourceMsg(what string, res jam.KitSourceResult) string {
	if res.Unchanged {
		return fmt.Sprintf("%s: %s unchanged (current v%d)", what, res.Name, res.Version)
	}
	return fmt.Sprintf("%s: %s is now v%d", what, res.Name, res.Version)
}
