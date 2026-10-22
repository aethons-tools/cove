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

// builtOnRow is the Built on row's value (the <dd> after its <dt>), or "".
func builtOnRow(t *testing.T, body string) string {
	t.Helper()
	const dt = "<dt>Built on</dt><dd>"
	i := strings.Index(body, dt)
	if i < 0 {
		return ""
	}
	row := body[i+len(dt):]
	if j := strings.Index(row, "</dd>"); j >= 0 {
		row = row[:j]
	}
	return row
}

func TestKitPageShowsChainAndEffective(t *testing.T) {
	body := get(t, kitsHandler(seedChain(t)), "/app/admin/kits/child").Body.String()
	row := builtOnRow(t, body)
	for _, want := range []string{`href="/app/admin/kits/web"`, "web v2", "follows"} {
		if !strings.Contains(row, want) {
			t.Errorf("Built on row missing %q:\n%s", want, row)
		}
	}
	if strings.Contains(row, "pinned") {
		t.Errorf("a following parent must not read as pinned:\n%s", row)
	}
	for _, want := range []string{
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
	row := builtOnRow(t, get(t, kitsHandler(store), "/app/admin/kits/pinned").Body.String())
	for _, want := range []string{`href="/app/admin/kits/web"`, "web v1", "pinned"} {
		if !strings.Contains(row, want) {
			t.Errorf("Built on row missing %q:\n%s", want, row)
		}
	}
	if strings.Contains(row, "follows") {
		t.Errorf("a pinned parent must not read as following:\n%s", row)
	}
}

func TestKitPageUsedByListsChildKits(t *testing.T) {
	body := get(t, kitsHandler(seedChain(t)), "/app/admin/kits/web").Body.String()
	if !strings.Contains(body, `href="/app/admin/kits/child"`) {
		t.Errorf("parent's Used by lacks the child kit:\n%s", body)
	}
	if strings.Contains(body, "Effective") || strings.Contains(body, "Built on") {
		t.Error("an unchained kit has no Built on / Effective")
	}
	if !strings.Contains(body, `class="danger" disabled`) {
		t.Error("Delete must be disabled while a kit builds on it")
	}
}

// A child whose parent's stored config is invalid shows the chain error in the
// banner, HTML-escaped (the parent's bad text reaches the message).
func TestKitPageChainErrorEscaped(t *testing.T) {
	store := seedKits(t)
	if _, err := store.PushKit("evil", "kind: studio\nbase:\n  kit: gone<b>x</b>\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PushKit("orphan", "kind: studio\nbase:\n  kit: evil\n"); err != nil {
		t.Fatal(err)
	}
	body := get(t, kitsHandler(store), "/app/admin/kits/orphan").Body.String()
	if strings.Contains(body, "<b>x</b>") {
		t.Error("chain error not escaped")
	}
	const banner = "Kit chain does not resolve: "
	i := strings.Index(body, banner)
	if i < 0 {
		t.Fatalf("chain error banner not shown:\n%s", body)
	}
	msg, _, _ := strings.Cut(body[i+len(banner):], "</div>")
	if !strings.Contains(msg, "evil@v1 (parent of orphan) is invalid") || !strings.Contains(msg, "gone&lt;b&gt;x&lt;/b&gt;") {
		t.Errorf("chain error banner lacks the escaped reason: %q", msg)
	}
}

func TestKitsListShowsKitBase(t *testing.T) {
	body := get(t, kitsHandler(seedChain(t)), "/app/admin/kits").Body.String()
	if !strings.Contains(body, "kit: web") {
		t.Errorf("Base column lacks %q", "kit: web")
	}
}
