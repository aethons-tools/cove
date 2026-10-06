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
	for name, tc := range map[string]struct{ kind, conn, want string }{
		"unknown":       {"linear", "nope", "no connection"},
		"wrong kind":    {"discord", "linear-acme", "is a linear connection"},
		"no credential": {"discord", "nocred", "no credential"},
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

	// The deprecated *-token-cred binds the implicit connection named by kind:
	// created when absent, its credential set when it has none.
	st2 := jam.NewMemStore()
	c, cred, err = resolveServeConnection(st2, "runtime.discord", "discord", "", "bot-tok", demanded)
	if err != nil || c.Name != "discord" || c.Kind != "discord" || cred != "bot-tok" {
		t.Fatalf("legacy, absent = %+v %q %v", c, cred, err)
	}
	if got, _ := st2.GetConnection(c.ID); got.CredName != "bot-tok" {
		t.Fatalf("legacy connection cred = %q", got.CredName)
	}
	again, _, err := resolveServeConnection(st2, "runtime.discord", "discord", "", "bot-tok", demanded)
	if err != nil || again.ID != c.ID || len(st2.ListConnections()) != 1 {
		t.Fatalf("legacy, present = %+v %v; connections %+v", again, err, st2.ListConnections())
	}
	// Renaming the implicit connection keeps the legacy key bound to it (the
	// connection of its kind), never forking a second, empty one.
	if err := st2.RenameConnection(c.ID, "discord-main"); err != nil {
		t.Fatal(err)
	}
	renamed, _, err := resolveServeConnection(st2, "runtime.discord", "discord", "", "bot-tok", demanded)
	if err != nil || renamed.ID != c.ID || len(st2.ListConnections()) != 1 {
		t.Fatalf("legacy after rename = %+v %v; connections %+v", renamed, err, st2.ListConnections())
	}
	// The legacy key is the credential's source: changing it rebinds.
	demanded["bot-tok-2"] = credSpec{}
	if _, cred, err := resolveServeConnection(st2, "runtime.discord", "discord", "", "bot-tok-2", demanded); err != nil || cred != "bot-tok-2" {
		t.Fatalf("legacy cred change = %q, %v", cred, err)
	}
	if got, _ := st2.GetConnection(c.ID); got.CredName != "bot-tok-2" {
		t.Fatalf("stored cred = %q, want bot-tok-2", got.CredName)
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
