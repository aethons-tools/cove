package jam

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

type jamFixtureIDs struct{ bob, alice ident.ID }

// populatedJam builds a MemStore through the store and admin functions that
// holds one of everything a config repo can describe.
func populatedJam(t *testing.T) (*MemStore, jamFixtureIDs) {
	t.Helper()
	st := NewMemStore()
	creds := credIs("gh-pat", "anthropic", "acme-bot")
	must(t, CreateDestination(st, Destination{Name: "gh", Route: "/api/v3/", Upstream: "https://api.github.com", CredName: "gh-pat",
		Env: map[string]string{"GH_HOST": "{host}"}, AllowPaths: []string{"/repos/*"}}, creds))

	must(t, CreateDestination(st, Destination{Name: "anthropic", Route: "/anthropic/", Upstream: "https://api.anthropic.com", CredName: "anthropic"}, creds))

	fast := validSpec()
	fast.Name = "fast"
	must(t, CreateModelSpec(st, fast, creds, false))
	if _, err := EnsureDefaultModelSpec(st, false); err != nil {
		t.Fatal(err)
	}
	def, _ := st.GetModelSpec(DefaultModelSpec)
	def.Version = "2.2.0"
	must(t, UpdateModelSpec(st, def, creds, false))

	bob, err := st.CreateUser(User{ID: ident.New(ident.User), Name: "bob", Status: StatusLive, Logins: []string{"bob@acme"}})
	must(t, err)
	alice, err := st.CreateUser(User{ID: ident.New(ident.User), Name: "alice", Status: StatusLive})
	must(t, err)
	conn, err := st.CreateConnection(Connection{ID: ident.New(ident.Connection), Kind: "discord", Name: "acme", CredName: "acme-bot", Status: StatusLive})
	must(t, err)

	if _, _, err := PushStudioKit(st, "web-dev", "kind: studio\n"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := PushStudioKit(st, "app", "kind: studio\nbase:\n  kit: web-dev@v1\n"); err != nil {
		t.Fatal(err)
	}

	must(t, st.CreateProject("app"))
	p, _ := st.GetProject("app")
	// The chat service is set on the record directly so the exporter's
	// id → name mapping is exercised.
	st.mu.Lock()
	p.ChatService = string(conn.ID)
	st.applyPutProject(p)
	st.mu.Unlock()
	must(t, st.SetEscalationPolicy("app", "", []EscalationTier{{Targets: []string{"user:" + string(bob.ID)}, Timeout: time.Hour}}))
	must(t, st.PutMembership(Membership{ProjectID: p.ID, UserID: bob.ID, Delivery: []DeliveryProfile{{Service: "discord", Address: "bob#1"}}}))
	_, err = st.CreateChannel(Channel{ID: ident.New(ident.Channel), ProjectID: p.ID, Kind: SourceRoom, Key: "ops", Label: "Ops", Status: StatusLive,
		Bindings: []Binding{{ConnectionID: conn.ID, Ref: "ACME-1", Mode: BindBoth}}})
	must(t, err)
	must(t, st.SetProjectContext("app", sessionctx.Layer{Core: "goals", Leaves: []sessionctx.Leaf{{Name: "arch.md", ReadWhen: "designing", Body: "body"}}},
		[]sessionctx.Resource{{Name: "repo", Kind: "repo", Ref: "github.com/acme/app"}}))

	must(t, st.PutRole("app", Role{
		Name: "ic", Kit: "app", ModelSpec: "fast",
		Scope:       Scope{Destinations: []string{"gh"}, TTL: time.Hour, Egress: &EgressPolicy{Domains: []string{"example.com"}}, Addressing: []string{"user:" + string(alice.ID)}},
		Allocation:  RoleAllocation{MaxPersonal: 2, Standing: []StandingSession{{Name: "watch", Prompt: "watch ci"}}},
		TurnEnd:     TurnEndPolicy{IdleTimeout: 30 * time.Minute, OnIdle: "teardown"},
		Collaborate: CollaboratePolicy{Preview: "members"},
		Context:     sessionctx.Layer{Core: "role rules"},
	}))
	must(t, st.SetJamContext(sessionctx.Layer{Core: "jam rules"}))
	return st, jamFixtureIDs{bob: bob.ID, alice: alice.ID}
}

// exportedKitYML is kit name's kit.yml in exp's kit dirs; ok=false: not written.
func exportedKitYML(exp ConfigTreeExport, name string) (string, bool) {
	for _, f := range exp.Kits["kits/"+name] {
		if f.Path == "kit.yml" {
			return string(f.Data), true
		}
	}
	return "", false
}

func TestExportConfigTreeAll(t *testing.T) {
	st, ids := populatedJam(t)
	exp, err := ExportConfigTree(st.ExportConfig(), nil, map[string]any{"display-name": "Aethon"}, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"jam.yml", "context.md", "destinations/gh.yml", "model-specs/fast.yml", "model-specs/" + DefaultModelSpec + ".yml", "users/bob.yml",
		"users/alice.yml", "connections/acme.yml",
		"projects/app/project.yml", "projects/app/context.md", "projects/app/roles/ic.yml"} {
		if _, ok := exp.Files[p]; !ok {
			t.Errorf("missing %s", p)
		}
	}
	for _, n := range []string{"web-dev", "app"} {
		if _, ok := exportedKitYML(exp, n); !ok {
			t.Errorf("missing kits/%s/kit.yml", n)
		}
	}
	proj := exp.Files["projects/app/project.yml"]
	for _, want := range []string{"chat-service: acme\n", "- user:bob\n", "user: bob\n", "connection: acme\n", "timeout: 1h\n"} {
		if !strings.Contains(proj, want) {
			t.Errorf("project.yml lacks %q:\n%s", want, proj)
		}
	}
	if strings.Contains(proj, string(ids.bob)) || strings.Contains(proj, "usr_") || strings.Contains(proj, "con_") {
		t.Fatalf("ids leaked:\n%s", proj)
	}
	if yml, _ := exportedKitYML(exp, "app"); !strings.Contains(yml, "kit: ../web-dev\n") {
		t.Fatalf("path parent not rendered:\n%s", yml)
	}
	if strings.Contains(exp.Files["users/bob.yml"], "oidc") {
		t.Fatal("identities written")
	}
	if role := exp.Files["projects/app/roles/ic.yml"]; strings.Contains(role, "usr_") || !strings.Contains(role, "user:alice") {
		t.Fatalf("role addressing not named:\n%s", role)
	}
}

func TestExportConfigTreeDeterministic(t *testing.T) {
	st, _ := populatedJam(t)
	a, _ := ExportConfigTree(st.ExportConfig(), nil, nil, true)
	b, _ := ExportConfigTree(st.ExportConfig(), nil, nil, true)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("two exports differ")
	}
}

