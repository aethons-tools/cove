package userui

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/aethons-tools/cove/internal/jam"
)

func (h *handler) newSessionRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /app/user/api/session-roles", h.apiSessionRoles)
	mux.HandleFunc("POST /app/user/api/sessions", h.apiStartSession)
}

// sessionRoles are the roles the viewer may start a personal session of:
// every role with personal sessions (max-personal > 0) in each of their
// projects, marked Full when its pool or their share is taken. The counts
// are live personal sessions; the allocator's grant stays the authority.
func sessionRoles(p jam.Participant, d Deps) []SessionRole {
	out := []SessionRole{}
	insts := d.Store.ListInstances()
	for _, project := range p.Projects {
		for _, role := range d.Store.ListRoles(project) {
			a := role.Allocation
			if a.MaxPersonal <= 0 {
				continue
			}
			pool, mine := 0, 0
			for _, i := range insts {
				if i.SessionKind != jam.SessionKindPersonal || i.Phase == jam.PhaseGone || i.Role != role.Name || !jam.SameProject(d.Store, i.Project, project) {
					continue
				}
				pool++
				if i.OwnerID == p.UserID {
					mine++
				}
			}
			r := SessionRole{Project: project, Role: role.Name}
			switch {
			case pool >= a.MaxPersonal:
				r.Full = fmt.Sprintf("all %d running", a.MaxPersonal)
			case a.MaxPersonalPerOwner > 0 && mine >= a.MaxPersonalPerOwner:
				r.Full = fmt.Sprintf("you have %d running", mine)
			}
			out = append(out, r)
		}
	}
	return out
}

func (h *handler) apiSessionRoles(w http.ResponseWriter, r *http.Request) {
	p, ok := jam.ParticipantFrom(r)
	if !ok {
		writeAPIError(w, http.StatusForbidden, "forbidden", "sign in as a project member")
		return
	}
	writeJSON(w, http.StatusOK, SessionRolesResponse{Roles: sessionRoles(p, h.d)})
}

// startPrompt is a New session's first prompt: open the conversation, or
// read the owner's first message waiting in the session channel.
func startPrompt(owner string, message bool) string {
	if message {
		return fmt.Sprintf("%s started you from the Jam inbox; their first message is waiting in your inbox. Read it and get to work.", owner)
	}
	return fmt.Sprintf("Squawk me (user:%s) and we will get to work.", owner)
}

// apiStartSession starts a personal session of a role for the viewer (any
// member of the role's project, within its caps), posts their first
// message into its session channel, and says where that conversation is.
func (h *handler) apiStartSession(w http.ResponseWriter, r *http.Request) {
	var req StartSessionRequest
	if !decodeJSONMax(w, r, &req, jam.MaxSquawkBodyBytes) {
		return
	}
	p, ok := jam.ParticipantFrom(r)
	if !ok {
		writeAPIError(w, http.StatusForbidden, "forbidden", "sign in as a project member")
		return
	}
	if h.d.Personal == nil || h.d.Intercom == nil || h.d.Send == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "unavailable", "sessions can't be started here")
		return
	}
	proj, ok := h.d.Store.GetProject(req.Project)
	var owner jam.Member
	if ok {
		owner, ok = jam.MemberOf(h.d.Store, proj.ID, p.UserID)
	}
	if !ok {
		writeAPIError(w, http.StatusNotFound, "not_found", "no such project")
		return
	}
	req.Message = strings.TrimSpace(req.Message)
	if role, ok := h.d.Store.GetRole(proj.Name, req.Role); !ok || role.Allocation.MaxPersonal <= 0 {
		writeAPIError(w, http.StatusNotFound, "not_found", "no such role to start")
		return
	}
	res, err := h.d.Personal.StartPersonal(r.Context(), owner, string(p.UserID), jam.PersonalSessionBody{
		Project: proj.Name, Role: req.Role, Prompt: startPrompt(owner.User.Name, req.Message != ""),
	})
	if err != nil {
		var pe *jam.PersonalSessionError
		if !errors.As(err, &pe) {
			h.lg.Error("userui api start session failed", "user", string(p.UserID), "error", err.Error())
			writeAPIError(w, http.StatusInternalServerError, "internal", "starting the session failed")
			return
		}
		msg := pe.Msg
		if pe.Status >= 500 { // the runtime's own error: logged, not shown
			h.lg.Error("userui api start session failed", "user", string(p.UserID), "error", pe.Msg)
			msg = "the session could not be started; try again, or ask an operator"
		}
		writeAPIError(w, pe.Status, statusCode(pe.Status), msg)
		return
	}
	inst, ok := h.d.Store.GetInstance(res.ID)
	if !ok {
		writeAPIError(w, http.StatusInternalServerError, "internal", "the session ended as it started")
		return
	}
	ch, err := h.d.Intercom.HomeChannel(inst)
	if err != nil {
		h.lg.Error("userui api start session: no session channel", "session", res.ID, "error", err.Error())
		writeAPIError(w, http.StatusInternalServerError, "internal", "the session started, but its conversation could not be opened")
		return
	}
	if req.Message != "" {
		if _, err := h.d.Send.Send(p, jam.SendRequest{To: string(ch.ID), Body: req.Message}); err != nil {
			// The session is up and the conversation open: the viewer sees
			// their message is missing and sends it again.
			h.lg.Warn("userui api start session: first message not posted", "session", res.ID, "error", err.Error())
		}
	}
	writeJSON(w, http.StatusCreated, SessionStarted{ID: res.ID, Name: res.Name, Channel: string(ch.ID)})
}

// statusCode is the APIError code for a refusal's HTTP status.
func statusCode(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "bad_request"
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusConflict:
		return "conflict"
	case http.StatusServiceUnavailable, http.StatusBadGateway:
		return "unavailable"
	}
	return "internal"
}
