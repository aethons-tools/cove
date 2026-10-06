package adminui_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
)

func TestProjectPageOffersPrefilledEdits(t *testing.T) {
	body := get(t, projHandler(seedProjects(t)), "/ui/projects/acme").Body.String()
	for _, want := range []string{
		`hx-post="/ui/projects/acme/members"`, "discord:dm-alice", `hx-delete="/ui/projects/acme/members/usr_`,
		`hx-post="/ui/projects/acme/channels"`, `hx-delete="/ui/projects/acme/channels/eng"`,
		`hx-post="/ui/projects/acme/escalation"`,
		"human:alice@30m", "human:alice,channel:eng@10m",
		`hx-delete="/ui/projects/acme/escalation?category=deploy"`,
		`hx-post="/ui/projects/acme/chat-service"`, `<option value="discord" selected`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("project page missing %q", want)
		}
	}
}

func TestAddRemoveChannel(t *testing.T) {
	store := seedProjects(t)
	h := projHandler(store)
	if rec := post(t, h, "/ui/projects/acme/channels", url.Values{"name": {"ops"}, "service": {"discord"}, "ref": {"chan-ops"}}); rec.Code != http.StatusOK {
		t.Fatalf("add channel = %d: %s", rec.Code, rec.Body.String())
	}
	if r, _ := store.GetRoster("acme"); len(r.Channels) != 2 {
		t.Fatalf("channels = %+v", r.Channels)
	}
	if rec := post(t, h, "/ui/projects/acme/channels", url.Values{"name": {"x"}, "service": {"discord"}}); rec.Code != http.StatusBadRequest {
		t.Errorf("channel without ref = %d, want 400", rec.Code)
	}
	if rec := del(t, h, "/ui/projects/acme/channels/eng"); rec.Code != http.StatusOK {
		t.Fatalf("remove channel = %d", rec.Code)
	}
	if r, _ := store.GetRoster("acme"); len(r.Channels) != 1 || r.Channels[0].Name != "ops" {
		t.Fatalf("channels after remove = %+v", r.Channels)
	}
}

func TestEditEscalation(t *testing.T) {
	store := seedProjects(t)
	h := projHandler(store)
	rec := post(t, h, "/ui/projects/acme/escalation", url.Values{"category": {""}, "tiers": {"human:alice@5m\r\n\r\nhuman:alice, channel:eng@1h\n"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("set default = %d: %s", rec.Code, rec.Body.String())
	}
	p, _ := store.GetProject("acme")
	if len(p.Escalation) != 2 || p.Escalation[0].Timeout != 5*time.Minute || len(p.Escalation[1].Targets) != 2 {
		t.Fatalf("default chain = %+v", p.Escalation)
	}
	if rec := post(t, h, "/ui/projects/acme/escalation", url.Values{"category": {"infra"}, "tiers": {"human:alice@10m"}}); rec.Code != http.StatusOK {
		t.Fatalf("new category = %d", rec.Code)
	}
	for name, tiers := range map[string]string{"no timeout": "human:alice", "bad timeout": "human:alice@soon", "empty": "  "} {
		if rec := post(t, h, "/ui/projects/acme/escalation", url.Values{"category": {"infra"}, "tiers": {tiers}}); rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", name, rec.Code)
		}
	}
	if rec := del(t, h, "/ui/projects/acme/escalation?category=deploy"); rec.Code != http.StatusOK {
		t.Fatalf("clear deploy = %d", rec.Code)
	}
	body := get(t, h, "/ui/projects/acme").Body.String()
	if strings.Contains(body, "category <span class=\"mono\">deploy</span>") {
		t.Errorf("a cleared chain should not be shown")
	}
	if !strings.Contains(body, "infra") {
		t.Errorf("new category chain missing")
	}
}

// Escalation targets that resolve to nobody on the roster are flagged.
func TestEscalationFlagsUnknownTargets(t *testing.T) {
	store := seedProjects(t)
	if err := store.SetEscalationPolicy("acme", "", []jam.EscalationTier{{Targets: []string{"human:ghost", "human:alice"}, Timeout: time.Minute}}); err != nil {
		t.Fatal(err)
	}
	body := get(t, projHandler(store), "/ui/projects/acme").Body.String()
	if n := strings.Count(body, `class="chip unknown-target"`); n != 1 {
		t.Errorf("want 1 flagged target (human:ghost), got %d", n)
	}
}

func TestSetChatService(t *testing.T) {
	store := seedProjects(t)
	h := projHandler(store)
	if rec := post(t, h, "/ui/projects/acme/chat-service", url.Values{"service": {""}}); rec.Code != http.StatusOK {
		t.Fatalf("clear chat service = %d", rec.Code)
	}
	if p, _ := store.GetProject("acme"); p.ChatService != "" {
		t.Fatalf("chat service = %q, want cleared", p.ChatService)
	}
	if rec := post(t, h, "/ui/projects/acme/chat-service", url.Values{"service": {"discord"}}); rec.Code != http.StatusOK {
		t.Fatalf("set chat service = %d", rec.Code)
	}
	if p, _ := store.GetProject("acme"); jam.ChatKind(store, p) != "discord" {
		t.Fatalf("chat service = %q", p.ChatService)
	}
}

func TestProjectEditsRefuseCrossOriginAndUnknownProject(t *testing.T) {
	h := projHandler(seedProjects(t))
	req := httptest.NewRequest(http.MethodPost, "/ui/projects/acme/members", strings.NewReader("user=x"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://evil.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-origin = %d, want 403", rec.Code)
	}
	if rec := post(t, h, "/ui/projects/ghost/members", url.Values{"user": {"alice"}}); rec.Code != http.StatusNotFound {
		t.Errorf("unknown project = %d, want 404", rec.Code)
	}
}
