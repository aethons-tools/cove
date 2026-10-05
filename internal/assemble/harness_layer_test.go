package assemble

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/harnessinstall"
	"github.com/aethons-tools/cove/internal/jam/modelspec"
	"github.com/aethons-tools/cove/internal/kit"
)

// The assembled Dockerfile layers FROM ${BASE} → harness → hardening: the
// generated harness stage (CLI install at the exact version + plugin seed)
// comes first, and every sealed hardening step follows it, built FROM harness.
func TestAssembleContextHarnessStageBetweenBaseAndHardening(t *testing.T) {
	buildDir := filepath.Join(t.TempDir(), ".build")
	in := harnessinstall.Install{Type: modelspec.HarnessClaude, Version: "2.1.100", Plugins: []string{"superpowers@claude-plugins-official"}}
	if err := AssembleContext(buildDir, []byte("k\n"), Egress{}, "", nil, in); err != nil {
		t.Fatal(err)
	}
	df := read(t, filepath.Join(buildDir, "Dockerfile"))
	order := []string{
		"ARG BASE\n",
		"FROM ${BASE} AS harness\n",
		"bash -s 2.1.100'",
		"ENV DISABLE_AUTOUPDATER=1\n",
		"seed-plugins.sh -m 'claude-plugins-official=anthropics/claude-plugins-official' -p 'superpowers@claude-plugins-official'",
		"FROM harness\n",
		"PasswordAuthentication no",
		"COPY image-files/. /.",
		"RUN /usr/local/lib/cove/apply-sshenv.sh",
		`ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]`,
	}
	at := -1
	for _, want := range order {
		i := strings.Index(df, want)
		if i < 0 {
			t.Fatalf("assembled Dockerfile missing %q:\n%s", want, df)
		}
		if i <= at {
			t.Fatalf("%q is out of order (want base → harness → hardening):\n%s", want, df)
		}
		at = i
	}
	if n := strings.Count(df, "\nFROM "); n != 2 {
		t.Fatalf("want exactly two stages (harness, hardening), got %d FROM lines:\n%s", n, df)
	}
	if _, err := os.Stat(filepath.Join(buildDir, harnessinstall.ContextDir, "seed-plugins.sh")); err != nil {
		t.Fatalf("plugin seed payload not staged into the harness context dir: %v", err)
	}
}

// Full config.yml kits (plain Assemble) get claude-default's harness: the one
// pinned version constant and its plugins.
func TestAssembleUsesDefaultHarness(t *testing.T) {
	buildDir := filepath.Join(t.TempDir(), ".build")
	if err := Assemble(t.TempDir(), buildDir, []byte("k\n"), Egress{}, "", harnessinstall.Default()); err != nil {
		t.Fatal(err)
	}
	df := read(t, filepath.Join(buildDir, "Dockerfile"))
	if !strings.Contains(df, "bash -s "+modelspec.DefaultClaudeVersion+"'") || !strings.Contains(df, "-p 'superpowers@claude-plugins-official'") {
		t.Fatalf("full kit must install claude-default's harness:\n%s", df)
	}
}

// A kit with a model-spec: block builds ITS harness (HarnessFor): the spec's
// exact CLI version and plugins, not claude-default's.
func TestAssembleUsesKitModelSpecHarness(t *testing.T) {
	cfg, err := kit.ParseConfig([]byte(`
name: k
model-spec:
  name: pinned
  type: claude
  version: 2.1.100
  claude:
    provider: anthropic
    plugins: [code-review@claude-plugins-official]
`))
	if err != nil {
		t.Fatal(err)
	}
	h := HarnessFor(cfg)
	if h.Version != "2.1.100" || len(h.Plugins) != 1 || h.Plugins[0] != "code-review@claude-plugins-official" {
		t.Fatalf("HarnessFor = %+v, want the kit spec's version and plugins", h)
	}
	buildDir := filepath.Join(t.TempDir(), ".build")
	if err := Assemble(t.TempDir(), buildDir, []byte("k\n"), EgressFor(cfg), "", h); err != nil {
		t.Fatal(err)
	}
	df := read(t, filepath.Join(buildDir, "Dockerfile"))
	if !strings.Contains(df, "bash -s 2.1.100'") || !strings.Contains(df, "-p 'code-review@claude-plugins-official'") || strings.Contains(df, "superpowers@") {
		t.Fatalf("the build must install the kit model-spec's harness:\n%s", df)
	}
	// No model-spec: claude-default's install.
	bare, _ := kit.ParseConfig([]byte("name: k\n"))
	if got, want := HarnessFor(bare), harnessinstall.Default(); got.Version != want.Version || strings.Join(got.Plugins, ",") != strings.Join(want.Plugins, ",") {
		t.Fatalf("HarnessFor(no model-spec) = %+v, want claude-default %+v", got, want)
	}
}

// An invalid harness install fails the assembly rather than rendering an
// unsafe RUN line.
func TestAssembleContextRefusesBadHarness(t *testing.T) {
	bad := harnessinstall.Install{Type: modelspec.HarnessClaude, Version: "2.x"}
	if err := AssembleContext(filepath.Join(t.TempDir(), ".build"), []byte("k\n"), Egress{}, "", nil, bad); err == nil {
		t.Fatal("a non-exact harness version must fail the assembly")
	}
}

// The sealed hardening layer no longer installs the harness or seeds plugins
// (both are the model-spec-driven harness layer's now), and builds FROM the
// harness stage rather than the raw base.
func TestHardeningDockerfileHasNoHarnessInstall(t *testing.T) {
	b, err := fs.ReadFile(hardeningFS, "hardening/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	// Instructions only: the header comment explains the layering.
	var code strings.Builder
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "#") {
			code.WriteString(line + "\n")
		}
	}
	df := code.String()
	for _, gone := range []string{"install.sh", "seed-plugins", "/usr/local/bin/claude", "DISABLE_AUTOUPDATER", "FROM ${BASE}", "ARG BASE"} {
		if strings.Contains(df, gone) {
			t.Errorf("hardening Dockerfile must not contain %q (harness layer owns it):\n%s", gone, df)
		}
	}
	if !strings.Contains("\n"+df, "\nFROM harness\n") {
		t.Errorf("hardening Dockerfile must build FROM harness:\n%s", df)
	}
	if _, err := fs.Stat(hardeningFS, "hardening/image-files/usr/local/lib/cove/seed-plugins.sh"); err == nil {
		t.Error("seed-plugins.sh must not ship in the sealed layer")
	}
}
