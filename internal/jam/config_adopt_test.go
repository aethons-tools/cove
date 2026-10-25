package jam

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam/gitsource"
	"github.com/aethons-tools/cove/internal/jam/sessionctx"
	"github.com/aethons-tools/cove/internal/kittree/kittreetest"
)

// adoptJam is a Jam with every object shape an operator's Jam has: custom
// and preset header specs, env templates, git, allow paths, notes; an
// uppercase project with members, rooms, escalation, resources, chat
// service and context; a kit with a Dockerfile; a kit tracked from git —
// and values that do not survive a naive YAML round trip.
func adoptJam(t *testing.T) (*MemStore, KitFetcher) {
	t.Helper()
	st := NewMemStore()
	creds := func(string) bool { return true }
	dests := []Destination{
		{Name: "nea-atlassian", Route: "/atlassian/", Upstream: "https://nea.atlassian.net",
			IdentityIn: ApplyBearer, CredName: "atlassian-token",
			Apply:      ApplyCustom,
			ApplySpec:  &OutboundSpec{Header: "Authorization", Template: "{cred}", Encoding: EncodingBasic, BasicUser: "bot@nea.example"},
			Env:        map[string]string{"JIRA_URL": "{url}", "JIRA_API_TOKEN": "{token}", "CONFLUENCE_URL": "{url}/wiki"},
			AllowPaths: []string{"/rest/api/3/*", "/wiki/rest/api/*"},
			Note:       "Use the jira CLI with JIRA_URL.\nConfluence lives under /wiki.\n"},
		// Values yaml.v3's own style loses or cannot write: a leading
		// newline, and a multi-line value opening with a tab.
		{Name: "odd", Route: "/odd/", Upstream: "https://odd.example", IdentityIn: ApplyRaw, CredName: "odd", Apply: ApplyRaw,
			Env: map[string]string{"ODD": "\n\nlead"}, Note: "\tfoo\nbar"},
		{Name: "nea-gitlab-api", Route: "/gitlab/api/", Upstream: "https://gitlab.nea.example/api/v4",
			IdentityIn: ApplyCustom, IdentityInSpec: &InboundSpec{Header: "Private-Token"},
			CredName: "gitlab-pat",
			Apply:    ApplyCustom, ApplySpec: &OutboundSpec{Header: "Private-Token", Template: "{cred}"},
			Env:  map[string]string{"GITLAB_HOST": "{host}", "GITLAB_TOKEN": "{token}", "GITLAB_API_HOST": "{base}"},
			Note: "glab api --hostname $GITLAB_HOST"},
		{Name: "nea-gitlab-git", Route: "/gitlab/git/", Upstream: "https://gitlab.nea.example",
			IdentityIn: ApplyCustom, IdentityInSpec: &InboundSpec{Header: "Authorization", Prefixes: []string{"Bearer ", "token "}, Encoding: "raw"},
			CredName: "gitlab-pat",
			Apply:    ApplyBasicPassword, Git: true},
		{Name: "linear", Route: "/linear/", Upstream: "https://api.linear.app", IdentityIn: ApplyRaw, CredName: "linear", Apply: ApplyRaw},
		{Name: "anthropic", Route: "/anthropic/", Upstream: "https://api.anthropic.com", IdentityIn: ApplyXAPIKey, CredName: "anthropic", Apply: ApplyXAPIKey},
	}
	for _, d := range dests {
		must(t, CreateDestination(st, d, creds))
	}
	if _, err := EnsureDefaultModelSpec(st, false); err != nil {
		t.Fatal(err)
	}

	bob, err := st.CreateUser(User{ID: ident.New(ident.User), Name: "bob", Status: StatusLive, Logins: []string{"bob@nea.example"}})
	must(t, err)
	alice, err := st.CreateUser(User{ID: ident.New(ident.User), Name: "alice", Status: StatusLive})
	must(t, err)
	conn, err := st.CreateConnection(Connection{ID: ident.New(ident.Connection), Kind: "discord", Name: "nea-discord", CredName: "discord-bot", Status: StatusLive})
	must(t, err)

	// A kit directory: a Dockerfile, a .dockerignore, an executable script
	// and a binary file in a subdirectory.
	if _, _, err := PushKitFiles(st, "builder", kittreetest.Files("kit.yml", "kind: studio\n", "Dockerfile", dockerfile, ".dockerignore", "*.log\n",
		"run.sh*", "#!/bin/sh\necho hi\n", "lib/blob.bin", "\xff\xfe\x00\x80\xc3\n")); err != nil {
		t.Fatal(err)
	}
	// A kit pushed first (an earlier config write --all wrote it as
	// kits/ic/kit.yml), then tracked from its own git source.
	kits := newFakeFetcher(t, map[string]string{"main": "c1"}, map[string]map[string]string{"c1": {
		"ic/kit.yml": "kind: studio\nprompt: work the ticket\n",
	}})
	src := gitsource.Source{Repo: kitsRepo, Dir: "ic", Ref: "main"}
	if _, err := ImportKit(context.Background(), st, kits, nil, "ic", src, time.Unix(5, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := TrackKit(context.Background(), st, kits, nil, "ic", src, time.Unix(10, 0)); err != nil {
		t.Fatal(err)
	}

	must(t, st.CreateProject("Conduit"))
	p, _ := st.GetProject("Conduit")
	st.mu.Lock()
	p.ChatService = string(conn.ID)
	st.applyPutProject(p)
	st.mu.Unlock()
	must(t, st.SetEscalationPolicy("Conduit", "", []EscalationTier{
		{Targets: []string{"user:" + string(bob.ID)}, Timeout: 90 * time.Minute},
		{Targets: []string{"user:" + string(alice.ID), "human:ops"}, Timeout: 24 * time.Hour}}))
	must(t, st.SetEscalationPolicy("Conduit", "infra", []EscalationTier{{Targets: []string{"user:" + string(alice.ID)}, Timeout: 30 * time.Second}}))
	must(t, st.PutMembership(Membership{ProjectID: p.ID, UserID: bob.ID, Delivery: []DeliveryProfile{
		{Service: "discord", Address: "bob#1", UserID: "123456789012345678"}, {Service: "email", Address: "bob@nea.example"}}}))
	must(t, st.PutMembership(Membership{ProjectID: p.ID, UserID: alice.ID}))
	_, err = st.CreateChannel(Channel{ID: ident.New(ident.Channel), ProjectID: p.ID, Kind: SourceRoom, Key: "ops", Label: "Ops Room", Status: StatusLive,
		Bindings: []Binding{{ConnectionID: conn.ID, Ref: "111", Mode: BindBoth}, {ConnectionID: conn.ID, Ref: "222", Mode: BindEgress}}})
	must(t, err)
	_, err = st.CreateChannel(Channel{ID: ident.New(ident.Channel), ProjectID: p.ID, Kind: SourceRoom, Key: "dev", Label: "dev", Status: StatusLive})
	must(t, err)
	must(t, st.SetProjectContext("Conduit", sessionctx.Layer{
		Core:   "# Conduit\r\n\r\nShip the pipeline.\r\n", // CRLF, as a browser form posts it
		Leaves: []sessionctx.Leaf{{Name: "arch.md", ReadWhen: "designing a change", Body: "## Arch\n\nbody\n"}}},
		[]sessionctx.Resource{{Name: "repo", Kind: "repo", Ref: "https://gitlab.nea.example/nea/conduit", Note: "\nmain repo"},
			{Name: "board", Kind: "tracker", Ref: "https://nea.atlassian.net/jira/CON"}}))
	must(t, st.PutRole("Conduit", Role{
		Name: "ic", Kit: "ic",
		Scope: Scope{Destinations: []string{"nea-gitlab-api", "nea-gitlab-git", "nea-atlassian"}, TTL: time.Hour,
			Addressing: []string{"user:" + string(alice.ID)}},
		TurnEnd: TurnEndPolicy{IdleTimeout: 30 * time.Minute, OnIdle: "teardown"},
	}))
	must(t, st.SetJamContext(sessionctx.Layer{Core: "jam rules"}))
	return st, repoFetcher{kitsRepo: kits}
}

// exportToDir writes exp under dir as at-jam config write does: Files and
// each kit dir's files, then Remove, then each kit dir mirrored: every file
// there the export does not hold removed (the fixtures put no ignored files
// on disk), and the dirs left empty.
func exportToDir(t *testing.T, dir string, exp ConfigTreeExport) {
	t.Helper()
	writeExport(t, dir, exp)
	for _, p := range exp.Remove {
		if err := os.Remove(filepath.Join(dir, filepath.FromSlash(p))); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	for d, files := range exp.Kits {
		keep := map[string]bool{}
		for _, f := range files {
			keep[f.Path] = true
		}
		root := filepath.Join(dir, filepath.FromSlash(d))
		var dirs []string
		err := filepath.WalkDir(root, func(q string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if e.IsDir() {
				dirs = append(dirs, q)
				return nil
			}
			rel, _ := filepath.Rel(root, q)
			if keep[filepath.ToSlash(rel)] {
				return nil
			}
			return os.Remove(q)
		})
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		for i := len(dirs) - 1; i >= 0; i-- {
			_ = os.Remove(dirs[i])
		}
	}
}

// planDirUnmarked plans the tree in dir against st with no marks, as the
// first apply of a freshly written repo does.
func planDirUnmarked(t *testing.T, st *MemStore, f KitFetcher, dir string) ConfigPlan {
	t.Helper()
	tr, err := LoadConfigTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	FetchConfigKits(context.Background(), f, tr)
	return PlanConfig(ConfigPlanInput{Tree: tr, Snapshot: st.ExportConfig(), Env: allCreds,
		Source: gitsource.Source{Repo: ConfigDirRepo}, Commit: ConfigDirCommit, Now: time.Unix(100, 0)})
}

// notAdopted lists a plan's refusals and every op that is not an adopt,
// with both canonical sides.
func notAdopted(p ConfigPlan) []string {
	out := slices.Clone(p.Refusals)
	for _, op := range p.Ops {
		if op.Action != ConfigAdopt {
			out = append(out, fmt.Sprintf("op %s %s: %s\n--- before:\n%s\n--- after:\n%s", op.Kind, op.Name, op.Action, op.Before, op.After))
		}
	}
	return out
}

// A fresh config write --all of a Jam, planned back against the same Jam
// with no marks, adopts every object.
func TestConfigWriteAllAdopts(t *testing.T) {
	st, f := adoptJam(t)
	exp, err := ExportConfigTree(st.ExportConfig(), nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	exportToDir(t, dir, exp)
	for _, r := range notAdopted(planDirUnmarked(t, st, f, dir)) {
		t.Errorf("%s", r)
	}
}

// A kit written as its directory kits/<n>/ by one config write --all, then
// tracked from its own source, is written as kits/<n>.source.yml by the
// next — which mirrors kits/<n>/ to nothing, so the tree loads and the kit
// adopts.
func TestConfigWriteAllRemovesStaleKitForm(t *testing.T) {
	st := NewMemStore()
	if _, _, err := PushKitFiles(st, "ic", kittreetest.Files("kit.yml", "kind: studio\n", "Dockerfile", dockerfile)); err != nil {
		t.Fatal(err)
	}
	exp, err := ExportConfigTree(st.ExportConfig(), nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(exp.Kits["kits/ic"]) != 2 {
		t.Fatalf("the kit was not written as its directory: %+v", exp.Kits)
	}
	dir := t.TempDir()
	exportToDir(t, dir, exp) // kits/ic/kit.yml and kits/ic/Dockerfile

	kits := newFakeFetcher(t, map[string]string{"main": "c1"}, map[string]map[string]string{"c1": {
		"ic/kit.yml": "kind: studio\nprompt: work the ticket\n",
	}})
	f := repoFetcher{kitsRepo: kits}
	src := gitsource.Source{Repo: kitsRepo, Dir: "ic", Ref: "main"}
	if _, err := ImportKit(context.Background(), st, f, nil, "ic", src, time.Unix(5, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := TrackKit(context.Background(), st, f, nil, "ic", src, time.Unix(10, 0)); err != nil {
		t.Fatal(err)
	}
	exp, err = ExportConfigTree(st.ExportConfig(), nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if files, ok := exp.Kits["kits/ic"]; !ok || len(files) != 0 || exp.Files["kits/ic.source.yml"] == "" {
		t.Fatalf("kits %+v files %v", exp.Kits, exp.Files)
	}
	exportToDir(t, dir, exp)
	if _, err := os.Stat(filepath.Join(dir, "kits/ic")); !os.IsNotExist(err) {
		t.Fatalf("kits/ic left behind: %v", err)
	}
	for _, r := range notAdopted(planDirUnmarked(t, st, f, dir)) {
		t.Errorf("%s", r)
	}

}

// Under --all and for a managed kit alike, the form a kit is not written in
// is removed: its kits/<n>/ dir (mirrored to nothing) for a kit written by
// reference, the .source.yml for one written as its directory.
func TestExportRemovesEveryWrittenKitsOtherForm(t *testing.T) {
	st, _ := trackedFixture(t)
	if _, _, err := PushStudioKit(st, "app", "kind: studio\n"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		marks []ConfigMark
		all   bool
	}{
		{"all", nil, true},
		{"managed", []ConfigMark{{Kind: ConfigKindKit, Name: "web-dev"}, {Kind: ConfigKindKit, Name: "app"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exp, err := ExportConfigTree(st.ExportConfig(), tc.marks, nil, tc.all)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Contains(exp.Remove, "kits/app.source.yml") {
				t.Errorf("remove %v lacks kits/app.source.yml", exp.Remove)
			}
			if web, ok := exp.Kits["kits/web-dev"]; !ok || len(web) != 0 || len(exp.Kits["kits/app"]) == 0 {
				t.Errorf("kits %+v", exp.Kits)
			}
		})
	}
}

// A kits/<n>/ dir with no kit.yml beside kits/<n>.source.yml is refused as
// what it is — left over from the kit's other form — and the source still
// loads; without a .source.yml it has no kit.yml.
func TestLoadConfigTreeKitDirWithoutKitYml(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "kits/ic.source.yml"), string(renderKitSourceFile(KitSource{Repo: kitsRepo, Dir: "ic", Ref: "main"})), 0o644)
	mustWrite(t, filepath.Join(dir, "kits/ic/build.log"), "x", 0o644)
	tr, err := LoadConfigTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(tr.Refusals, []string{"kits/ic/ is left over beside kits/ic.source.yml (only files its .dockerignore excludes); remove it"}) {
		t.Errorf("refusals %v", tr.Refusals)
	}
	if _, ok := tr.KitSources["ic"]; !ok {
		t.Error("the source was dropped")
	}
	if err := os.Remove(filepath.Join(dir, "kits/ic.source.yml")); err != nil {
		t.Fatal(err)
	}
	if tr, err = LoadConfigTree(dir); err != nil || !slices.Equal(tr.Refusals, []string{"kits/ic: no kit.yml"}) {
		t.Errorf("refusals %v %v", tr.Refusals, err)
	}
}

// The same source is the same repo, ref and checkout dir, however the dir
// is spelled.
func TestPlannerTracksSourceCleansDir(t *testing.T) {
	m := newMemState()
	m.kits["ic"] = Kit{Name: "ic", Source: &KitSource{Repo: kitsRepo, Dir: "./ic/", Ref: "main", Commit: "c1"}}
	p := &planner{m: m}
	for _, tc := range []struct {
		at   KitSource
		want bool
	}{
		{KitSource{Repo: kitsRepo, Dir: "ic", Ref: "main", Commit: "c2"}, true},
		{KitSource{Repo: kitsRepo, Dir: "ic", Ref: "dev"}, false},
		{KitSource{Repo: kitsRepo, Dir: "other", Ref: "main"}, false},
		{KitSource{Repo: "https://github.com/acme/other", Dir: "ic", Ref: "main"}, false},
	} {
		if got := p.tracksSource("ic", tc.at); got != tc.want {
			t.Errorf("tracksSource(%+v) = %v", tc.at, got)
		}
	}
	if p.tracksSource("missing", KitSource{Repo: kitsRepo, Dir: "ic", Ref: "main"}) {
		t.Error("a missing kit tracks a source")
	}
}

// A value the renderer cannot write (here jam.yml holding a channel) is an
// error naming what failed, never a panic; the log has the panic's type and
// stack, never its value.
func TestExportRenderFailureIsAnError(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)
	_, err := ExportConfigTree(NewMemStore().ExportConfig(), nil, map[string]any{"x": make(chan int)}, true)
	if err == nil || err.Error() != "jam.yml cannot be rendered as a config file" {
		t.Fatalf("err %v", err)
	}
	if out := buf.String(); !strings.Contains(out, "panic_type=string") || !strings.Contains(out, "stack=") || strings.Contains(out, "chan int") {
		t.Errorf("log %s", out)
	}
}

// trackedBehind is a Jam whose kit ic tracks kits//ic@main at c1, a repo
// written by config write --all, and the kits repo's main moved to c2.
func trackedBehind(t *testing.T) (*MemStore, KitFetcher, *fakeFetcher, string) {
	t.Helper()
	st := NewMemStore()
	kits := newFakeFetcher(t, map[string]string{"main": "c1"}, map[string]map[string]string{"c1": {
		"ic/kit.yml": "kind: studio\nprompt: work the ticket\n",
	}})
	f := repoFetcher{kitsRepo: kits}
	src := gitsource.Source{Repo: kitsRepo, Dir: "ic", Ref: "main"}
	if _, err := TrackKit(context.Background(), st, f, nil, "ic", src, time.Unix(10, 0)); err != nil {
		t.Fatal(err)
	}
	exp, err := ExportConfigTree(st.ExportConfig(), nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	exportToDir(t, dir, exp)
	kits.commit("main", "c2", map[string]string{"ic/kit.yml": "kind: studio\nprompt: work the ticket, then review\n"})
	return st, f, kits, dir
}

// An unmanaged kit tracking the very source its .source.yml names is that
// kit, whatever commit each side is at: the plan adopts it and updates it
// to the fetched commit, instead of refusing.
func TestConfigPlanAdoptsTrackedKitBehind(t *testing.T) {
	st, f, _, dir := trackedBehind(t)
	p := planDirUnmarked(t, st, f, dir)
	if len(p.Refusals) > 0 {
		t.Fatalf("refusals %v", p.Refusals)
	}
	op := opOf(p, ConfigKindKit, "ic")
	if op.Action != ConfigUpdate || op.Unmanaged || !strings.Contains(op.Before, "prompt: work the ticket\n") || !strings.Contains(op.After, "then review") {
		t.Fatalf("op %+v", op)
	}
	applyPlan(t, st, p)
	k, _ := st.GetKit("ic")
	cfg, _ := testKitYML(st, "ic", k.Current)
	if !strings.Contains(cfg, "then review") || k.Source == nil || k.Source.Commit != "c2" {
		t.Fatalf("kit after apply: v%d %q source %+v", k.Current, cfg, k.Source)
	}
	if !slices.ContainsFunc(st.ListConfigMarks(), func(m ConfigMark) bool { return m.Key() == ConfigKey{ConfigKindKit, "ic"} }) {
		t.Fatal("kit ic is not managed after the apply")
	}
	if again := planDirUnmarked(t, st, f, dir); len(again.Refusals) > 0 {
		t.Fatalf("re-plan refusals %v", again.Refusals)
	}
}

// Only the same source adopts: a .source.yml naming another ref of the
// repo, or an inline kit.yml, that differs from Jam's kit is still refused
// — with both sides in the plan.
func TestConfigPlanRefusesOtherSourceForUnmanagedKit(t *testing.T) {
	t.Run("other ref", func(t *testing.T) {
		st, f, kits, dir := trackedBehind(t)
		kits.commit("dev", "d1", map[string]string{"ic/kit.yml": "kind: studio\nprompt: dev\n"})
		mustWrite(t, filepath.Join(dir, "kits/ic.source.yml"), string(renderKitSourceFile(KitSource{Repo: kitsRepo, Dir: "ic", Ref: "dev"})), 0o644)
		assertUnmanagedRefusal(t, planDirUnmarked(t, st, f, dir), ConfigKindKit, "ic")
	})
	t.Run("inline", func(t *testing.T) {
		st, f, _, dir := trackedBehind(t)
		if err := os.Remove(filepath.Join(dir, "kits/ic.source.yml")); err != nil {
			t.Fatal(err)
		}
		mustWrite(t, filepath.Join(dir, "kits/ic/kit.yml"), "kind: studio\nprompt: work the ticket, then review\n", 0o644)
		assertUnmanagedRefusal(t, planDirUnmarked(t, st, f, dir), ConfigKindKit, "ic")
	})
}

// An unmanaged object whose repo copy differs is refused, and the plan
// carries both canonical forms (an op flagged Unmanaged) so the diff shows
// why; a refused plan never applies.
func TestConfigPlanUnmanagedRefusalShowsBothSides(t *testing.T) {
	st := NewMemStore()
	must(t, CreateDestination(st, Destination{Name: "gh", Route: "/gh/", Upstream: "https://api.github.com",
		IdentityIn: ApplyBearer, CredName: "gh", Apply: ApplyBearer, Note: "jam"}, func(string) bool { return true }))
	dir := t.TempDir()
	exp, err := ExportConfigTree(st.ExportConfig(), nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	exportToDir(t, dir, exp)
	path := filepath.Join(dir, "destinations/gh.yml")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, path, strings.Replace(string(b), "note: jam", "note: repo", 1), 0o644)
	p := planDirUnmarked(t, st, nil, dir)
	op := assertUnmanagedRefusal(t, p, ConfigKindDestination, "gh")
	if !strings.Contains(op.Before, "note: jam") || !strings.Contains(op.After, "note: repo") {
		t.Errorf("op sides:\n%s\n---\n%s", op.Before, op.After)
	}
}

func assertUnmanagedRefusal(t *testing.T, p ConfigPlan, kind, name string) ConfigOp {
	t.Helper()
	key := ConfigKey{kind, name}
	if !slices.Contains(p.Refusals, key.String()+" exists and is not managed; config write --all it into the repo, or rename") {
		t.Errorf("refusals %v", p.Refusals)
	}
	op := opOf(p, kind, name)
	if !op.Unmanaged || op.Action != ConfigUpdate || op.Before == "" || op.After == "" || op.Before == op.After {
		t.Errorf("op %+v", op)
	}
	return op
}

// trickyStrings are values a Jam accepts that yaml.v3's own styles do not
// all round-trip (found by fuzzing), and their neighbours.
var trickyStrings = []string{
	"", "\n", "\n\n", "\n\nlead", "\nlead\n", "\tfoo\nbar", "\tlead", "trail\t", "\t", " lead", "trail ", "  indented\nnext",
	"a\r\nb", "a\rb", "\r\n", "multi\nline\n", "multi\nline\n\n\n", "x\n\ty", "a\t\nb", " \n", "\n \n", "x\n \n",
	"#hash", "- dash", ": colon", "key: val", "'quote", "\"dq", "---", "...", "a\n---\nb", "---\nx", "null", "~", "true", "0x1F",
	"1e3", "\u00a0nbsp", "a\u2028b", "\x7f", "émoji 🎉", `a\b`, "{flow}", "[x]", "*alias", "&anchor", "!tag", "%dir", "|pipe",
	">gt", "\ue000cfg0\ue000", strings.Repeat("long ", 40) + "\nline", "\x01ctl",
}

// render∘parse∘render is render, byte for byte, for every file kind and
// every tricky string — or an object written by config write never adopts
// (and a string the renderer cannot write panics a serve).
func TestConfigFileRenderRoundTrips(t *testing.T) {
	type kind struct {
		name  string
		build func(s string) configFile
		parse func(files map[string][]byte) (configFile, error)
	}
	layer := func(s string) sessionctx.Layer {
		return sessionctx.Layer{Core: "core " + s, Leaves: []sessionctx.Leaf{{Name: "a.md", ReadWhen: "when " + s, Body: s}}}
	}
	kinds := []kind{
		{"destination", func(s string) configFile {
			return DestinationFile{Kind: ConfigKindDestination, Version: 1, Destination: Destination{Name: "d", Route: "/d/", Upstream: "https://x",
				Env: map[string]string{"K": s}, AllowPaths: []string{s}, Note: s}}
		}, func(f map[string][]byte) (configFile, error) {
			return ParseDestinationFile("destinations/d.yml", "d", f["destinations/d.yml"])
		}},
		{"model spec", func(s string) configFile {
			return ModelSpecFile{Kind: ConfigKindModelSpec, Version: 1, Name: "m", Type: "claude-code", Note: s}
		}, func(f map[string][]byte) (configFile, error) {
			return ParseModelSpecFile("model-specs/m.yml", "m", f["model-specs/m.yml"])
		}},
		{"user", func(s string) configFile {
			return UserFile{Kind: ConfigKindUser, Version: 1, Name: "u", Logins: []string{s}}
		}, func(f map[string][]byte) (configFile, error) {
			return ParseUserFile("users/u.yml", "u", f["users/u.yml"])
		}},
		{"connection", func(s string) configFile {
			return ConnectionFile{Kind: ConfigKindConnection, Version: 1, Name: "c", Service: "discord", Cred: s}
		}, func(f map[string][]byte) (configFile, error) {
			return ParseConnectionFile("connections/c.yml", "c", f["connections/c.yml"])
		}},
		{"project", func(s string) configFile {
			return ProjectFile{Kind: ConfigKindProject, Version: 1, Name: "P",
				Resources: []sessionctx.Resource{{Name: "r", Kind: "repo", Ref: "https://x", Note: s}},
				Rooms:     []RoomFile{{Key: "k", Label: "label " + s}}, Context: layer(s)}
		}, func(f map[string][]byte) (configFile, error) {
			pf, err := ParseProjectFile("projects/P/project.yml", "P", f["projects/P/project.yml"])
			if err != nil {
				return nil, err
			}
			if md, ok := f["projects/P/context.md"]; ok {
				pf.Context, err = ParseContextMarkdown("projects/P/context.md", md)
			}
			return pf, err
		}},
		{"role", func(s string) configFile {
			return RoleFile{Kind: ConfigKindRole, Version: 1, Name: "r", Project: "P", Kit: "k",
				Scope:      RoleScopeFile{Credentials: map[string]string{"K": "cred"}},
				Allocation: AllocationFile{Standing: []StandingFile{{Name: "s", Prompt: s}}},
				Context:    &ContextFile{Core: "core " + s, Leaves: layer(s).Leaves}}
		}, func(f map[string][]byte) (configFile, error) {
			return ParseRoleFile("projects/P/roles/r.yml", "r", f["projects/P/roles/r.yml"])
		}},
		{"jam context", func(s string) configFile { return JamContextFile{Layer: layer(s)} },
			func(f map[string][]byte) (configFile, error) {
				l, err := ParseContextMarkdown("context.md", f["context.md"])
				return JamContextFile{Layer: l}, err
			}},
	}
	for _, k := range kinds {
		for _, s := range trickyStrings {
			t.Run(fmt.Sprintf("%s %q", k.name, s), func(t *testing.T) {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("render panics: %v", r)
					}
				}()
				o := newConfigObject(k.build(s))
				f, err := k.parse(o.Files)
				if err != nil {
					t.Fatalf("re-parse: %v", err)
				}
				if got := newConfigObject(f); string(got.Canon) != string(o.Canon) {
					t.Errorf("render∘parse∘render differs:\n%q\n---\n%q", o.Canon, got.Canon)
				}
			})
		}
	}
}

// A kit whose prompt is a tricky string is written as kit.yml and adopts.
// (A control character is left out: a kit stores it raw in its config JSON,
// which the kit parser then refuses — a separate gap.)
func TestConfigWriteAllKitPromptRoundTrips(t *testing.T) {
	for _, s := range trickyStrings {
		if strings.ContainsAny(s, "\x01\x7f") {
			continue
		}
		t.Run(fmt.Sprintf("%q", s), func(t *testing.T) {
			st := NewMemStore()
			if _, _, err := PushStudioKit(st, "k", string(renderYAML(map[string]string{"kind": "studio", "prompt": s}))); err != nil {
				t.Skipf("not a kit Jam accepts: %v", err)
			}
			exp, err := ExportConfigTree(st.ExportConfig(), nil, nil, true)
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			exportToDir(t, dir, exp)
			for _, r := range notAdopted(planDirUnmarked(t, st, nil, dir)) {
				t.Errorf("%s", r)
			}
		})
	}
}

// The drift check of a Jam holding a value the renderer once panicked on
// (a multi-line note opening with a tab) renders it, rather than crashing
// the serve's background goroutine.
func TestConfigMarkStatesRendersTabbedNote(t *testing.T) {
	st := appliedFull(t)
	editGH(t, st, "\tfoo\nbar")
	states, err := ConfigMarkStates(st)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(states, func(s ConfigMarkState) bool { return s.Name == "gh" && s.State == ConfigDrifted }) {
		t.Fatalf("states %+v", states)
	}
}

// A Postgres serve reloads every object from its JSON doc (no migration once
// its roster schema is current): a repo written before a restart still
// adopts after it (the live side planned from the snapshot round-tripped
// through JSON).
func TestConfigWriteAllAdoptsAfterReload(t *testing.T) {
	st, f := adoptJam(t)
	exp, err := ExportConfigTree(st.ExportConfig(), nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	exportToDir(t, dir, exp)
	b, err := json.Marshal(st.ExportConfig())
	if err != nil {
		t.Fatal(err)
	}
	var snap ConfigSnapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		t.Fatal(err)
	}
	tr, err := LoadConfigTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	FetchConfigKits(context.Background(), f, tr)
	p := PlanConfig(ConfigPlanInput{Tree: tr, Snapshot: snap, Env: allCreds,
		Source: gitsource.Source{Repo: ConfigDirRepo}, Commit: ConfigDirCommit, Now: time.Unix(100, 0)})
	for _, r := range notAdopted(p) {
		t.Errorf("%s", r)
	}
}
