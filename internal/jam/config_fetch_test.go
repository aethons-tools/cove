package jam

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam/gitsource"
)

const kitsRepo = "https://github.com/acme/kits"

const appSourceYML = "kind: kit-source\nversion: 1\ngit: https://github.com/acme/kits//app@main\n"

// repoFetcher serves several fake repos by URL; an unknown repo is a fetch
// failure.
type repoFetcher map[string]*fakeFetcher

func (r repoFetcher) Fetch(ctx context.Context, src gitsource.Source, fn func(root, commit string) error) error {
	f, ok := r[src.Repo]
	if !ok {
		return fmt.Errorf("%w: repository not found", gitsource.ErrFetch)
	}
	return f.Fetch(ctx, src, fn)
}

func (r repoFetcher) Resolve(ctx context.Context, src gitsource.Source) (string, error) {
	f, ok := r[src.Repo]
	if !ok {
		return "", fmt.Errorf("%w: repository not found", gitsource.ErrFetch)
	}
	return f.Resolve(ctx, src)
}

// fetchedTree loads files as a config tree and fetches its kit sources from f.
func fetchedTree(t *testing.T, files map[string]string, f KitFetcher) *ConfigTree {
	t.Helper()
	tr := treeOf(t, files)
	FetchConfigKits(context.Background(), f, tr)
	return tr
}

func TestFetchConfigKitsOneCheckoutPerSource(t *testing.T) {
	kits := newFakeFetcher(t, map[string]string{"main": "k1"}, map[string]map[string]string{"k1": {
		"app/kit.yml": "kind: studio\nbase: {kit: ../web}\n",
		"api/kit.yml": "kind: studio\nbase: {kit: ../web}\n",
		"web/kit.yml": "kind: studio\nbase: {kit: base}\n", // a registry parent kept inline in the config repo
	}})
	tr := fetchedTree(t, map[string]string{
		"kits/base/kit.yml":   "kind: studio\n",
		"kits/app.source.yml": appSourceYML,
		"kits/api.source.yml": "kind: kit-source\nversion: 1\ngit: https://github.com/acme/kits//api@main\n",
	}, repoFetcher{kitsRepo: kits})
	if len(tr.Refusals) > 0 {
		t.Fatal(tr.Refusals)
	}
	if kits.fetches != 1 {
		t.Fatalf("fetches = %d, want 1 (one checkout per repo and ref)", kits.fetches)
	}
	var names []string
	for _, e := range tr.Kits {
		names = append(names, e.Name)
	}
	if !slices.Equal(names, []string{"base", "web", "api", "app"}) {
		t.Fatalf("order %v", names)
	}
	g := kitGroupKey(gitsource.Source{Repo: kitsRepo, Ref: "main"})
	if tr.KitGroups[g].Commit != "k1" || tr.KitSources["app"].Commit != "k1" || !tr.KitsFetched {
		t.Fatalf("groups %+v sources %+v", tr.KitGroups, tr.KitSources)
	}
	if tr.Kits[0].Group != "" || tr.Kits[1].Group != g || tr.Kits[3].Group != g {
		t.Fatalf("groups %+v", tr.Kits)
	}
	FetchConfigKits(context.Background(), repoFetcher{kitsRepo: kits}, tr)
	if kits.fetches != 1 {
		t.Fatal("a second FetchConfigKits fetched again")
	}
}

func TestFetchConfigKitsRefusals(t *testing.T) {
	kits := newFakeFetcher(t, map[string]string{"main": "k1"}, map[string]map[string]string{"k1": {
		"app/kit.yml":    "kind: studio\nbase: {kit: ../base}\n", // its ancestor "base" collides with the inline kit
		"base/kit.yml":   "kind: studio\n",
		"pinned/kit.yml": "kind: studio\nbase: {kit: web@v2}\n",
	}})
	tr := fetchedTree(t, map[string]string{
		"kits/base/kit.yml":      "kind: studio\n",
		"kits/app.source.yml":    appSourceYML,
		"kits/pinned.source.yml": "kind: kit-source\nversion: 1\ngit: https://github.com/acme/kits//pinned@main\n",
		"kits/gone.source.yml":   "kind: kit-source\nversion: 1\ngit: https://github.com/acme/missing//x@main\n",
	}, repoFetcher{kitsRepo: kits})
	want := []string{
		"kits/app.source.yml: kit base is also kits/base/kit.yml",
		"kits/gone.source.yml: git fetch failed: repository not found",
		"kits/pinned.source.yml: kit pinned's base.kit web@v2 pins a version; versions are per-Jam — name the kit or use a path parent",
	}
	if !slices.Equal(tr.Refusals, want) {
		t.Fatalf("refusals:\n%s", strings.Join(tr.Refusals, "\n"))
	}
	// A set with a colliding name is skipped whole: neither app nor the
	// repo's base joins the tree, so no orphaned child is planned.
	for _, e := range tr.Kits {
		if e.Name != "base" || e.Group != "" {
			t.Fatalf("kits %+v: only the inline base may load", tr.Kits)
		}
	}
	// Every repo and ref with a failure is recorded, so the planner keeps
	// the kits tracking it (and their ancestors) off the delete list.
	failed := []string{
		kitGroupKey(gitsource.Source{Repo: kitsRepo, Ref: "main"}),
		kitGroupKey(gitsource.Source{Repo: "https://github.com/acme/missing", Ref: "main"}),
	}
	if !slices.Equal(slices.Sorted(maps.Keys(tr.KitGroupsFailed)), failed) {
		t.Fatalf("failed groups %v, want %v", tr.KitGroupsFailed, failed)
	}
	if _, ok := tr.KitGroups[failed[1]]; ok {
		t.Fatal("an unfetched repo has no checkout group")
	}
}

