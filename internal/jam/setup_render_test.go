package jam

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/modelspec"
	"github.com/aethons-tools/cove/internal/kit"
	"github.com/aethons-tools/cove/internal/studio"
)

var updateSetupGolden = flag.Bool("update-setup", false, "rewrite internal/jam/testdata/setup/*.golden")

type fakeImages struct {
	facts map[string]ImageFacts
	err   error
	calls []string
}

func (f *fakeImages) ImageFacts(_ context.Context, tag string) (ImageFacts, error) {
	f.calls = append(f.calls, tag)
	if f.err != nil {
		return ImageFacts{}, f.err
	}
	x, ok := f.facts[tag]
	if !ok {
		return ImageFacts{}, ErrImageNotPresent
	}
	return x, nil
}

// Secret values the setup fixture plants; none may appear in any render,
// JSON or stored snapshot.
const (
	secretDestEnv      = "ghp_SECRETLITERAL"
	secretImageEnv     = "hunter2-SECRET"
	secretUpstreamPass = "Upstr3amPass"
	secretBuildArg     = "npmBUILDARGsecret"
	secretMCPHeader    = "MCPHDRVALUEREF" // header values must be ${VAR} refs; even those are never shown
	secretMCPArg       = "mcpARGsecret"
	secretPrincipalHdr = "principalHDRsecret"
	secretSettingsEnv  = "settingsENVsecret"
	secretProviderEnv  = "providerENVsecret"
	secretClaudeMD     = "ghp_CLAUDEMDtoken0123456789"
)

// setupFixture raises studio w1 of role dev (the my-app → web-dev → default
// chain) with two destinations — one holding a literal secret in its env and
// userinfo in its upstream — a model-spec carrying secrets in its principal
// headers and settings env, a kit with a secret build arg and MCP header/arg,
// and an image whose ENV, labels, settings.json and CLAUDE.md hold secrets.
func setupFixture(t *testing.T) (SetupDeps, Instance, []string, *fakeImages) {
	t.Helper()
	sup, store, fl := chainRaiseKit(t)
	fl.asm = "asm1"
	_ = store.AddDestination(Destination{Name: "anthropic", Route: "/anthropic/", Upstream: "https://api.anthropic.com", IdentityIn: ApplyXAPIKey, Apply: ApplyXAPIKey, CredName: "anthropic-key"})
	_ = store.AddDestination(Destination{Name: "gh", Route: "/gh/", Upstream: "https://bot:" + secretUpstreamPass + "@api.github.com", IdentityIn: ApplyBearer,
		Apply: ApplyCustom, ApplySpec: &OutboundSpec{Header: "Authorization", Template: "token {cred}"}, CredName: "gh-pat",
		Env: map[string]string{"GH_TOKEN": secretDestEnv, "GH_HOST": "{host}"}, Git: true})
	// my-app v2: a secret build arg and an MCP server with a secret header and arg.
	app := studio.StudioKit{Kind: studio.Kind, Prompt: "leaf prompt", Egress: []string{"npmjs.org"}, BuildArgs: map[string]string{"NODE_VERSION": "22", "NPM_TOKEN": secretBuildArg},
		Notes: []studio.KitNote{{Name: "b.md", ReadWhen: "leaf b", Body: "LEAF-B"}, {Name: "c.md", ReadWhen: "leaf c", Body: "LEAF-C"}},
		MCPServers: map[string]kit.MCPServer{"api": {Type: "stdio", Command: "api-mcp", Args: []string{"--token", secretMCPArg}},
			"remote": {Type: "http", URL: "https://mcp.example.com/mcp", Headers: map[string]string{"Authorization": "Bearer ${" + secretMCPHeader + "}"}}}}
	app.Base.Kit = "web-dev"
	mustEnsure(t, store, "my-app", app)
	ms := ModelSpec{Name: "spec1", Type: "claude", Version: "2.1.0"}
	ms.Principal.Credential = "anthropic-key"
	ms.Principal.Headers = []modelspec.HeaderRule{{Name: "X-Extra", Set: secretPrincipalHdr}}
	ms.Claude = &ClaudeSpec{Provider: "anthropic", ProviderEnv: map[string]string{"VERTEX_API_KEY": secretProviderEnv},
		Settings: map[string]any{"env": map[string]any{"FOO_TOKEN": secretSettingsEnv}}}
	if err := store.PutModelSpec(ms); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRole("default", Role{Name: "dev", Kit: "my-app", ModelSpec: "spec1", Scope: Scope{Destinations: []string{"anthropic", "gh"}, Credentials: map[string]string{"gh": "gh-bot"}, TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	inst, tok, launch, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Project: "default", Role: "dev", Prompt: "P"})
	if err != nil {
		t.Fatal(err)
	}
	facts := seedFacts() // Task 7's fixture: DB_PASSWORD, u:p@ label, ghp_SETTINGS
	facts.Env = append(facts.Env, "API_KEY="+secretImageEnv)
	for i, s := range facts.Seed {
		if s.Path == "CLAUDE.md" {
			s.Text += "deploy with " + secretClaudeMD + "\n"
			s.Size = int64(len(s.Text))
			facts.Seed[i] = s
		}
	}
	img := &fakeImages{facts: map[string]ImageFacts{inst.ImageTag: facts}}
	return SetupDeps{Store: store, Sup: sup, Images: img, SealedEgress: []string{".anthropic.com", ".github.com"}}, inst, []string{tok, launch}, img
}

var (
	hexRun   = regexp.MustCompile(`[0-9a-f]{12,}`)
	pushedAt = regexp.MustCompile(`pushed \d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ`) // kit pushes use the wall clock
)

func checkGolden(t *testing.T, name string, s StudioSetup) {
	t.Helper()
	var b strings.Builder
	RenderSetupText(&b, s)
	got := hexRun.ReplaceAllString(b.String(), "<hex>") // digests move with the harness default; the shape must not
	got = pushedAt.ReplaceAllString(got, "pushed <time>")
	path := filepath.Join("testdata", "setup", name+".golden")
	if *updateSetupGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update-setup once, then review the file)", err)
	}
	if got != string(want) {
		t.Fatalf("%s render changed (run with -update-setup and review the diff):\n%s", name, got)
	}
}

