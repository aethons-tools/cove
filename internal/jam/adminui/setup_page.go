package adminui

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
)

// WithSetupDeps enables the Setup tab of a running studio's agent page and of
// a role page: it renders jam.InstanceSetup / jam.RoleSetup in-process with
// d — the deps serve gives the admin API's jam.WithSetupInspect. Without it
// the tab is hidden and its routes are not mounted.
func WithSetupDeps(d jam.SetupDeps) Option {
	return func(o *options) { o.setup = &d }
}

// WithSetupManagers gives a role's Setup tab the managers to pick the image
// facts' manager from (the router's AvailableNames). With fewer than two the
// tab shows no selector.
func WithSetupManagers(names func() []string) Option {
	return func(o *options) { o.setupManagers = names }
}

// setupView is the sections fragment.
type setupView struct {
	Note     string // the render's note (a studio whose role is gone: no diff)
	Stale    int    // sections stale vs role
	Diffable bool   // some section has a DiffURL (its stale badge loads a diff)
	Sections []setupSectionView
}

// setupSectionView is one collapsible section. Every string in it has been
// through jam.SetupDisplayText; html/template escapes the rest.
type setupSectionView struct {
	Name, Source, Reason string
	Stale                bool
	At, AtISO            string // the snapshot time ("" without one)
	Body                 string
	Head                 string          // context: the file table, budgets and warnings
	Files                []setupFileView // context: each file, raw (none: Body is shown whole)
	Open                 bool
	DiffURL              string // a stale, undiffed studio section: its diff fragment
	Diffed, DiffTooLarge bool
	DiffSame             bool // diffed, and identical to a raise now
	Diff                 []diffLine
	DiffNote             string // why there is no diff (the render's note)
}

// setupFileView is one context file at its real path; Bytes is its shown size.
type setupFileView struct {
	Path, Body string
	Bytes      int
}

var errSetupDiff = errors.New(`diff: want "role"`)

// setupQuery reads ?sections= (empty = all) and, on a studio, ?diff=role.
func setupQuery(q url.Values, studio bool) (sections []string, diff bool, err error) {
	sections, err = jam.ParseSetupSections(q.Get("sections"))
	if err != nil {
		return nil, false, err
	}
	switch d := q.Get("diff"); {
	case d == "":
		return sections, false, nil
	case d == "role" && studio:
		return sections, true, nil
	default:
		return nil, false, errSetupDiff
	}
}

// newSetupView is the fragment of s; diffBase ("" = none) is where a stale
// section's diff loads from (diffBase + "/" + name).
func newSetupView(s jam.StudioSetup, diffBase string) setupView {
	v := setupView{Note: jam.SetupDisplayText(s.Note)}
	for _, sec := range s.Sections {
		sv := newSectionView(sec, diffBase)
		if sv.Stale {
			v.Stale++
		}
		v.Diffable = v.Diffable || sv.DiffURL != ""
		v.Sections = append(v.Sections, sv)
	}
	if len(v.Sections) == 1 {
		v.Sections[0].Open = true
	}
	return v
}

func newSectionView(sec jam.SetupSection, diffBase string) setupSectionView {
	sv := setupSectionView{
		Name: sec.Name, Source: sec.Source, Reason: jam.SetupDisplayText(sec.Reason), Stale: sec.Stale,
		Body: jam.SetupDisplayText(sec.Body), Diffed: sec.Diffed, DiffTooLarge: sec.DiffTooLarge,
		Open: sec.Name == jam.SectionIdentity,
	}
	if !sec.At.IsZero() {
		sv.At, sv.AtISO = sec.At.UTC().Format("2006-01-02 15:04:05 UTC"), sec.At.UTC().Format(time.RFC3339)
	}
	if sec.Name == jam.SectionContext {
		if head, files, ok := jam.SplitContextBody(sec.Body); ok {
			sv.Head = jam.SetupDisplayText(head)
			for _, f := range files {
				sv.Files = append(sv.Files, setupFileView{Path: jam.SetupDisplayText(f.Path), Body: jam.SetupDisplayText(f.Body), Bytes: len(f.Body)})
			}
		}
	}
	if sec.Diffed {
		changed := false
		for _, l := range sec.Diff {
			sv.Diff = append(sv.Diff, diffLine{Op: l.Op, Text: jam.SetupDisplayText(l.Text)})
			changed = changed || l.Op == "+" || l.Op == "-"
		}
		sv.DiffSame = !sec.DiffTooLarge && !changed
		sv.Open = sv.Open || changed || sec.DiffTooLarge
	}
	if sec.Stale && !sec.Diffed && diffBase != "" {
		sv.DiffURL = diffBase + "/" + url.PathEscape(sec.Name)
	}
	return sv
}

// auditSetup logs an inspection as the admin API does — never a body.
func auditSetup(log *slog.Logger, r *http.Request, subject string, sections []string, diff bool) {
	log.Info("ui setup inspected", "operator", jam.OperatorID(r), "subject", subject, "sections", strings.Join(sections, ","), "diff", diff)
}

// setupFailJSON is setupFail for the JSON API.
func setupFailJSON(w http.ResponseWriter, log *slog.Logger, err error, subject, notFound string) {
	if code, ok := jam.ManagerErrStatus(err); ok {
		writeAPIError(w, code, apiCode(code), jam.SetupDisplayText(err.Error()))
		return
	}
	switch {
	case errors.Is(err, jam.ErrNotFound):
		writeAPIError(w, http.StatusNotFound, "not_found", notFound)
	case errors.Is(err, jam.ErrUnknownSection):
		writeAPIError(w, http.StatusBadRequest, "bad_request", jam.SetupDisplayText(err.Error()))
	default:
		log.Warn("ui setup inspect failed", "subject", subject, "err", err.Error())
		writeAPIError(w, http.StatusInternalServerError, "internal", "setup unavailable")
	}
}
