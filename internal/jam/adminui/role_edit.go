package adminui

import (
	"html/template"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
)

// roleForm is a role's current values in the edit forms' input syntax.
type roleForm struct {
	Destinations, Addressing, TTL                  string
	MaxEphemeral, MaxPersonal, MaxPersonalPerOwner string
	IdleAfter, NagEvery, ReclaimAfter              string
	Egress                                         string
}

func newRoleForm(r jam.Role) roleForm {
	a := r.Allocation
	f := roleForm{
		Destinations:        jam.FormatDestinations(r.Scope.Destinations, r.Scope.Credentials),
		Addressing:          strings.Join(r.Scope.Addressing, ","),
		TTL:                 durField(r.Scope.TTL),
		MaxEphemeral:        intField(a.MaxEphemeral),
		MaxPersonal:         intField(a.MaxPersonal),
		MaxPersonalPerOwner: intField(a.MaxPersonalPerOwner),
		IdleAfter:           durField(a.IdleAfter),
		NagEvery:            durField(a.NagEvery),
		ReclaimAfter:        durField(a.ReclaimAfter),
	}
	if r.Scope.Egress != nil {
		f.Egress = strings.Join(r.Scope.Egress.Domains, "\n")
	}
	return f
}

// Unset (zero) values prefill as blank, so a blank field round-trips as unset.
func intField(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}

func durField(d time.Duration) string {
	if d == 0 {
		return ""
	}
	return fmtDur(d)
}

// parseDur reads a duration field: blank is 0, a bare integer is seconds,
// otherwise Go syntax ("90m", "1h30m"). Negative is refused.
func parseDur(field, v string) (time.Duration, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(v)
	if n, aerr := strconv.Atoi(v); aerr == nil {
		d, err = time.Duration(n)*time.Second, nil
	}
	if err != nil || d < 0 {
		return 0, &jam.WriteError{Status: http.StatusBadRequest, Msg: field + ` must be a duration like "30m" or "1h30m"`}
	}
	return d, nil
}

// parseCount reads a non-negative integer field; blank is 0.
func parseCount(field, v string) (int, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, &jam.WriteError{Status: http.StatusBadRequest, Msg: field + " must be a whole number ≥ 0"}
	}
	return n, nil
}

// splitList splits a list field on commas, whitespace and newlines.
func splitList(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\r' || r == '\t' })
}

func badRequest(msg string) error { return &jam.WriteError{Status: http.StatusBadRequest, Msg: msg} }

