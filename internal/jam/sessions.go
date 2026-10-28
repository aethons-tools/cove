package jam

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
)

// SessionKindPersonal is Instance.SessionKind for a human's personal session
// (mirrors allocator.SessionPersonal; Jam does not import allocator).
const SessionKindPersonal = "personal"

// SessionKindStanding is Instance.SessionKind for an operator-declared, named
// standing session (mirrors allocator.SessionStanding).
const SessionKindStanding = "standing"

// IsResident reports whether a session kind is resident: its cove waits after
// every turn (instead of ending) and is never reaped for waiting. Personal and
// standing sessions are resident; ephemeral ones are not.
func IsResident(kind string) bool {
	return kind == SessionKindPersonal || kind == SessionKindStanding
}

// NagMessageID is the intercom message id of an idle nag sent to a personal
// session's owner at at: "nag:<actorID>:<unix-nanos>". The id lets wake-on
// recognize a reply to one of this session's nags (IsNagReply) with no log
// schema change.
func NagMessageID(actorID string, at time.Time) string {
	return fmt.Sprintf("nag:%s:%d", actorID, at.UnixNano())
}

// IsNagReply reports whether replyTo is the id of one of actorID's nags. The
// trailing ':' of the prefix keeps actors whose ids share a prefix apart.
func IsNagReply(replyTo, actorID string) bool {
	return actorID != "" && strings.HasPrefix(replyTo, "nag:"+actorID+":")
}

// ErrNeedsLedger is returned (possibly wrapped) by a SessionAllocator when
// personal sessions cannot be admitted because Jam has no allocation ledger
// (the Postgres allocator ledger is absent; tests only).
var ErrNeedsLedger = errors.New("personal sessions need the allocation ledger (store-postgres)")

// SessionAllocator is the capacity authority the personal-session routes admit
// against: GrantPersonal reserves a slot for owner's personal session of
// (project, role) against the role's pool and per-owner caps; RecordRelease
// frees it. Jam does not import allocator, so cmd/jam adapts
// *allocator.Allocator to this interface (translating its no-ledger error to
// ErrNeedsLedger).
type SessionAllocator interface {
	GrantPersonal(ctx context.Context, project, role, reservationID, owner string) (bool, error)
	RecordRelease(ctx context.Context, project, role, reservationID string) error
}

// PersonalSessionBody is the POST /admin/sessions/personal request.
type PersonalSessionBody struct {
	Project string `json:"project,omitempty"`
	Role    string `json:"role"`
	Prompt  string `json:"prompt,omitempty"`
	// Managers and PlacementTimeout place the raise as on CoveRaiseBody.
	Managers         []string `json:"managers,omitempty"`
	PlacementTimeout string   `json:"placement_timeout,omitempty"`
}

// PersonalSessionResult is the POST /admin/sessions/personal response. Unlike
// a cove raise it never carries the identity token or launch secret.
type PersonalSessionResult struct {
	ID      string `json:"id"`
	Name    string `json:"name"` // its display name, <role>-NN (reservePersonalName)
	Owner   string `json:"owner"`
	Project string `json:"project"`
	Role    string `json:"role"`
	Phase   string `json:"phase"`
}

// PersonalSessionSummary is a GET /admin/sessions/personal item.
type PersonalSessionSummary struct {
	ID       string    `json:"id"`
	Owner    string    `json:"owner"`
	Project  string    `json:"project"`
	Role     string    `json:"role"`
	Phase    string    `json:"phase"`
	Activity string    `json:"activity,omitempty"`
	RaisedAt time.Time `json:"raised_at"`
}

