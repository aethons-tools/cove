package adminui_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
)

func userID(t *testing.T, store jam.Store, name string) ident.ID {
	t.Helper()
	id, ok := store.LookupName(ident.User, name)
	if !ok {
		t.Fatalf("no user %q", name)
	}
	return id
}

func TestUsersPageListsAndCreates(t *testing.T) {
	store := seedProjects(t)
	h := projHandler(store)
	alice := userID(t, store, "alice")
	body := get(t, h, "/ui/users").Body.String()
	for _, want := range []string{`href="/ui/users/` + string(alice) + `"`, "sub-alice", "acme", `hx-post="/ui/users"`,
		`href="/ui/users" aria-current="page">Users`} {
		if !strings.Contains(body, want) {
			t.Errorf("users page missing %q", want)
		}
	}
	rec := post(t, h, "/ui/users", url.Values{"name": {"bob"}, "logins": {"l1\n\nl2\n"}, "oidc": {"https://idp.example:bob-sub"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body.String())
	}
	bob := userID(t, store, "bob")
	if got := rec.Header().Get("HX-Redirect"); got != "/ui/users/"+string(bob) {
		t.Errorf("HX-Redirect = %q", got)
	}
	if u, _ := store.GetUser(bob); len(u.Logins) != 2 || len(u.OIDC) != 1 {
		t.Fatalf("bob = %+v", u)
	}
	for name, form := range map[string]url.Values{
		"bad name":    {"name": {"a b"}},
		"taken name":  {"name": {"bob"}},
		"taken login": {"name": {"carol"}, "logins": {"sub-alice"}},
		"bad oidc":    {"name": {"carol"}, "oidc": {"no-colon"}},
	} {
		if rec := post(t, h, "/ui/users", form); rec.Code < 400 {
			t.Errorf("%s = %d, want a refusal", name, rec.Code)
		}
	}
}

func TestUserPageEdits(t *testing.T) {
	store := seedProjects(t)
	h := projHandler(store)
	alice := userID(t, store, "alice")
	base := "/ui/users/" + string(alice)
	body := get(t, h, "/ui/users/alice").Body.String() // a name works too
	for _, want := range []string{
		`hx-post="` + base + `/name"`, `hx-post="` + base + `/logins"`, `hx-post="` + base + `/oidc"`,
		"sub-alice", "https://idp.example:oidc-alice", "123456789", "alice-h",
		`hx-post="` + base + `/accounts"`, `href="/ui/projects/acme"`, `hx-delete="` + base + `"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("user page missing %q", want)
		}
	}
	if get(t, h, "/ui/users/nobody").Code != http.StatusNotFound {
		t.Error("an unknown user must be 404")
	}

	if rec := post(t, h, base+"/name", url.Values{"name": {"alicia"}}); rec.Code != http.StatusOK {
		t.Fatalf("rename = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := post(t, h, base+"/logins", url.Values{"logins": {""}}); rec.Code != http.StatusOK {
		t.Fatalf("clear logins = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := post(t, h, base+"/oidc", url.Values{"oidc": {""}}); rec.Code != http.StatusOK {
		t.Fatalf("clear oidc = %d: %s", rec.Code, rec.Body.String())
	}
	if u, _ := store.GetUser(alice); u.Name != "alicia" || len(u.Logins) != 0 || len(u.OIDC) != 0 {
		t.Fatalf("alicia = %+v", u)
	}

	if rec := post(t, h, base+"/accounts", url.Values{"connection": {"discord"}, "uid": {"999"}}); rec.Code != http.StatusOK {
		t.Fatalf("add account = %d: %s", rec.Code, rec.Body.String())
	}
	var linked []jam.Account
	for _, c := range store.ListConnections() {
		for _, a := range store.ListAccounts(c.ID) {
			if a.UserID == alice {
				linked = append(linked, a)
			}
		}
	}
	if len(linked) != 3 { // linear handle + two discord ids
		t.Fatalf("alicia's accounts = %+v", linked)
	}
	if rec := del(t, h, "/ui/accounts/"+string(linked[0].ID)+"/user?user="+string(alice)); rec.Code != http.StatusOK {
		t.Fatalf("unlink = %d: %s", rec.Code, rec.Body.String())
	}
	if a, _ := store.GetAccount(linked[0].ID); a.UserID != "" {
		t.Fatal("unlink did not unlink")
	}

	rec := del(t, h, base)
	if rec.Code != http.StatusOK || rec.Header().Get("HX-Redirect") != "/ui/users" {
		t.Fatalf("remove = %d (redirect %q): %s", rec.Code, rec.Header().Get("HX-Redirect"), rec.Body.String())
	}
	if u, _ := store.GetUser(alice); u.Status != jam.StatusRemoved {
		t.Fatalf("removed user = %+v", u)
	}
}

func TestProjectMembersSection(t *testing.T) {
	store := seedProjects(t)
	h := projHandler(store)
	alice := userID(t, store, "alice")
	body := get(t, h, "/ui/projects/acme").Body.String()
	for _, want := range []string{
		`hx-post="/ui/projects/acme/members"`, `href="/ui/users/` + string(alice) + `"`,
		"discord:dm-alice", `hx-delete="/ui/projects/acme/members/` + string(alice) + `"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("project page missing %q", want)
		}
	}
	if strings.Contains(body, "/humans") {
		t.Error("the project page must no longer post humans")
	}
	if rec := post(t, h, "/ui/projects/acme/members", url.Values{"user": {"bob"}}); rec.Code != http.StatusNotFound {
		t.Errorf("unknown user = %d, want 404", rec.Code)
	}
	if _, err := store.CreateUser(jam.User{Name: "bob"}); err != nil {
		t.Fatal(err)
	}
	if rec := post(t, h, "/ui/projects/acme/members", url.Values{"user": {"bob"}, "delivery": {"discord:dm-bob\n"}}); rec.Code != http.StatusOK {
		t.Fatalf("add member = %d: %s", rec.Code, rec.Body.String())
	}
	p, _ := store.GetProject("acme")
	bob := userID(t, store, "bob")
	if ms, ok := store.GetMembership(p.ID, bob); !ok || len(ms.Delivery) != 1 || ms.Delivery[0].Address != "dm-bob" {
		t.Fatalf("bob membership = %+v, %v", ms, ok)
	}
	for name, d := range map[string]string{"no address": "discord", "user id": "discord:dm-bob:123"} {
		if rec := post(t, h, "/ui/projects/acme/members", url.Values{"user": {"bob"}, "delivery": {d}}); rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", name, rec.Code)
		}
	}
	if rec := del(t, h, "/ui/projects/acme/members/"+string(bob)); rec.Code != http.StatusOK {
		t.Fatalf("remove member = %d", rec.Code)
	}
	if store.IsMember(p.ID, bob) {
		t.Error("bob is still a member")
	}
}

func TestActorsPageReplacesRoster(t *testing.T) {
	h := projHandler(seedProjects(t))
	if rec := get(t, h, "/ui/actors"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `aria-current="page">Actors`) {
		t.Fatalf("actors page = %d", rec.Code)
	}
	if rec := get(t, h, "/ui/roster"); rec.Code != http.StatusNotFound {
		t.Errorf("/ui/roster = %d, want 404", rec.Code)
	}
}
