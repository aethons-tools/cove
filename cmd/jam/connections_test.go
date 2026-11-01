package main

import (
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
)

func TestResolveServeConnection(t *testing.T) {
	demanded := map[string]credSpec{"lin-tok": {}, "bot-tok": {}}
	st := jam.NewMemStore()
	if _, err := st.CreateConnection(jam.Connection{Kind: "linear", Name: "linear-acme", CredName: "lin-tok"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateConnection(jam.Connection{Kind: "discord", Name: "nocred"}); err != nil {
		t.Fatal(err)
	}

	c, cred, err := resolveServeConnection(st, "runtime.requisitioner", "linear", "linear-acme", "", demanded)
	if err != nil || c.Name != "linear-acme" || cred != "lin-tok" {
		t.Fatalf("by name = %+v %q %v", c, cred, err)
	}
	if _, err := st.CreateConnection(jam.Connection{Kind: "github", Name: "gh-acme", CredName: "lin-tok"}); err != nil {
		t.Fatal(err)
	}
	if c, _, err := resolveServeConnection(st, "runtime.requisitioner", "github", "gh-acme", "", demanded); err != nil || c.Kind != "github" {
		t.Fatalf("github connection = %+v %v", c, err)
	}
	for name, tc := range map[string]struct{ kind, conn, want string }{
		"linear as github": {"github", "linear-acme", "is a linear connection, want github"},
		"unknown":          {"linear", "nope", "no connection"},
		"wrong kind":       {"discord", "linear-acme", "is a linear connection"},
		"no credential":    {"discord", "nocred", "no credential"},
	} {
		if _, _, err := resolveServeConnection(st, "runtime.x", tc.kind, tc.conn, "", demanded); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
	if err := st.SetConnectionCred(mustConnID(t, st, "nocred"), "undeclared"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := resolveServeConnection(st, "runtime.x", "discord", "nocred", "", demanded); err == nil || !strings.Contains(err.Error(), "not a demanded credential") {
		t.Errorf("undeclared credential: err = %v", err)
	}
}

func mustConnID(t *testing.T, st jam.Store, name string) ident.ID {
	t.Helper()
	id, ok := st.LookupName(ident.Connection, name)
	if !ok {
		t.Fatalf("no connection %q", name)
	}
	return id
}

func TestServeConfigConnectionKeys(t *testing.T) {
	base := "credentials:\n  lin-tok: {}\n  bot-tok: {}\nruntime:\n"
	ok := map[string]string{
		"discord connection": base + "  discord:\n    connection: discord-main\n",
		"discord legacy":     base + "  discord:\n    bot-token-cred: bot-tok\n",
	}
	for name, y := range ok {
		cfg, err := parseServeConfig([]byte(y))
		if err == nil {
			err = cfg.validateDiscord()
		}
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if strings.Contains(name, "legacy") && len(cfg.deprecated) == 0 {
			t.Errorf("%s: no deprecation recorded", name)
		}
	}
	bad := map[string]string{
		"discord both":    base + "  discord:\n    connection: d\n    bot-token-cred: bot-tok\n",
		"discord neither": base + "  discord: {}\n",
	}
	for name, y := range bad {
		cfg, err := parseServeConfig([]byte(y))
		if err == nil {
			err = cfg.validateDiscord()
		}
		if err == nil {
			t.Errorf("%s: accepted, want an error", name)
		}
	}
}

func TestLegacyTokenCredBindsExisting(t *testing.T) {
	demanded := map[string]credSpec{"bot": {}}
	st := jam.NewMemStore()
	if _, err := st.CreateConnection(jam.Connection{Kind: "discord", Name: "discord", CredName: "bot"}); err != nil {
		t.Fatal(err)
	}
	c, cred, err := resolveServeConnection(st, "runtime.discord", "discord", "", "bot", demanded)
	if err != nil || c.Name != "discord" || cred != "bot" {
		t.Fatalf("= %+v %q %v", c, cred, err)
	}
}

func TestLegacyTokenCredNoConnection(t *testing.T) {
	st := jam.NewMemStore()
	_, _, err := resolveServeConnection(st, "runtime.discord", "discord", "", "bot", map[string]credSpec{"bot": {}})
	want := "runtime.discord: no discord connection (the deprecated *-token-cred no longer creates one; add connections/discord.yml to the config repo, or set runtime.discord.connection)"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
	if _, ok := st.ConnectionOfKind("discord"); ok {
		t.Fatal("a connection was created")
	}
}

func TestLegacyTokenCredOtherCred(t *testing.T) {
	st := jam.NewMemStore()
	c, err := st.CreateConnection(jam.Connection{Kind: "discord", Name: "discord", CredName: "old"})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = resolveServeConnection(st, "runtime.discord", "discord", "", "bot", map[string]credSpec{"bot": {}, "old": {}})
	want := `runtime.discord: the discord connection's credential is "old", not "bot" (set it in the config repo, or set runtime.discord.connection)`
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
	if got, _ := st.GetConnection(c.ID); got.CredName != "old" {
		t.Fatalf("cred = %q", got.CredName)
	}
}