func TestFetchConfigKitsCollisionAcrossSources(t *testing.T) {
	kits := newFakeFetcher(t, map[string]string{"main": "k1"}, map[string]map[string]string{"k1": {
		"app/kit.yml": "kind: studio\nbase: {kit: ../web}\n",
		"web/kit.yml": "kind: studio\n",
	}})
	other := newFakeFetcher(t, map[string]string{"main": "o1"}, map[string]map[string]string{"o1": {
		"api/kit.yml": "kind: studio\nbase: {kit: ../web}\n",
		"web/kit.yml": "kind: studio\n",
	}})
	tr := fetchedTree(t, map[string]string{
		"kits/app.source.yml": appSourceYML,
		"kits/api.source.yml": "kind: kit-source\nversion: 1\ngit: https://github.com/acme/other//api@main\n",
	}, repoFetcher{kitsRepo: kits, "https://github.com/acme/other": other})
	want := []string{"kits/api.source.yml: kit web is also kits/app.source.yml"}
	if !slices.Equal(tr.Refusals, want) {
		t.Fatalf("refusals:\n%s", strings.Join(tr.Refusals, "\n"))
	}
	var names []string
	for _, e := range tr.Kits {
		names = append(names, e.Name)
	}
	if !slices.Equal(names, []string{"web", "app"}) {
		t.Fatalf("kits %v: the colliding set is skipped whole", names)
	}
	if g := kitGroupKey(gitsource.Source{Repo: "https://github.com/acme/other", Ref: "main"}); !tr.KitGroupsFailed[g] || len(tr.KitGroupsFailed) != 1 {
		t.Fatalf("failed groups %v", tr.KitGroupsFailed)
	}
}

func TestFetchConfigKitsWithoutFetcherOrOverLimit(t *testing.T) {
	tr := fetchedTree(t, map[string]string{"kits/app.source.yml": appSourceYML}, nil)
	if !slices.Equal(tr.Refusals, []string{"kits/app.source.yml: kit sources cannot be fetched here"}) {
		t.Fatal(tr.Refusals)
	}
	if !tr.KitGroupsFailed[kitGroupKey(gitsource.Source{Repo: kitsRepo, Ref: "main"})] {
		t.Fatalf("failed groups %v", tr.KitGroupsFailed)
	}
	files := map[string]string{}
	for i := range ConfigKitSourceLimit + 1 {
		files[fmt.Sprintf("kits/k%d.source.yml", i)] = fmt.Sprintf("kind: kit-source\nversion: 1\ngit: https://github.com/acme/kits//k@r%d\n", i)
	}
	tr = fetchedTree(t, files, repoFetcher{})
	if !slices.Equal(tr.Refusals, []string{"kits: 17 kit sources (distinct repo and ref); at most 16 per config repo"}) {
		t.Fatal(tr.Refusals)
	}
	if len(tr.KitGroupsFailed) != ConfigKitSourceLimit+1 {
		t.Fatalf("failed groups %v", tr.KitGroupsFailed)
	}
}

func TestFetchConfigKitsSameDirTwoNames(t *testing.T) {
	kits := newFakeFetcher(t, map[string]string{"main": "k1"}, map[string]map[string]string{"k1": {
		"app/kit.yml": "kind: studio\n",
	}})
	tr := fetchedTree(t, map[string]string{
		"kits/app.source.yml":  appSourceYML,
		"kits/app2.source.yml": "kind: kit-source\nversion: 1\ngit: https://github.com/acme/kits//app@main\n",
	}, repoFetcher{kitsRepo: kits})
	want := []string{"kits/app2.source.yml: kit app2 is the same kit as app (kits/app.source.yml)"}
	if !slices.Equal(tr.Refusals, want) {
		t.Fatalf("refusals:\n%s", strings.Join(tr.Refusals, "\n"))
	}
	if len(tr.Kits) != 1 || tr.Kits[0].Name != "app" {
		t.Fatalf("kits %+v", tr.Kits)
	}
}
