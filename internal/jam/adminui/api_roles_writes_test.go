package adminui_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
	"github.com/aethons-tools/cove/internal/jam/appui"
	"github.com/aethons-tools/cove/internal/studio"
)

// credOK accepts only the credential "known-cred".
func credOK(name string) bool { return name == "known-cred" }

// A new role stores its destinations, their credential mapping, TTL, kit and
// model-spec; a missing kit or model-spec, a bad TTL, an unknown or empty
// credential are refused — without echoing the credential name.
func TestRolesAPICreateFields(t *testing.T) {
	store := newStore(t)
	mustCreateProject(t, store, "acme")
	if _, _, err := jam.PushStudioKit(store, "web", "kind: studio\n"); err != nil {
		t.Fatal(err)
	}
	if err := store.PutModelSpec(jam.ModelSpec{Name: "opus", Type: jam.HarnessClaude, Version: "2.1.0",
		Principal: jam.ModelPrincipal{Credential: "c"}, Claude: &jam.ClaudeSpec{Provider: "anthropic"}}); err != nil {
		t.Fatal(err)
	}
	e := editOn(t, editSetup{seed: store, cred: func(n string) bool { return credOK(n) || n == "c" }})
	h := e.H
	const path = "/app/admin/api/projects/acme/roles"
	if rec := apiDo(t, h, "POST", path, map[string]string{"name": " w ", "destinations": "git=known-cred,anthropic", "ttlSeconds": "3600", "kit": "web", "modelSpec": "opus"}, nil); rec.Code != 201 {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	r, ok := e.Store().GetRole("acme", "w")
	if !ok || r.Scope.Credentials["git"] != "known-cred" || strings.Join(r.Scope.Destinations, ",") != "git,anthropic" || r.Scope.TTL != time.Hour || r.Kit != "web" || r.ModelSpec != "opus" {
		t.Fatalf("stored role = %+v", r)
	}
	for _, tc := range []struct {
		body map[string]string
		want string
	}{
		{map[string]string{"name": ""}, "name is required"},
		{map[string]string{"name": "x", "kit": "ghost"}, "kit does not exist"},
		{map[string]string{"name": "x", "modelSpec": "ghost"}, "model-spec"},
		{map[string]string{"name": "x", "ttlSeconds": "soon"}, "ttl-seconds must be an integer"},
		{map[string]string{"name": "x", "destinations": "git=SECRET-TYPO"}, ""},
		{map[string]string{"name": "x", "destinations": "git="}, ""},
	} {
		rec := apiDo(t, h, "POST", path, tc.body, nil)
		wantErr(t, rec, 400, "bad_request")
		if !strings.Contains(rec.Body.String(), tc.want) || strings.Contains(rec.Body.String(), "SECRET-TYPO") {
			t.Errorf("%v = %s, want %q (and no credential name)", tc.body, rec.Body, tc.want)
		}
	}
	if _, ok := e.Store().GetRole("acme", "x"); ok {
		t.Error("a refused create stored the role")
	}
	// Credential names are references, not secrets: the agents view shows the mapping.
	var pa adminui.ProjectAgents // live state: read over the branch's config
	apiDo(t, uiHandler(t, e.Store()), "GET", "/app/admin/api/projects/acme/agents", nil, &pa)
	if !strings.Contains(fmt.Sprintf("%+v", pa.Roles), "known-cred") {
		t.Errorf("roles = %+v, want the credential mapping", pa.Roles)
	}
}

// grantingAlloc is a jam.SessionAllocator that always grants and records grants.
type grantingAlloc struct{ grants []string }

func (a *grantingAlloc) GrantPersonal(_ context.Context, project, role, id, owner string) (bool, error) {
	a.grants = append(a.grants, project+"/"+role+"/"+owner)
	return true, nil
}
func (a *grantingAlloc) RecordRelease(context.Context, string, string, string) error { return nil }

// promptLauncher records the prompt of each raise.
type promptLauncher struct {
	fakeLauncher
	prompts []string
}

func (l *promptLauncher) Raise(ctx context.Context, spec jam.RaiseSpec, c jam.LaunchCreds) (string, error) {
	l.prompts = append(l.prompts, spec.Prompt)
	return l.fakeLauncher.Raise(ctx, spec, c)
}

// requestFixture is an acme/pair role, a discord chat service, and roster
// human alice linked to login auth0|alice.
func requestFixture(t *testing.T) (jam.Store, *promptLauncher, *grantingAlloc, http.Handler) {
	t.Helper()
	store := newStore(t)
	mustCreateProject(t, store, "acme")
	if err := store.PutRole("acme", jam.Role{Name: "pair"}); err != nil {
		t.Fatal(err)
	}
	if err := jam.AddPerson(store, "acme", jam.Human{Name: "alice", Handle: "@alice", Login: "auth0|alice",
		Delivery: []jam.DeliveryProfile{{Service: "discord", Address: "111"}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetChatService("acme", "discord"); err != nil {
		t.Fatal(err)
	}
	l := &promptLauncher{}
	sup := jam.NewSupervisor(store, l, "test-holder", time.Minute, 30*time.Second, nil, testLogger())
	a := &grantingAlloc{}
	return store, l, a, adminui.Handler(store, testLogger(), sup, a, anyCred, nil)
}

func requestAs(t *testing.T, h http.Handler, login string) *httptest.ResponseRecorder {
	t.Helper()
	p := appui.Principal{Admin: true, Operator: jam.Operator{ID: login}}
	return apiDoAs(t, h, p, "POST", "/app/admin/api/projects/acme/roles/pair/request", map[string]string{}, nil)
}

// A signed-in member's Request raises a personal session of the role owned
// by them, prompted to squawk them, and names it.
func TestRoleRequestRaisesPersonalSession(t *testing.T) {
	store, l, a, h := requestFixture(t)
	rec := requestAs(t, h, "auth0|alice")
	if rec.Code != 200 {
		t.Fatalf("request = %d %s", rec.Code, rec.Body)
	}
	var res adminui.Requested
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	alice, _ := store.LookupName(ident.User, "alice")
	if len(a.grants) != 1 || a.grants[0] != "acme/pair/"+string(alice) {
		t.Errorf("grants = %v", a.grants)
	}
	insts := store.ListInstances()
	if len(insts) != 1 || insts[0].Owner != "alice" || insts[0].SessionKind != jam.SessionKindPersonal || insts[0].Name != "pair-01" {
		t.Fatalf("instances = %+v", insts)
	}
	if len(l.prompts) != 1 || !strings.HasSuffix(l.prompts[0], "Squawk me (user:alice) and we will get to work.") {
		t.Errorf("prompts = %q", l.prompts)
	}
	if res.ID != insts[0].ActorID || res.Name != "pair-01" || res.Message != "Requested agent pair-01 ("+insts[0].ActorID+"); it will squawk you on the intercom." {
		t.Errorf("requested = %+v", res)
	}
}

// A login linked to no member is refused, and nothing is granted or raised.
func TestRoleRequestUnlinkedLogin(t *testing.T) {
	store, _, a, h := requestFixture(t)
	rec := requestAs(t, h, "auth0|stranger")
	wantErr(t, rec, 403, "forbidden")
	if !strings.Contains(rec.Body.String(), "no member of acme is linked to your login") || len(a.grants) != 0 || len(store.ListInstances()) != 0 {
		t.Errorf("request = %s grants=%v", rec.Body, a.grants)
	}
	rec = requestAs(t, h, "local")
	wantErr(t, rec, 403, "forbidden")
	if len(a.grants) != 0 {
		t.Error("an unidentified operator was granted")
	}
	// A member's request of a role that doesn't exist carries the request's
	// own refusal.
	p := appui.Principal{Admin: true, Operator: jam.Operator{ID: "auth0|alice"}}
	rec = apiDoAs(t, h, p, "POST", "/app/admin/api/projects/acme/roles/nope/request", map[string]string{}, nil)
	if rec.Code < 400 || rec.Code >= 500 || !strings.Contains(rec.Body.String(), "nope") || len(store.ListInstances()) != 0 {
		t.Errorf("request of a missing role = %d %s", rec.Code, rec.Body)
	}
}

// A standing reset tears the studio down and keeps the declaration.
func TestRoleStandingReset(t *testing.T) {
	store := seedRichRole(t)
	sup := newSup(t, store)
	sup.SetStandingResetter(&teardownResetter{sup: sup})
	id := jam.SeedStandingSession(store, "acme", "review", "nightly")
	if _, _, _, err := sup.Raise(context.Background(), jam.RaiseSpec{ActorID: id, Project: "acme", Role: "review", Name: "nightly", SessionKind: jam.SessionKindStanding}); err != nil {
		t.Fatal(err)
	}
	h := adminui.Handler(store, testLogger(), sup, nil, credKnown, nil)
	var res adminui.RoleResult
	if rec := apiDo(t, h, "POST", "/app/admin/api/projects/acme/roles/review/standing/nightly/reset", nil, &res); rec.Code != 200 {
		t.Fatalf("reset = %d %s", rec.Code, rec.Body)
	}
	if res.Message != "reset standing agent nightly; Jam raises it fresh" {
		t.Errorf("message = %q", res.Message)
	}
	if _, ok := store.GetInstance(id); ok {
		t.Error("reset must tear the studio down")
	}
	if r, _ := store.GetRole("acme", "review"); len(r.Allocation.Standing) != 1 {
		t.Errorf("reset must keep the declaration: %+v", r.Allocation.Standing)
	}
}

// taggingLauncher names images like the real launcher (kit digest + a Jam-side
// fingerprint), so a test can stale every raised image by changing asm.
type taggingLauncher struct {
	fakeLauncher
	asm *string
}

func (l taggingLauncher) ImageTag(ref jam.KitRef) string {
	return "jam-kit:" + ref.Digest + "-" + *l.asm
}

// queueUpgrader is a jam.StandingUpgrader queue: QueueUpgrade records the
// name as queued (or fails with err); UpgradeState reports it.
type queueUpgrader struct {
	state map[string]string
	err   error
}

func (q *queueUpgrader) QueueUpgrade(_, _, name string, _ bool) error {
	if q.err != nil {
		return q.err
	}
	q.state[name] = jam.UpgradeQueued
	return nil
}

func (q *queueUpgrader) UpgradeState(_, _, name string) string { return q.state[name] }

// An upgrade of a current studio restarts nothing; of a stale one it is
// queued and the standing row shows it; during a pending reset it is a 409.
func TestRoleStandingUpgrade(t *testing.T) {
	store := newStore(t)
	mustCreateProject(t, store, "acme")
	if _, err := jam.EnsureStudioKit(store, "web", studio.StudioKit{Kind: studio.Kind}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRole("acme", jam.Role{Name: "review", Kit: "web", Allocation: jam.RoleAllocation{
		Standing: []jam.StandingSession{{Name: "nightly", Prompt: "sweep"}}}}); err != nil {
		t.Fatal(err)
	}
	asm := "a1"
	sup := jam.NewSupervisor(store, taggingLauncher{asm: &asm}, "test-holder", time.Minute, 30*time.Second, time.Now,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	id := jam.SeedStandingSession(store, "acme", "review", "nightly")
	if _, _, _, err := sup.Raise(context.Background(), jam.RaiseSpec{ActorID: id, Project: "acme", Role: "review", Name: "nightly", SessionKind: "standing"}); err != nil {
		t.Fatal(err)
	}
	q := &queueUpgrader{state: map[string]string{}}
	sup.SetStandingUpgrader(q)
	h := adminui.Handler(store, testLogger(), sup, nil, anyCred, nil)
	const path = "/app/admin/api/projects/acme/roles/review/standing/nightly/upgrade"

	var res adminui.RoleResult
	if rec := apiDo(t, h, "POST", path, nil, &res); rec.Code != 200 ||
		res.Message != "standing agent nightly already runs the current image; nothing restarted" || len(q.state) != 0 {
		t.Fatalf("already current = %d %q %v", rec.Code, res.Message, q.state)
	}
	asm = "a2" // a Jam-side rebuild: the raised image is stale
	if rec := apiDo(t, h, "POST", path, nil, &res); rec.Code != 200 ||
		res.Message != "upgrade of standing agent nightly queued; Jam restarts it once the image is ready and the agent is idle" {
		t.Fatalf("upgrade = %d %q", rec.Code, res.Message)
	}
	if len(res.Role.Standing) != 1 || res.Role.Standing[0].Upgrade != jam.UpgradeQueued {
		t.Errorf("standing row = %+v", res.Role.Standing)
	}
	q.err = fmt.Errorf("%w: x", jam.ErrStandingResetPending)
	wantErr(t, apiDo(t, h, "POST", path, nil, nil), 409, "conflict")

	// Without a supervisor neither write can run.
	ro := adminui.Handler(store, testLogger(), nil, nil, anyCred, nil)
	for _, p := range []string{path, strings.Replace(path, "upgrade", "reset", 1)} {
		wantErr(t, apiDo(t, ro, "POST", p, nil, nil), 503, "internal")
	}
}
