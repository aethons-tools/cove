package jam

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/ticketkey"
)

// newGitHubICFixture is newICFixture on a GitHub-tracked Jam: its linear
// connection removed, its tracker the github connection "gh-acme", and the
// ticket session on acme/board#7.
func newGitHubICFixture(t *testing.T, addressing ...string) *icFixture {
	t.Helper()
	return newGitHubICFixtureFrom(t, newICFixture(t, addressing...))
}

// newGitHubICFixtureFrom moves fixture f onto a github tracker.
func newGitHubICFixtureFrom(t *testing.T, f *icFixture) *icFixture {
	t.Helper()
	if err := f.store.RemoveConnection(f.tracker.ID); err != nil {
		t.Fatal(err)
	}
	gh, err := f.store.CreateConnection(Connection{Kind: "github", Name: "gh-acme"})
	if err != nil {
		t.Fatal(err)
	}
	f.tracker = gh
	f.ticket.Unit = "acme/board#7"
	if err := f.store.PutInstance(f.ticket); err != nil {
		t.Fatal(err)
	}
	f.ic = NewIntercom(f.store, func() (ident.ID, bool) { return f.tracker.ID, true }, f.log, func() int64 { return f.tail }, nil)
	return f
}

func TestGitHubTicketChannel(t *testing.T) {
	f := newGitHubICFixture(t)
	if err := f.ic.SetUp(f.ticket); err != nil {
		t.Fatalf("SetUp: %v", err)
	}
	ch, ok := f.store.ChannelByKey(f.project.ID, SourceTicket, string(f.tracker.ID)+"/acme/board#7")
	if !ok || ch.Label != "acme/board#7" || len(ch.Bindings) != 1 || ch.Bindings[0] != (Binding{ConnectionID: f.tracker.ID, Ref: "acme/board#7", Mode: BindBoth}) {
		t.Fatalf("ticket channel = %+v, %v", ch, ok)
	}

	// Every spelling of one issue is one key, so one channel (F16).
	variant := f.ticket
	variant.ActorID, variant.Unit = string(ident.New(ident.Session)), "Acme/Board#007"
	if err := f.ic.SetUp(variant); err != nil {
		t.Fatal(err)
	}
	if got, ok := f.ic.TicketChannelOf(variant); !ok || got.ID != ch.ID {
		t.Fatalf("TicketChannelOf(unit Acme/Board#007) = %+v, %v; want %s", got, ok, ch.ID)
	}

	// A unit that is no GitHub key (a manual raise's free text) has no
	// ticket channel: its home is its own session channel, never a failure.
	for _, unit := range []string{"#7", "ACME-7", "acme/board#x"} {
		bad := f.ticket
		bad.ActorID, bad.Unit = string(ident.New(ident.Session)), unit
		home, err := f.ic.HomeChannel(bad)
		if err != nil || home.Kind != SourceSession || home.Key != bad.ActorID {
			t.Fatalf("HomeChannel(unit %q on a github tracker) = %+v, %v; want its session channel", unit, home, err)
		}
		if _, ok := f.ic.TicketChannelOf(bad); ok {
			t.Fatalf("unit %q: a ticket channel", unit)
		}
	}
	if n := len(f.store.ListChannels(f.project.ID, SourceTicket)); n != 1 {
		t.Fatalf("%d ticket channels; want only acme/board#7's", n)
	}
}

