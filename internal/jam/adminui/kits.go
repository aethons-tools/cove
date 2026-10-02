package adminui

import (
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/aethons-tools/cove/internal/jam"
)

// kitURL is the page path for a kit.
func kitURL(name string) string { return "/ui/kits/" + url.PathEscape(name) }

func kitTableData(store jam.Store) map[string]any {
	return map[string]any{"Kits": kitListRows(store)}
}

// renderKitBody answers a kit-page write with the re-rendered kit body (the
// current version), plus an out-of-band #flash message when msg is set.
func renderKitBody(w http.ResponseWriter, store jam.Store, name, msg string) {
	d, ok := buildKitDetail(store, name, 0, 0)
	if !ok {
		renderError(w, http.StatusNotFound, "kit no longer exists")
		return
	}
	renderFragment(w, "kit", "kit-body", d)
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

func registerKits(mux *http.ServeMux, store jam.Store, log *slog.Logger, guardWrite func(http.ResponseWriter, *http.Request) bool) {
	mux.HandleFunc("GET /ui/kits", func(w http.ResponseWriter, r *http.Request) {
		data := kitTableData(store)
		data["Title"] = "Kits"
		render(w, "kits", data)
	})

	mux.HandleFunc("GET /ui/kits/{name}", func(w http.ResponseWriter, r *http.Request) {
		view, _ := strconv.Atoi(r.URL.Query().Get("v"))
		diff, _ := strconv.Atoi(r.URL.Query().Get("diff"))
		d, ok := buildKitDetail(store, r.PathValue("name"), view, diff)
		if !ok {
			renderStatus(w, http.StatusNotFound, "kit", kitDetail{Title: "Kits", NotFound: true, NotFoundFor: r.PathValue("name")})
			return
		}
		render(w, "kit", d)
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
		renderFragment(w, "kits", "kits-table", kitTableData(store))
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
		renderKitBody(w, store, name, pushedMsg(name, v, unchanged))
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
		if err := store.PinKit(name, v); err != nil {
			renderError(w, http.StatusNotFound, err.Error())
			return
		}
		log.Info("ui kit pinned", "operator", jam.OperatorID(r), "kit", name, "version", v)
		if r.Header.Get("HX-Target") == "kit" { // from the kit's own page
			renderKitBody(w, store, name, fmt.Sprintf("%s current is now v%d", name, v))
			return
		}
		renderFragment(w, "kits", "kits-table", kitTableData(store))
	})

	mux.HandleFunc("DELETE /ui/kits/{name}", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		name := r.PathValue("name")
		if project, role, ok := store.RoleReferencingKit(name); ok {
			renderError(w, http.StatusConflict, fmt.Sprintf("kit %q is referenced by role %s/%s", name, project, role))
			return
		}
		if err := store.RemoveKit(name); err != nil {
			renderError(w, http.StatusNotFound, err.Error())
			return
		}
		log.Info("ui kit removed", "operator", jam.OperatorID(r), "kit", name)
		renderFragment(w, "kits", "kits-table", kitTableData(store))
	})
}
