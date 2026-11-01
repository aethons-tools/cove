package adminui

import (
	"fmt"
	"log/slog"
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
}

// configPage is the Config page payload.
type configPage struct {
	Title       string
	Source      *configSourceView // the followed repo; nil = none configured
	Rows        []configMarkRow
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
		return "/app/admin/users/" + url.PathEscape(name)
	case jam.ConfigKindKit:
		return kitURL(name)
	case jam.ConfigKindProject:
		return projectURL(name)
	case jam.ConfigKindRole:
		p, r, _ := strings.Cut(name, "/")
		return roleURL(p, r)
	case jam.ConfigKindJamContext:
		return "/app/admin/#jam-context"
	}
	return ""
}

// newConfigSourceView is the followed repo as shown, labelled label (the
// serve's ConfigEnv.FollowedLabel: a git repo, a local path or ssh, does not
// re-parse as a kit source). With no label the stored source is re-parsed
// (gitsource.Parse) first: one that does not parse — never one the API
// built, say a hand-made URL with userinfo — is shown only as "invalid", so
// the page never renders a credential. Its commit is 7 chars, as on the
// managed badges.
func newConfigSourceView(cs jam.ConfigSource, label string) *configSourceView {
	v := &configSourceView{Label: label}
	if v.Label == "" {
		v.Label = cs.Source().String()
		if _, err := gitsource.Parse(v.Label); err != nil {
			v.Label = "invalid"
		}
	}
	if v.Label == "invalid" {
		v.Repo, v.Ref = "invalid", "invalid"
	} else {
		v.sourceView = *newSourceView(&jam.KitSource{Repo: cs.Repo, Dir: cs.Dir, Ref: cs.Ref, Commit: cs.Commit})
	}
	v.Commit = jam.ShortConfigCommit(cs.Commit)
	if !cs.AppliedAt.IsZero() {
		v.Applied = configTime(cs.AppliedAt)
	}
	return v
}

// buildConfigPage gathers the Config page: the followed repo (with its last
// apply when the stored record is of it), every managed object with its
// state, and the newest apply.
func buildConfigPage(store jam.Store, env jam.ConfigEnv, canGit bool, log *slog.Logger) configPage {
	followed := env.Followed
	d := configPage{Title: "Config", CanGit: canGit}
	if followed.Repo != "" {
		cs := jam.ConfigSource{Repo: followed.Repo, Dir: followed.Dir, Ref: followed.Ref}
		if rec, ok := store.GetConfigSource(); ok && rec.Source() == followed {
			cs = rec
		}
		d.Source = newConfigSourceView(cs, env.FollowedLabel)
	}
	var newest time.Time
	for _, mk := range store.ListConfigMarks() {
		row := configMarkRow{Kind: mk.Kind, Name: mk.Name, Href: configObjectHref(mk.Kind, mk.Name), Commit: jam.ShortConfigCommit(mk.Commit),
			Applied: configTime(mk.AppliedAt)}
		if mk.AppliedAt.After(newest) {
			newest, d.LastCommit = mk.AppliedAt, row.Commit
		}
		d.Rows = append(d.Rows, row)
	}
	if !newest.IsZero() {
		d.LastApplied = configTime(newest)
	}
	return d
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
	Digest         string
	Ops            []planOpView
	Unchanged      int
	Forget         []jam.ConfigKey
	Unmanaged      []jam.ConfigKey
	KitSources     []jam.ConfigKitSource // commits: 7 chars
	Refusals       []string
	Serve          jam.ServeConfigDiff
	Changes        int // jam.ConfigChanges
}

func newPlanView(res jam.ConfigApplyResult) *planView {
	p := res.Plan
	v := &planView{Source: res.Source, Commit: jam.ShortConfigCommit(res.Commit), Digest: p.Digest,
		Forget: p.Forget, Unmanaged: p.Unmanaged, Refusals: p.Refusals, Serve: p.ServeConfig,
		Changes: jam.ConfigChanges(p)}
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
			ov.Lines, ov.TooLarge = lineDiff(op.Before, op.After)
		}
		v.Ops = append(v.Ops, ov)
	}
	return v
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

// configTime is a config apply's time as the UI shows it.
func configTime(t time.Time) string { return t.UTC().Format("2006-01-02 15:04 UTC") }
