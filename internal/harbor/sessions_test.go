package harbor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeSessionAlloc is a scripted SessionAllocator: it records grants and
// releases and returns a configured grant result.
type fakeSessionAlloc struct {
	granted  bool
	grantErr error
	grants   []string // project/role/id/owner
	releases []string // project/role/id
}

func (f *fakeSessionAlloc) GrantPersonal(_ context.Context, project, role, id, owner string) (bool, error) {
	f.grants = append(f.grants, project+"/"+role+"/"+id+"/"+owner)
	return f.granted, f.grantErr
}

func (f *fakeSessionAlloc) RecordRelease(_ context.Context, project, role, id string) error {
	f.releases = append(f.releases, project+"/"+role+"/"+id)
	return nil
}

// switchOperator authenticates every request as whatever id it currently holds,
// so one test can act as several operators.
type switchOperator struct{ id *string }

func (s switchOperator) Authenticate(*http.Request) (Operator, error) {
	return Operator{ID: *s.id}, nil
}

type sessionKit struct {
	h        http.Handler
	store    Store
	launcher *fakeLauncher
	alloc    *fakeSessionAlloc
	sup      *Supervisor
	op       *string
}

// newSessionKit builds an admin handler over a FileStore with an acme/pair role,
// a discord chat service, two linked roster humans with discord delivery
// profiles (alice ↔ auth0|alice, bob ↔ auth0|bob), one unlinked human (carol),
// one linked human with no discord profile (dave ↔ auth0|dave), a fake launcher-backed Supervisor, and a granting
// fake allocator. Requests are authenticated as *op (initially alice).
func newSessionKit(t *testing.T) *sessionKit {
	t.Helper()
	store, err := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutRole("acme", Role{Name: "pair", Scope: Scope{Destinations: []string{"anthropic"}, TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	discord := func(inbox string) []DeliveryProfile { return []DeliveryProfile{{Service: "discord", Address: inbox}} }
	for _, h := range []Human{
		{Name: "alice", Handle: "@alice", Login: "auth0|alice", Delivery: discord("111")},
		{Name: "bob", Handle: "@bob", Login: "auth0|bob", Delivery: discord("222")},
		{Name: "carol", Handle: "@carol"},
		{Name: "dave", Handle: "@dave", Login: "auth0|dave"}, // linked, but no discord delivery profile
	} {
		if err := store.AddHuman("acme", h); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SetChatService("acme", "discord"); err != nil {
		t.Fatal(err)
	}
	l := &fakeLauncher{liveness: LivenessAlive}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sup := NewSupervisor(store, l, "holder-sessions", time.Minute, 30*time.Second, nil, log)
	fa := &fakeSessionAlloc{granted: true}
	op := "auth0|alice"
	h := NewAdminHandler(store, sup, fa, switchOperator{id: &op}, func(string) bool { return true }, nil, log, nil)
	return &sessionKit{h: h, store: store, launcher: l, alloc: fa, sup: sup, op: &op}
}

func (k *sessionKit) request(t *testing.T) (PersonalSessionResult, int, string) {
	t.Helper()
	rec := doJSON(t, k.h, "POST", "/admin/sessions/personal", PersonalSessionBody{Project: "acme", Role: "pair", Prompt: "help me"})
	var res PersonalSessionResult
	if rec.Code == http.StatusCreated {
		decodeJSON(t, rec, &res)
	}
	return res, rec.Code, rec.Body.String()
}

func TestPersonalSessionRequest_LinkedHuman(t *testing.T) {
	k := newSessionKit(t)
	res, code, body := k.request(t)
	if code != http.StatusCreated {
		t.Fatalf("request = %d %s", code, body)
	}
	if !strings.HasPrefix(res.ID, "personal-alice-") || res.Owner != "alice" || res.Project != "acme" || res.Role != "pair" || res.Phase != string(PhaseLive) {
		t.Fatalf("result = %+v", res)
	}
	if strings.Contains(body, "token") || strings.Contains(body, "secret") {
		t.Fatalf("personal-session response must not carry identity secrets: %s", body)
	}
	if want := "acme/pair/" + res.ID + "/alice"; len(k.alloc.grants) != 1 || k.alloc.grants[0] != want {
		t.Fatalf("grants = %v, want [%s]", k.alloc.grants, want)
	}
	if s := k.launcher.gotSpec; s.Owner != "alice" || s.SessionKind != "personal" || !strings.HasSuffix(s.Prompt, "\nhelp me") || s.Project != "acme" || s.Role != "pair" {
		t.Fatalf("raise spec = %+v", s)
	}
	inst, ok := k.store.GetInstance(res.ID)
	if !ok || inst.Owner != "alice" || inst.SessionKind != "personal" {
		t.Fatalf("instance = %+v,%v", inst, ok)
	}
}

func TestPersonalSessionRequest_UnlinkedLogin403(t *testing.T) {
	k := newSessionKit(t)
	*k.op = "auth0|nobody"
	if _, code, _ := k.request(t); code != http.StatusForbidden {
		t.Fatalf("unlinked login = %d, want 403", code)
	}
	if len(k.alloc.grants) != 0 || len(k.launcher.raised) != 0 {
		t.Fatalf("an unlinked caller must neither grant nor raise: grants=%v raised=%v", k.alloc.grants, k.launcher.raised)
	}
}

func TestPersonalSessionRequest_Denied409(t *testing.T) {
	k := newSessionKit(t)
	k.alloc.granted = false
	if _, code, body := k.request(t); code != http.StatusConflict || !strings.Contains(body, "capacity") {
		t.Fatalf("denied grant = %d %q, want 409 at capacity", code, body)
	}
	if len(k.launcher.raised) != 0 {
		t.Fatal("a denied grant must not raise")
	}
}

func TestPersonalSessionRequest_NeedsLedger409(t *testing.T) {
	k := newSessionKit(t)
	k.alloc.grantErr = fmt.Errorf("adapter: %w", ErrNeedsLedger)
	if _, code, body := k.request(t); code != http.StatusConflict || !strings.Contains(body, "store-postgres") {
		t.Fatalf("no ledger = %d %q, want 409 naming store-postgres", code, body)
	}
}

func TestPersonalSessionRequest_GrantError502(t *testing.T) {
	k := newSessionKit(t)
	k.alloc.grantErr = errors.New("ledger unreachable")
	if _, code, _ := k.request(t); code != http.StatusBadGateway {
		t.Fatalf("grant error = %d, want 502", code)
	}
}

func TestPersonalSessionRequest_RaiseFailsCompensates(t *testing.T) {
	k := newSessionKit(t)
	k.launcher.raiseErr = errors.New("colima down")
	if _, code, _ := k.request(t); code != http.StatusBadGateway {
		t.Fatalf("raise failure = %d, want 502", code)
	}
	if len(k.alloc.grants) != 1 || len(k.alloc.releases) != 1 {
		t.Fatalf("grants=%v releases=%v; want the grant compensated by one release", k.alloc.grants, k.alloc.releases)
	}
	if g, r := k.alloc.grants[0], k.alloc.releases[0]; !strings.HasPrefix(g, r+"/") {
		t.Fatalf("release %q does not match grant %q", r, g)
	}
}

func TestPersonalSessionRequest_UnknownRole400(t *testing.T) {
	k := newSessionKit(t)
	rec := doJSON(t, k.h, "POST", "/admin/sessions/personal", PersonalSessionBody{Project: "acme", Role: "nope"})
	if rec.Code != http.StatusBadRequest || len(k.alloc.grants) != 0 {
		t.Fatalf("unknown role = %d grants=%v, want 400 and no grant", rec.Code, k.alloc.grants)
	}
}

func TestPersonalSessionList_OnlyCallersOwn(t *testing.T) {
	k := newSessionKit(t)
	a, _, _ := k.request(t)
	*k.op = "auth0|bob"
	b, _, _ := k.request(t)
	// A non-personal cove in the same project is never listed.
	if err := k.store.PutInstance(Instance{ActorID: "cove-AET-1", Project: "acme", Role: "pair", Phase: PhaseLive}); err != nil {
		t.Fatal(err)
	}
	var got []PersonalSessionSummary
	getJSON(t, k.h, "/admin/sessions/personal?project=acme", &got)
	if len(got) != 1 || got[0].ID != b.ID || got[0].Owner != "bob" {
		t.Fatalf("bob's list = %+v, want only %s", got, b.ID)
	}
	*k.op = "auth0|alice"
	getJSON(t, k.h, "/admin/sessions/personal?project=acme", &got)
	if len(got) != 1 || got[0].ID != a.ID {
		t.Fatalf("alice's list = %+v, want only %s", got, a.ID)
	}
	*k.op = "auth0|nobody"
	if rec := doReq(t, k.h, "GET", "/admin/sessions/personal?project=acme", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("unlinked list = %d, want 403", rec.Code)
	}
}

func TestPersonalSessionRelease_OwnerOnly(t *testing.T) {
	k := newSessionKit(t)
	res, _, _ := k.request(t)

	*k.op = "auth0|bob" // another linked human
	if rec := doReq(t, k.h, "DELETE", "/admin/sessions/personal/"+res.ID, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("release by bob = %d, want 403", rec.Code)
	}
	if _, ok := k.store.GetInstance(res.ID); !ok {
		t.Fatal("a refused release must leave the session running")
	}

	*k.op = "auth0|alice"
	if rec := doReq(t, k.h, "DELETE", "/admin/sessions/personal/nope", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("release unknown = %d, want 404", rec.Code)
	}
	if err := k.store.PutInstance(Instance{ActorID: "cove-AET-1", Project: "acme", Role: "pair", Phase: PhaseLive}); err != nil {
		t.Fatal(err)
	}
	if rec := doReq(t, k.h, "DELETE", "/admin/sessions/personal/cove-AET-1", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("release of a non-personal cove = %d, want 404", rec.Code)
	}
	if rec := doReq(t, k.h, "DELETE", "/admin/sessions/personal/"+res.ID, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("release by owner = %d, want 204", rec.Code)
	}
	if _, ok := k.store.GetInstance(res.ID); ok {
		t.Fatal("instance still present after the owner's release")
	}
	if len(k.launcher.tornDown) != 1 || k.launcher.tornDown[0] != res.ID {
		t.Fatalf("torn down = %v, want [%s]", k.launcher.tornDown, res.ID)
	}
}

// Teardown (the release path) records the reservation release through the
// Supervisor's releaser — the same seam the dispatcher's coves use.
func TestPersonalSessionRelease_RecordsRelease(t *testing.T) {
	k := newSessionKit(t)
	res, _, _ := k.request(t)
	fr := &fakeReleaser{}
	k.sup.SetReleaser(fr)
	if rec := doReq(t, k.h, "DELETE", "/admin/sessions/personal/"+res.ID, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("release = %d", rec.Code)
	}
	if len(fr.calls) != 1 || fr.calls[0] != "acme/pair/"+res.ID {
		t.Fatalf("releases = %v", fr.calls)
	}
}

func TestPersonalSessions_NoSupervisorOrAllocator503(t *testing.T) {
	store, err := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err != nil {
		t.Fatal(err)
	}
	h := NewAdminHandler(store, nil, nil, fixedOperator{id: "auth0|alice"}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	if rec := doJSON(t, h, "POST", "/admin/sessions/personal", PersonalSessionBody{Project: "acme", Role: "pair"}); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no runtime = %d, want 503", rec.Code)
	}
}

// The session id is used as an actor id and in URL paths (DELETE
// /admin/sessions/personal/{id}), so an owner name with path or URL-unsafe
// characters must not leak into it. The real owner is stored on the Instance.
func TestPersonalSessionID_SanitizesOwner(t *testing.T) {
	for _, owner := range []string{"a/b", "alice smith", "bob?x=1", "ok.name_1-x"} {
		id, err := personalSessionID(owner)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(id, "personal-") {
			t.Fatalf("id %q lacks the personal- prefix", id)
		}
		for _, r := range id {
			ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '.' || r == '-'
			if !ok {
				t.Fatalf("owner %q produced unsafe id %q (char %q)", owner, id, r)
			}
		}
	}
	a, _ := personalSessionID("alice")
	b, _ := personalSessionID("alice")
	if a == b {
		t.Fatalf("two ids for the same owner collided: %q", a)
	}
}

// A personal session is delivered over Discord, so a project whose chat service
// isn't discord is refused at request time — before any grant.
func TestPersonalSessionRequest_NonDiscordProject400(t *testing.T) {
	k := newSessionKit(t)
	if err := k.store.SetChatService("acme", ""); err != nil {
		t.Fatal(err)
	}
	_, code, body := k.request(t)
	if code != http.StatusBadRequest || !strings.Contains(body, "chat service") || !strings.Contains(body, "chat-service set") {
		t.Fatalf("non-discord project = %d %q, want 400 naming the chat-service fix", code, body)
	}
	if len(k.alloc.grants) != 0 || len(k.launcher.raised) != 0 {
		t.Fatalf("must neither grant nor raise: grants=%v raised=%v", k.alloc.grants, k.launcher.raised)
	}
}

// An owner with no discord delivery profile can't be messaged: 400, no grant.
func TestPersonalSessionRequest_OwnerWithoutDiscordProfile400(t *testing.T) {
	k := newSessionKit(t)
	*k.op = "auth0|dave"
	_, code, body := k.request(t)
	if code != http.StatusBadRequest || !strings.Contains(body, "dave has no discord delivery profile") || !strings.Contains(body, "--delivery discord:") {
		t.Fatalf("no discord profile = %d %q, want 400 naming the --delivery fix", code, body)
	}
	if len(k.alloc.grants) != 0 || len(k.launcher.raised) != 0 {
		t.Fatalf("must neither grant nor raise: grants=%v raised=%v", k.alloc.grants, k.launcher.raised)
	}
}

// The raised prompt starts with the personal-session preamble (naming the
// owner and the intercom flow) followed by the owner's own prompt.
func TestPersonalSessionRequest_PromptPreamble(t *testing.T) {
	k := newSessionKit(t)
	if _, code, body := k.request(t); code != http.StatusCreated {
		t.Fatalf("request = %d %s", code, body)
	}
	p := k.launcher.gotSpec.Prompt
	if !strings.HasPrefix(p, "You are a personal session for alice.") {
		t.Fatalf("prompt does not start with the preamble: %q", p)
	}
	for _, want := range []string{"intercom `send` tool (omit `to`)", "until alice releases it", "\n---\nhelp me"} {
		if !strings.Contains(p, want) {
			t.Fatalf("prompt missing %q: %q", want, p)
		}
	}
}

// Personal and standing sessions are resident (wait after every turn, never
// reaped for waiting); ephemeral ones are not.
func TestIsResident(t *testing.T) {
	for kind, want := range map[string]bool{"": false, "ephemeral": false, SessionKindPersonal: true, SessionKindStanding: true, "bogus": false} {
		if got := IsResident(kind); got != want {
			t.Fatalf("IsResident(%q) = %v, want %v", kind, got, want)
		}
	}
}