func TestPlanGitHubTicketTargets(t *testing.T) {
	f := newGitHubICFixture(t, "ticket:*")
	if err := f.ic.SetUp(f.ticket); err != nil {
		t.Fatal(err)
	}
	own := f.mustPlan(f.ticket, "ticket:acme/board#7")
	if got := f.mustPlan(f.ticket, "ticket:Acme/Board#007"); got.Channel.ID != own.Channel.ID {
		t.Fatalf("own ticket by a variant spelling → %s; want %s", got.Channel.ID, own.Channel.ID)
	}
	for _, addr := range []string{"ticket:acme/board#7", "ticket:Acme/Board#007", "ticket:gh-acme/acme/board#7", "ticket:" + string(f.tracker.ID) + "/acme/board#7"} {
		if got := f.mustPlan(f.standing, addr); got.Channel.ID != own.Channel.ID {
			t.Fatalf("%s → %s; want %s", addr, got.Channel.ID, own.Channel.ID)
		}
	}
	if _, err := f.plan(f.standing, "ticket:acme/board#9"); !errors.Is(err, ErrSendUnresolved) {
		t.Fatalf("a ticket with no channel: %v", err)
	}
	for _, addr := range []string{"ticket:acme/board#x", "ticket:gh-acme/acme/board#x", "ticket:a/b/c#1"} {
		_, err := f.plan(f.standing, addr)
		if !errors.Is(err, ErrSendUnresolved) || !errors.Is(err, ticketkey.ErrGitHub) || strings.Contains(err.Error(), "board#x") {
			t.Fatalf("malformed key %s: %v", addr, err)
		}
	}

	repo := newGitHubICFixture(t, "ticket:acme/*")
	if err := repo.ic.SetUp(repo.ticket); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.plan(repo.standing, "ticket:acme/board#7"); err != nil {
		t.Fatalf("ticket:acme/* reaches acme/board#7: %v", err)
	}
	other := newGitHubICFixture(t, "ticket:other/*")
	if err := other.ic.SetUp(other.ticket); err != nil {
		t.Fatal(err)
	}
	if _, err := other.plan(other.standing, "ticket:acme/board#7"); !errors.Is(err, ErrSendDenied) {
		t.Fatalf("ticket:other/* must not reach acme/board#7: %v", err)
	}
	// Addressing is checked before the key's shape (403 before 404, F14).
	if _, err := other.plan(other.standing, "ticket:acme/board#x"); !errors.Is(err, ErrSendDenied) || errors.Is(err, ticketkey.ErrGitHub) {
		t.Fatalf("a malformed key the addressing doesn't allow: %v; want ErrSendDenied", err)
	}
	// A session's own ticket needs no addressing.
	if _, err := other.plan(other.ticket, "ticket:acme/board#7"); err != nil {
		t.Fatalf("own ticket: %v", err)
	}
}

// Linear keys resolve exactly as before on a Jam that also has a github
// connection: bare, scoped by connection, and never the GitHub refusal.
func TestPlanLinearTicketsBesideGitHub(t *testing.T) {
	f := newICFixture(t, "ticket:*")
	if _, err := f.store.CreateConnection(Connection{Kind: "github", Name: "gh-acme"}); err != nil {
		t.Fatal(err)
	}
	if err := f.ic.SetUp(f.ticket); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.store.ChannelByKey(f.project.ID, SourceTicket, string(f.tracker.ID)+"/ACME-7"); !ok {
		t.Fatal("linear ticket channel keyed as before")
	}
	own := f.mustPlan(f.ticket, "ticket:ACME-7")
	for _, addr := range []string{"ticket:ACME-7", "ticket:linear/ACME-7"} {
		if got := f.mustPlan(f.standing, addr); got.Channel.ID != own.Channel.ID {
			t.Fatalf("%s → %s; want %s", addr, got.Channel.ID, own.Channel.ID)
		}
	}
	for _, addr := range []string{"ticket:acme-7", "ticket:nope/ACME-7"} {
		if _, err := f.plan(f.standing, addr); !errors.Is(err, ErrSendUnresolved) || errors.Is(err, ticketkey.ErrGitHub) {
			t.Fatalf("%s: %v; want a plain ErrSendUnresolved", addr, err)
		}
	}
}

