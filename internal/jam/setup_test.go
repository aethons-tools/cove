package jam

import (
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/linediff"
	"github.com/aethons-tools/cove/internal/jam/modelspec"
	"github.com/aethons-tools/cove/internal/jam/sessionctx"
	"github.com/aethons-tools/cove/internal/jam/snippet"
)

func TestParseSetupSections(t *testing.T) {
	all, err := ParseSetupSections("")
	if err != nil || strings.Join(all, ",") != "identity,context,egress,destinations,env,model,kit,seed,mcp,policy" {
		t.Fatalf("all = %v, %v", all, err)
	}
	got, err := ParseSetupSections(" policy, egress ,egress")
	if err != nil || strings.Join(got, ",") != "egress,policy" {
		t.Fatalf("got %v, %v (want render order, deduped)", got, err)
	}
	if _, err := ParseSetupSections("egress,bogus"); err == nil || !strings.Contains(err.Error(), `"bogus"`) {
		t.Fatalf("unknown section err = %v", err)
	}
}

func TestRenderSetupText(t *testing.T) {
	s := StudioSetup{Subject: "studio s1", Note: "n", Sections: []SetupSection{
		{Name: SectionEgress, Source: SourceSnapshot, Stale: true, At: time.Unix(0, 0).UTC(), Body: "policy: kit default\n",
			Diffed: true, Diff: []linediff.Line{{Op: "-", Text: "policy: kit default"}, {Op: "+", Text: "policy: role (1 domain(s))"}, {Op: "…", Text: "4 unchanged line(s)"}}},
		{Name: SectionSeed, Source: SourceUnavailable, Reason: "image not present on the launcher host"},
		{Name: SectionModel, Source: SourceConfig, Body: "x", Diffed: true, Diff: []linediff.Line{{Op: " ", Text: "x"}}},
		{Name: SectionKit, Source: SourceConfig, Body: "k\n", Diffed: true, DiffTooLarge: true},
	}}
	var b strings.Builder
	RenderSetupText(&b, s)
	want := "# studio s1\n(n)\n" +
		"\n== egress (snapshot, stale, 1970-01-01T00:00:00Z)\npolicy: kit default\n-- diff (- this studio, + a raise now)\n- policy: kit default\n+ policy: role (1 domain(s))\n… 4 unchanged line(s)\n" +
		"\n== seed (unavailable)\n(image not present on the launcher host)\n" +
		"\n== model (config)\nx\n-- same as a raise now\n" +
		"\n== kit (config)\nk\n-- diff too large to compute\n"
	if b.String() != want {
		t.Fatalf("got:\n%s\nwant:\n%s", b.String(), want)
	}
}

func TestContextBody(t *testing.T) {
	b := sessionctx.Bundle{Core: "# Session context\n", Files: map[string]string{"INDEX.md": "idx\n", "kit/tools.md": "tools"},
		Budgets: map[string]sessionctx.LayerBudget{sessionctx.LayerKit: {Used: 3, Budget: 800}}, Warnings: []string{"kit core is long\nreally"}}
	got := contextBody(b)
	for _, want := range []string{
		"/agent-data/context/CORE.md", "18", "/agent-data/context/kit/tools.md", "5",
		"  kit 3/800 B\n", "warning: kit core is long\\nreally\n",
		"\n--- /agent-data/context/CORE.md\n# Session context\n", "\n--- /agent-data/context/kit/tools.md\ntools\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Index(got, "INDEX.md\n") > strings.Index(got, "kit/tools.md\n") {
		t.Error("files must be listed CORE.md first, then sorted")
	}
}

func TestEgressBody(t *testing.T) {
	got := egressBody(&EgressPolicy{Domains: []string{"api.github.com", "evil.org"}}, []string{".github.com"}, true)
	for _, want := range []string{"policy: role (2 domain(s))\n", "kit ceiling: 1 domain(s)\n", "api.github.com  role\n", "evil.org        role, outside the kit ceiling (refused in-box)\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	got = egressBody(nil, []string{".github.com", "pypi.org"}, true)
	if !strings.Contains(got, "policy: kit default\n") || !strings.Contains(got, "pypi.org     kit\n") {
		t.Errorf("kit default:\n%s", got)
	}
	if got := egressBody(nil, nil, false); !strings.Contains(got, "kit ceiling: unknown\n") {
		t.Errorf("unknown ceiling:\n%s", got)
	}
	if got := sealedEgressBody([]string{".anthropic.com"}); got != "\nsealed base (always on):\nHOST            FROM\n.anthropic.com  sealed\n" {
		t.Errorf("sealed = %q", got)
	}
}

func TestConnectorBodyMasks(t *testing.T) {
	c := snippet.Connector{Env: map[string]string{"GH_TOKEN": "ghp_live", "ANTHROPIC_API_KEY": "{token}", "ANTHROPIC_BASE_URL": "{base}/anthropic"},
		GitRoute: "/git/", ModelSpec: &modelspec.Spec{Name: "claude-default"}}
	got := connectorBody(c)
	want := "ANTHROPIC_API_KEY=«identity token»\nANTHROPIC_BASE_URL={base}/anthropic\nAT_HARBOR_IDENTITY_TOKEN=«identity token»\n" +
		"AT_JAM_IDENTITY_TOKEN=«identity token»\nGH_TOKEN=«masked»\ngit: https://github.com/ → {base}/git/\nmodel-spec: claude-default\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if got := connectorBody(snippet.Connector{}); !strings.Contains(got, "git: not routed\n") || !strings.Contains(got, "model-spec: none\n") {
		t.Fatalf("empty connector:\n%s", got)
	}
}

func TestKitRefsBody(t *testing.T) {
	kit := KitRef{ID: "my-app", Version: 2, Digest: "0123456789abcdef"}
	got := kitRefsBody(kit, []KitRef{{ID: "default", Version: 1}, {ID: "my-app", Version: 2}}, "cove-kit:0123-asm")
	want := "kit: my-app@v2 digest 0123456789ab\nchain (root first): default@v1 → my-app@v2\nimage tag: cove-kit:0123-asm\n"
	if got != want {
		t.Fatalf("got %q", got)
	}
	if got := kitRefsBody(kit, nil, ""); !strings.Contains(got, "chain (root first): my-app@v2\n") || !strings.Contains(got, "image tag: unknown\n") {
		t.Fatalf("unchained: %q", got)
	}
	if got := kitRefsBody(KitRef{}, nil, ""); got != "kit: none (the launcher's static image)\n" {
		t.Fatalf("no kit: %q", got)
	}
	if kitFingerprint(kit, "t") == kitFingerprint(kit, "u") {
		t.Fatal("the image tag must key the kit fingerprint")
	}
}
