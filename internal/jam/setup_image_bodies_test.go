package jam

import (
	"strings"
	"testing"
	"time"
)

func seedFacts() ImageFacts {
	text := func(p, s string) SeedFile { return SeedFile{Path: p, Size: int64(len(s)), Text: s, HasText: true} }
	return ImageFacts{ID: "sha256:0123456789abcdef", SeedNote: "seed listing truncated at 200 entries",
		Env:    []string{"PATH=/usr/bin", "DB_PASSWORD=hunter2", "COVE_SSHENV=GOPATH:GOPROXY"},
		Labels: map[string]string{"org.opencontainers.image.source": "https://u:p@github.com/x/y", "evil": "a\nb"},
		Seed: []SeedFile{
			{Path: ".claude.json", Size: 210, Text: `{"oauthAccount":"secret"}`, HasText: true},
			text(".refresh", "# comment\nCLAUDE.md\nskills\n"),
			text("CLAUDE.md", "Read these:\n@SANDBOX.md\n@../escape.md\n"),
			text("SANDBOX.md", "sandbox rules\n"),
			text("reference/x.md", "ref"),
			text("settings.json", `{"env":{"GH_TOKEN":"ghp_SETTINGS"},"model":"opus"}`),
			text("skills/docs-audit/SKILL.md", "---\nname: docs-audit\ndescription: Check the docs\n---\nbody"),
		}}
}

func TestSeedBody(t *testing.T) {
	got := seedBody(seedFacts())
	for _, want := range []string{
		"image: sha256:01234\n", "\n--- /agent-data/CLAUDE.md\nRead these:\n", "\n--- /agent-data/SANDBOX.md (imported)\nsandbox rules\n",
		"docs-audit  Check the docs\n", "\n--- /agent-data/settings.json (secret-named values masked)\n", `"model": "opus"`,
		"re-copied every boot (.refresh): CLAUDE.md, skills\n", "/agent-data/.claude.json", "210 B", "/agent-data/reference/x.md",
		"(seed listing truncated at 200 entries)\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	for _, bad := range []string{"ghp_SETTINGS", "oauthAccount", "escape.md (imported)"} {
		if strings.Contains(got, bad) {
			t.Errorf("%q shown:\n%s", bad, got)
		}
	}
}

func TestImageEnvAndLabelBodies(t *testing.T) {
	got := imageEnvBody(seedFacts())
	if !strings.Contains(got, "  PATH=/usr/bin\n") || !strings.Contains(got, "  DB_PASSWORD=«masked»\n") || !strings.Contains(got, "transferred into sessions (COVE_SSHENV): GOPATH, GOPROXY\n") {
		t.Errorf("env:\n%s", got)
	}
	got = imageLabelsBody(seedFacts())
	if !strings.Contains(got, "  org.opencontainers.image.source=https://github.com/x/y\n") || !strings.Contains(got, "  evil=invalid\n") || strings.Contains(got, "u:p") {
		t.Errorf("labels:\n%s", got)
	}
}

func TestKitDetailBody(t *testing.T) {
	st := NewMemStore()
	ref := mustEnsure(t, st, "base", chainKit("", "b"))
	k, _ := st.GetKit("base")
	k.Meta = map[int]KitVersionMeta{ref.Version: {PushedAt: time.Unix(0, 0).UTC(), Source: &KitSource{Repo: "https://x:y@github.com/o/kits", Dir: "base", Ref: "main", Commit: "abcdef0123456789"}}}
	if err := st.PutKitDocs([]Kit{k}); err != nil {
		t.Fatal(err)
	}
	got := kitDetailBody(st, []KitRef{ref, {ID: "ghost", Version: 3}}, map[string]string{"GO_VERSION": "1.27", "NPM_TOKEN": "npm_live"})
	for _, want := range []string{"base@v1", "https://github.com/o/kits//base@main (commit abcdef012345), pushed 1970-01-01T00:00:00Z",
		"ghost@v3", "unknown (not in the registry)", "  GO_VERSION=1.27\n", "  NPM_TOKEN=«masked»\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "x:y") || strings.Contains(got, "npm_live") {
		t.Errorf("leak:\n%s", got)
	}
}

func TestImageBodiesInertAndNoMutation(t *testing.T) {
	f := ImageFacts{ID: "sha256:abc", Env: []string{"A\x1b=b\nc", "TOKEN=zzz"},
		Seed: []SeedFile{{Path: "x\x1b[2J", Size: 1}, {Path: "CLAUDE.md", Text: "hi\x1b[31m\n", HasText: true}}}
	env := append([]string(nil), f.Env...)
	for _, s := range []string{seedBody(f), imageEnvBody(f)} {
		if strings.ContainsRune(s, 0x1b) || strings.Contains(s, "zzz") {
			t.Errorf("unsafe:\n%q", s)
		}
	}
	if f.Env[1] != env[1] || f.Seed[1].Text != "hi\x1b[31m\n" {
		t.Error("facts mutated")
	}
}
