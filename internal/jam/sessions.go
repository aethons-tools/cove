package jam

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
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
// frees it. Jam does not import allocator, so cmd/at-jam adapts
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
}

// PersonalSessionResult is the POST /admin/sessions/personal response. Unlike
// a cove raise it never carries the identity token or launch secret.
type PersonalSessionResult struct {
	ID      string `json:"id"`
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
		human, ok := HumanByLogin(store, project, OperatorID(r))
		if !ok {
			http.Error(w, fmt.Sprintf("no roster human in %s is linked to your login", project), http.StatusForbidden)
			return
		}
		out := []PersonalSessionSummary{}
		for _, i := range store.ListInstances() {
			if i.SessionKind != SessionKindPersonal || i.Project != project || !ownedBy(i, human) {
				continue
			}
			out = append(out, PersonalSessionSummary{
				ID: i.ActorID, Owner: i.Owner, Project: i.Project, Role: i.Role,
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
		human, ok := HumanByLogin(store, inst.Project, OperatorID(r))
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
// roster Human in b.Project whose Login is login: validate, check delivery,
// reserve a slot (GrantPersonal), then raise — releasing the slot if the raise
// fails. It is the one path behind both POST /admin/sessions/personal and the
// operator UI's role Request action. Refusals are *PersonalSessionError.
func RequestPersonalSession(ctx context.Context, store Store, sup *Supervisor, alloc SessionAllocator, log *slog.Logger, login string, b PersonalSessionBody) (PersonalSessionResult, error) {
	refuse := func(status int, format string, a ...any) (PersonalSessionResult, error) {
		return PersonalSessionResult{}, &PersonalSessionError{Status: status, Msg: fmt.Sprintf(format, a...)}
	}
	if sup == nil || alloc == nil {
		return refuse(http.StatusServiceUnavailable, "runtime supervisor or allocator not configured")
	}
	if b.Role == "" {
		return refuse(http.StatusBadRequest, "role is required")
	}
	project := orDefaultProject(b.Project)
	human, ok := HumanByLogin(store, project, login)
	if !ok {
		return refuse(http.StatusForbidden, "no roster human in %s is linked to your login", project)
	}
	if _, ok := store.GetRole(project, b.Role); !ok {
		return refuse(http.StatusBadRequest, "role %s/%s does not exist", project, b.Role)
	}
	// A personal session talks to its owner over the intercom, and a
	// ticketless cove's messages can only be delivered via Discord (the
	// Linear fallback needs a ticket): fail now, before any grant, rather
	// than silently later.
	if msg := personalDeliveryProblem(store, project, human); msg != "" {
		return refuse(http.StatusBadRequest, "%s", msg)
	}
	id, err := personalSessionID(human.Name)
	if err != nil {
		return PersonalSessionResult{}, err
	}
	granted, err := alloc.GrantPersonal(ctx, project, b.Role, id, human.Name)
	switch {
	case errors.Is(err, ErrNeedsLedger):
		return refuse(http.StatusConflict, "%s", ErrNeedsLedger.Error())
	case err != nil:
		log.Warn("personal session grant failed", "operator", login, "project", project, "role", b.Role, "owner", human.Name, "err", err.Error())
		return refuse(http.StatusBadGateway, "allocation failed: %s", err.Error())
	case !granted:
		return refuse(http.StatusConflict, "at capacity: no personal session of %s/%s available for %s", project, b.Role, human.Name)
	}
	inst, _, _, err := sup.Raise(ctx, RaiseSpec{
		ActorID: id, Project: project, Role: b.Role, Prompt: b.Prompt,
		Owner: human.Name, OwnerID: human.UserID, SessionKind: SessionKindPersonal,
	})
	if err != nil {
		// Grant, then raise, then compensate: free the reserved slot.
		if rerr := alloc.RecordRelease(context.WithoutCancel(ctx), project, b.Role, id); rerr != nil {
			log.Warn("personal session: compensating release failed", "id", id, "err", rerr.Error())
		}
		log.Warn("personal session raise failed", "operator", login, "id", id, "err", err.Error())
		return refuse(http.StatusBadGateway, "raise failed: %s", err.Error())
	}
	log.Info("admin personal session raised", "operator", login, "id", id, "owner", human.Name, "project", project, "role", b.Role)
	return PersonalSessionResult{ID: id, Owner: human.Name, Project: project, Role: b.Role, Phase: string(inst.Phase)}, nil
}

// personalDeliveryProblem returns why the owner of a personal session in
// project could not be messaged — the project's chat service isn't discord, or
// the owner has no discord delivery profile — with the command that fixes it;
// "" when delivery is possible.
func personalDeliveryProblem(store Store, project string, owner Human) string {
	p, _ := store.GetProject(project)
	if ChatKind(store, p) != "discord" {
		return fmt.Sprintf("personal sessions need project %s's chat service set to discord (at-jam project chat-service set --project %s --service discord)", project, project)
	}
	if _, ok := owner.DeliveryFor("discord"); !ok {
		return fmt.Sprintf("%s has no discord delivery profile in project %s (at-jam project member add %s %s --delivery discord:<inbox-channel>)",
			owner.Name, project, project, owner.Name)
	}
	return ""
}

// personalSessionID mints a personal session's actor/reservation id:
// "personal-<owner>-<8 hex>". The id is used as an actor id and in URL paths, so
// characters outside [A-Za-z0-9._-] in the owner name become '-'; the real owner
// is recorded on the Instance.
func personalSessionID(owner string) (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "personal-" + safeIDPart(owner) + "-" + hex.EncodeToString(b[:]), nil
}

// safeIDPart maps characters outside [A-Za-z0-9._-] to '-', so s can be part of
// an actor id (used in URL paths).
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
func ownedBy(inst Instance, h Human) bool {
	if inst.OwnerID != "" {
		return inst.OwnerID == h.UserID
	}
	return inst.Owner != "" && inst.Owner == h.Name
}
