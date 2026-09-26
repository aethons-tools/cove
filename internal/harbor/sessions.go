package harbor

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
// (mirrors allocator.SessionPersonal; harbor does not import allocator).
const SessionKindPersonal = "personal"

// ErrNeedsLedger is returned (possibly wrapped) by a SessionAllocator when
// personal sessions cannot be admitted because harbor has no allocation ledger
// (it runs on the file store, not store-postgres).
var ErrNeedsLedger = errors.New("personal sessions need the allocation ledger (store-postgres)")

// SessionAllocator is the capacity authority the personal-session routes admit
// against: GrantPersonal reserves a slot for owner's personal session of
// (project, role) against the role's pool and per-owner caps; RecordRelease
// frees it. harbor does not import allocator, so cmd/at-harbor adapts
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
		if sup == nil || alloc == nil {
			http.Error(w, "runtime supervisor or allocator not configured", http.StatusServiceUnavailable)
			return
		}
		var b PersonalSessionBody
		if !decode(w, r, &b) {
			return
		}
		if b.Role == "" {
			http.Error(w, "role is required", http.StatusBadRequest)
			return
		}
		project := orDefaultProject(b.Project)
		human, ok := HumanByLogin(store, project, OperatorID(r))
		if !ok {
			http.Error(w, fmt.Sprintf("no roster human in %s is linked to your login", project), http.StatusForbidden)
			return
		}
		if _, ok := store.GetRole(project, b.Role); !ok {
			http.Error(w, fmt.Sprintf("role %s/%s does not exist", project, b.Role), http.StatusBadRequest)
			return
		}
		// A personal session talks to its owner over the intercom, and a
		// ticketless cove's messages can only be delivered via Discord (the
		// Linear fallback needs a ticket): fail now, before any grant, rather
		// than silently later.
		if msg := personalDeliveryProblem(store, project, human); msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		id, err := personalSessionID(human.Name)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		granted, err := alloc.GrantPersonal(r.Context(), project, b.Role, id, human.Name)
		switch {
		case errors.Is(err, ErrNeedsLedger):
			http.Error(w, ErrNeedsLedger.Error(), http.StatusConflict)
			return
		case err != nil:
			log.Warn("personal session grant failed", "operator", OperatorID(r), "project", project, "role", b.Role, "owner", human.Name, "err", err.Error())
			http.Error(w, "allocation failed: "+err.Error(), http.StatusBadGateway)
			return
		case !granted:
			http.Error(w, fmt.Sprintf("at capacity: no personal session of %s/%s available for %s", project, b.Role, human.Name), http.StatusConflict)
			return
		}
		inst, _, _, err := sup.Raise(r.Context(), RaiseSpec{
			ActorID: id, Project: project, Role: b.Role, Prompt: personalPrompt(human.Name, b.Prompt),
			Owner: human.Name, SessionKind: SessionKindPersonal,
		})
		if err != nil {
			// Grant, then raise, then compensate: free the reserved slot.
			if rerr := alloc.RecordRelease(context.WithoutCancel(r.Context()), project, b.Role, id); rerr != nil {
				log.Warn("personal session: compensating release failed", "id", id, "err", rerr.Error())
			}
			log.Warn("personal session raise failed", "operator", OperatorID(r), "id", id, "err", err.Error())
			http.Error(w, "raise failed: "+err.Error(), http.StatusBadGateway)
			return
		}
		log.Info("admin personal session raised", "operator", OperatorID(r), "id", id, "owner", human.Name, "project", project, "role", b.Role)
		writeJSON(w, http.StatusCreated, PersonalSessionResult{ID: id, Owner: human.Name, Project: project, Role: b.Role, Phase: string(inst.Phase)})
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
			if i.SessionKind != SessionKindPersonal || i.Project != project || i.Owner != human.Name {
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
		if !ok || human.Name != inst.Owner {
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

// personalDeliveryProblem returns why the owner of a personal session in
// project could not be messaged — the project's chat service isn't discord, or
// the owner has no discord delivery profile — with the command that fixes it;
// "" when delivery is possible.
func personalDeliveryProblem(store Store, project string, owner Human) string {
	p, _ := store.GetProject(project)
	if p.ChatService != "discord" {
		return fmt.Sprintf("personal sessions need project %s's chat service set to discord (at-harbor project chat-service set --project %s --service discord)", project, project)
	}
	if _, ok := owner.DeliveryFor("discord"); !ok {
		return fmt.Sprintf("%s has no discord delivery profile in project %s (at-harbor project roster add-human %s --name %s --handle %s --login %s --delivery discord:<inbox-channel>)",
			owner.Name, project, project, owner.Name, owner.Handle, owner.Login)
	}
	return ""
}

// personalPrompt prefixes the owner's prompt with a preamble telling the agent
// how a personal session works: it reports to its owner over the intercom and
// is resumed with their reply, until they release it.
func personalPrompt(owner, prompt string) string {
	return fmt.Sprintf("You are a personal session for %[1]s. Work on the request below. When you have results or need\n"+
		"input, message %[1]s with the intercom `send` tool (omit `to`); they will reply, and you will\n"+
		"be resumed with their reply available via `read`. This session stays open until %[1]s releases it.\n"+
		"---\n%[2]s", owner, prompt)
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
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '.', r == '-':
			return r
		default:
			return '-'
		}
	}, owner)
	return "personal-" + safe + "-" + hex.EncodeToString(b[:]), nil
}
