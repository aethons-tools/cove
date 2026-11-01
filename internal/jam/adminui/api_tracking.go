package adminui

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/gitsource"
)

// The tracking JSON API: Config › Tracking, the old Config page's JSON twin
// (config_page.go), through the same jam calls.

// objectPath is a config object's page in the app; "" when it has none.
func objectPath(store jam.Store, kind, name string) string {
	esc := url.PathEscape
	switch kind {
	case jam.ConfigKindDestination:
		return "config/destinations/" + esc(name)
	case jam.ConfigKindModelSpec:
		return "config/model-specs/" + esc(name)
	case jam.ConfigKindUser:
		if id, err := jam.ResolveRegistryRef(store, ident.User, name); err == nil {
			name = string(id)
		}
		return "config/users/" + esc(name)
	case jam.ConfigKindKit:
		return "config/kits/" + esc(name)
	case jam.ConfigKindProject:
		return "config/projects/" + esc(name)
	case jam.ConfigKindRole:
		p, r, _ := strings.Cut(name, "/")
		return "config/projects/" + esc(p) + "/agents/" + esc(r)
	}
	return ""
}

func diffLines(ls []diffLine) []DiffLine {
	out := []DiffLine{}
	for _, l := range ls {
		out = append(out, DiffLine{Op: l.Op, Text: l.Text})
	}
	return out
}

func keyRows(ks []jam.ConfigKey) []ConfigKeyRow {
	out := []ConfigKeyRow{}
	for _, k := range ks {
		out = append(out, ConfigKeyRow{Kind: k.Kind, Name: k.Name})
	}
	return out
}

func planOf(store jam.Store, v *planView) *Plan {
	if v == nil {
		return nil
	}
	p := &Plan{Source: v.Source, Commit: v.Commit, Digest: v.Digest, Forced: v.Forced, HasDrift: v.HasDrift, Changes: v.Changes, Unchanged: v.Unchanged,
		Ops: []PlanOpRow{}, Forget: keyRows(v.Forget), Unmanaged: keyRows(v.Unmanaged), KitSources: []KitSourceRow{}, Refusals: []string{},
		Serve: ServeDiff{State: v.Serve.State, Keys: append([]string{}, v.Serve.Keys...)}}
	p.Refusals = append(p.Refusals, v.Refusals...)
	for _, ks := range v.KitSources {
		p.KitSources = append(p.KitSources, KitSourceRow{Path: ks.Path, Source: ks.Source, Commit: ks.Commit})
	}
	for _, op := range v.Ops {
		row := PlanOpRow{Action: op.Action, Kind: op.Kind, Name: op.Name, File: op.Path, Drift: op.Drift, Unmanaged: op.Unmanaged,
			Lines: diffLines(op.Lines), TooLarge: op.TooLarge, Diff: op.Diff}
		if op.Href != "" {
			row.Path = objectPath(store, op.Kind, op.Name)
		}
		p.Ops = append(p.Ops, row)
	}
	return p
}

func trackingOf(store jam.Store, env jam.ConfigEnv, d configPage) Tracking {
	t := Tracking{Rows: []ManagedRow{}, Drifted: d.Drifted, StatesErr: d.StatesErr, LastCommit: d.LastCommit, LastApplied: d.LastApplied,
		CanGit: d.CanGit, Plan: planOf(store, d.Plan), Follow: followOf(env)}
	if s := d.Source; s != nil {
		t.Source = &TrackingSource{Label: s.Label, Repo: s.Repo, RepoLink: s.RepoLink, Dir: s.Dir, Ref: s.Ref, Commit: s.Commit, CommitLink: s.CommitLink, Applied: s.Applied}
	}
	for _, r := range d.Rows {
		row := ManagedRow{Kind: r.Kind, Name: r.Name, Commit: r.Commit, Applied: r.Applied, State: r.State}
		if r.Href != "" {
			row.Path = objectPath(store, r.Kind, r.Name)
		}
		t.Rows = append(t.Rows, row)
	}
	return t
}

