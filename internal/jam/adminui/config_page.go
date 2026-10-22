package adminui

import (
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/gitsource"
)

// configSourceView is the tracked config source on the Config page.
type configSourceView struct {
	sourceView
	Label   string // <repo>[//<dir>]@<ref>
	Applied string // the last apply from it; "" = none since tracked
}

// configMarkRow is one managed object on the Config page.
type configMarkRow struct {
	Kind, Name string
	Href       string // its page; "" for a connection or a missing object
	Commit     string // the config commit it was applied from (7 chars)
	Applied    string
	State      string // jam.ConfigInSync|ConfigDrifted|ConfigMissing; "" = unknown
	ReleaseURL string
}

// configPage is the Config page payload.
type configPage struct {
	Title       string
	Source      *configSourceView // nil = none tracked
	Rows        []configMarkRow
	Drifted     int    // rows drifted or missing
	StatesErr   bool   // the live Jam could not be compared (see the log)
	LastCommit  string // the newest mark's commit (7 chars)
	LastApplied string // and time
	CanGit      bool   // a fetcher: Diff, Apply and Track are offered
	Plan        *planView
}

// configObjectHref is a config object's page; "" for a connection (no page).
func configObjectHref(kind, name string) string {
	switch kind {
	case jam.ConfigKindDestination:
		return destURL(name)
	case jam.ConfigKindModelSpec:
		return specURL(name)
	case jam.ConfigKindUser:
		return "/ui/users/" + url.PathEscape(name)
	case jam.ConfigKindKit:
		return kitURL(name)
	case jam.ConfigKindProject:
		return projectURL(name)
	case jam.ConfigKindRole:
		p, r, _ := strings.Cut(name, "/")
		return roleURL(p, r)
	case jam.ConfigKindJamContext:
		return "/ui/#jam-context"
	}
	return ""
}

// configReleaseURL is the UI's release route for kind/name (a role's name
// keeps its slash).
func configReleaseURL(kind, name string) string {
	segs := strings.Split(name, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return "/ui/config/managed/" + url.PathEscape(kind) + "/" + strings.Join(segs, "/")
}

// newConfigSourceView is the tracked source as shown. The stored source is
// re-parsed (gitsource.Parse) first: one that does not parse — never one the
// API built, say a hand-made URL with userinfo — is shown only as "invalid",
// so the page never renders a credential. Its commit is 7 chars, as on the
// managed badges.
func newConfigSourceView(cs jam.ConfigSource) *configSourceView {
	v := &configSourceView{Label: cs.Source().String()}
	if _, err := gitsource.Parse(v.Label); err != nil {
		v.Label, v.Repo, v.Ref = "invalid", "invalid", "invalid"
	} else {
		v.sourceView = *newSourceView(&jam.KitSource{Repo: cs.Repo, Dir: cs.Dir, Ref: cs.Ref, Commit: cs.Commit})
	}
	v.Commit = jam.ShortConfigCommit(cs.Commit)
	if !cs.AppliedAt.IsZero() {
		v.Applied = configTime(cs.AppliedAt)
	}
	return v
}

// buildConfigPage gathers the Config page: the tracked source, every managed
// object with its state, and the newest apply.
func buildConfigPage(store jam.Store, canGit bool, log *slog.Logger) configPage {
	d := configPage{Title: "Config", CanGit: canGit}
	if cs, ok := store.GetConfigSource(); ok {
		d.Source = newConfigSourceView(cs)
	}
	states, err := jam.ConfigMarkStates(store)
	if err != nil {
		log.Warn("ui config: drift not computed", "err", err.Error())
		d.StatesErr = true
		states = nil
		for _, mk := range store.ListConfigMarks() {
			states = append(states, jam.ConfigMarkState{ConfigMark: mk}) // State "" = unknown
		}
	}
	var newest time.Time
	for _, s := range states {
		row := configMarkRow{Kind: s.Kind, Name: s.Name, Href: configObjectHref(s.Kind, s.Name), Commit: jam.ShortConfigCommit(s.Commit),
			Applied: configTime(s.AppliedAt), State: s.State, ReleaseURL: configReleaseURL(s.Kind, s.Name)}
		if s.State == jam.ConfigMissing {
			row.Href = ""
		}
		if s.State != "" && s.State != jam.ConfigInSync {
			d.Drifted++
		}
		if s.AppliedAt.After(newest) {
			newest, d.LastCommit = s.AppliedAt, row.Commit
		}
		d.Rows = append(d.Rows, row)
	}
	if !newest.IsZero() {
		d.LastApplied = configTime(newest)
	}
	return d
}

// renderConfigBody answers a Config-page write with the re-rendered body,
// plus an out-of-band #flash message when msg is set.
func renderConfigBody(w http.ResponseWriter, r *http.Request, d configPage, msg string) {
	renderFragment(w, r, "config", "config-body", d)
	if msg != "" {
		_, _ = w.Write([]byte(`<div id="flash" hx-swap-oob="innerHTML"><p class="ok">` + template.HTMLEscapeString(msg) + `</p></div>`))
	}
}

// configFail answers a config operation's error as the admin API maps it
// (jam.ConfigStatus), logging store failures.
func configFail(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	status, msg := jam.ConfigStatus(err)
	if status == http.StatusInternalServerError {
		log.Error("ui config: store error", "operator", jam.OperatorID(r), "err", err.Error())
	}
	renderError(w, status, msg)
}

func registerConfig(mux *http.ServeMux, store jam.Store, f jam.KitFetcher, env jam.ConfigEnv, log *slog.Logger, guardWrite func(http.ResponseWriter, *http.Request) bool) {
	canGit := f != nil
	mux.HandleFunc("GET /ui/config", func(w http.ResponseWriter, r *http.Request) {
		render(w, r, "config", buildConfigPage(store, canGit, log))
	})
	mux.HandleFunc("DELETE /ui/config/source", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		if err := jam.UntrackConfigSource(store, log, jam.OperatorID(r)); err != nil {
			configFail(w, r, log, err)
			return
		}
		log.Info("ui config untracked", "operator", jam.OperatorID(r))
		renderConfigBody(w, r, buildConfigPage(store, canGit, log), "config source untracked; managed objects and their marks stay")
	})
	mux.HandleFunc("DELETE /ui/config/managed/{kind}/{name...}", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		key := jam.ConfigKey{Kind: r.PathValue("kind"), Name: r.PathValue("name")}
		if err := jam.ReleaseConfigMark(store, key, env.Conditions, log, jam.OperatorID(r)); err != nil {
			configFail(w, r, log, err)
			return
		}
		log.Info("ui config released", "operator", jam.OperatorID(r), "kind", key.Kind, "name", key.Name)
		renderConfigBody(w, r, buildConfigPage(store, canGit, log), "released "+key.Kind+" "+key.Name+"; it stays in Jam, unmanaged")
	})
}

// planView is a rendered config plan (Task 8 fills it in).
type planView struct{}
