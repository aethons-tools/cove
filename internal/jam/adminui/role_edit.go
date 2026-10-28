package adminui

import (
	"context"
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

// setRoleScope sets a role's scope section from its form fields (get reads
// one by its form name: destinations, addressing, ttl, kit, model-spec).
func setRoleScope(store jam.Store, credExists func(string) bool, project, name string, get func(string) string) error {
	dests, creds, err := jam.ParseDestinations(get("destinations"))
	if err != nil {
		return badRequest(err.Error())
	}
	ttl, err := parseDur("TTL", get("ttl"))
	if err != nil {
		return err
	}
	kit := strings.TrimSpace(get("kit"))
	if kit != "" {
		if _, ok := store.GetKit(kit); !ok {
			return badRequest("kit " + strconv.Quote(kit) + " does not exist")
		}
	}
	spec := strings.TrimSpace(get("model-spec"))
	addressing := splitList(get("addressing"))
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
}

// setRoleAllocation sets a role's allocation section from its form fields
// (get reads one by its form name); its standing sessions are kept.
func setRoleAllocation(store jam.Store, project, name string, get func(string) string) error {
	var a jam.RoleAllocation
	var err error
	for _, c := range []struct {
		field string
		dst   *int
	}{{"max-ephemeral", &a.MaxEphemeral}, {"max-personal", &a.MaxPersonal}, {"max-personal-per-owner", &a.MaxPersonalPerOwner}} {
		if *c.dst, err = parseCount(c.field, get(c.field)); err != nil {
			return err
		}
	}
	for _, d := range []struct {
		field string
		dst   *time.Duration
	}{{"idle-after", &a.IdleAfter}, {"nag-every", &a.NagEvery}, {"reclaim-after", &a.ReclaimAfter}} {
		if *d.dst, err = parseDur(d.field, get(d.field)); err != nil {
			return err
		}
	}
	return jam.UpdateRole(store, project, name, func(role *jam.Role) error {
		a.Standing = role.Allocation.Standing // managed by the standing section
		role.Allocation = a
		return nil
	})
}

// resetStandingMsg runs a standing reset: a pending one is accepted with its
// reason as the message.
func resetStandingMsg(ctx context.Context, store jam.Store, sup *jam.Supervisor, project, name, session string) (string, error) {
	res, err := jam.ResetStanding(ctx, store, sup, project, name, session)
	switch {
	case err != nil:
		return "", err
	case res.Pending:
		return res.Reason, nil
	}
	return "reset standing agent " + session + "; Jam raises it fresh", nil
}

// upgradeStandingMsg queues a standing upgrade (never forced from the UI).
func upgradeStandingMsg(store jam.Store, sup *jam.Supervisor, project, name, session string) (string, error) {
	res, err := jam.UpgradeStanding(store, sup, project, name, session, false)
	switch {
	case err != nil:
		return "", err
	case !res.Pending:
		return "standing agent " + session + " already runs the current image; nothing restarted", nil
	}
	return "upgrade of standing agent " + session + " " + res.State + "; Jam restarts it once the image is ready and the agent is idle", nil
}