func TestRoleSetupGolden(t *testing.T) {
	d, _, _, _ := setupFixture(t)
	s, err := RoleSetup(context.Background(), d, "default", "dev", SetupSectionNames())
	if err != nil {
		t.Fatal(err)
	}
	checkGolden(t, "role", s)
}

func TestInstanceSetupGolden(t *testing.T) {
	d, _, _, _ := setupFixture(t)
	s, err := InstanceSetup(context.Background(), d, "w1", SetupSectionNames(), true)
	if err != nil {
		t.Fatal(err)
	}
	checkGolden(t, "studio", s)
}

func TestSetupNeverShowsSecrets(t *testing.T) {
	d, inst, toks, _ := setupFixture(t)
	role, err := RoleSetup(context.Background(), d, "default", "dev", SetupSectionNames())
	if err != nil {
		t.Fatal(err)
	}
	studioSetup, err := InstanceSetup(context.Background(), d, "w1", SetupSectionNames(), true)
	if err != nil {
		t.Fatal(err)
	}
	// A pre-recording studio's recomputed parts must be just as clean.
	old := inst
	old.ActorID, old.ImageTag = "old", ""
	if err := d.Store.PutInstance(old); err != nil {
		t.Fatal(err)
	}
	recomputed, err := InstanceSetup(context.Background(), d, "old", SetupSectionNames(), true)
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	for _, s := range []StudioSetup{role, studioSetup, recomputed} {
		RenderSetupText(&text, s)
	}
	js, _ := json.Marshal([]StudioSetup{role, studioSetup, recomputed})
	stored, _ := json.Marshal(d.Store.SetupSnapshots("w1"))
	if len(d.Store.SetupSnapshots("w1")) != 4 {
		t.Fatalf("the raise must record four parts: %s", stored)
	}
	secrets := append([]string{secretDestEnv, secretImageEnv, "hunter2", "ghp_SETTINGS", "u:p@", "oauthAccount", secretUpstreamPass,
		"bot:", secretBuildArg, secretMCPHeader, secretMCPArg, secretPrincipalHdr, secretSettingsEnv, secretProviderEnv, secretClaudeMD}, toks...)
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		for where, s := range map[string]string{"text": text.String(), "json": string(js), "snapshots": string(stored)} {
			if strings.Contains(s, secret) {
				t.Errorf("%q in the %s", secret, where)
			}
		}
	}
	for _, want := range []string{"GH_TOKEN=«masked»", "credential:  gh-bot", "NPM_TOKEN=«masked»", "ANTHROPIC_API_KEY=«identity token»"} {
		if !strings.Contains(text.String(), want) {
			t.Errorf("credentials must appear by name and secrets as marks: %q missing", want)
		}
	}
}