func TestGlobMatchGitHubTickets(t *testing.T) {
	for _, c := range []struct {
		glob, form string
		want       bool
	}{
		{"ticket:*", "ticket:acme/board#7", true},
		{"*", "ticket:acme/board#7", true},
		{"ticket:acme/*", "ticket:acme/board#7", true},
		{"ticket:acme/board#*", "ticket:acme/board#7", true},
		{"ticket:gh-acme/*", "ticket:gh-acme/acme/board#7", true},
		{"ticket:other/*", "ticket:acme/board#7", false},
		{"user:*", "ticket:acme/board#7", false},
		{"ticket:acme/b*", "ticket:acme-co/board#7", false},
		// Linear globs keep path.Match semantics exactly:
		{"ticket:*", "ticket:ACME-7", true},
		{"ticket:ACME-*", "ticket:ACME-7", true},
		{"ticket:lin*", "ticket:linear/ACME-7", false},
		{"ticket:*", "ticket:linear/ACME-7", false},
		{"ticket:linear/*", "ticket:linear/ACME-7", true},
		{"*", "channel:a/b", false},
		// A glob's GitHub-shaped parts are canonical (final fix 4):
		{"ticket:Acme/*", "ticket:acme/board#7", true},
		{"ticket:acme/board#007", "ticket:acme/board#7", true},
		{"ticket:acme/board#??", "ticket:acme/board#7", false},
		{"ticket:GH-Acme/*", "ticket:GH-Acme/acme/board#7", true},
		// Connection names are case-sensitive: canonicalising a glob never
		// widens it onto a connection whose name differs only in case.
		{"ticket:gh/*", "ticket:GH/acme/board#7", false},
		{"ticket:GH/*", "ticket:gh/acme/board#7", false},
		{"ticket:GH/*", "ticket:GH/acme/board#7", true},
	} {
		if got := globMatch(c.glob, c.form); got != c.want {
			t.Errorf("globMatch(%q, %q) = %v; want %v", c.glob, c.form, got, c.want)
		}
	}
}

func TestListTargetsGitHubTicketSessions(t *testing.T) {
	for _, c := range []struct {
		addressing []string
		listed     bool
	}{
		{[]string{"session:*"}, false}, // another's ticket session needs ticket: addressing too
		{[]string{"session:*", "ticket:acme/*"}, true},
	} {
		f := newGitHubICFixture(t, c.addressing...)
		var got []string
		for _, tg := range ListTargets(f.store, f.ic.Tracker, f.actor, time.Now()) {
			got = append(got, tg.Kind+":"+tg.Name)
		}
		if slices.Contains(got, "session:"+f.ticket.ActorID) != c.listed {
			t.Errorf("addressing %v: targets %v; want ticket session listed=%v", c.addressing, got, c.listed)
		}
	}
}

// ListTargets checks ticket sessions against the tracker serve resolved, not
// whichever connection kind the store holds first (F7).
func TestListTargetsUsesTheResolvedTracker(t *testing.T) {
	f := newGitHubICFixture(t, "session:*", "ticket:gh-acme/*")
	if _, err := f.store.CreateConnection(Connection{Kind: "linear", Name: "linear"}); err != nil {
		t.Fatal(err)
	}
	listed := func(tracker func() (ident.ID, bool)) bool {
		for _, tg := range ListTargets(f.store, tracker, f.actor, time.Now()) {
			if tg.Kind == "session" && tg.Name == f.ticket.ActorID {
				return true
			}
		}
		return false
	}
	if !listed(f.ic.Tracker) {
		t.Fatal("tracker gh-acme: ticket:gh-acme/* reaches acme/board#7's session")
	}
	if listed(nil) { // no resolved tracker: TrackerConnection, the linear one
		t.Fatal("tracker linear: ticket:gh-acme/* must not reach the session")
	}
}

