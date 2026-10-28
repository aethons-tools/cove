package adminui_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
)

// searchFixture: one of everything, with "zephyr" planted in a field of each
// kind of entity, plus twelve squawks mentioning it.
func searchFixture(t *testing.T) (http.Handler, jam.Store) {
	t.Helper()
	store := newStore(t)
	mustCreateProject(t, store, "zephyr-labs")
	mustCreateProject(t, store, "acme")
	if err := store.AddDestination(jam.Destination{Name: "gh", Route: "/api/v3/", Upstream: "https://zephyr.example"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := jam.PushStudioKit(store, "web", "kind: studio\nprompt: You maintain the Zephyr service.\n"); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRole("acme", jam.Role{Name: "zephyr-dev", Kit: "web", Scope: jam.Scope{Destinations: []string{"gh"}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddActor(jam.Actor{ID: "bot-zephyr", TokenHash: "h1", Grants: []jam.Grant{{Project: "acme", Role: "zephyr-dev"}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutInstance(jam.Instance{ActorID: "studio-7", Project: "acme", Role: "zephyr-dev", Unit: "COV-42", Owner: "zoe", Phase: jam.PhaseLive}); err != nil {
		t.Fatal(err)
	}
	if err := jam.AddPerson(store, "acme", jam.Human{Name: "zoe", Handle: "zephyr-zoe", Login: "zoe@example.com"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := jam.PutRoom(store, "acme", jam.RoomBody{Name: "ops", Connection: "discord", Ref: "zephyr-ops"}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutModelSpec(jam.ModelSpec{Name: "fast", Type: jam.HarnessClaude, Version: "2.1.0", Model: jam.ModelChoice{ID: "zephyr-1"},
		Principal: jam.ModelPrincipal{Credential: "anth"}, Claude: &jam.ClaudeSpec{Provider: "anthropic"}}); err != nil {
		t.Fatal(err)
	}
	var squawks []intercom.LegacySquawk
	t0 := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	for i := range 12 {
		squawks = append(squawks, intercom.LegacySquawk{From: human("zoe"), To: []intercom.Target{actor("studio-7")},
			Body: fmt.Sprintf("zephyr update %d", i), At: t0.Add(time.Duration(i) * time.Minute), Project: "acme"})
	}
	squawks = append(squawks, intercom.LegacySquawk{From: human("zoe"), To: []intercom.Target{channel("ops")}, Body: "nothing relevant", At: t0, Project: "acme"})
	return adminui.Handler(store, testLogger(), nil, nil, anyCred, newIntercomLog(t, squawks...)), store
}

func doSearch(t *testing.T, h http.Handler, q string) adminui.SearchResult {
	t.Helper()
	var r adminui.SearchResult
	if rec := apiDo(t, h, "GET", "/app/admin/api/search?q="+q, nil, &r); rec.Code != 200 {
		t.Fatalf("search %s = %d %s", q, rec.Code, rec.Body)
	}
	return r
}

// Every entity kind is searched, case-insensitively, by its own fields.
func TestSearchAPIEveryKind(t *testing.T) {
	h, _ := searchFixture(t)
	r := doSearch(t, h, "ZEPHYR")
	var keys []string
	got := map[string]adminui.SearchGroup{}
	for _, g := range r.Groups {
		keys = append(keys, g.Key)
		got[g.Key] = g
	}
	// groups in display order, only those with hits
	if strings.Join(keys, ",") != "agents,roles,projects,kits,destinations,model-specs,users,channels,squawks" {
		t.Errorf("groups = %v", keys)
	}
	hit := func(key string, i int) adminui.SearchHit {
		t.Helper()
		if len(got[key].Hits) <= i {
			t.Fatalf("%s: no hit %d in %+v", key, i, got[key])
		}
		return got[key].Hits[i]
	}
	for _, tc := range []struct {
		key  string
		i    int
		want adminui.SearchHit
	}{
		// a studio by its role; an actor by its id (and it is one hit, not also a studio)
		{"agents", 0, adminui.SearchHit{Title: "studio-7", Sub: "acme/zephyr-dev · orienting · COV-42 · owner zoe", Path: "agents/studio-7"}},
		{"agents", 1, adminui.SearchHit{Title: "bot-zephyr", Sub: "grants: acme/zephyr-dev", Path: "agents/bot-zephyr"}},
		{"roles", 0, adminui.SearchHit{Title: "acme/zephyr-dev", Sub: "destinations: gh · kit web", Path: "config/projects/acme/agents/zephyr-dev"}},
		{"projects", 0, adminui.SearchHit{Title: "zephyr-labs", Sub: "0 roles", Path: "config/projects/zephyr-labs"}},
		{"kits", 0, adminui.SearchHit{Title: "web", Sub: "current v1 · You maintain the Zephyr service.", Path: "config/kits/web"}},   // by its prompt
		{"destinations", 0, adminui.SearchHit{Title: "gh", Sub: "/api/v3/ → https://zephyr.example", Path: "config/destinations/gh"}}, // by its upstream
		{"model-specs", 0, adminui.SearchHit{Title: "fast", Sub: "claude · anth · zephyr-1", Path: "config/model-specs/fast"}},        // by its model
		{"channels", 0, adminui.SearchHit{Title: "channel:ops", Sub: "acme · discord zephyr-ops", Path: "config/projects/acme"}},      // by its ref
		{"squawks", 0, adminui.SearchHit{Title: "zephyr update 11", Sub: "Oct 2 09:11 · human:zoe → actor:studio-7", Path: "health/intercom?q=ZEPHYR"}},
	} {
		if h := hit(tc.key, tc.i); h != tc.want {
			t.Errorf("%s[%d] = %+v, want %+v", tc.key, tc.i, h, tc.want)
		}
	}
	if u := hit("users", 0); u.Title != "zoe" || u.Sub != "acme · @zephyr-zoe · login zoe@example.com" || !strings.HasPrefix(u.Path, "config/users/usr_") {
		t.Errorf("user hit (by handle) = %+v", u)
	}
	// squawks: the full count, the newest ten, the rest in the channel log
	if g := got["squawks"]; g.Count != 12 || len(g.Hits) != 10 || g.More != "health/intercom?q=ZEPHYR" || g.Hits[9].Title != "zephyr update 2" {
		t.Errorf("squawks = %+v", g)
	}
	if len(got["agents"].Hits) != 2 || r.Total != 21 || r.Jump != "" {
		t.Errorf("total %d jump %q agents %+v", r.Total, r.Jump, got["agents"])
	}
}

// A query that is exactly one entity's name jumps to it; several exact
// matches, or none, don't. Other fields match too: a user's login, an
// actor's grant, a studio's unit and owner.
func TestSearchAPIExactAndFields(t *testing.T) {
	h, store := searchFixture(t)
	zoe, _ := store.LookupName(ident.User, "zoe")
	for q, want := range map[string]string{
		"studio-7":        "agents/studio-7",
		"bot-zephyr":      "agents/bot-zephyr",
		"web":             "config/kits/web",
		"acme/zephyr-dev": "config/projects/acme/agents/zephyr-dev",
		"gh":              "config/destinations/gh",
		"FAST":            "config/model-specs/fast",
		"zephyr-labs":     "config/projects/zephyr-labs",
		"zoe":             "config/users/" + string(zoe),
		"zephyr":          "", // nothing is named exactly that
	} {
		if r := doSearch(t, h, q); r.Jump != want {
			t.Errorf("q=%s: jump %q, want %q", q, r.Jump, want)
		}
	}
	for q, key := range map[string]string{"zoe@example": "users", "cov-42": "agents", "acme/zephyr": "agents", "anth": "model-specs", "/api/v3": "destinations"} {
		r := doSearch(t, h, q)
		found := false
		for _, g := range r.Groups {
			found = found || g.Key == key
		}
		if !found {
			t.Errorf("q=%s: no %s hit in %+v", q, key, r.Groups)
		}
	}
	// A user by an OIDC subject.
	if err := store.SetUserOIDC(zoe, []jam.OIDCIdentity{{Issuer: "https://idp.example", Subject: "oidc-zoe-77"}}); err != nil {
		t.Fatal(err)
	}
	if r := doSearch(t, h, "zoe-77"); len(r.Groups) != 1 || r.Groups[0].Key != "users" {
		t.Errorf("q=zoe-77 = %+v", r.Groups)
	}
	if r := doSearch(t, h, "z"); !r.TooShort || r.Total != 0 {
		t.Errorf("a 1-char query = %+v", r)
	}
	if r := doSearch(t, h, "+"); r.TooShort || r.Total != 0 {
		t.Errorf("a blank query = %+v", r)
	}
	if r := doSearch(t, h, "qqqqq"); r.TooShort || r.Total != 0 || len(r.Groups) != 0 {
		t.Errorf("a miss = %+v", r)
	}
}

// A model-spec hit's sub-line has no dangling separator when its principal
// and model are empty; a channel-log squawk names its channel.
func TestSearchAPISubLines(t *testing.T) {
	store := newStore(t)
	if err := store.PutModelSpec(jam.ModelSpec{Name: "bare", Type: jam.HarnessClaude, Version: "2.1.0"}); err != nil {
		t.Fatal(err)
	}
	msgs := fakeLog{{At: time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC), From: "alice", Channel: "eng · room", Body: "bare necessities"}}
	h := adminui.Handler(store, testLogger(), nil, nil, anyCred, msgs)
	r := doSearch(t, h, "bare")
	for _, g := range r.Groups {
		switch g.Key {
		case "model-specs":
			if g.Hits[0].Sub != "claude" {
				t.Errorf("spec sub = %q", g.Hits[0].Sub)
			}
		case "squawks":
			if g.Hits[0].Sub != "Oct 2 09:00 · alice → eng · room" {
				t.Errorf("squawk sub = %q", g.Hits[0].Sub)
			}
		}
	}
	if len(r.Groups) != 2 || r.Jump != "config/model-specs/bare" {
		t.Errorf("search = %+v", r)
	}
}
