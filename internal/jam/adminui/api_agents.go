package adminui

import (
	"cmp"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/collabhttp"
	"github.com/aethons-tools/cove/internal/jam/sessionevents"
)

// The agents JSON API: the Agents tab's list and an agent's page with its
// writes and Setup — the JSON twins of agents.go, agent.go, access.go,
// writes.go's enroll/grant/studio routes and setup_page.go's studio routes.

// agentAPI is what the agents API needs beyond the frame source.
type agentAPI struct {
	alloc      jam.SessionAllocator // a manual raise takes its grant here
	sup        *jam.Supervisor
	msgs       SquawkReader
	sess       sessionevents.Store
	hub        *sessionevents.Hub
	acc        accessDeps
	setup      *jam.SetupDeps
	credExists func(string) bool
}

func timeOr(t time.Time, layout string) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(layout)
}

func accessCardOf(c accessCard) AccessCard {
	out := AccessCard{Enabled: c.Enabled, Shares: []ShareRow{}, Live: []LiveRow{}, Previews: []PreviewRow{}}
	if !c.Enabled {
		return out
	}
	v := c.View
	if v.Requester != nil {
		out.Requester = &UserRefRow{ID: string(v.Requester.ID), Name: v.Requester.Name}
	}
	out.MembersPreview, out.MembersSSH, out.RoleProject, out.RoleName = v.Role.Preview, v.Role.SSH, v.Role.Project, v.Role.Role
	for _, g := range v.Grants {
		out.Shares = append(out.Shares, ShareRow{UserID: string(g.UserID), User: g.User, Rights: append([]string{}, g.Rights...),
			ExpiresAt: g.ExpiresAt.Format("2006-01-02 15:04 MST"), Expired: g.Expired, GrantedBy: g.GrantedByName})
	}
	for _, l := range v.Live {
		out.Live = append(out.Live, LiveRow{Name: l.Name, Right: l.Right, Since: l.Since.Format("Jan 2 15:04")})
	}
	out.PreviewsOn = v.PreviewsOn
	for _, p := range v.Previews {
		out.Previews = append(out.Previews, PreviewRow{Name: p.Name, Port: p.Port, URL: p.URL})
	}
	return out
}

func agentDetailOf(store jam.Store, a agentAPI, id string) (AgentDetail, bool) {
	d, ok := buildAgentDetail(store, a.msgs, a.sess, a.acc, id, a.sup != nil)
	if !ok {
		return AgentDetail{}, false
	}
	in := d.Inst
	out := AgentDetail{ID: id, Running: d.Running, Kind: d.Kind, HasActor: d.HasActor, Grants: []GrantRow{}, Access: accessCardOf(d.Access),
		SessionsEnabled: d.SessionsEnabled, Streams: []StreamRow{}, SquawksConfigured: d.SquawksConfigured, Squawks: []SquawkRow{}, SquawksMore: d.SquawksMore,
		CanEdit: d.CanEdit, SetupEnabled: a.setup != nil && d.Running && in.Phase != jam.PhaseGone}
	if d.Running {
		out.Status = statusText(string(in.Phase), string(in.Activity), true)
		out.Owner, out.OwnerID, out.Name, out.Project, out.Role, out.Unit = in.Owner, string(in.OwnerID), in.Name, in.Project, in.Role, in.Unit
		out.Raised, out.LastSeen = timeOr(in.RaisedAt, "Jan 2 15:04"), timeOr(in.LastSeen, "Jan 2 15:04:05")
		out.Lease, out.Backend, out.Location = in.Lease.Holder, in.Backend, in.Location
		out.WaitingSince, out.WaitSeq = timeOr(in.WaitingSince, "Jan 2 15:04"), in.WaitSeq
		out.EscalationTier, out.TierPingedAt, out.EscalationCategory = in.EscalationTier, timeOr(in.TierPingedAt, "Jan 2 15:04"), in.EscalationCategory
		out.Nags, out.LastNagAt, out.CommitSeq = in.Nags, timeOr(in.LastNagAt, "Jan 2 15:04"), in.CommitSeq
		out.EgressID, out.EgressKnown, out.EgressFailures = d.EgressID, in.Egress != "", in.EgressFailures
	}
	if d.HasActor {
		for _, g := range d.Actor.Grants {
			out.Grants = append(out.Grants, GrantRow{Project: g.Project, Role: g.Role, Destinations: append([]string{}, g.Destinations...)})
		}
		out.TokenExpiry = timeOr(d.Actor.Expiry, "2006-01-02")
	}
	for _, s := range d.Streams {
		out.Streams = append(out.Streams, StreamRow{StreamID: s.StreamID, Started: s.FirstAt.Format("Jan 2 15:04"), LastEvent: s.LastAt.Format("Jan 2 15:04:05"), Events: s.Events})
	}
	for _, r := range d.Squawks {
		out.Squawks = append(out.Squawks, SquawkRow{At: r.At, From: r.From, FromID: r.FromID, AgentID: r.AgentID, Channel: r.Channel, ChannelID: r.ChannelID,
			Project: r.Project, Body: string(r.Body), Plain: r.Plain})
	}
	return out, true
}