func TestGitHubConnectionKindAndRooms(t *testing.T) {
	s := NewMemStore()
	if err := s.CreateProject("acme"); err != nil {
		t.Fatal(err)
	}
	if _, ok := TrackerConnection(s); ok {
		t.Fatal("no tracker connection yet")
	}
	gh, err := s.CreateConnection(Connection{Kind: "github", Name: "gh-acme"})
	if err != nil {
		t.Fatalf("a github connection: %v", err)
	}
	if c, ok := TrackerConnection(s); !ok || c.ID != gh.ID {
		t.Fatalf("TrackerConnection = %+v, %v", c, ok)
	}
	if _, _, err := PutRoom(s, "acme", RoomBody{Name: "eng", Connection: "gh-acme", Ref: "ACME-1"}); !errors.Is(err, ticketkey.ErrGitHub) {
		t.Fatalf("github room with a Linear ref = %v; want ErrGitHub", err)
	}
	if _, _, err := PutRoom(s, "acme", RoomBody{Name: "eng", Connection: "gh-acme", Ref: "acme/board#3"}); err != nil {
		t.Fatalf("github room: %v", err)
	}
	if _, _, err := PutRoom(s, "acme", RoomBody{Name: "eng2", Connection: "gh-acme", Ref: "Acme/Board#004"}); err != nil {
		t.Fatalf("github room, variant spelling: %v", err)
	}
	if ch, ok := s.ChannelByBinding(gh.ID, "acme/board#4"); !ok || ch.Key != "eng2" {
		t.Fatalf("room bound to the canonical key: %+v, %v", ch, ok)
	}
	lin, err := s.CreateConnection(Connection{Kind: "linear", Name: "linear"})
	if err != nil {
		t.Fatal(err)
	}
	if c, ok := TrackerConnection(s); !ok || c.ID != lin.ID {
		t.Fatalf("TrackerConnection with both = %+v, %v; want the linear one", c, ok)
	}
	if _, _, err := PutRoom(s, "acme", RoomBody{Name: "ops", Connection: "linear", Ref: "ACME-1"}); err != nil {
		t.Fatalf("linear room: %v", err)
	}
}

