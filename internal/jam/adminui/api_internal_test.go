package adminui

import "testing"

func TestAppTabBadges(t *testing.T) {
	items := []attnItem{
		{Kind: attnBroken, Scope: "acme", Tab: "agents", Subject: "ses_1"},
		{Kind: attnConfig, Scope: "acme", Tab: "agents", Subject: "acme/coder"},
		{Kind: attnAlert, Tab: "health", Subject: "cond:x"},
	}
	b := appTabBadges(items)
	if g := b["agents"]; g.N != 1 || !g.Red {
		t.Errorf("agents = %+v, want 1 red", g)
	}
	if g := b["config"]; g.N != 1 || g.Red {
		t.Errorf("config = %+v, want 1 amber", g)
	}
	if g := b["health"]; g.N != 1 {
		t.Errorf("health = %+v, want 1", g)
	}
	if g := b["dashboard"]; g.N != 3 || !g.Red {
		t.Errorf("dashboard = %+v, want all 3, red", g)
	}
}
