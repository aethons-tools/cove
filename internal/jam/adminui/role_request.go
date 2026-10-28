package adminui

import (
	"context"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"

	"github.com/aethons-tools/cove/internal/jam"
)

// requestPrompt is the prompt a role Request raises the operator's personal
// session with: it asks the agent to open the conversation with its owner.
func requestPrompt(owner string) string {
	return fmt.Sprintf("Squawk me (user:%s) and we will get to work.", owner)
}

// requestRole raises a personal session of project/role for login, who must
// be linked to a member of the project; refusals carry their status.
func requestRole(ctx context.Context, store jam.Store, sup *jam.Supervisor, alloc jam.SessionAllocator, log *slog.Logger, login, project, role string) (jam.PersonalSessionResult, error) {
	human, ok := jam.MemberByLogin(store, project, login)
	if !ok {
		msg := fmt.Sprintf("no member of %s is linked to your login", project)
		if login == "local" {
			msg = "sign in to request an agent (/app/auth/login): Jam needs to know which member you are"
		}
		return jam.PersonalSessionResult{}, &jam.WriteError{Status: http.StatusForbidden, Msg: msg}
	}
	res, err := jam.RequestPersonalSession(ctx, store, sup, alloc, log, login,
		jam.PersonalSessionBody{Project: project, Role: role, Prompt: requestPrompt(human.User.Name)})
	if err != nil {
		return res, &jam.WriteError{Status: jam.PersonalSessionStatus(err), Msg: err.Error()}
	}
	return res, nil
}

// registerRoleRequest mounts the roles screen's Request action: raise a
// personal session of the role for the operator, who must be signed in (a
// loopback request is anonymous "local") and linked to a roster human in the
// role's project. It shares jam.RequestPersonalSession with POST
// /admin/sessions/personal, so capacity, delivery checks, and rollback match.
func registerRoleRequest(mux *http.ServeMux, store jam.Store, log *slog.Logger, sup *jam.Supervisor, alloc jam.SessionAllocator, guardWrite func(http.ResponseWriter, *http.Request) bool) {
	mux.HandleFunc("POST /app/admin/roles/{project}/{name}/request", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		res, err := requestRole(r.Context(), store, sup, alloc, log, jam.OperatorID(r), r.PathValue("project"), r.PathValue("name"))
		if err != nil {
			renderError(w, jam.WriteStatus(err, http.StatusInternalServerError), err.Error())
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<p class="ok">Requested agent ` + template.HTMLEscapeString(res.Name) + ` (` + template.HTMLEscapeString(res.ID) + `)` +
			`; it will squawk you on the intercom.</p>`))
	})
}