// registerPersonalSessions mounts the personal-session routes. The caller is
// the roster Human in the target project whose Login is the request's
// OperatorID; an unlinked caller is refused (403).
func registerPersonalSessions(mux *http.ServeMux, store Store, sup *Supervisor, alloc SessionAllocator, log *slog.Logger) {
	mux.HandleFunc("POST /admin/sessions/personal", func(w http.ResponseWriter, r *http.Request) {
		var b PersonalSessionBody
		if !decode(w, r, &b) {
			return
		}
		res, err := RequestPersonalSession(r.Context(), store, sup, alloc, log, OperatorID(r), b)
		if err != nil {
			http.Error(w, err.Error(), PersonalSessionStatus(err))
			return
		}
		writeJSON(w, http.StatusCreated, res)
	})

	mux.HandleFunc("GET /admin/sessions/personal", func(w http.ResponseWriter, r *http.Request) {
		project := orDefaultProject(r.URL.Query().Get("project"))
		human, ok := MemberByLogin(store, project, OperatorID(r))
		if !ok {
			http.Error(w, fmt.Sprintf("no member of %s is linked to your login", project), http.StatusForbidden)
			return
		}
		out := []PersonalSessionSummary{}
		for _, i := range store.ListInstances() {
			if i.SessionKind != SessionKindPersonal || !SameProject(store, i.Project, project) || !ownedBy(i, human) {
				continue
			}
			out = append(out, PersonalSessionSummary{
				ID: i.ActorID, Owner: i.Owner, Project: ProjectName(store, i.Project), Role: i.Role,
				Phase: string(i.Phase), Activity: string(i.Activity), RaisedAt: i.RaisedAt,
			})
		}
		writeJSON(w, http.StatusOK, out)
	})

	mux.HandleFunc("DELETE /admin/sessions/personal/{id}", func(w http.ResponseWriter, r *http.Request) {
		if sup == nil {
			http.Error(w, "runtime supervisor not configured", http.StatusServiceUnavailable)
			return
		}
		id := r.PathValue("id")
		inst, ok := store.GetInstance(id)
		if !ok || inst.SessionKind != SessionKindPersonal {
			http.Error(w, fmt.Sprintf("no personal session %q", id), http.StatusNotFound)
			return
		}
		human, ok := MemberByLogin(store, inst.Project, OperatorID(r))
		if !ok || !ownedBy(inst, human) {
			http.Error(w, "only the session's owner may release it", http.StatusForbidden)
			return
		}
		// Teardown records the reservation release (Supervisor's releaser).
		if err := sup.Teardown(r.Context(), id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		log.Info("admin personal session released", "operator", OperatorID(r), "id", id, "owner", inst.Owner)
		w.WriteHeader(http.StatusNoContent)
	})
}

// PersonalSessionError is a refused personal-session request: Status is the
// HTTP status the admin API answers with, Msg the operator-facing reason.
type PersonalSessionError struct {
	Status int
	Msg    string
}

func (e *PersonalSessionError) Error() string { return e.Msg }

// PersonalSessionStatus maps a RequestPersonalSession error to its HTTP status
// (500 for an unexpected error).
func PersonalSessionStatus(err error) int {
	var pe *PersonalSessionError
	if errors.As(err, &pe) {
		return pe.Status
	}
	return http.StatusInternalServerError
}

// RequestPersonalSession admits and raises a personal session of b.Role for the
// member of b.Project whose Login is login (StartPersonalSession). It is the
// path behind POST /admin/sessions/personal and the operator UI's role Request
// action. Refusals are *PersonalSessionError.
func RequestPersonalSession(ctx context.Context, store Store, sup *Supervisor, alloc SessionAllocator, log *slog.Logger, login string, b PersonalSessionBody) (PersonalSessionResult, error) {
	if sup == nil || alloc == nil {
		return PersonalSessionResult{}, &PersonalSessionError{Status: http.StatusServiceUnavailable, Msg: "runtime supervisor or allocator not configured"}
	}
	project := orDefaultProject(b.Project)
	human, ok := MemberByLogin(store, project, login)
	if !ok {
		return PersonalSessionResult{}, &PersonalSessionError{Status: http.StatusForbidden, Msg: fmt.Sprintf("no member of %s is linked to your login", project)}
	}
	b.Project = project
	return StartPersonalSession(ctx, store, sup, alloc, log, human, login, b)
}

// StartPersonalSession admits and raises a personal session of b.Role owned by
// owner, a member of b.Project: validate, reserve a slot (GrantPersonal), then
// raise — releasing the slot if the raise fails. by names who asked, for the
// log. Callers must have checked owner is a member of b.Project. It is the one path behind RequestPersonalSession and the user area's
// New session. Refusals are *PersonalSessionError.
func StartPersonalSession(ctx context.Context, store Store, sup *Supervisor, alloc SessionAllocator, log *slog.Logger, owner Member, by string, b PersonalSessionBody) (PersonalSessionResult, error) {
	refuse := func(status int, format string, a ...any) (PersonalSessionResult, error) {
		return PersonalSessionResult{}, &PersonalSessionError{Status: status, Msg: fmt.Sprintf(format, a...)}
	}
	if sup == nil || alloc == nil {
		return refuse(http.StatusServiceUnavailable, "runtime supervisor or allocator not configured")
	}
	if b.Role == "" {
		return refuse(http.StatusBadRequest, "role is required")
	}
	placement, err := ParsePlacement(b.Managers, b.PlacementTimeout)
	if err != nil {
		return refuse(http.StatusBadRequest, "%s", err.Error())
	}
	project := orDefaultProject(b.Project)
	if _, ok := store.GetRole(project, b.Role); !ok {
		return refuse(http.StatusBadRequest, "role %s/%s does not exist", project, b.Role)
	}
	id := string(ident.New(ident.Session)) // each request starts a new session
	granted, err := alloc.GrantPersonal(ctx, project, b.Role, id, string(owner.User.ID))
	switch {
	case errors.Is(err, ErrNeedsLedger):
		return refuse(http.StatusConflict, "%s", ErrNeedsLedger.Error())
	case err != nil:
		log.Warn("personal session grant failed", "by", by, "project", project, "role", b.Role, "owner", owner.User.Name, "err", err.Error())
		return refuse(http.StatusBadGateway, "allocation failed: %s", err.Error())
	case !granted:
		return refuse(http.StatusConflict, "at capacity: no personal session of %s/%s available for %s", project, b.Role, owner.User.Name)
	}
	name, release := reservePersonalName(store, b.Role)
	defer release()
	inst, _, _, err := sup.Raise(ctx, RaiseSpec{
		ActorID: id, Name: name, Project: project, Role: b.Role, Prompt: b.Prompt,
		Owner: owner.User.Name, OwnerID: owner.User.ID, SessionKind: SessionKindPersonal, Placement: placement,
	})
	if err != nil {
		// Grant, then raise, then compensate: free the reserved slot.
		if rerr := alloc.RecordRelease(context.WithoutCancel(ctx), project, b.Role, id); rerr != nil {
			log.Warn("personal session: compensating release failed", "id", id, "err", rerr.Error())
		}
		log.Warn("personal session raise failed", "by", by, "id", id, "err", err.Error())
		return refuse(http.StatusBadGateway, "raise failed: %s", err.Error())
	}
	log.Info("personal session raised", "by", by, "id", id, "owner", owner.User.Name, "project", project, "role", b.Role)
	return PersonalSessionResult{ID: id, Name: name, Owner: owner.User.Name, Project: project, Role: b.Role, Phase: string(inst.Phase)}, nil
}

// personalName is a personal session's n-th candidate name: <role>-01, -02, …
// (three digits and up past 99).
func personalName(role string, n int) string { return fmt.Sprintf("%s-%02d", role, n) }

// pendingNames are names picked for personal sessions whose raise hasn't
// finished, so two requests in flight can't pick the same one.
var pendingNames = struct {
	sync.Mutex
	m map[string]bool
}{m: map[string]bool{}}

// reservePersonalName picks the first free <role>-NN — not carried by a live
// non-standing session (sessionLabelTaken, the manual-label rule) nor reserved
// by a request in flight — and reserves it until release is called.
func reservePersonalName(store Store, role string) (name string, release func()) {
	pendingNames.Lock()
	defer pendingNames.Unlock()
	for n := 1; ; n++ {
		if name = personalName(role, n); !pendingNames.m[name] && !sessionLabelTaken(store, name) {
			break
		}
	}
	pendingNames.m[name] = true
	return name, func() {
		pendingNames.Lock()
		defer pendingNames.Unlock()
		delete(pendingNames.m, name)
	}
}

// safeIDPart maps characters outside [A-Za-z0-9._-] to '-', so s can be part of
// a pre-registry standing id (StandingActorID).
func safeIDPart(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '.', r == '-':
			return r
		default:
			return '-'
		}
	}, s)
}

// ownedBy reports whether a personal session belongs to the roster person h:
// by user id, or by name for an instance raised before owners had ids.
func ownedBy(inst Instance, m Member) bool {
	if inst.OwnerID != "" {
		return inst.OwnerID == m.User.ID
	}
	return inst.Owner != "" && inst.Owner == m.User.Name
}