func apiAgents(api *http.ServeMux, src frameSource, a agentAPI, log *slog.Logger) {
	store := src.store
	canEdit := a.sup != nil
	noSup := func(w http.ResponseWriter) bool {
		if canEdit {
			return false
		}
		writeAPIError(w, http.StatusServiceUnavailable, "internal", "runtime supervisor not configured")
		return true
	}
	detail := func(w http.ResponseWriter, id string) {
		d, ok := agentDetailOf(store, a, id)
		if !ok {
			writeAPIError(w, http.StatusNotFound, "not_found", "nothing is known about agent "+id)
			return
		}
		writeJSON(w, http.StatusOK, d)
	}
	api.HandleFunc("GET "+apiPrefix+"agents", func(w http.ResponseWriter, r *http.Request) {
		data := newAgentsData(store, src.img, statusFilter(r), canEdit)
		items := src.items()
		out := AgentsResponse{Agents: []AgentListRow{}, Filters: []KV{}, Status: data.Status, CanEdit: canEdit}
		for _, f := range agentFilters {
			out.Filters = append(out.Filters, KV{Key: f.Key, Value: f.Label})
		}
		for _, x := range data.Agents {
			row := AgentListRow{ID: x.ID, Name: x.Name, Kind: x.Kind, Project: x.Project, Role: x.Role, Unit: x.Unit, Status: string(x.Status),
				Connector: x.Connector, Image: x.Image, LastSeen: timeOr(x.LastSeen, "Jan 2 15:04:05"), HasStudio: x.HasStudio, Attn: attnFlag(items, "agents", x.ID)}
			if len(x.Grants) > 1 {
				row.MoreGrants = len(x.Grants) - 1
			}
			out.Agents = append(out.Agents, row)
		}
		writeJSON(w, http.StatusOK, out)
	})
	api.HandleFunc("POST "+apiPrefix+"agents", func(w http.ResponseWriter, r *http.Request) {
		if noSup(w) {
			return
		}
		var b struct{ ID, Role, Project, Unit, Prompt string }
		if !decodeJSON(w, r, &b) {
			return
		}
		label, project, role := strings.TrimSpace(b.ID), strings.TrimSpace(b.Project), strings.TrimSpace(b.Role)
		sid, err := raiseManual(r.Context(), store, a.sup, a.alloc, label, project, role, strings.TrimSpace(b.Unit), b.Prompt)
		if err != nil {
			apiFail(w, err, http.StatusBadRequest)
			return
		}
		log.Info("ui cove raised", "operator", jam.OperatorID(r), "id", sid, "label", label, "project", orDefaultProject(project), "role", role)
		writeJSON(w, http.StatusCreated, Created{ID: sid, Path: "agents/" + url.PathEscape(sid)})
	})
	api.HandleFunc("DELETE "+apiPrefix+"agents/{id}/studio", func(w http.ResponseWriter, r *http.Request) {
		if noSup(w) {
			return
		}
		id := jam.ResolveSession(store, r.PathValue("id"))
		if err := a.sup.Teardown(r.Context(), id); err != nil {
			writeAPIError(w, http.StatusInternalServerError, "internal", err.Error())
			return
		}
		log.Info("ui cove torn down", "operator", jam.OperatorID(r), "id", id)
		w.WriteHeader(http.StatusNoContent)
	})
	api.HandleFunc("POST "+apiPrefix+"enrollments", func(w http.ResponseWriter, r *http.Request) {
		var b struct{ ID, Role, Project, Destinations string }
		if !decodeJSON(w, r, &b) {
			return
		}
		id, project, role := strings.TrimSpace(b.ID), strings.TrimSpace(b.Project), strings.TrimSpace(b.Role)
		token, err := enrollActor(store, a.credExists, id, project, role, b.Destinations)
		if err != nil {
			apiFail(w, err, http.StatusBadRequest)
			return
		}
		log.Info("ui enrolled", "operator", jam.OperatorID(r), "id", id, "project", project, "role", role)
		writeJSON(w, http.StatusOK, Enrolled{ID: id, Token: token})
	})
	api.HandleFunc("DELETE "+apiPrefix+"enrollments/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if err := store.RemoveActor(id); err != nil {
			writeAPIError(w, http.StatusNotFound, "not_found", err.Error())
			return
		}
		log.Info("ui revoked", "operator", jam.OperatorID(r), "id", id)
		w.WriteHeader(http.StatusNoContent)
	})
	api.HandleFunc("POST "+apiPrefix+"agents/{id}/grants", func(w http.ResponseWriter, r *http.Request) {
		var b struct{ Role, Project, Destinations string }
		if !decodeJSON(w, r, &b) {
			return
		}
		id, project, role := r.PathValue("id"), strings.TrimSpace(b.Project), strings.TrimSpace(b.Role)
		if err := addActorGrant(store, a.credExists, id, project, role, b.Destinations); err != nil {
			apiFail(w, err, http.StatusBadRequest)
			return
		}
		log.Info("ui grant added", "operator", jam.OperatorID(r), "id", id, "project", orDefaultProject(project), "role", role)
		detail(w, id)
	})
	api.HandleFunc("DELETE "+apiPrefix+"agents/{id}/grants/{project}/{role}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if err := store.RemoveGrant(id, r.PathValue("project"), r.PathValue("role")); err != nil {
			writeAPIError(w, http.StatusNotFound, "not_found", err.Error())
			return
		}
		log.Info("ui grant removed", "operator", jam.OperatorID(r), "id", id, "project", r.PathValue("project"), "role", r.PathValue("role"))
		detail(w, id)
	})
	if a.acc.live != nil {
		api.HandleFunc("DELETE "+apiPrefix+"agents/{id}/shares/{user}", func(w http.ResponseWriter, r *http.Request) {
			if err := unshareStudio(store, a.acc.live, log, cmp.Or(jam.OperatorID(r), "local"), r.PathValue("id"), r.PathValue("user")); err != nil {
				apiFail(w, err, http.StatusInternalServerError)
				return
			}
			detail(w, r.PathValue("id"))
		})
	}
	api.HandleFunc("GET "+apiPrefix+"agents/{id}", func(w http.ResponseWriter, r *http.Request) { detail(w, r.PathValue("id")) })
	api.HandleFunc("GET "+apiPrefix+"agents/{id}/setup", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		id := r.PathValue("id")
		if a.setup == nil {
			writeAPIError(w, http.StatusNotFound, "not_found", "no setup for this agent")
			return
		}
		sections, diff, err := setupQuery(r.URL.Query(), true)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "bad_request", jam.SetupDisplayText(err.Error()))
			return
		}
		s, err := jam.InstanceSetup(r.Context(), *a.setup, id, sections, diff)
		if err != nil {
			setupFailJSON(w, log, err, "studio "+id, "studio "+id+" not found")
			return
		}
		auditSetup(log, r, s.Subject, sections, diff)
		writeJSON(w, http.StatusOK, setupResponseOf(newSetupView(s, agentURL(id)+"/setup/sections"), "agents/"+url.PathEscape(id)+"/setup/sections"))
	})
	api.HandleFunc("GET "+apiPrefix+"agents/{id}/setup/sections/{section}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		id, name := r.PathValue("id"), r.PathValue("section")
		if a.setup == nil {
			writeAPIError(w, http.StatusNotFound, "not_found", "no setup for this agent")
			return
		}
		s, err := jam.InstanceSetup(r.Context(), *a.setup, id, []string{name}, true)
		if err != nil {
			setupFailJSON(w, log, err, "studio "+id, "studio "+id+" not found")
			return
		}
		auditSetup(log, r, s.Subject, []string{name}, true)
		v := newSetupView(s, "")
		sec := v.Sections[0]  // InstanceSetup validated name: exactly one section
		sec.DiffNote = v.Note // a role gone: why there is no diff
		writeJSON(w, http.StatusOK, setupSectionOf(sec, ""))
	})
}

// unshareStudio removes user's share grant on studio id (grantor the
// operator), closing their SSH connections when it was their way in.
func unshareStudio(store jam.Store, live *collabhttp.Live, log *slog.Logger, operator, id, user string) error {
	uid, err := ident.Parse(user)
	if err != nil || uid.Kind() != ident.User {
		return &jam.WriteError{Status: http.StatusNotFound, Msg: "no such user"}
	}
	g, found := collabhttp.GrantOf(store, id, uid)
	if !found {
		return &jam.WriteError{Status: http.StatusNotFound, Msg: "no such share on this studio"}
	}
	removed, err := collabhttp.Unshare(store, live, log, time.Now(), "op:"+operator, g)
	switch {
	case err != nil:
		return &jam.WriteError{Status: http.StatusInternalServerError, Msg: "could not remove the grant"}
	case !removed:
		return &jam.WriteError{Status: http.StatusConflict, Msg: "the grant changed while being removed; reload and retry"}
	}
	return nil
}