func TestExportConfigTreeManagedOnly(t *testing.T) {
	st, _ := populatedJam(t)
	marks := []ConfigMark{{Kind: ConfigKindUser, Name: "bob"}, {Kind: ConfigKindKit, Name: "app"},
		{Kind: ConfigKindDestination, Name: "gone"}, {Kind: ConfigKindProject, Name: "old"}}
	exp, _ := ExportConfigTree(st.ExportConfig(), marks, nil, false)
	if _, ok := exp.Files["users/alice.yml"]; ok {
		t.Fatal("unmanaged user written without --all")
	}
	if yml, _ := exportedKitYML(exp, "app"); !strings.Contains(yml, "kit: web-dev@v1\n") {
		t.Fatal("a parent outside the tree must keep its stored ref")
	}
	want := []string{"destinations/gone.yml", "kits/app.source.yml", "projects/old/context.md", "projects/old/project.yml"} // an inline kit's other form
	if !slices.Equal(exp.Remove, want) {
		t.Fatalf("remove = %v, want %v", exp.Remove, want)
	}
}

func TestExportSkipsTombstonesAndEmptyContext(t *testing.T) {
	st, ids := populatedJam(t)
	// bob is a project member and alice is only addressed; remove alice.
	if err := st.RemoveUser(ids.alice); err != nil {
		t.Fatal(err)
	}
	if err := st.SetJamContext(sessionctx.Layer{}); err != nil {
		t.Fatal(err)
	}
	exp, _ := ExportConfigTree(st.ExportConfig(), nil, nil, true)
	if _, ok := exp.Files["users/alice.yml"]; ok {
		t.Fatal("tombstone written")
	}
	if _, ok := exp.Files["context.md"]; ok {
		t.Fatal("empty jam context written")
	}
}

func TestExportAllRemovesEmptyProjectContext(t *testing.T) {
	st, _ := populatedJam(t)
	must(t, st.SetProjectContext("app", sessionctx.Layer{}, nil))
	exp, _ := ExportConfigTree(st.ExportConfig(), nil, nil, true)
	if !slices.Contains(exp.Remove, "projects/app/context.md") {
		t.Fatalf("remove = %v", exp.Remove)
	}
}

// A kit pinned behind its in-tree parent's current version cannot be written
// as a path parent (apply would move it onto the parent's latest), so the
// export fails with the remedy.
func TestExportConfigTreeRefusesBehindPin(t *testing.T) {
	st, _ := populatedJam(t)
	if _, _, err := PushStudioKit(st, "web-dev", "kind: studio\nprompt: v2\n"); err != nil {
		t.Fatal(err)
	}
	_, err := ExportConfigTree(st.ExportConfig(), nil, nil, true)
	want := "kit app pins web-dev@v1 but web-dev is at v2; versions are per-Jam — build app on web-dev by name (its current version) before config write"
	if !errors.Is(err, ErrConfigKitPinBehind) || err.Error() != want {
		t.Fatalf("err = %v\nwant %s", err, want)
	}
	// A parent outside the tree keeps the stored pin.
	if _, err := ExportConfigTree(st.ExportConfig(), []ConfigMark{{Kind: ConfigKindKit, Name: "app"}}, nil, false); err != nil {
		t.Fatal(err)
	}
}

