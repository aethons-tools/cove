package adminui_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
	"github.com/aethons-tools/cove/internal/jam/gitsource"
)

const kitV1 = `kind: studio
base:
  image: ghcr.io/acme/web@sha256:abc
egress: [github.com, .anthropic.com]
build-args: {GO_VERSION: "1.23"}
secrets:
  GH_TOKEN: {description: clone access}
prompt: You work on the web service.
`

// kitV2 changes only the prompt: a new version, same build digest.
var kitV2 = strings.Replace(kitV1, "the web service", "the web and api services", 1)

const kitChild = `kind: studio
base:
  kit: web
egress: [api.acme.dev]
prompt: Child prompt.
`

// seedKits: kit web (v1, v2) bound by acme/builder, acme/plain naming no kit
// (it raises the default kit) and the default kit.
func seedKits(t *testing.T) jam.Store {
	t.Helper()
	store := newStore(t)
	mustCreateProject(t, store, "acme")
	for _, c := range []string{kitV1, kitV2} {
		if _, _, err := jam.PushStudioKit(store, "web", c); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.PutRole("acme", jam.Role{Name: "builder", Kit: "web"}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRole("acme", jam.Role{Name: "plain"}); err != nil {
		t.Fatal(err)
	}
	if _, err := jam.EnsureDefaultStudioKit(store); err != nil {
		t.Fatal(err)
	}
	return store
}

func getKit(t *testing.T, store jam.Store, path string) adminui.KitDetail {
	t.Helper()
	var d adminui.KitDetail
	if rec := apiDo(t, uiHandler(t, store), "GET", "/app/admin/api/kits/"+path, nil, &d); rec.Code != 200 {
		t.Fatalf("GET kits/%s = %d %s", path, rec.Code, rec.Body)
	}
	return d
}

func kvString(xs []adminui.KV) string {
	var s []string
	for _, x := range xs {
		s = append(s, x.Key+"="+x.Value)
	}
	return strings.Join(s, ",")
}

// A kit's view carries its current version's parsed fields and the roles
// bound to it; the default kit also lists the roles naming no kit.
func TestKitsAPIViewCurrent(t *testing.T) {
	store := seedKits(t)
	d := getKit(t, store, "web")
	v := d.View
	if v.N != 2 || v.ParseErr != "" || v.Image != "ghcr.io/acme/web@sha256:abc" || v.Base != "image" || v.Prompt != "You work on the web and api services." || v.Digest == "" {
		t.Errorf("view = %+v", v)
	}
	if strings.Join(v.Egress, ",") != "github.com,.anthropic.com" || kvString(v.BuildArgs) != "GO_VERSION=1.23" || kvString(v.Secrets) != "GH_TOKEN=clone access" {
		t.Errorf("view lists = %+v", v)
	}
	if !strings.Contains(v.YAML, "prompt: You work on the web and api services.") || v.PushText != kitV2 {
		t.Errorf("yaml = %q push = %q", v.YAML, v.PushText)
	}
	if len(d.Uses) != 1 || d.Uses[0] != (adminui.KitUse{Project: "acme", Role: "builder"}) {
		t.Errorf("uses = %+v", d.Uses)
	}
	if len(d.Versions) != 2 || !d.Versions[1].Current || d.Versions[1].Prev != 1 || d.Versions[0].Digest == "" {
		t.Errorf("versions = %+v", d.Versions)
	}
	if len(d.Chain) != 0 || d.Effective != nil || d.ChainErr != "" {
		t.Errorf("an unchained kit has a chain: %+v %+v %q", d.Chain, d.Effective, d.ChainErr)
	}

	def := getKit(t, store, "default")
	if !def.IsDefault || len(def.Uses) != 1 || def.Uses[0] != (adminui.KitUse{Project: "acme", Role: "plain", Implicit: true}) {
		t.Errorf("default uses = %+v", def.Uses)
	}

	diff := getKit(t, store, "web?v=2&diff=1")
	if diff.Diff == nil || diff.Diff.From != 1 || diff.Diff.To != 2 || diff.Diff.DigestChanged {
		t.Fatalf("diff = %+v", diff.Diff)
	}
	var del, add bool
	for _, l := range diff.Diff.Lines {
		del = del || l.Op == "-" && strings.Contains(l.Text, "You work on the web service.")
		add = add || l.Op == "+" && strings.Contains(l.Text, "You work on the web and api services.")
	}
	if !del || !add {
		t.Errorf("diff lines = %+v", diff.Diff.Lines)
	}

	var list adminui.KitsResponse
	apiDo(t, uiHandler(t, store), "GET", "/app/admin/api/kits", nil, &list)
	rows := map[string]adminui.KitRow{}
	for _, r := range list.Kits {
		rows[r.Name] = r
	}
	if r := rows["web"]; r.Current != 2 || r.Count != 2 || r.Base != "image" || r.Egress != 2 || r.UsedBy != 1 || r.Invalid {
		t.Errorf("web row = %+v", r)
	}
	if r := rows["default"]; r.Base != "blessed default" || r.UsedBy != 1 {
		t.Errorf("default row = %+v", r)
	}
}

// A pre-studio row shows its parse error and its text as stored, which is
// also the push box's prefill.
func TestKitsAPIViewLegacyRow(t *testing.T) {
	store := newStore(t)
	if _, err := store.PushKit("old", "listen: :443\n"); err != nil {
		t.Fatal(err)
	}
	v := getKit(t, store, "old").View
	if !strings.Contains(v.ParseErr, "field listen not found") || !strings.Contains(v.RawShown, "listen: :443") || v.PushText != v.RawShown || v.Masked || v.Hidden || v.YAML != "" {
		t.Errorf("legacy view = %+v", v)
	}
}

// A chained kit shows its parents root first — following or pinned — and
// the merged effective kit; its parent lists it among the kits built on it.
func TestKitsAPIChain(t *testing.T) {
	store := seedKits(t)
	if _, _, err := jam.PushStudioKit(store, "child", kitChild); err != nil {
		t.Fatal(err)
	}
	if _, _, err := jam.PushStudioKit(store, "pinned", strings.Replace(kitChild, "kit: web", "kit: web@v1", 1)); err != nil {
		t.Fatal(err)
	}
	d := getKit(t, store, "child")
	if len(d.Chain) != 1 || d.Chain[0] != (adminui.KitParent{Name: "web", Version: 2, Follow: true}) || d.ChainErr != "" {
		t.Fatalf("chain = %+v %q", d.Chain, d.ChainErr)
	}
	if d.View.Base != "kit: web" {
		t.Errorf("base = %q", d.View.Base)
	}
	e := d.Effective
	if e == nil {
		t.Fatal("no effective kit")
	}
	for _, host := range []string{"github.com", "api.acme.dev"} {
		if !slices.Contains(e.Egress, host) {
			t.Errorf("effective egress %v lacks %s", e.Egress, host)
		}
	}
	// prompts join root first; build args and secrets come from the parent
	if e.Prompt != "You work on the web and api services.\n\nChild prompt." || kvString(e.BuildArgs) != "GO_VERSION=1.23" || kvString(e.Secrets) != "GH_TOKEN=clone access" ||
		strings.Join(e.Excluded, ",") != ".anthropic.com" || e.ParseErr != "" || e.Digest == "" {
		t.Errorf("effective = %+v", e)
	}

	p := getKit(t, store, "pinned")
	if len(p.Chain) != 1 || p.Chain[0] != (adminui.KitParent{Name: "web", Version: 1}) {
		t.Errorf("pinned chain = %+v", p.Chain)
	}
	web := getKit(t, store, "web")
	if len(web.UsedByKits) != 2 || web.UsedByKits[0] != (adminui.KitChild{Name: "child"}) || web.UsedByKits[1] != (adminui.KitChild{Name: "pinned", Pinned: true}) {
		t.Errorf("web used by kits = %+v", web.UsedByKits)
	}

	var list adminui.KitsResponse
	apiDo(t, uiHandler(t, store), "GET", "/app/admin/api/kits", nil, &list)
	for _, r := range list.Kits {
		if r.Name == "child" && r.Base != "kit: web" {
			t.Errorf("child row base = %q", r.Base)
		}
	}
}

// A chain that doesn't resolve comes back as the reason, with no chain.
func TestKitsAPIChainError(t *testing.T) {
	store := newStore(t)
	if _, err := store.PushKit("evil", "kind: studio\nbase:\n  kit: gone<b>x</b>\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PushKit("orphan", "kind: studio\nbase:\n  kit: evil\n"); err != nil {
		t.Fatal(err)
	}
	d := getKit(t, store, "orphan")
	if !strings.Contains(d.ChainErr, "evil@v1 (parent of orphan) is invalid") || !strings.Contains(d.ChainErr, "gone<b>x</b>") || len(d.Chain) != 0 || d.Effective != nil {
		t.Errorf("chain error = %q chain=%+v eff=%+v", d.ChainErr, d.Chain, d.Effective)
	}
}

func trackedKit(t *testing.T, store jam.Store, f *srcFetcher, repo string) {
	t.Helper()
	src, err := gitsource.Parse(repo + "@main")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jam.TrackKit(context.Background(), store, f, nil, "web-dev", src, time.Unix(10, 0)); err != nil {
		t.Fatal(err)
	}
}

// A tracked source links its repo only when it is https, and its commit
// (shortened to 12) only on github.com or a gitlab host.
func TestKitsAPISourceLinks(t *testing.T) {
	f := &srcFetcher{t: t, refs: map[string]string{"main": srcSHA1}, kits: map[string]string{srcSHA1: srcKitV1}}
	h, store := sourceUI(t, f)
	trackedKit(t, store, f, "https://github.com/acme/kits.git")
	var d adminui.KitDetail
	apiDo(t, h, "GET", "/app/admin/api/kits/web-dev", nil, &d)
	want := adminui.SourceInfo{Repo: "https://github.com/acme/kits.git", RepoLink: "https://github.com/acme/kits.git", Ref: "main", Commit: srcSHA1[:12],
		CommitLink: "https://github.com/acme/kits/commit/" + srcSHA1}
	if d.Source == nil || *d.Source != want {
		t.Errorf("github source = %+v, want %+v", d.Source, want)
	}
	if len(d.Versions) != 1 || d.Versions[0].Source == nil || d.Versions[0].Source.CommitLink != want.CommitLink || d.Versions[0].Pushed == "" {
		t.Errorf("version row = %+v", d.Versions)
	}

	for repo, wantLink := range map[string]string{
		"https://gitlab.example.com/acme/kits": "https://gitlab.example.com/acme/kits/-/commit/" + srcSHA1,
		"https://git.example.com/acme/kits":    "", // an unknown host: the commit is plain text
	} {
		if err := store.SetKitSource("web-dev", &jam.KitSource{Repo: repo, Ref: "main", Commit: srcSHA1}); err != nil {
			t.Fatal(err)
		}
		apiDo(t, h, "GET", "/app/admin/api/kits/web-dev", nil, &d)
		if d.Source == nil || d.Source.RepoLink != repo || d.Source.CommitLink != wantLink || d.Source.Commit != srcSHA1[:12] {
			t.Errorf("%s: source = %+v", repo, d.Source)
		}
	}
	for _, repo := range []string{"http://localhost:1/<b>x</b>", "https://user@github.com/acme/kits", "git@github.com:acme/kits.git"} {
		if err := store.SetKitSource("web-dev", &jam.KitSource{Repo: repo, Ref: "<i>ref</i>", Commit: srcSHA1}); err != nil {
			t.Fatal(err)
		}
		apiDo(t, h, "GET", "/app/admin/api/kits/web-dev", nil, &d)
		if d.Source == nil || d.Source.RepoLink != "" || d.Source.CommitLink != "" || d.Source.Repo != repo || d.Source.Ref != "<i>ref</i>" || d.Source.Commit != srcSHA1[:12] {
			t.Errorf("%s: a repo that isn't plain https must not be linked: %+v", repo, d.Source)
		}
	}
	if err := store.SetKitSource("web-dev", &jam.KitSource{Repo: "https://github.com/acme/kits", Ref: "main"}); err != nil {
		t.Fatal(err)
	}
	apiDo(t, h, "GET", "/app/admin/api/kits/web-dev", nil, &d)
	if d.Source == nil || d.Source.CommitLink != "" || d.Source.RepoLink == "" {
		t.Errorf("no commit: %+v", d.Source)
	}
}

// Syncing an unchanged source says so; an unknown kit's sync is a 404.
func TestKitsAPISyncUnchangedAndUnknown(t *testing.T) {
	f := &srcFetcher{t: t, refs: map[string]string{"main": srcSHA1}, kits: map[string]string{srcSHA1: srcKitV1}}
	h, store := sourceUI(t, f)
	trackedKit(t, store, f, "https://github.com/acme/kits")
	var res adminui.KitResult
	apiDo(t, h, "POST", "/app/admin/api/kits/web-dev/sync", map[string]string{}, &res)
	if res.Message != "synced: web-dev unchanged (current v1)" {
		t.Errorf("unchanged sync = %q", res.Message)
	}
	wantErr(t, apiDo(t, h, "POST", "/app/admin/api/kits/ghost/sync", map[string]string{}, nil), 404, "not_found")
	wantErr(t, apiDo(t, h, "POST", "/app/admin/api/kits/web-dev/source", map[string]string{"source": "not a source"}, nil), 400, "bad_request")
}

// setFetcher serves one commit of a repo as a file tree, so a source can name
// a kit dir whose base.kit points at a sibling.
type setFetcher struct {
	t     *testing.T
	sha   string
	files map[string]string
}

func (f *setFetcher) Resolve(context.Context, gitsource.Source) (string, error) { return f.sha, nil }

func (f *setFetcher) Fetch(_ context.Context, _ gitsource.Source, fn func(root, commit string) error) error {
	root := f.t.TempDir()
	for p, text := range f.files {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, []byte(text), 0o644); err != nil {
			return err
		}
	}
	return fn(root, f.sha)
}

// Importing and syncing a kit with a path parent names the ancestors in the
// message, and the kit's view notes the set.
func TestKitsAPISetImportAndSync(t *testing.T) {
	f := &setFetcher{t: t, sha: srcSHA1, files: map[string]string{
		"web/kit.yml": srcKitV1,
		"app/kit.yml": "kind: studio\nbase:\n  kit: ../web\nprompt: app\n",
	}}
	h, _ := sourceUI(t, f)
	var c adminui.Created
	if rec := apiDo(t, h, "POST", "/app/admin/api/kits/import", map[string]any{"source": "https://github.com/acme/kits//app@main", "track": true}, &c); rec.Code != 201 {
		t.Fatalf("import = %d %s", rec.Code, rec.Body)
	}
	if c.Path != "config/kits/app" || c.Message != "tracking https://github.com/acme/kits//app@main: app is now v1. With: web-dev v1" {
		t.Errorf("import = %+v", c)
	}
	var d adminui.KitDetail
	apiDo(t, h, "GET", "/app/admin/api/kits/app", nil, &d)
	if d.SetWith != "web-dev" {
		t.Errorf("app set with = %q", d.SetWith)
	}
	apiDo(t, h, "GET", "/app/admin/api/kits/web-dev", nil, &d)
	if d.SetWith != "" {
		t.Errorf("the root kit has no parents: set with = %q", d.SetWith)
	}

	var res adminui.KitResult
	apiDo(t, h, "POST", "/app/admin/api/kits/app/sync", map[string]string{}, &res)
	if res.Message != "synced: app unchanged (current v1)" {
		t.Errorf("unchanged sync = %q", res.Message)
	}
	f.sha = srcSHA2
	f.files["web/kit.yml"] = srcKitV2
	apiDo(t, h, "POST", "/app/admin/api/kits/app/sync", map[string]string{}, &res)
	if res.Message != "synced: app is now v2. With: web-dev v2" {
		t.Errorf("sync = %q, want the ancestors", res.Message)
	}
	f.sha = "1111111111111111111111111111111111111111"
	f.files["app/kit.yml"] = "kind: studio\nbase:\n  kit: ../web\nprompt: app two\n"
	apiDo(t, h, "POST", "/app/admin/api/kits/app/sync", map[string]string{}, &res)
	if res.Message != "synced: app is now v3. With: web-dev v2 (unchanged)" {
		t.Errorf("sync with an unchanged parent = %q", res.Message)
	}
}

// A registry parent imported from the same repo and dir as the child is not
// a set member.
func TestKitsAPISetNeedsAnotherDir(t *testing.T) {
	f := &setFetcher{t: t, sha: srcSHA1, files: map[string]string{"web/kit.yml": srcKitV1}}
	h, store := sourceUI(t, f)
	src, err := gitsource.Parse("https://github.com/acme/kits//web@main")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jam.ImportKit(context.Background(), store, f, nil, "", src, time.Unix(10, 0)); err != nil {
		t.Fatal(err)
	}
	f.files["web/kit.yml"] = "kind: studio\nbase:\n  kit: web-dev\nprompt: app\n"
	if _, err := jam.ImportKit(context.Background(), store, f, nil, "app", src, time.Unix(20, 0)); err != nil {
		t.Fatal(err)
	}
	var d adminui.KitDetail
	apiDo(t, h, "GET", "/app/admin/api/kits/app", nil, &d)
	if len(d.Chain) != 1 || d.SetWith != "" {
		t.Errorf("a parent from the same dir is not a set member: chain=%+v set=%q", d.Chain, d.SetWith)
	}
}
