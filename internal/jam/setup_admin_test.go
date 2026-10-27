package jam

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSetupRoutes(t *testing.T) {
	d, inst, _, _ := setupFixture(t)
	ghost := inst
	ghost.ActorID, ghost.Role = "g1", "ghost"
	_ = d.Store.PutInstance(ghost)
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	h := NewAdminHandler(d.Store, d.Sup, nil, LoopbackAuthenticator{}, func(string) bool { return true }, nil, log, nil, WithSetupInspect(d, log))
	ts := httptest.NewServer(h)
	defer ts.Close()
	get := func(path string) (int, StudioSetup, string) {
		t.Helper()
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		var s StudioSetup
		_ = json.Unmarshal(b, &s)
		return resp.StatusCode, s, string(b)
	}
	if code, s, _ := get("/admin/roles/default/dev/setup?sections=egress,policy"); code != 200 || len(s.Sections) != 2 || s.Subject != "role default/dev" {
		t.Fatalf("role setup = %d %+v", code, s)
	}
	if code, s, _ := get("/admin/instances/w1/setup?diff=role"); code != 200 || len(s.Sections) != 10 || !s.Sections[1].Diffed {
		t.Fatalf("studio setup = %d %+v", code, s)
	}
	if code, s, _ := get("/admin/instances/g1/setup?diff=role"); code != 200 || s.Note == "" {
		t.Fatalf("role-gone studio = %d %+v", code, s)
	}
	for path, want := range map[string]int{
		"/admin/roles/default/dev/setup?sections=bogus": 400,
		"/admin/instances/w1/setup?diff=yes":            400,
		"/admin/roles/default/nope/setup":               404,
		"/admin/instances/nope/setup":                   404,
	} {
		if code, _, body := get(path); code != want {
			t.Errorf("%s = %d %s, want %d", path, code, body, want)
		}
	}
	if !strings.Contains(logs.String(), "setup inspected") || !strings.Contains(logs.String(), "sections=egress,policy") {
		t.Errorf("audit log missing: %s", logs.String())
	}
	for _, leak := range []string{"ANTHROPIC_BASE_URL", "--- /agent-data", "GH_HOST", "core budget used"} {
		if strings.Contains(logs.String(), leak) {
			t.Errorf("a body reached the log (%q): %s", leak, logs.String())
		}
	}
}

type denyAuth struct{}

func (denyAuth) Authenticate(*http.Request) (Operator, error) { return Operator{}, io.EOF }

func TestSetupRoutesNeedOperatorAuth(t *testing.T) {
	d, _, _, _ := setupFixture(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ts := httptest.NewServer(NewAdminHandler(d.Store, d.Sup, nil, denyAuth{}, func(string) bool { return true }, nil, log, nil, WithSetupInspect(d, log)))
	defer ts.Close()
	for _, p := range []string{"/admin/roles/default/dev/setup", "/admin/instances/w1/setup"} {
		resp, err := http.Get(ts.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s = %d, want 403", p, resp.StatusCode)
		}
	}
}

// ?manager= is validated up front: an unknown manager is a 404 naming it.
func TestRoleSetupUnknownManager404(t *testing.T) {
	p := newPlacingFake("m0")
	sup, store, _ := supTestKit(t, p)
	withDefaultKit(t, sup, store)
	d := SetupDeps{Store: store, Sup: sup, Images: checkedImages{&fakeImages{}, p}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ts := httptest.NewServer(NewAdminHandler(d.Store, d.Sup, nil, LoopbackAuthenticator{}, func(string) bool { return true }, nil, log, nil, WithSetupInspect(d, log)))
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/admin/roles/default/guest/setup?sections=identity&manager=nope")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 404 || !strings.Contains(string(b), "unknown manager: nope") {
		t.Fatalf("= %d %s, want 404 unknown manager: nope", resp.StatusCode, b)
	}
}
