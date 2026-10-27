package jam

import (
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/modelspec"
	"github.com/aethons-tools/cove/internal/kit"
)

func TestDestinationsBody(t *testing.T) {
	st := NewMemStore()
	_ = st.AddDestination(Destination{Name: "gh", Route: "/gh/", Upstream: "https://user:pw@api.github.com", IdentityIn: ApplyBearer,
		Apply: ApplyCustom, ApplySpec: &OutboundSpec{Header: "Authorization", Template: "token {cred}"}, CredName: "gh-pat",
		Env: map[string]string{"GH_TOKEN": "ghp_SECRET", "GH_HOST": "{host}"}, Git: true, AllowPaths: []string{"/repos/*"}})
	if err := st.PutRole("default", Role{Name: "r", Scope: Scope{Destinations: []string{"gh", "ghost"}, Credentials: map[string]string{"gh": "gh-bot"}}}); err != nil {
		t.Fatal(err)
	}
	got := destinationsBody(st, Actor{Grants: []Grant{{Project: "default", Role: "r"}}})
	for _, want := range []string{"gh\n", "  route:       /gh/\n", "  upstream:    https://api.github.com\n", "  identity-in: bearer\n",
		"  apply:       custom (header Authorization, template \"token {cred}\")\n", "  credential:  gh-bot\n", "  git:         routed (https://api.github.com/)\n",
		"  allow-paths: /repos/*\n", "  env keys:    GH_HOST, GH_TOKEN\n", "ghost: not configured (skipped)\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	for _, bad := range []string{"ghp_SECRET", "user:pw"} {
		if strings.Contains(got, bad) {
			t.Errorf("%q leaked", bad)
		}
	}
	if got := destinationsBody(st, Actor{}); got != "no destinations in scope\n" {
		t.Errorf("empty scope = %q", got)
	}
}

