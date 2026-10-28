package userui

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
)

// fakeStarter raises a personal session as the supervisor would: an
// instance owned by owner in the store (or refuses with err).
type fakeStarter struct {
	st     *jam.MemStore
	err    error
	bodies []jam.PersonalSessionBody
}

func (f *fakeStarter) StartPersonal(_ context.Context, owner jam.Member, _ string, b jam.PersonalSessionBody) (jam.PersonalSessionResult, error) {
	f.bodies = append(f.bodies, b)
	if f.err != nil {
		return jam.PersonalSessionResult{}, f.err
	}
	id := string(ident.New(ident.Session))
	inst := jam.Instance{ActorID: id, Name: b.Role + "-01", Project: b.Project, Role: b.Role, Phase: jam.PhaseRaising,
		SessionKind: jam.SessionKindPersonal, Owner: owner.User.Name, OwnerID: owner.User.ID}
	if err := f.st.PutInstance(inst); err != nil {
		return jam.PersonalSessionResult{}, err
	}
	return jam.PersonalSessionResult{ID: id, Name: inst.Name, Owner: owner.User.Name, Project: b.Project, Role: b.Role}, nil
}

// newSessionEnv is the fixture with roles pair (2 personal, 1 each) and
// ops (none personal), and a fake starter.
func newSessionEnv(t *testing.T) (*env, jam.Participant, *fakeStarter) {
	t.Helper()
	e, p := fixture()
	proj, _ := e.st.GetProject("proj")
	for _, r := range []jam.Role{
		{Name: "pair", Allocation: jam.RoleAllocation{MaxPersonal: 2, MaxPersonalPerOwner: 1}},
		{Name: "ops"},
	} {
		if err := e.st.PutRole(string(proj.ID), r); err != nil {
			t.Fatal(err)
		}
	}
	f := &fakeStarter{st: e.st}
	e.Personal = f
	e.Send = jam.NewParticipantSendHandler(e.st, e.Intercom, nil)
	return e, p, f
}

func sessionRolesOf(t *testing.T, h http.Handler, p jam.Participant) []SessionRole {
	t.Helper()
	rec := apiGet(t, h, &p, "/app/user/api/session-roles")
	if rec.Code != 200 {
		t.Fatalf("session-roles = %d %s", rec.Code, rec.Body)
	}
	var got SessionRolesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	return got.Roles
}

func TestAPINewSession(t *testing.T) {
	e, p, f := newSessionEnv(t)
	h := Handler(e.Deps, nil)
	if b := bootOf(t, h, p); !b.NewSession {
		t.Error("bootstrap.newSession = false with a starter")
	}
	// Only the role that takes personal sessions is offered.
	if got := sessionRolesOf(t, h, p); len(got) != 1 || got[0] != (SessionRole{Project: "proj", Role: "pair"}) {
		t.Fatalf("roles = %+v, want proj/pair only", got)
	}

	rec := apiPost(t, h, p, "/app/user/api/sessions", `{"project":"proj","role":"pair","message":"fix the build"}`)
	if rec.Code != 201 {
		t.Fatalf("start = %d %s", rec.Code, rec.Body)
	}
	var started SessionStarted
	if err := json.Unmarshal(rec.Body.Bytes(), &started); err != nil || started.ID == "" || started.Channel == "" || started.Name != "pair-01" {
		t.Fatalf("started = %+v %v", started, err)
	}
	if len(f.bodies) != 1 || f.bodies[0].Project != "proj" || !strings.Contains(f.bodies[0].Prompt, "first message") {
		t.Errorf("start body = %+v", f.bodies)
	}
	// The viewer is in the session channel, and their first message is in it.
	if _, c, _ := getConv(t, e, p, started.Channel, ""); len(c.Messages) != 1 || c.Messages[0].Raw != "fix the build" || !c.Messages[0].Mine {
		t.Errorf("session conversation = %+v", c)
	}
	// Their share is now taken.
	if got := sessionRolesOf(t, h, p); len(got) != 1 || got[0].Full != "you have 1 running" {
		t.Errorf("roles after start = %+v, want pair full for alice", got)
	}

	for _, tc := range []struct{ body, want string }{
		{`{"project":"proj","role":"ops"}`, `"code":"not_found"`},
		{`{"project":"proj","role":"nope"}`, `"code":"not_found"`},
		{`{"project":"elsewhere","role":"pair"}`, `"code":"not_found"`},
	} {
		if rec := apiPost(t, h, p, "/app/user/api/sessions", tc.body); rec.Code != 404 || !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("start %s = %d %s, want 404", tc.body, rec.Code, rec.Body)
		}
	}
	// A refusal keeps its status and reason.
	f.err = &jam.PersonalSessionError{Status: 409, Msg: "at capacity: no personal session of proj/pair available for alice"}
	if rec := apiPost(t, h, p, "/app/user/api/sessions", `{"project":"proj","role":"pair"}`); rec.Code != 409 || !strings.Contains(rec.Body.String(), `"code":"conflict"`) || !strings.Contains(rec.Body.String(), "at capacity") {
		t.Errorf("refused start = %d %s", rec.Code, rec.Body)
	}
}

func TestAPINewSessionOff(t *testing.T) {
	e, p := fixture() // Deps.Personal unset
	h := Handler(e.Deps, nil)
	if b := bootOf(t, h, p); b.NewSession {
		t.Error("bootstrap.newSession = true without a starter")
	}
	if rec := apiPost(t, h, p, "/app/user/api/sessions", `{"project":"proj","role":"pair"}`); rec.Code != 503 {
		t.Errorf("start without a starter = %d %s", rec.Code, rec.Body)
	}
}

func bootOf(t *testing.T, h http.Handler, p jam.Participant) Bootstrap {
	t.Helper()
	var b Bootstrap
	if rec := apiGet(t, h, &p, "/app/user/api/bootstrap"); rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &b) != nil {
		t.Fatalf("bootstrap = %d %s", rec.Code, rec.Body)
	}
	return b
}

// A runtime failure's detail is logged, not shown to the member.
func TestAPINewSessionHidesRuntimeErrors(t *testing.T) {
	e, p, f := newSessionEnv(t)
	f.err = &jam.PersonalSessionError{Status: 502, Msg: "raise failed: dial tcp 10.0.0.7:443: refused"}
	rec := apiPost(t, Handler(e.Deps, nil), p, "/app/user/api/sessions", `{"project":"proj","role":"pair"}`)
	if rec.Code != 502 || strings.Contains(rec.Body.String(), "10.0.0.7") {
		t.Errorf("failed start = %d %s, want 502 without the runtime's detail", rec.Code, rec.Body)
	}
}
