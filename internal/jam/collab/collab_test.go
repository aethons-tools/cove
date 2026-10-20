package collab_test

import (
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/collab"
)

func mustProject(t *testing.T, st jam.Store, name string) ident.ID {
	t.Helper()
	if err := st.CreateProject(name); err != nil {
		t.Fatal(err)
	}
	p, _ := st.GetProject(name)
	return p.ID
}

// mustUser creates a user and makes it a member of each project given.
func mustUser(t *testing.T, st jam.Store, name string, projects ...ident.ID) ident.ID {
	t.Helper()
	u, err := st.CreateUser(jam.User{Name: name})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range projects {
		if err := st.AddMember(p, u.ID); err != nil {
			t.Fatal(err)
		}
	}
	return u.ID
}

func mustRole(t *testing.T, st jam.Store, project ident.ID, r jam.Role) {
	t.Helper()
	if err := st.PutRole(string(project), r); err != nil {
		t.Fatal(err)
	}
}

func TestDecideMatrix(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	st := jam.NewMemStore()
	proj := mustProject(t, st, "acme")
	alice := mustUser(t, st, "alice", proj)
	bob := mustUser(t, st, "bob", proj)
	carol := mustUser(t, st, "carol")
	mustRole(t, st, proj, jam.Role{Name: "ic"})
	mustRole(t, st, proj, jam.Role{Name: "pair", Collaborate: jam.CollaboratePolicy{SSH: "members"}})
	mustRole(t, st, proj, jam.Role{Name: "quiet", Collaborate: jam.CollaboratePolicy{Preview: "none"}})
	personal := jam.Instance{ActorID: "ses_p", Project: "acme", Role: "ic", SessionKind: jam.SessionKindPersonal, OwnerID: alice}
	ic := jam.Instance{ActorID: "ses_i", Project: "acme", Role: "ic"}
	pair := jam.Instance{ActorID: "ses_x", Project: "acme", Role: "pair"}
	quiet := jam.Instance{ActorID: "ses_q", Project: "acme", Role: "quiet"}
	_ = st.PutShareGrant(jam.ShareGrant{SessionID: "ses_i", UserID: carol, Rights: []string{"ssh"}, ExpiresAt: now.Add(time.Hour), GrantedBy: "op:local"})
	_ = st.PutShareGrant(jam.ShareGrant{SessionID: "ses_i", UserID: bob, Rights: []string{"ssh"}, ExpiresAt: now.Add(-time.Minute), GrantedBy: "op:local"})

	for _, tc := range []struct {
		name string
		p    collab.Principal
		inst jam.Instance
		r    collab.Right
		want collab.Reason
		ok   bool
	}{
		{"operator any", collab.Principal{Operator: "op1"}, ic, collab.SSH, "operator", true},
		{"requester own personal", collab.Principal{UserID: alice}, personal, collab.SSH, "requester", true},
		{"member default ssh none", collab.Principal{UserID: bob}, ic, collab.SSH, "", false},
		{"member default preview", collab.Principal{UserID: bob}, ic, collab.Preview, "role", true},
		{"member preview none", collab.Principal{UserID: bob}, quiet, collab.Preview, "", false},
		{"member role allows ssh", collab.Principal{UserID: bob}, pair, collab.SSH, "role", true},
		{"non-member role policy", collab.Principal{UserID: carol}, pair, collab.SSH, "", false},
		{"grant holder", collab.Principal{UserID: carol}, ic, collab.SSH, "grant", true},
		{"grant other session", collab.Principal{UserID: carol}, pair, collab.SSH, "", false},
		{"expired grant", collab.Principal{UserID: bob}, ic, collab.SSH, "", false},
		{"grant without that right", collab.Principal{UserID: carol}, ic, collab.Preview, "", false},
		{"nobody", collab.Principal{}, ic, collab.Preview, "", false},
	} {
		got, ok := collab.Decide(st, now, tc.p, tc.inst, tc.r)
		if ok != tc.ok || got != tc.want {
			t.Errorf("%s: Decide = (%q, %v), want (%q, %v)", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

func TestValidateGrant(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	ok := jam.ShareGrant{SessionID: "s", UserID: "usr_1", Rights: []string{"ssh"}, ExpiresAt: now.Add(collab.DefaultGrant)}
	if err := collab.ValidateGrant(now, ok); err != nil {
		t.Fatal(err)
	}
	for name, g := range map[string]jam.ShareGrant{
		"bad right": {SessionID: "s", UserID: "usr_1", Rights: []string{"root"}, ExpiresAt: now.Add(time.Hour)},
		"too long":  {SessionID: "s", UserID: "usr_1", Rights: []string{"ssh"}, ExpiresAt: now.Add(collab.MaxGrant + time.Hour)},
		"past":      {SessionID: "s", UserID: "usr_1", Rights: []string{"ssh"}, ExpiresAt: now.Add(-time.Hour)},
		"no rights": {SessionID: "s", UserID: "usr_1", ExpiresAt: now.Add(time.Hour)},
	} {
		if collab.ValidateGrant(now, g) == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
