package adminui

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
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
	if canGit {
		registerConfigGit(mux, store, f, env, log, guardWrite)
	}
}

// planOpView is one op the plan changes, as the Config page shows it.
type planOpView struct {
	jam.ConfigOp
	Href     string     // the object's page; "" for a create (it does not exist yet)
	Lines    []diffLine // Before → After
	TooLarge bool
}

// planView is a config plan rendered for review: the ops that change
// something (with their diffs), the rest counted or listed, and the digest
// Apply posts back.
type planView struct {
	Source, Commit string // as planned (commit: 7 chars)
	Forced         bool   // planned as a forced apply
	Digest         string
	Ops            []planOpView
	Unchanged      int
	Forget         []jam.ConfigKey
	Unmanaged      []jam.ConfigKey
	KitSources     []jam.ConfigKitSource // commits: 7 chars
	Refusals       []string
	Serve          jam.ServeConfigDiff
	HasDrift       bool // the force checkbox is offered
	Changes        int  // jam.ConfigChanges
}

func newPlanView(res jam.ConfigApplyResult, forced bool) *planView {
	p := res.Plan
	v := &planView{Source: res.Source, Commit: jam.ShortConfigCommit(res.Commit), Forced: forced, Digest: p.Digest,
		Forget: p.Forget, Unmanaged: p.Unmanaged, Refusals: p.Refusals, Serve: p.ServeConfig,
		HasDrift: p.HasDrift(), Changes: jam.ConfigChanges(p)}
	for _, ks := range p.KitSources {
		ks.Commit = jam.ShortConfigCommit(ks.Commit)
		v.KitSources = append(v.KitSources, ks)
	}
	for _, op := range p.Ops {
		if op.Action == jam.ConfigUnchanged {
			v.Unchanged++
			continue
		}
		ov := planOpView{ConfigOp: op}
		if op.Action != jam.ConfigCreate {
			ov.Href = configObjectHref(op.Kind, op.Name)
		}
		if op.Before != "" || op.After != "" {
			ov.Lines, ov.TooLarge = lineDiff(canonDisplay(op.Kind, op.Before), canonDisplay(op.Kind, op.After))
		}
		v.Ops = append(v.Ops, ov)
	}
	return v
}

// canonDisplay is a canonical form as the plan diff shows it: a kit's stored
// config JSON indented one key per line (its "# source:" line kept); every
// other kind's YAML as is.
func canonDisplay(kind, canon string) string {
	if kind != jam.ConfigKindKit || canon == "" {
		return canon
	}
	body, src, tracked := strings.Cut(canon, "\n# source: ")
	var b bytes.Buffer
	if json.Indent(&b, []byte(body), "", "  ") != nil {
		return canon
	}
	if tracked {
		b.WriteString("\n# source: " + src)
	}
	return b.String()
}

// applyMsg is an apply's outcome for the flash.
func applyMsg(res jam.ConfigApplyResult) string {
	at := ""
	if res.Commit != "" {
		at = " config @" + jam.ShortConfigCommit(res.Commit)
	}
	if !res.Applied {
		return "nothing to apply" + at
	}
	k := res.Counts
	return fmt.Sprintf("applied%s: %d created, %d updated, %d deleted, %d adopted, %d forgotten", at,
		k[jam.ConfigCreate], k[jam.ConfigUpdate], k[jam.ConfigDelete], k[jam.ConfigAdopt], k[jam.ConfigForget])
}

// configPlanStaleMsg is the page's answer to an Apply whose reviewed plan is
// no longer the plan (the Jam or the repo changed since the Diff).
const configPlanStaleMsg = "The Jam or the config repo changed since this plan was shown; nothing was applied. Run Diff again and review the new plan."

// registerConfigGit mounts the Config page's git actions on the tracked
// source: Diff (a dry run), Apply (the reviewed plan's digest), Track.
func registerConfigGit(mux *http.ServeMux, store jam.Store, f jam.KitFetcher, env jam.ConfigEnv, log *slog.Logger, guardWrite func(http.ResponseWriter, *http.Request) bool) {
	mux.HandleFunc("POST /ui/config/diff", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		if err := r.ParseForm(); err != nil {
			renderError(w, http.StatusBadRequest, "invalid form")
			return
		}
		force := r.FormValue("force") == "1" // plans as a forced apply would, so the digests match
		res, err := jam.ApplyConfigSource(r.Context(), store, f, gitsource.Source{}, env,
			jam.ConfigApplyOptions{DryRun: true, Force: force, Now: time.Now()}, log, jam.OperatorID(r))
		if err != nil {
			configFail(w, r, log, err)
			return
		}
		renderFragment(w, r, "config", "config-plan", newPlanView(res, force))
	})
	mux.HandleFunc("POST /ui/config/apply", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		if err := r.ParseForm(); err != nil {
			renderError(w, http.StatusBadRequest, "invalid form")
			return
		}
		digest := strings.TrimSpace(r.FormValue("plan"))
		if digest == "" {
			renderError(w, http.StatusBadRequest, "plan is required: run Diff, review it, then Apply")
			return
		}
		force := r.FormValue("force") == "1"
		res, err := jam.ApplyConfigSource(r.Context(), store, f, gitsource.Source{}, env,
			jam.ConfigApplyOptions{Force: force, WantDigest: digest, Now: time.Now()}, log, jam.OperatorID(r))
		if errors.Is(err, jam.ErrConfigPlanChanged) {
			log.Info("ui config apply refused: plan changed since reviewed", "operator", jam.OperatorID(r), "forced", force)
			renderError(w, http.StatusConflict, configPlanStaleMsg)
			return
		}
		if err != nil {
			configFail(w, r, log, err)
			return
		}
		log.Info("ui config applied", "operator", jam.OperatorID(r), "source", res.Source, "commit", res.Commit, "applied", res.Applied, "forced", force)
		renderConfigBody(w, r, buildConfigPage(store, true, log), applyMsg(res))
	})
	mux.HandleFunc("POST /ui/config/source", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		// Track only when nothing is tracked (the page hides the form
		// otherwise): a stale tab must not replace a source and lose its
		// commit and apply record. Switching is Untrack, then Track.
		if cs, tracked := store.GetConfigSource(); tracked {
			renderError(w, http.StatusConflict, "a config source is already tracked ("+newConfigSourceView(cs).Label+"); Untrack it first")
			return
		}
		src, ok := formSource(w, r)
		if !ok {
			return
		}
		res, err := jam.TrackConfigSource(r.Context(), store, f, src, env, time.Now(), log, jam.OperatorID(r))
		if err != nil {
			configFail(w, r, log, err)
			return
		}
		log.Info("ui config tracked", "operator", jam.OperatorID(r), "source", res.Source, "commit", res.Commit)
		d := buildConfigPage(store, true, log)
		d.Plan = newPlanView(res, false)
		renderConfigBody(w, r, d, "tracking "+res.Source)
	})
}
