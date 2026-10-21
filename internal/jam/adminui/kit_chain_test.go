package adminui_test

import (
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
)

const kitChild = `kind: studio
base:
  kit: web
egress: [api.acme.dev]
prompt: Child prompt.
`

func seedChain(t *testing.T) jam.Store {
	t.Helper()
	store := seedKits(t)
	if _, _, err := jam.PushStudioKit(store, "child", kitChild); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestKitPageShowsChainAndEffective(t *testing.T) {
	body := get(t, kitsHandler(seedChain(t)), "/ui/kits/child").Body.String()
	for _, want := range []string{
		"Built on", `href="/ui/kits/web"`, "v2", "follows",
		"Effective", "github.com", "api.acme.dev", "You work on the web and api services.", "Child prompt.",
		"kit: web",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("chained kit page missing %q", want)
		}
	}
	if strings.Contains(body, "blessed default") {
		t.Error("a kit-based base must not read as the blessed default")
	}
}

func TestKitPageShowsPinnedParent(t *testing.T) {
	store := seedKits(t)
	if _, _, err := jam.PushStudioKit(store, "pinned", strings.Replace(kitChild, "kit: web", "kit: web@v1", 1)); err != nil {
		t.Fatal(err)
	}
	body := get(t, kitsHandler(store), "/ui/kits/pinned").Body.String()
	if !strings.Contains(body, "pinned") || !strings.Contains(body, "v1") {
		t.Errorf("pinned parent not shown:\n%s", body)
	}
}

func TestKitPageUsedByListsChildKits(t *testing.T) {
	body := get(t, kitsHandler(seedChain(t)), "/ui/kits/web").Body.String()
	if !strings.Contains(body, `href="/ui/kits/child"`) {
		t.Errorf("parent's Used by lacks the child kit:\n%s", body)
	}
	if strings.Contains(body, "Effective") || strings.Contains(body, "Built on") {
		t.Error("an unchained kit has no Built on / Effective")
	}
	if !strings.Contains(body, `class="danger" disabled`) {
		t.Error("Delete must be disabled while a kit builds on it")
	}
}

func TestKitPageChainErrorEscaped(t *testing.T) {
	store := seedKits(t)
	if _, err := store.PushKit("orphan", "kind: studio\nbase:\n  kit: gone<b>x</b>\n"); err != nil {
		t.Fatal(err)
	}
	body := get(t, kitsHandler(store), "/ui/kits/orphan").Body.String()
	if strings.Contains(body, "<b>x</b>") {
		t.Error("chain error not escaped")
	}
	if !strings.Contains(body, "gone") {
		t.Errorf("chain error not shown:\n%s", body)
	}
}

func TestKitsListShowsKitBase(t *testing.T) {
	body := get(t, kitsHandler(seedChain(t)), "/ui/kits").Body.String()
	if !strings.Contains(body, "kit: web") {
		t.Errorf("Base column lacks %q", "kit: web")
	}
}