// A kit the write leaves out is neither rendered nor checked: only the
// managed parent is written, so the unmanaged behind-pinned child is no
// refusal.
func TestExportConfigTreeIgnoresUnwrittenBehindPin(t *testing.T) {
	st, _ := populatedJam(t)
	if _, _, err := PushStudioKit(st, "web-dev", "kind: studio\nprompt: v2\n"); err != nil {
		t.Fatal(err)
	}
	exp, err := ExportConfigTree(st.ExportConfig(), []ConfigMark{{Kind: ConfigKindKit, Name: "web-dev"}}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := exportedKitYML(exp, "web-dev"); !ok {
		t.Fatal("managed parent not written")
	}
	if _, ok := exp.Kits["kits/app"]; ok {
		t.Fatal("unmanaged child written")
	}
	if _, err := ExportConfigTree(st.ExportConfig(), []ConfigMark{{Kind: ConfigKindKit, Name: "web-dev"}}, nil, true); !errors.Is(err, ErrConfigKitPinBehind) {
		t.Fatalf("--all: err = %v, want ErrConfigKitPinBehind", err)
	}
}

// With several behind-pinned kits the refusal names the first by name.
func TestExportConfigTreeBehindPinDeterministic(t *testing.T) {
	st, _ := populatedJam(t)
	if _, _, err := PushStudioKit(st, "zed", "kind: studio\nbase:\n  kit: web-dev@v1\n"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := PushStudioKit(st, "web-dev", "kind: studio\nprompt: v2\n"); err != nil {
		t.Fatal(err)
	}
	for range 20 {
		if _, err := ExportConfigTree(st.ExportConfig(), nil, nil, true); err == nil || !strings.HasPrefix(err.Error(), "kit app pins") {
			t.Fatalf("err = %v", err)
		}
	}
}

func TestExportRendersTrackedKitAsSourceFile(t *testing.T) {
	st, _ := trackedFixture(t)
	exp, err := ExportConfigTree(st.ExportConfig(), nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := exp.Files["kits/web-dev.source.yml"]; got != "kind: kit-source\nversion: 1\ngit: https://github.com/acme/kits//web@main\n" {
		t.Fatalf("source file %q", got)
	}
	if files, ok := exp.Kits["kits/web-dev"]; !ok || len(files) != 0 {
		t.Fatalf("a tracked kit's directory must be mirrored to nothing: %+v", exp.Kits)
	}
}

func TestKitCanonCarriesTrackedSource(t *testing.T) {
	st, _ := trackedFixture(t)
	objs, err := exportObjects(stateFromSnapshot(st.ExportConfig(), nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := LoadKitVersion(st, "web-dev", 0)
	untracked := string(kitCanon(v.Tree, st.KitBlob, nil))
	if got := string(objs[ConfigKey{ConfigKindKit, "web-dev"}].Canon); got != untracked+"# source: https://github.com/acme/kits//web@main\n" {
		t.Fatalf("canon %q", got)
	}
	if !strings.HasPrefix(untracked, string(v.KitYML)) || strings.Contains(untracked, "# source:") {
		t.Fatalf("an untracked kit's canon: %q", untracked)
	}
}

func TestExportRemovesAManagedKitsOtherForm(t *testing.T) {
	st, _ := trackedFixture(t)
	if _, _, err := PushStudioKit(st, "app", "kind: studio\n"); err != nil {
		t.Fatal(err)
	}
	marks := []ConfigMark{{Kind: ConfigKindKit, Name: "web-dev"}, {Kind: ConfigKindKit, Name: "app"}, {Kind: ConfigKindKit, Name: "gone"}}
	exp, err := ExportConfigTree(st.ExportConfig(), marks, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"kits/app.source.yml", "kits/gone.source.yml"}
	if !slices.Equal(exp.Remove, want) {
		t.Fatalf("remove %v", exp.Remove)
	}
	for _, d := range []string{"kits/gone", "kits/web-dev"} { // gone from Jam; written by reference
		if files, ok := exp.Kits[d]; !ok || len(files) != 0 {
			t.Fatalf("%s must be mirrored to nothing: %+v", d, exp.Kits)
		}
	}
}

func TestExportRefusesPinToSourceKit(t *testing.T) {
	st, _ := trackedFixture(t)
	if _, _, err := PushStudioKit(st, "app", "kind: studio\nbase:\n  kit: web-dev@v1\n"); err != nil {
		t.Fatal(err)
	}
	_, err := ExportConfigTree(st.ExportConfig(), nil, nil, true)
	want := "kit app pins web-dev@v1, a kit from its own source (kits/web-dev.source.yml); versions are per-Jam — base app on web-dev (unpinned) before config write"
	if !errors.Is(err, ErrConfigKitPinBehind) || err.Error() != want {
		t.Fatalf("%v", err)
	}
}