// followOf is the follower's status as the page shows it.
func followOf(env jam.ConfigEnv) FollowStatus {
	s := jam.ConfigFollowStatus{State: jam.ConfigFollowOff}
	if env.Follower != nil {
		s = env.Follower.Status()
	}
	f := FollowStatus{State: s.State, Head: jam.ShortConfigCommit(s.Head), Applied: jam.ShortConfigCommit(s.Applied), Error: s.Error}
	if !s.CheckedAt.IsZero() {
		f.Checked = configTime(s.CheckedAt)
	}
	return f
}

// trackingFail answers a config operation's error as configFail does.
func trackingFail(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	status, msg := jam.ConfigStatus(err)
	if status == http.StatusInternalServerError {
		log.Error("ui config: store error", "operator", jam.OperatorID(r), "err", err.Error())
	}
	writeAPIError(w, status, apiCode(status), msg)
}

func apiTracking(api *http.ServeMux, store jam.Store, f jam.KitFetcher, env jam.ConfigEnv, log *slog.Logger) {
	canGit := f != nil
	page := func() configPage { return buildConfigPage(store, env, canGit, log) }
	done := func(w http.ResponseWriter, d configPage, msg string) {
		writeJSON(w, http.StatusOK, TrackingResult{Tracking: trackingOf(store, env, d), Message: msg})
	}
	api.HandleFunc("GET "+apiPrefix+"tracking", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, trackingOf(store, env, page()))
	})
	api.HandleFunc("DELETE "+apiPrefix+"tracking/managed/{kind}/{name...}", func(w http.ResponseWriter, r *http.Request) {
		key := jam.ConfigKey{Kind: r.PathValue("kind"), Name: r.PathValue("name")}
		if err := jam.ReleaseConfigMark(store, key, env.Conditions, log, jam.OperatorID(r)); err != nil {
			trackingFail(w, r, log, err)
			return
		}
		log.Info("ui config released", "operator", jam.OperatorID(r), "kind", key.Kind, "name", key.Name)
		done(w, page(), "released "+key.Kind+" "+key.Name+"; it stays in Jam, unmanaged")
	})
	// git wraps the actions that need a fetcher: without one they don't exist.
	git := func(h http.HandlerFunc) http.HandlerFunc {
		if canGit {
			return h
		}
		return func(w http.ResponseWriter, r *http.Request) {
			writeAPIError(w, http.StatusNotFound, "not_found", "git actions are not available on this serve")
		}
	}
	api.HandleFunc("POST "+apiPrefix+"tracking/diff", git(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Force bool }
		if !decodeJSON(w, r, &body) {
			return
		}
		res, err := jam.ApplyConfigSource(r.Context(), store, f, gitsource.Source{}, env,
			jam.ConfigApplyOptions{DryRun: true, Force: body.Force, Now: time.Now()}, log, jam.OperatorID(r))
		if err != nil {
			trackingFail(w, r, log, err)
			return
		}
		writeJSON(w, http.StatusOK, planOf(store, newPlanView(res, body.Force)))
	}))
	api.HandleFunc("POST "+apiPrefix+"tracking/apply", git(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Plan  string
			Force bool
		}
		if !decodeJSON(w, r, &body) {
			return
		}
		digest := strings.TrimSpace(body.Plan)
		if digest == "" {
			writeAPIError(w, http.StatusBadRequest, "bad_request", "plan is required: run Diff, review it, then Apply")
			return
		}
		res, err := jam.ApplyConfigSource(r.Context(), store, f, gitsource.Source{}, env,
			jam.ConfigApplyOptions{Force: body.Force, WantDigest: digest, Now: time.Now()}, log, jam.OperatorID(r))
		if errors.Is(err, jam.ErrConfigPlanChanged) {
			log.Info("ui config apply refused: plan changed since reviewed", "operator", jam.OperatorID(r), "forced", body.Force)
			writeAPIError(w, http.StatusConflict, "conflict", configPlanStaleMsg)
			return
		}
		if err != nil {
			trackingFail(w, r, log, err)
			return
		}
		log.Info("ui config applied", "operator", jam.OperatorID(r), "source", res.Source, "commit", res.Commit, "applied", res.Applied, "forced", body.Force)
		if env.Follower != nil {
			env.Follower.Refresh() // a confirm leaves awaiting-confirm now
		}
		done(w, page(), applyMsg(res))
	}))
}