func TestMemberHandleFromGitHubAccount(t *testing.T) {
	s := NewMemStore()
	if err := s.CreateProject("acme"); err != nil {
		t.Fatal(err)
	}
	p, _ := s.GetProject("acme")
	u, err := s.CreateUser(User{Name: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddMember(p.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	gh, _ := s.CreateConnection(Connection{Kind: "github", Name: "gh-acme"})
	if _, err := s.UpsertAccount(Account{ConnectionID: gh.ID, Handle: "alice-gh", UserID: u.ID, Status: StatusLive}); err != nil {
		t.Fatal(err)
	}
	if m, ok := MemberOf(s, p.ID, u.ID); !ok || m.Handle != "alice-gh" {
		t.Fatalf("member = %+v, %v; want handle alice-gh", m, ok)
	}
	lin, _ := s.CreateConnection(Connection{Kind: "linear", Name: "linear"})
	if _, err := s.UpsertAccount(Account{ConnectionID: lin.ID, Handle: "alice-lin", UserID: u.ID, Status: StatusLive}); err != nil {
		t.Fatal(err)
	}
	if m, _ := MemberOf(s, p.ID, u.ID); m.Handle != "alice-lin" {
		t.Fatalf("handle = %q; the linear account's comes first", m.Handle)
	}
}

// POST /squawks refuses a malformed GitHub key with the fixed text (never
// the key sent), and only once the addressing allows the address.
func TestSquawksSendMalformedGitHubKey(t *testing.T) {
	for _, c := range []struct {
		addressing string
		code       int
	}{{"ticket:*", http.StatusNotFound}, {"ticket:other/*", http.StatusForbidden}} {
		f := newSqFixture(t, c.addressing)
		f.icFixture = newGitHubICFixtureFrom(t, f.icFixture)
		f.h = NewSquawksHandler(f.store, f.ic, f.log, f.legacy, slog.New(slog.NewTextHandler(f.logbuf, nil)))
		rec := f.do(http.MethodPost, "/squawks", f.standing, `{"body":"x","to":"ticket:acme/board#x"}`)
		if rec.Code != c.code || strings.Contains(rec.Body.String(), "board#x") {
			t.Fatalf("addressing %s: %d %q; want %d", c.addressing, rec.Code, rec.Body, c.code)
		}
		if c.code == http.StatusNotFound && !strings.Contains(rec.Body.String(), "github ticket key must be owner/repo#<n>") {
			t.Fatalf("body %q lacks the fixed refusal", rec.Body)
		}
	}
}

// A roster channel on github migrates to a room bound to its canonical
// ticket key; one whose ref is no key stays in the doc, noted.
func TestPlanRoomGitHubRef(t *testing.T) {
	m := legacyState(t, map[string][]Human{"acme": nil})
	acme := m.projects["acme"]
	acme.Roster.Channels = []RosterChannel{{Name: "gh", Service: "github", Ref: "Acme/Board#03"}, {Name: "bad", Service: "github", Ref: "ACME-1"}}
	m.projects["acme"] = acme
	plan := m.planRegistryMigration(4)
	if len(plan.channels) != 1 || plan.channels[0].Key != "gh" || plan.channels[0].Bindings[0].Ref != "acme/board#3" {
		t.Fatalf("channels = %+v; want room gh bound to acme/board#3", plan.channels)
	}
	if len(plan.report.Notes) != 1 || !strings.Contains(plan.report.Notes[0], ticketkey.ErrGitHub.Error()) {
		t.Fatalf("notes = %q; want the bad ref refused", plan.report.Notes)
	}
}

// GET /squawks/targets lists a ticket session's own ticket by its canonical
// key on a github tracker, and no ticket: for a Unit that is no key there.
func TestSquawksTargetsGitHubTicketCanonical(t *testing.T) {
	targets := func(f *sqFixture) []string {
		rec := f.do(http.MethodGet, "/squawks/targets", f.ticket, "")
		var r struct {
			Targets []targetOut `json:"targets"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &r); rec.Code != http.StatusOK || err != nil {
			t.Fatalf("targets = %d %v", rec.Code, err)
		}
		var out []string
		for _, tg := range r.Targets {
			if tg.Kind == "ticket" {
				out = append(out, tg.Target)
			}
		}
		return out
	}
	for unit, want := range map[string][]string{"Acme/Board#007": {"ticket:acme/board#7"}, "ACME-7": nil} {
		f := newSqFixture(t, "user:*")
		f.icFixture = newGitHubICFixtureFrom(t, f.icFixture)
		f.ticket.Unit = unit
		if err := f.store.PutInstance(f.ticket); err != nil {
			t.Fatal(err)
		}
		f.h = NewSquawksHandler(f.store, f.ic, f.log, f.legacy, slog.New(slog.NewTextHandler(f.logbuf, nil)))
		if got := targets(f); !slices.Equal(got, want) {
			t.Errorf("unit %q: ticket targets = %q; want %q", unit, got, want)
		}
	}
}

// Final fix 3: with the tracker's repo known (serve's SetTrackerRepo), a
// room can't be bound to another repo's issue on the tracker connection —
// nothing could ever be delivered there — and another repo's key is no
// ticket on the tracker.
func TestTrackerRepoScopesBindingsAndTickets(t *testing.T) {
	f := newGitHubICFixture(t)
	SetTrackerRepo(f.tracker.ID, "Acme/Board")
	t.Cleanup(func() { SetTrackerRepo(f.tracker.ID, "") })
	_, _, err := PutRoom(f.store, "acme", RoomBody{Name: "eng", Connection: "gh-acme", Ref: "other/repo#3"})
	if !errors.Is(err, ticketkey.ErrOtherRepo) || strings.Contains(err.Error(), "other/repo") {
		t.Fatalf("room on another repo = %v; want the fixed ErrOtherRepo", err)
	}
	if _, _, err := PutRoom(f.store, "acme", RoomBody{Name: "eng", Connection: "gh-acme", Ref: "ACME/board#3"}); err != nil {
		t.Fatalf("room on the tracker's repo (any case): %v", err)
	}
	if unit, ok := TicketUnit(f.store, f.tracker.ID, "other/repo#7"); ok {
		t.Fatalf("TicketUnit(other/repo#7) = %q; want none on the acme/board tracker", unit)
	}
	if unit, ok := TicketUnit(f.store, f.tracker.ID, "Acme/BOARD#07"); !ok || unit != "acme/board#7" {
		t.Fatalf("TicketUnit(Acme/BOARD#07) = %q, %v; want acme/board#7", unit, ok)
	}
	// Another repo's ticket session has no ticket channel: its own channel.
	other := f.ticket
	other.ActorID, other.Unit = string(ident.New(ident.Session)), "other/repo#7"
	if home, err := f.ic.HomeChannel(other); err != nil || home.Kind != SourceSession {
		t.Fatalf("HomeChannel(other/repo#7) = %+v, %v; want its session channel", home, err)
	}
}

// Final fix 4: a GitHub ticket is authorized by its canonical key only, and
// a glob's GitHub-shaped parts are canonical too — so the spelling of the
// address can't change the answer, and ticket: and session: agree.
func TestGitHubTicketAuthorizationIsCanonical(t *testing.T) {
	for _, c := range []struct {
		glob string
		want error // for both ticket:acme/board#07 and the ticket session's session:
	}{
		{"ticket:acme/board#??", ErrSendDenied}, // #07 is #7: one character
		{"ticket:acme/board#07", nil},           // a glob's leading zeros are stripped
		{"ticket:Acme/*", nil},                  // a glob's owner is lowercased
		{"ticket:ACME/BOARD#7", nil},
		{"ticket:other/*", ErrSendDenied},
	} {
		f := newGitHubICFixture(t, c.glob, "session:*")
		if err := f.ic.SetUp(f.ticket); err != nil {
			t.Fatal(err)
		}
		if _, err := f.plan(f.standing, "ticket:acme/board#07"); !errors.Is(err, c.want) {
			t.Errorf("glob %s: ticket:acme/board#07 → %v; want %v", c.glob, err, c.want)
		}
		if _, err := f.plan(f.standing, "session:"+f.ticket.ActorID); !errors.Is(err, c.want) {
			t.Errorf("glob %s: session:<ticket session> → %v; want %v", c.glob, err, c.want)
		}
	}
}

// A malformed key with no '#' is no GitHub form: 403 unless a glob covers
// it as written, and only then the malformed-key 404.
func TestMalformedGitHubKeyWithoutHash(t *testing.T) {
	for glob, want := range map[string]error{"ticket:*": ErrSendDenied, "ticket:acme/*": ticketkey.ErrGitHub} {
		f := newGitHubICFixture(t, glob)
		if _, err := f.plan(f.standing, "ticket:acme/board"); !errors.Is(err, want) {
			t.Errorf("glob %s: ticket:acme/board → %v; want %v", glob, err, want)
		}
	}
}

// Final fix 6: a room's default connection is the tracker's — on a
// GitHub-tracked Jam the github connection (serve's, when it recorded one),
// not a linear one created for it.
func TestRoomDefaultConnectionIsTheTrackers(t *testing.T) {
	s := NewMemStore()
	if err := s.CreateProject("acme"); err != nil {
		t.Fatal(err)
	}
	gh, err := s.CreateConnection(Connection{Kind: "github", Name: "gh-acme"})
	if err != nil {
		t.Fatal(err)
	}
	v, _, err := PutRoom(s, "acme", RoomBody{Name: "eng", Ref: "acme/board#3"})
	if err != nil || v.ConnectionID != gh.ID {
		t.Fatalf("default-connection room = %+v, %v; want on %s", v, err, gh.ID)
	}
	if _, ok := s.ConnectionOfKind("linear"); ok {
		t.Fatal("a default room created a linear connection on a github-tracked Jam")
	}
	// With a linear connection too, serve's github tracker still wins.
	if _, err := s.CreateConnection(Connection{Kind: "linear", Name: "linear"}); err != nil {
		t.Fatal(err)
	}
	SetTrackerRepo(gh.ID, "acme/board")
	t.Cleanup(func() { SetTrackerRepo(gh.ID, "") })
	if v, _, err := PutRoom(s, "acme", RoomBody{Name: "ops", Ref: "acme/board#4"}); err != nil || v.ConnectionID != gh.ID {
		t.Fatalf("default-connection room beside linear = %+v, %v; want on serve's tracker %s", v, err, gh.ID)
	}
}
