package adminui_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
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
const kitV2 = `kind: studio
base:
  image: ghcr.io/acme/web@sha256:abc
egress: [github.com, .anthropic.com]
build-args: {GO_VERSION: "1.23"}
secrets:
  GH_TOKEN: {description: clone access}
prompt: You work on the web and api services.
`

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
	if err := store.PutRole("acme", jam.Role{Name: "plain"}); err != nil { // raises the default
		t.Fatal(err)
	}
	if _, err := jam.EnsureDefaultStudioKit(store); err != nil {
		t.Fatal(err)
	}
	return store
}

func kitsHandler(store jam.Store) http.Handler {
	return adminui.Handler(store, testLogger(), nil, nil, anyCred, nil)
}

func TestKitsListSummarizesCurrent(t *testing.T) {
	body := get(t, kitsHandler(seedKits(t)), "/ui/kits").Body.String()
	for _, want := range []string{`href="/ui/kits/web"`, `href="/ui/kits/default"`, "v2", "image", "blessed default", `data-used-by="1"`} {
		if !strings.Contains(body, want) {
			t.Errorf("kits list missing %q", want)
		}
	}
}

func TestKitPageShowsCurrentVersion(t *testing.T) {
	rec := get(t, kitsHandler(seedKits(t)), "/ui/kits/web")
	if rec.Code != http.StatusOK {
		t.Fatalf("kit page = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"<h1>web</h1>", `aria-current="page">Kits`,
		`href="/ui/kits/web?v=1"`, `class="ver current`,
		"ghcr.io/acme/web@sha256:abc",
		"github.com", "anthropic.com", // egress + excluded by the ceiling
		"GO_VERSION", "1.23", "GH_TOKEN", "clone access",
		"You work on the web and api services.",
		`href="/ui/roles/acme/builder"`,
		`hx-post="/ui/kits/web/versions"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("kit page missing %q", want)
		}
	}
	if strings.Contains(body, "name: web") {
		t.Errorf("rendered kit must not carry a name")
	}
}

func TestDefaultKitListsImplicitRoles(t *testing.T) {
	body := get(t, kitsHandler(seedKits(t)), "/ui/kits/default").Body.String()
	if !strings.Contains(body, `href="/ui/roles/acme/plain"`) || !strings.Contains(body, "no kit set") {
		t.Errorf("default kit should list roles with no kit set")
	}
}

func TestKitVersionDiff(t *testing.T) {
	body := get(t, kitsHandler(seedKits(t)), "/ui/kits/web?v=2&diff=1").Body.String()
	for _, want := range []string{
		`class="d-del"`, "You work on the web service.",
		`class="d-add"`, "You work on the web and api services.",
		"same build digest",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("diff missing %q", want)
		}
	}
}

func TestKitPagePackedContextSummarized(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range map[string]string{"Dockerfile": "FROM ${COVE_BASE_IMAGE}\n", "install.sh": "echo hi\n"} {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write([]byte(body))
	}
	_ = tw.Close()
	_ = gz.Close()
	b64 := base64.StdEncoding.EncodeToString(buf.Bytes())
	store := newStore(t)
	if _, _, err := jam.PushStudioKit(store, "ctx", "kind: studio\nbase:\n  context: "+b64+"\n"); err != nil {
		t.Fatal(err)
	}
	body := get(t, kitsHandler(store), "/ui/kits/ctx").Body.String()
	for _, want := range []string{"Dockerfile", "install.sh", "0755", "packed tar.gz"} {
		if !strings.Contains(body, want) {
			t.Errorf("packed-context kit page missing %q", want)
		}
	}
	// the display YAML abbreviates the blob (the push form keeps it whole);
	// every base64 gzip starts "H4sI"
	i := strings.Index(body, `id="kit-yaml">`)
	if i < 0 {
		t.Fatal("no display YAML")
	}
	yaml := body[i : i+strings.Index(body[i:], "</pre>")]
	if strings.Contains(yaml, "H4sI") || !strings.Contains(yaml, "packed tar.gz") {
		t.Errorf("display YAML should abbreviate the packed context:\n%s", yaml)
	}
	if !strings.Contains(body, "H4sI") {
		t.Errorf("the push form should carry the full context")
	}
}

func TestKitPageLegacyRowShowsParseError(t *testing.T) {
	store := newStore(t)
	if _, err := store.PushKit("old", "listen: :443\n"); err != nil { // a pre-studio row
		t.Fatal(err)
	}
	h := kitsHandler(store)
	if body := get(t, h, "/ui/kits").Body.String(); !strings.Contains(body, "invalid") {
		t.Errorf("list should mark the unparseable kit invalid")
	}
	rec := get(t, h, "/ui/kits/old")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "not a valid studio kit") || !strings.Contains(rec.Body.String(), "listen: :443") {
		t.Errorf("legacy row page = %d, should show the parse error and raw text", rec.Code)
	}
}

func TestKitPageNotFound(t *testing.T) {
	rec := get(t, kitsHandler(newStore(t)), "/ui/kits/nope")
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "<nav") {
		t.Fatalf("missing kit = %d", rec.Code)
	}
}

func TestNewKitForm(t *testing.T) {
	store := newStore(t)
	h := kitsHandler(store)
	rec := post(t, h, "/ui/kits", url.Values{"name": {"web"}, "config": {kitV1}})
	if rec.Code != http.StatusOK || rec.Header().Get("HX-Redirect") != "/ui/kits/web" {
		t.Fatalf("new kit = %d redirect=%q: %s", rec.Code, rec.Header().Get("HX-Redirect"), rec.Body.String())
	}
	for name, form := range map[string]url.Values{
		"existing":     {"name": {"web"}, "config": {kitV2}},
		"invalid yaml": {"name": {"x"}, "config": {"listen: :443"}},
		"bad name":     {"name": {"no/slash"}, "config": {kitV1}},
		"legacy name":  {"name": {"api"}, "config": {"kind: studio\nname: web\n"}},
	} {
		want := http.StatusBadRequest
		if name == "existing" {
			want = http.StatusConflict
		}
		if rec := post(t, h, "/ui/kits", form); rec.Code != want {
			t.Errorf("%s = %d, want %d", name, rec.Code, want)
		}
	}
}

func TestPushVersionOnKitPage(t *testing.T) {
	store := seedKits(t)
	h := kitsHandler(store)
	rec := post(t, h, "/ui/kits/web/versions", url.Values{"config": {kitV1}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `id="kit"`) || !strings.Contains(rec.Body.String(), "pushed web v3") {
		t.Fatalf("push v3 = %d: %s", rec.Code, rec.Body.String())
	}
	rec = post(t, h, "/ui/kits/web/versions", url.Values{"config": {kitV1}})
	if !strings.Contains(rec.Body.String(), "unchanged") {
		t.Errorf("identical push should report unchanged: %s", rec.Body.String())
	}
	if k, _ := store.GetKit("web"); k.Current != 3 || len(k.Versions) != 3 {
		t.Fatalf("kit = current v%d, %d versions; want v3 of 3", k.Current, len(k.Versions))
	}
	if rec := post(t, h, "/ui/kits/ghost/versions", url.Values{"config": {kitV1}}); rec.Code != http.StatusNotFound {
		t.Errorf("push to missing kit = %d, want 404", rec.Code)
	}
}

// Pinning from the kit page (htmx target #kit) answers with the kit body.
func TestPinFromKitPage(t *testing.T) {
	store := seedKits(t)
	req := httptest.NewRequest(http.MethodPost, "/ui/kits/web/pin", strings.NewReader("version=1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://"+req.Host)
	req.Header.Set("HX-Target", "kit")
	rec := httptest.NewRecorder()
	kitsHandler(store).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `id="kit"`) {
		t.Fatalf("pin from page = %d", rec.Code)
	}
	if k, _ := store.GetKit("web"); k.Current != 1 {
		t.Fatalf("current = v%d, want v1", k.Current)
	}
}