func TestModelAndMCPBodies(t *testing.T) {
	if got := modelBody(nil); got != "model-spec: none (the harness's built-in defaults)\n" {
		t.Errorf("nil spec = %q", got)
	}
	ms := &ModelSpec{Name: "opus", Type: "claude", Version: "2.1.0"}
	ms.Principal.Credential = "anthropic-key"
	ms.Claude = &ClaudeSpec{Provider: "anthropic", Settings: map[string]any{"env": map[string]any{"GH_TOKEN": "ghp_LITERAL"}}}
	got := modelBody(ms)
	if !strings.Contains(got, `"name": "opus"`) || !strings.Contains(got, `"credential": "anthropic-key"`) {
		t.Errorf("spec = %q", got)
	}
	if strings.Contains(got, "ghp_LITERAL") || !strings.Contains(got, `"GH_TOKEN": "`+MarkMasked+`"`) {
		t.Errorf("settings env.GH_TOKEN not masked:\n%s", got)
	}
	got = mcpBody(map[string]kit.MCPServer{
		"docs":   {Type: "stdio", Command: "docs-mcp", Args: []string{"--key", "sk-live"}},
		"search": {Type: "http", URL: "https://u:p@search.example/mcp", Headers: map[string]string{"Authorization": "Bearer ${SEARCH_TOKEN}"}},
	}, true)
	for _, want := range []string{"docs", "docs-mcp (+2 args)", "https://search.example/mcp", "Authorization", "intercom (jam-studio-agent mcp, always loaded): alarm_clear,"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	for _, bad := range []string{"sk-live", "SEARCH_TOKEN", "u:p"} {
		if strings.Contains(got, bad) {
			t.Errorf("%q leaked:\n%s", bad, got)
		}
	}
	if got := mcpBody(nil, false); !strings.Contains(got, "kit servers: unknown (no kit resolved)\n") {
		t.Errorf("unknown kit = %q", got)
	}
}

func TestPolicyBodies(t *testing.T) {
	st := NewMemStore()
	if err := st.SetEscalationPolicy("default", "", []EscalationTier{{Targets: []string{"user:alice"}, Timeout: 30 * time.Minute}}); err != nil {
		t.Fatal(err)
	}
	r := Role{Name: "r", Allocation: RoleAllocation{MaxEphemeral: 2, Standing: []StandingSession{{Name: "bot"}}},
		TurnEnd: TurnEndPolicy{IdleTimeout: time.Hour, OnIdle: OnIdleTeardown}, Collaborate: CollaboratePolicy{SSH: "members"}}
	got := rolePolicyBody(st, "default", r)
	for _, want := range []string{
		"allocation: max-ephemeral 2, max-personal 0, max-personal-per-owner pool only\n",
		"personal idle: idle-after 4h0m0s, nag-every 24h0m0s, reclaim-after never\n",
		"standing: bot\n", "turn-end: idle-timeout 1h0m0s, on-idle teardown, time-zone UTC\n",
		"collaborate: preview members, ssh members\n", "escalation (default): 1. user:alice (after 30m0s)\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	inst := Instance{IdleOverride: &IdleOverride{Duration: 45 * time.Minute, Scope: IdleScopeAlways},
		Alarms: []Alarm{{Name: "nightly", Schedule: "0 3 * * *", NextAt: time.Unix(0, 0).UTC(), Gate: "curl -H 'x: sk-live' …", Note: "private"}}}
	got = instancePolicyBody(inst)
	if !strings.Contains(got, "idle override: 45m0s (always)\n") || !strings.Contains(got, "  nightly  0 3 * * *  next 1970-01-01T00:00:00Z  gate yes\n") {
		t.Errorf("instance policy:\n%s", got)
	}
	if strings.Contains(got, "sk-live") || strings.Contains(got, "private") {
		t.Errorf("a gate command or note leaked:\n%s", got)
	}
}

func TestIdentityBodies(t *testing.T) {
	st := NewMemStore()
	if got := roleIdentityBody(st, "", "dev"); got != "project: default\nrole: dev\nsession kind: ephemeral (a would-be raise: no unit, no owner)\n" {
		t.Errorf("role identity = %q", got)
	}
	inst := Instance{ActorID: "s1", Project: "default", Role: "dev", Name: "bot", SessionKind: SessionKindStanding, Phase: PhaseLive, RaisedAt: time.Unix(0, 0).UTC()}
	want := "project: default\nrole: dev\nsession kind: standing (name bot)\nstudio: s1\nphase: live\nraised at: 1970-01-01T00:00:00Z\n"
	if got := instanceIdentityBody(st, inst); got != want {
		t.Errorf("instance identity = %q", got)
	}
}

func TestSetupConfigFix1(t *testing.T) {
	ms := &ModelSpec{Name: "m", Type: "claude"}
	ms.Principal.Credential = "anthropic-key"
	ms.Principal.Headers = []modelspec.HeaderRule{
		{Name: "X-Goog-Api-Key", Set: "sk_live_HDR1"},
		{Name: "x-custom-token", EnsureListItem: "sk_live_HDR2"},
	}
	ms.Claude = &ClaudeSpec{Provider: "vertex", ProviderEnv: map[string]string{"VERTEX_API_KEY": "sk_live_PENV"}}
	got := modelBody(ms)
	for _, bad := range []string{"sk_live_HDR1", "sk_live_HDR2", "sk_live_PENV"} {
		if strings.Contains(got, bad) {
			t.Errorf("%q leaked:\n%s", bad, got)
		}
	}
	ms.Principal.Credential = "Bearer sk_live_CRED1234"
	if got := modelBody(ms); strings.Contains(got, "sk_live_CRED1234") {
		t.Errorf("secret-looking credential leaked:\n%s", got)
	}

	st := NewMemStore()
	_ = st.AddDestination(Destination{Name: "gh", Route: "/gh/", Upstream: "https://api.github.com", IdentityIn: ApplyBearer,
		Apply: ApplyCustom, ApplySpec: &OutboundSpec{Header: "Authorization", Template: "{cred};static=sk_live_LITERAL123"}, CredName: "gh-pat"})
	_ = st.PutRole("default", Role{Name: "r", Scope: Scope{Destinations: []string{"gh"}}})
	d := destinationsBody(st, Actor{Grants: []Grant{{Project: "default", Role: "r"}}})
	if strings.Contains(d, "sk_live_LITERAL123") || !strings.Contains(d, MarkMasked) {
		t.Errorf("template literal leaked:\n%s", d)
	}
	if got := outboundText(ApplyCustom, &OutboundSpec{Header: "Authorization", Template: "Bearer {cred}"}); !strings.Contains(got, "Bearer {cred}") {
		t.Errorf("scheme template = %q", got)
	}

	m := mcpBody(map[string]kit.MCPServer{"x": {Type: "stdio", Command: "env API_KEY=sk_live_12345678 mcp"}}, true)
	if strings.Contains(m, "sk_live_12345678") {
		t.Errorf("command leaked:\n%s", m)
	}
}

func TestSetupConfigFix2(t *testing.T) {
	ms := &ModelSpec{Name: "m", Type: "claude"}
	ms.Principal.Credential = "c"
	ms.Principal.Headers = []modelspec.HeaderRule{{Name: "Cookie", Set: "session=COOKIEVAL12345"}, {Name: "X-Base", Set: "{base}"}}
	got := modelBody(ms)
	if strings.Contains(got, "COOKIEVAL12345") {
		t.Errorf("cookie leaked:\n%s", got)
	}
	if !strings.Contains(got, `"set": "{base}"`) {
		t.Errorf("placeholder-only value hidden:\n%s", got)
	}
	m := mcpBody(map[string]kit.MCPServer{"x": {Type: "stdio", Command: "mcp --token sk_live_SPACE9999"}}, true)
	if strings.Contains(m, "sk_live_SPACE9999") {
		t.Errorf("command leaked:\n%s", m)
	}
}