// registerRoleEdits mounts the role page's per-section writes. Each goes
// through jam's role read-modify-write functions (one shared lock with the JSON
// API) and answers with the re-rendered role body.
func registerRoleEdits(mux *http.ServeMux, store jam.Store, img jam.ImageResolver, log *slog.Logger, sup *jam.Supervisor, credExists func(string) bool, canRequest bool, guardWrite func(http.ResponseWriter, *http.Request) bool) {
	// editFlash wraps one section write: guard, parse the form, run apply, log,
	// and render the role body — plus a success flash when apply returns a
	// message — or the refusal, with its status. Every success (including an
	// accepted-but-pending standing reset or upgrade) is a 200 role body;
	// renderError is for refusals only.
	editFlash := func(what string, apply func(r *http.Request, project, name string) (string, error)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !guardWrite(w, r) {
				return
			}
			if err := r.ParseForm(); err != nil {
				renderError(w, http.StatusBadRequest, "invalid form")
				return
			}
			project, name := r.PathValue("project"), r.PathValue("name")
			msg, err := apply(r, project, name)
			if err != nil {
				renderError(w, jam.WriteStatus(err, http.StatusInternalServerError), err.Error())
				return
			}
			log.Info("ui role "+what, "operator", jam.OperatorID(r), "project", orDefaultProject(project), "role", name)
			d, ok := buildRoleDetail(store, img, project, name)
			if !ok { // deleted between the write and the read
				renderError(w, http.StatusNotFound, "role no longer exists")
				return
			}
			d.CanRequest = canRequest
			renderFragment(w, "role", "role-body", d)
			if msg != "" {
				_, _ = w.Write([]byte(`<div id="flash" hx-swap-oob="innerHTML"><p class="ok">` + template.HTMLEscapeString(msg) + `</p></div>`))
			}
		}
	}
	// edit is editFlash for writes with no message.
	edit := func(what string, apply func(r *http.Request, project, name string) error) http.HandlerFunc {
		return editFlash(what, func(r *http.Request, project, name string) (string, error) { return "", apply(r, project, name) })
	}

	mux.HandleFunc("POST /ui/roles/{project}/{name}/scope", edit("scope set", func(r *http.Request, project, name string) error {
		dests, creds, err := jam.ParseDestinations(r.FormValue("destinations"))
		if err != nil {
			return badRequest(err.Error())
		}
		ttl, err := parseDur("TTL", r.FormValue("ttl"))
		if err != nil {
			return err
		}
		kit := strings.TrimSpace(r.FormValue("kit"))
		if kit != "" {
			if _, ok := store.GetKit(kit); !ok {
				return badRequest("kit " + strconv.Quote(kit) + " does not exist")
			}
		}
		spec := strings.TrimSpace(r.FormValue("model-spec"))
		addressing := splitList(r.FormValue("addressing"))
		return jam.UpdateRole(store, project, name, func(role *jam.Role) error {
			// Fresh slices/maps: never mutate what the stored role may alias.
			scope := role.Scope
			scope.Destinations, scope.Credentials, scope.Addressing, scope.TTL = dests, creds, addressing, ttl
			if err := jam.ValidateCredentials(scope, credExists); err != nil {
				return badRequest(err.Error())
			}
			role.Scope, role.Kit, role.ModelSpec = scope, kit, spec // UpdateRole checks spec exists
			return nil
		})
	}))

	mux.HandleFunc("POST /ui/roles/{project}/{name}/allocation", edit("allocation set", func(r *http.Request, project, name string) error {
		var a jam.RoleAllocation
		var err error
		for _, c := range []struct {
			field string
			dst   *int
		}{{"max-ephemeral", &a.MaxEphemeral}, {"max-personal", &a.MaxPersonal}, {"max-personal-per-owner", &a.MaxPersonalPerOwner}} {
			if *c.dst, err = parseCount(c.field, r.FormValue(c.field)); err != nil {
				return err
			}
		}
		for _, d := range []struct {
			field string
			dst   *time.Duration
		}{{"idle-after", &a.IdleAfter}, {"nag-every", &a.NagEvery}, {"reclaim-after", &a.ReclaimAfter}} {
			if *d.dst, err = parseDur(d.field, r.FormValue(d.field)); err != nil {
				return err
			}
		}
		return jam.UpdateRole(store, project, name, func(role *jam.Role) error {
			a.Standing = role.Allocation.Standing // managed by the standing section
			role.Allocation = a
			return nil
		})
	}))

	mux.HandleFunc("POST /ui/roles/{project}/{name}/context", edit("context set", func(r *http.Request, project, name string) error {
		b, err := parseContextForm(r)
		if err != nil {
			return err
		}
		return jam.SetRoleContextChecked(store, project, name, b)
	}))
	mux.HandleFunc("DELETE /ui/roles/{project}/{name}/context", edit("context cleared", func(r *http.Request, project, name string) error {
		return jam.ClearRoleContext(store, project, name)
	}))

	mux.HandleFunc("POST /ui/roles/{project}/{name}/egress", edit("egress set", func(r *http.Request, project, name string) error {
		_, err := jam.SetRoleEgress(store, project, name, splitList(r.FormValue("domains")))
		return err
	}))
	mux.HandleFunc("DELETE /ui/roles/{project}/{name}/egress", edit("egress cleared", func(_ *http.Request, project, name string) error {
		return jam.ClearRoleEgress(store, project, name)
	}))

	mux.HandleFunc("POST /ui/roles/{project}/{name}/standing", edit("standing declared", func(r *http.Request, project, name string) error {
		return jam.AddStanding(store, project, name, jam.StandingSession{
			Name: strings.TrimSpace(r.FormValue("name")), Prompt: strings.TrimSpace(r.FormValue("prompt")),
		})
	}))
	mux.HandleFunc("DELETE /ui/roles/{project}/{name}/standing/{session}", edit("standing dismissed", func(r *http.Request, project, name string) error {
		return jam.RemoveStanding(store, project, name, r.PathValue("session"))
	}))
	// Reset: the session's studio and persisted state are deleted, the
	// declaration kept — Jam raises it fresh on its next standing pass. A
	// pending reset is accepted (flashed), not an error.
	mux.HandleFunc("POST /ui/roles/{project}/{name}/standing/{session}/reset", editFlash("standing reset", func(r *http.Request, project, name string) (string, error) {
		session := r.PathValue("session")
		res, err := jam.ResetStanding(r.Context(), store, sup, project, name, session)
		switch {
		case err != nil:
			return "", err
		case res.Pending:
			return res.Reason, nil
		}
		return "reset standing session " + session + "; Jam raises it fresh", nil
	}))
	// Upgrade: queued with the standing reconciler, which prepares the current
	// image, waits for the session to be idle, and re-raises it keeping its
	// conversation and workspace (the UI never forces; the CLI's --force does).
	mux.HandleFunc("POST /ui/roles/{project}/{name}/standing/{session}/upgrade", editFlash("standing upgrade", func(r *http.Request, project, name string) (string, error) {
		session := r.PathValue("session")
		res, err := jam.UpgradeStanding(store, sup, project, name, session, false)
		switch {
		case err != nil:
			return "", err
		case !res.Pending:
			return "standing session " + session + " already runs the current image; nothing restarted", nil
		}
		return "upgrade of standing session " + session + " " + res.State + "; Jam restarts it once the image is ready and the session is idle", nil
	}))
}