func TestInstanceSetupWithoutSnapshots(t *testing.T) {
	d, inst, _, _ := setupFixture(t)
	old := inst
	old.ActorID, old.ImageTag = "old", "" // raised before setup recording and image tags
	if err := d.Store.PutInstance(old); err != nil {
		t.Fatal(err)
	}
	s, err := InstanceSetup(context.Background(), d, "old", SetupSectionNames(), false)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]SetupSection{}
	for _, sec := range s.Sections {
		got[sec.Name] = sec
	}
	for _, n := range []string{SectionContext, SectionEgress, SectionEnv, SectionKit} {
		if got[n].Source != SourceRecomputed || got[n].Reason != reasonNoSnapshot || got[n].Body == "" {
			t.Errorf("%s = %+v, want recomputed with the fixed reason", n, got[n])
		}
	}
	if got[SectionSeed].Source != SourceUnavailable || got[SectionSeed].Reason != reasonImageUnknown {
		t.Errorf("seed = %+v", got[SectionSeed])
	}
	if len(s.Sections) != len(SetupSectionNames()) {
		t.Fatalf("every section must render: %d", len(s.Sections))
	}
	// The recompute is redacted exactly like the stored snapshot.
	if want := d.Store.SetupSnapshots("w1"); len(want) == 0 {
		t.Fatal("no snapshots for w1")
	} else {
		for _, sn := range want {
			if sn.Part == SetupPartContext && got[SectionContext].Body != sn.Body {
				t.Errorf("recomputed context differs from the snapshot:\n%s\n---\n%s", got[SectionContext].Body, sn.Body)
			}
		}
	}
}

func TestInstanceSetupStaleAndDiff(t *testing.T) {
	d, _, _, _ := setupFixture(t)
	if _, err := SetRoleEgress(d.Store, "default", "dev", []string{"github.com", "npmjs.org"}); err != nil {
		t.Fatal(err)
	}
	s, err := InstanceSetup(context.Background(), d, "w1", []string{SectionIdentity, SectionEgress}, true)
	if err != nil {
		t.Fatal(err)
	}
	if s.Sections[0].Diffed {
		t.Error("identity is never diffed")
	}
	eg := s.Sections[1]
	if !eg.Stale || eg.Source != SourceSnapshot || !eg.Diffed {
		t.Fatalf("egress = %+v, want a stale, diffed snapshot", eg)
	}
	has := func(op, text string) bool {
		for _, l := range eg.Diff {
			if l.Op == op && strings.Contains(l.Text, text) {
				return true
			}
		}
		return false
	}
	if !has("-", "policy: kit default") || !has("+", "policy: role (2 domain(s))") {
		t.Fatalf("diff = %+v", eg.Diff)
	}
	js, _ := json.Marshal(eg)
	if !strings.Contains(string(js), `"op":"-"`) || !strings.Contains(string(js), `"text":"policy: kit default"`) {
		t.Errorf("diff lines must marshal as op/text: %s", js)
	}
}

