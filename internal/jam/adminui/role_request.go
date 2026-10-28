package adminui

import (
	"context"
	"fmt"
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
