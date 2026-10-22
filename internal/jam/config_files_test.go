package jam

import (
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

func TestParseUserFileStrict(t *testing.T) {
	f, err := ParseUserFile("users/bob.yml", "bob", []byte("kind: user\nversion: 1\nlogins: [bob@acme]\n"))
	if err != nil || f.Name != "bob" || len(f.Logins) != 1 {
		t.Fatalf("f=%+v err=%v", f, err)
	}
	for in, want := range map[string]string{
		"kind: user\nversion: 1\noidc: [x]\n":      `users/bob.yml: unknown field "oidc"`,
		"kind: role\nversion: 1\n":                 "users/bob.yml: kind must be user",
		"kind: user\nversion: 2\n":                 "users/bob.yml: version must be 1",
		"kind: user\nversion: 1\nname: alice\n":    `users/bob.yml: name "alice" does not match the file name "bob"`,
		"kind: user\nversion: 1\nlogins: {a: b}\n": "users/bob.yml: line 3: a value has the wrong type",
		"": "users/bob.yml: empty file",
	} {
		if _, err := ParseUserFile("users/bob.yml", "bob", []byte(in)); err == nil || err.Error() != want {
			t.Errorf("%q: err=%v, want %q", in, err, want)
		}
	}
}

func TestTypeErrorNeverEchoesValue(t *testing.T) {
	_, err := ParseConnectionFile("connections/acme.yml", "acme", []byte("kind: connection\nversion: 1\nservice: [sk-secret-value]\n"))
	if err == nil || strings.Contains(err.Error(), "sk-secret-value") {
		t.Fatalf("err = %v", err)
	}
}

func TestModelSpecFileHarnessVersion(t *testing.T) {
	f, err := ParseModelSpecFile("model-specs/default.yml", "default", []byte(
		"kind: model-spec\nversion: 1\ntype: claude\nharness-version: 2.1.3\nprincipal: {credential: anthropic}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if ms := f.spec(); ms.Name != "default" || ms.Version != "2.1.3" || ms.Principal.Credential != "anthropic" {
		t.Fatalf("spec = %+v", ms)
	}
	if got := string(newConfigObject(f).Files["model-specs/default.yml"]); !strings.Contains(got, "harness-version: 2.1.3\n") {
		t.Fatalf("render:\n%s", got)
	}
}

func TestRoleFileRoundTrip(t *testing.T) {
	in := "kind: role\nversion: 1\nscope:\n  destinations: [anthropic]\n  ttl: 90m\n  egress: [GitHub.com]\n" +
		"kit: web-dev\nallocation:\n  max-personal: 2\n  idle-after: 4h\n  standing: [{name: lead, prompt: run it}]\n" +
		"turn-end: {idle-timeout: 30m, on-idle: wake}\ncollaborate: {ssh: members}\n" +
		"context:\n  core: be brief\n  leaves: [{name: a.md, read-when: always, body: x}]\n"
	f, err := ParseRoleFile("projects/app/roles/ic.yml", "ic", []byte(in))
	if err != nil {
		t.Fatal(err)
	}
	r, err := f.role()
	if err != nil {
		t.Fatal(err)
	}
	if r.Scope.TTL != 90*time.Minute || r.Scope.Egress == nil || r.Scope.Egress.Domains[0] != "github.com" ||
		r.Allocation.IdleAfter != 4*time.Hour || r.Allocation.Standing[0].Name != "lead" || r.Context.Core != "be brief" {
		t.Fatalf("role = %+v", r)
	}
	back := roleFileOf("app", "ic", r, func(ident.ID) (string, bool) { return "", false })
	if a, b := newConfigObject(f).Canon, newConfigObject(back).Canon; string(a) != string(b) {
		t.Fatalf("canon differs:\n%s\n---\n%s", a, b)
	}
	if !strings.Contains(string(newConfigObject(back).Canon), "ttl: 1h30m\n") {
		t.Fatalf("duration not canonical:\n%s", newConfigObject(back).Canon)
	}
	if _, err := ParseRoleFile("projects/app/roles/ic.yml", "ic", []byte("kind: role\nversion: 1\nallocation: {max-personal: -1}\n")); err == nil ||
		err.Error() != "projects/app/roles/ic.yml: allocation.max-personal must be >= 0" {
		t.Fatalf("negative: %v", err)
	}
}

func TestRoleFileAddressingByName(t *testing.T) {
	r := Role{Name: "ic", Scope: Scope{Addressing: []string{"user:usr_01hx0000000000000000000000", "channel:*"}}}
	f := roleFileOf("app", "ic", r, func(id ident.ID) (string, bool) { return "bob", id == "usr_01hx0000000000000000000000" })
	if f.Scope.Addressing[0] != "user:bob" || f.Scope.Addressing[1] != "channel:*" {
		t.Fatalf("addressing = %v", f.Scope.Addressing)
	}
}

func TestContextMarkdown(t *testing.T) {
	l, err := ParseContextMarkdown("context.md", []byte("---\r\nleaves:\r\n  - {name: a.md, read-when: always, body: hi}\r\n---\r\nBe kind.\r\n\r\n"))
	if err != nil || l.Core != "Be kind." || len(l.Leaves) != 1 || l.Leaves[0].ReadWhen != "always" {
		t.Fatalf("l=%+v err=%v", l, err)
	}
	if got := string(RenderContextMarkdown(sessionctx.Layer{Core: "Be kind."})); got != "Be kind.\n" {
		t.Fatalf("plain render %q", got)
	}
	again, _ := ParseContextMarkdown("context.md", RenderContextMarkdown(l))
	if again.Core != l.Core || len(again.Leaves) != 1 {
		t.Fatalf("round trip %+v", again)
	}
	// A core that opens with a markdown rule gets empty front matter, so it
	// is not read back as front matter.
	for _, core := range []string{"---\nBelow the rule.", "---", "---\nleaves: x\n---\nhi", "----\nx"} {
		out := RenderContextMarkdown(sessionctx.Layer{Core: core})
		back, err := ParseContextMarkdown("context.md", out)
		if err != nil || back.Core != core || len(back.Leaves) != 0 {
			t.Fatalf("core %q rendered %q: back %+v err %v", core, out, back, err)
		}
	}
	for in, want := range map[string]string{
		"---\nleaves: []\nBe kind.\n":   "context.md: front matter is not closed with ---",
		"---\ncore: x\n---\nBe kind.\n": `context.md: unknown field "core"`,
	} {
		if _, err := ParseContextMarkdown("context.md", []byte(in)); err == nil || err.Error() != want {
			t.Errorf("%q: %v", in, err)
		}
	}
}

func TestProjectFileNormalize(t *testing.T) {
	f, err := ParseProjectFile("projects/app/project.yml", "app", []byte("kind: project\nversion: 1\n"+
		"escalation: [{targets: [user:bob], timeout: 60m}]\n"+
		"members: [{user: zed}, {user: bob, delivery: [{service: discord, address: \"123\"}]}]\n"+
		"rooms: [{key: ops, label: ops, bindings: [{connection: acme, ref: ACME-1, mode: both}]}, {key: alpha, bindings: [{connection: acme, ref: ACME-2}]}]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if f.Members[0].User != "bob" || f.Rooms[0].Key != "alpha" || f.Rooms[1].Label != "" || f.Rooms[1].Bindings[0].Mode != "" || f.Escalation[0].Timeout != "1h" {
		t.Fatalf("not normalized: %+v", f)
	}
}

func TestFormatConfigDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{0: "", 30 * time.Second: "30s", 90 * time.Second: "1m30s",
		5 * time.Minute: "5m", time.Hour: "1h", 90 * time.Minute: "1h30m", time.Hour + 5*time.Second: "1h0m5s"} {
		if got := formatConfigDuration(d); got != want {
			t.Errorf("%v: %q, want %q", d, got, want)
		}
	}
}

func TestConfigKeyPaths(t *testing.T) {
	for k, want := range map[ConfigKey]string{
		{ConfigKindDestination, "gh"}: "destinations/gh.yml", {ConfigKindModelSpec, "m"}: "model-specs/m.yml",
		{ConfigKindUser, "bob"}: "users/bob.yml", {ConfigKindConnection, "acme"}: "connections/acme.yml",
		{ConfigKindKit, "web"}: "kits/web/kit.yml", {ConfigKindProject, "app"}: "projects/app/project.yml",
		{ConfigKindRole, "app/ic"}: "projects/app/roles/ic.yml", {ConfigKindJamContext, "jam"}: "context.md",
	} {
		if k.Path() != want {
			t.Errorf("%v: %s", k, k.Path())
		}
	}
}
func TestRoleFileKey(t *testing.T) {
	f, err := ParseRoleFile("projects/app/roles/ic.yml", "ic", []byte("kind: role\nversion: 1\n"))
	if err != nil {
		t.Fatal(err)
	}
	if k := newConfigObject(f).Key; k != (ConfigKey{ConfigKindRole, "app/ic"}) {
		t.Fatalf("key = %v", k)
	}
}

func TestDestinationFileRender(t *testing.T) {
	f, err := ParseDestinationFile("destinations/gh.yml", "gh", []byte("kind: destination\nversion: 1\nroute: /gh\nupstream: https://github.com\n"))
	if err != nil || f.Destination.Name != "gh" {
		t.Fatalf("f=%+v err=%v", f, err)
	}
	o := newConfigObject(f)
	got := string(o.Files["destinations/gh.yml"])
	if !strings.HasPrefix(got, "kind: destination\nversion: 1\n") || strings.Contains(got, "\nname:") {
		t.Fatalf("render:\n%s", got)
	}
	if o.Digest() == "" || o.Key.Path() != "destinations/gh.yml" {
		t.Fatalf("o=%+v", o)
	}
}
