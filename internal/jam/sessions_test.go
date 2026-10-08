package jam

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
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

// newSessionKit builds an admin handler over a MemStore with an acme/pair role,
// a discord chat service, two linked roster humans with discord delivery
// profiles (alice ↔ auth0|alice, bob ↔ auth0|bob), one unlinked human (carol),
// one linked human with no discord profile (dave ↔ auth0|dave), a fake launcher-backed Supervisor, and a granting
// fake allocator. Requests are authenticated as *op (initially alice).
func newSessionKit(t *testing.T) *sessionKit {
	t.Helper()
	store := NewMemStore()
	mustCreateProject(t, store, "acme")
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
		if err := AddPerson(store, "acme", h); err != nil {
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
	h := NewAdminHandler(store, sup, fa, switchOperator{id: &op}, func(string) bool { return true }, nil, log, nil, nil)
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
	if !strings.HasPrefix(res.ID, "ses_") || res.Owner != "alice" || res.Project != "acme" || res.Role != "pair" || res.Phase != string(PhaseLive) {
		t.Fatalf("result = %+v", res)
	}
	if strings.Contains(body, "token") || strings.Contains(body, "secret") {
		t.Fatalf("personal-session response must not carry identity secrets: %s", body)
	}
	aliceID, _ := k.store.LookupName(ident.User, "alice")
	if want := "acme/pair/" + res.ID + "/" + string(aliceID); len(k.alloc.grants) != 1 || k.alloc.grants[0] != want {
		t.Fatalf("grants = %v, want [%s]", k.alloc.grants, want)
	}
	if s := k.launcher.gotSpec; s.Owner != "alice" || s.SessionKind != "personal" || s.Prompt != "help me" || ProjectName(k.store, s.Project) != "acme" || s.Role != "pair" {
		t.Fatalf("raise spec = %+v", s)
	}
	inst, ok := k.store.GetInstance(res.ID)
	alice, _ := k.store.LookupName(ident.User, "alice")
	if !ok || inst.Owner != "alice" || inst.OwnerID != alice || inst.SessionKind != "personal" {
		t.Fatalf("instance = %+v,%v", inst, ok)
	}
	// The session may address its owner, and only them — by user id.
	var a Actor
	for _, x := range k.store.ListActors() {
		if x.ID == res.ID {
			a = x
		}
	}
	if len(a.Grants) != 1 || a.Grants[0].Overrides == nil || !slices.Equal(a.Grants[0].Overrides.Addressing, []string{"user:" + string(alice)}) {
		t.Fatalf("grant = %+v; want an override addressing user:%s", a.Grants, alice)
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
// Supervisor's releaser — the same seam the Requisitioner's coves use.
func TestPersonalSessionRelease_RecordsRelease(t *testing.T) {
	k := newSessionKit(t)
	res, _, _ := k.request(t)
	fr := &fakeReleaser{}
	k.sup.SetReleaser(fr)
	if rec := doReq(t, k.h, "DELETE", "/admin/sessions/personal/"+res.ID, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("release = %d", rec.Code)
	}
	acme, _ := ProjectIDOf(k.store, "acme")
	if len(fr.calls) != 1 || fr.calls[0] != string(acme)+"/pair/"+res.ID {
		t.Fatalf("releases = %v", fr.calls)
	}
}

func TestPersonalSessions_NoSupervisorOrAllocator503(t *testing.T) {
	store := NewMemStore()
	h := NewAdminHandler(store, nil, nil, fixedOperator{id: "auth0|alice"}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	if rec := doJSON(t, h, "POST", "/admin/sessions/personal", PersonalSessionBody{Project: "acme", Role: "pair"}); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no runtime = %d, want 503", rec.Code)
	}
}

// Each request starts a new session with a minted id (URL-safe, never derived
// from the owner's name).
func TestPersonalSessionRequestsAreNewSessions(t *testing.T) {
	k := newSessionKit(t)
	a, code, body := k.request(t)
	if code != http.StatusCreated {
		t.Fatalf("first = %d %s", code, body)
	}
	if id, err := ident.Parse(a.ID); err != nil || id.Kind() != ident.Session {
		t.Fatalf("id %q is not a session id", a.ID)
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
// The owner's prompt is delivered as-is; who the session is and how it
// reaches its owner come from the session context's Boilerplate.
func TestPersonalSessionRequest_ContextNamesOwner(t *testing.T) {
	k := newSessionKit(t)
	if _, code, body := k.request(t); code != http.StatusCreated {
		t.Fatalf("request = %d %s", code, body)
	}
	spec := k.launcher.gotSpec
	if spec.Prompt != "help me" {
		t.Fatalf("prompt = %q, want the owner's prompt alone", spec.Prompt)
	}
	if spec.Context == nil || !strings.Contains(spec.Context.Core, "a personal session for alice") || !strings.Contains(spec.Context.Core, "until alice releases") {
		t.Fatalf("context must name the owner: %+v", spec.Context)
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

func TestNagMessageID(t *testing.T) {
	at := time.Unix(1700000000, 123)
	id := NagMessageID("p-a", at)
	if id != "nag:p-a:1700000000000000123" {
		t.Fatalf("NagMessageID = %q", id)
	}
	if !IsNagReply(id, "p-a") {
		t.Fatal("a nag's own id must be recognized as that actor's nag")
	}
	// actors whose ids share a prefix must not match each other's nags
	if IsNagReply(id, "p-ab") || IsNagReply(NagMessageID("p-ab", at), "p-a") {
		t.Fatal("nag ids must not match across actors sharing a prefix")
	}
	for _, replyTo := range []string{"", "nag:", "nag:p-a", "in:discord:D1", "00000000-abc", "xnag:p-a:1"} {
		if IsNagReply(replyTo, "p-a") {
			t.Fatalf("IsNagReply(%q) must be false", replyTo)
		}
	}
	if IsNagReply("nag::1", "") {
		t.Fatal("an empty actor id never matches")
	}
}

// A personal session is named after its role: <role>-01, else the next free
// -NN. A name is taken while a live non-standing session carries it (the
// manual-label rule); a gone one frees it.
func TestPersonalSessionNames(t *testing.T) {
	k := newSessionKit(t)
	name := func() string {
		t.Helper()
		res, code, body := k.request(t)
		if code != http.StatusCreated {
			t.Fatalf("request = %d %s", code, body)
		}
		inst, _ := k.store.GetInstance(res.ID)
		if inst.Name != res.Name {
			t.Fatalf("instance name %q, result name %q", inst.Name, res.Name)
		}
		return res.Name
	}
	if got := name(); got != "pair-01" {
		t.Fatalf("first = %q, want pair-01", got)
	}
	if got := name(); got != "pair-02" {
		t.Fatalf("second = %q, want pair-02", got)
	}
	// a manual raise labelled pair-03 takes that name; a standing session's
	// declared name (scoped to its role) does not.
	for _, i := range []Instance{
		{ActorID: "manual-x", Project: "acme", Role: "pair", Name: "pair-03", Phase: PhaseLive},
		{ActorID: "standing-x", Project: "acme", Role: "pair", Name: "pair-04", SessionKind: SessionKindStanding, Phase: PhaseLive},
	} {
		if err := k.store.PutInstance(i); err != nil {
			t.Fatal(err)
		}
	}
	if got := name(); got != "pair-04" {
		t.Fatalf("third = %q, want pair-04 (pair-03 is a live manual label)", got)
	}
	// pair-01 goes away: its name is free again.
	for _, i := range k.store.ListInstances() {
		if i.Name == "pair-01" {
			i.Phase = PhaseGone
			if err := k.store.PutInstance(i); err != nil {
				t.Fatal(err)
			}
		}
	}
	if got := name(); got != "pair-01" {
		t.Fatalf("after pair-01 went = %q, want pair-01", got)
	}
}

// Two requests in flight can't pick the same name: a name is reserved from
// the moment it's picked until its raise is done.
func TestReservePersonalName(t *testing.T) {
	store := NewMemStore()
	a, releaseA := reservePersonalName(store, "pair")
	b, releaseB := reservePersonalName(store, "pair")
	if a != "pair-01" || b != "pair-02" {
		t.Fatalf("reserved %q, %q; want pair-01, pair-02", a, b)
	}
	releaseA()
	c, releaseC := reservePersonalName(store, "pair")
	if c != "pair-01" {
		t.Fatalf("after release = %q, want pair-01", c)
	}
	releaseB()
	releaseC()
	if got := personalName("pair", 100); got != "pair-100" {
		t.Fatalf("past 99 = %q", got)
	}
}