func TestSetupImageUnavailable(t *testing.T) {
	d, inst, _, _ := setupFixture(t)
	seedErr := seedFacts()
	seedErr.Seed, seedErr.SeedErr = nil, true
	for _, c := range []struct {
		images    ImageFactsReader
		reason    string
		envReason string
	}{
		{nil, reasonNoLauncher, reasonNoLauncher},
		{&fakeImages{err: ErrImageUnreadable}, reasonImageError, reasonImageError},
		{&fakeImages{}, reasonNoImage, reasonNoImage},
		{&fakeImages{facts: map[string]ImageFacts{inst.ImageTag: seedErr}}, reasonSeedUnreadable, ""},
	} {
		d.Images = c.images
		s, err := InstanceSetup(context.Background(), d, "w1", []string{SectionEnv, SectionSeed}, false)
		if err != nil {
			t.Fatal(err)
		}
		if seed := s.Sections[1]; seed.Source != SourceUnavailable || seed.Reason != c.reason {
			t.Errorf("seed = %+v, want unavailable: %s", seed, c.reason)
		}
		env := s.Sections[0]
		if env.Source != SourceSnapshot {
			t.Errorf("env = %+v", env)
		}
		if c.envReason != "" && !strings.Contains(env.Body, "image env: unavailable ("+c.envReason+")") {
			t.Errorf("env = %+v", env)
		}
		if c.envReason == "" && !strings.Contains(env.Body, "DB_PASSWORD=«masked»") {
			t.Errorf("a seed error must not hide the image env: %+v", env)
		}
	}
	if reasonNoImage != ErrImageNotPresent.Error() || reasonImageError != ErrImageUnreadable.Error() {
		t.Error("image reasons must be the error values' text")
	}
	if reasonSeedUnreadable != "seed unreadable: docker error on the launcher host" {
		t.Errorf("seed reason = %q", reasonSeedUnreadable)
	}
}

func TestSetupDoesNotMutateImageFacts(t *testing.T) {
	d, inst, _, img := setupFixture(t)
	before, _ := json.Marshal(img.facts[inst.ImageTag])
	if _, err := InstanceSetup(context.Background(), d, "w1", SetupSectionNames(), true); err != nil {
		t.Fatal(err)
	}
	if _, err := RoleSetup(context.Background(), d, "default", "dev", SetupSectionNames()); err != nil {
		t.Fatal(err)
	}
	if after, _ := json.Marshal(img.facts[inst.ImageTag]); string(after) != string(before) {
		t.Errorf("image facts mutated:\n%s\n%s", before, after)
	}
}

func TestInstanceSetupRoleGone(t *testing.T) {
	d, inst, _, _ := setupFixture(t)
	ghost := inst
	ghost.ActorID, ghost.Role = "g1", "ghost"
	if err := d.Store.PutInstance(ghost); err != nil {
		t.Fatal(err)
	}
	s, err := InstanceSetup(context.Background(), d, "g1", SetupSectionNames(), true)
	if err != nil {
		t.Fatal(err)
	}
	if s.Note != "the role no longer exists: no diff" {
		t.Errorf("note = %q", s.Note)
	}
	for _, sec := range s.Sections {
		if sec.Diffed {
			t.Errorf("%s diffed against a missing role", sec.Name)
		}
		if sec.Name == SectionPolicy && (sec.Source != SourceUnavailable || sec.Reason != reasonRoleGone || !strings.Contains(sec.Body, "idle override:")) {
			t.Errorf("policy = %+v", sec)
		}
	}
}

func TestSetupSectionsFilterAndNotFound(t *testing.T) {
	d, _, _, img := setupFixture(t)
	img.calls = nil
	s, err := RoleSetup(context.Background(), d, "default", "dev", []string{SectionEgress})
	if err != nil || len(s.Sections) != 1 || s.Sections[0].Name != SectionEgress || !strings.Contains(s.Sections[0].Body, ".anthropic.com  sealed") {
		t.Fatalf("filtered = %+v, %v", s, err)
	}
	if len(img.calls) != 0 {
		t.Fatalf("an egress-only inspect read the image: %v", img.calls)
	}
	if _, err := RoleSetup(context.Background(), d, "default", "nope", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown role err = %v", err)
	}
	if _, err := InstanceSetup(context.Background(), d, "nope", nil, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown studio err = %v", err)
	}
	d.Sup = nil
	s, err = RoleSetup(context.Background(), d, "default", "dev", []string{SectionContext, SectionKit})
	if err != nil || s.Sections[0].Reason != reasonNoSupervisor || s.Sections[1].Reason != reasonNoSupervisor {
		t.Fatalf("no supervisor = %+v, %v", s, err)
	}
}
