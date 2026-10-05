package assemble

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/harnessinstall"
	"github.com/aethons-tools/cove/internal/kit"
)

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Assemble must write the kit's managed .gitignore, so a kit built by any path
// (build/create/work) never leaks its .build/.state artifacts into git.
func TestAssembleEnsuresGitignore(t *testing.T) {
	kitDir := t.TempDir()
	if err := Assemble(kitDir, filepath.Join(kitDir, ".build"), []byte("ssh-ed25519 AAAA"), Egress{}, ""); err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	gi := read(t, filepath.Join(kitDir, ".gitignore"))
	if !strings.Contains(gi, ".build/") || !strings.Contains(gi, ".state/") {
		t.Fatalf(".gitignore missing managed entries:\n%s", gi)
	}
}

// AssembleContext builds the whole context from in-binary resources + supplied
// data, with NO source kit directory — the property that lets a Launcher build a
// managed cove from a kit communicated as data. It must produce the Dockerfile
// (embedded), bake the key, and write the egress list, touching no kit dir.
func TestAssembleContextNeedsNoKitDir(t *testing.T) {
	buildDir := filepath.Join(t.TempDir(), ".build")
	if err := AssembleContext(buildDir, []byte("ssh-ed25519 AAAA k\n"), Egress{Policy: []string{"proxy.golang.org"}}, "", nil, harnessinstall.Default()); err != nil {
		t.Fatalf("AssembleContext: %v", err)
	}
	if _, err := os.Stat(filepath.Join(buildDir, "Dockerfile")); err != nil {
		t.Fatalf("Dockerfile missing: %v", err)
	}
	if got := read(t, filepath.Join(buildDir, "image-files/home/agent/.ssh/authorized_keys")); got != "ssh-ed25519 AAAA k\n" {
		t.Fatalf("authorized_keys = %q", got)
	}
	if kitList := read(t, filepath.Join(buildDir, "image-files/etc/squid/allowed_domains.kit.txt")); !strings.Contains(kitList, "proxy.golang.org") {
		t.Fatalf("kit egress list missing the policy domain:\n%s", kitList)
	}
}

func TestWriteSwitchboard_WritesArchFiles(t *testing.T) {
	dir := t.TempDir()
	if err := writeSwitchboard(dir); err != nil {
		t.Fatal(err)
	}
	for _, arch := range []string{"amd64", "arm64"} {
		p := filepath.Join(dir, "switchboard", "at-switchboard-linux-"+arch)
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("missing %s: %v", p, err)
		}
	}
}

func TestWriteCoveMaster_WritesArchFiles(t *testing.T) {
	dir := t.TempDir()
	if err := writeCoveMaster(dir); err != nil {
		t.Fatal(err)
	}
	for _, arch := range []string{"amd64", "arm64"} {
		p := filepath.Join(dir, "covemaster", "cove-master-linux-"+arch)
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("missing %s: %v", p, err)
		}
	}
}

func TestAssembleLayersAndKey(t *testing.T) {
	buildDir := filepath.Join(t.TempDir(), ".build")

	if err := Assemble(t.TempDir(), buildDir, []byte("ssh-ed25519 AAAA k\n"), Egress{}, ""); err != nil {
		t.Fatal(err)
	}

	// Dockerfile present (from the sealed hardening layer).
	if _, err := os.Stat(filepath.Join(buildDir, "Dockerfile")); err != nil {
		t.Fatalf("Dockerfile missing: %v", err)
	}
	// Managed key injected.
	if got := read(t, filepath.Join(buildDir, "image-files/home/agent/.ssh/authorized_keys")); got != "ssh-ed25519 AAAA k\n" {
		t.Fatalf("authorized_keys = %q", got)
	}
}

// Assemble stages the at-task binaries into the build context (buildDir/attask/)
// for the sealed layer to install. In hermetic tests the embed is unstaged, so
// the placeholders are 0-byte — hardening then keeps the base image's at-task.
func TestAssembleStagesAtTask(t *testing.T) {
	buildDir := filepath.Join(t.TempDir(), ".build")
	if err := Assemble(t.TempDir(), buildDir, []byte("k\n"), Egress{}, ""); err != nil {
		t.Fatal(err)
	}
	for _, arch := range []string{"amd64", "arm64"} {
		if _, err := os.Stat(filepath.Join(buildDir, "attask", "at-task-linux-"+arch)); err != nil {
			t.Fatalf("at-task-linux-%s not staged into the context: %v", arch, err)
		}
	}
}

func mustWrite(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAssembleAllowedDomains(t *testing.T) {
	kitDir := t.TempDir()
	buildDir := filepath.Join(t.TempDir(), ".build")
	img := kit.ImageConfig{AllowedDomains: []string{".example.com", "pkg.go.dev"}}
	if err := Assemble(kitDir, buildDir, []byte("k\n"), Egress{Policy: img.AllowedDomains}, ""); err != nil {
		t.Fatal(err)
	}
	got := read(t, filepath.Join(buildDir, "image-files/etc/squid/allowed_domains.kit.txt"))
	if !strings.Contains(got, ".example.com") || !strings.Contains(got, "pkg.go.dev") {
		t.Fatalf("kit allow-list = %q", got)
	}
}

func TestAssembleAllowedDomainsAlwaysWritten(t *testing.T) {
	kitDir := t.TempDir()
	buildDir := filepath.Join(t.TempDir(), ".build")
	if err := Assemble(kitDir, buildDir, []byte("k\n"), Egress{}, ""); err != nil {
		t.Fatal(err)
	}
	// File must exist even with no domains, so squid.conf never references a missing file.
	if _, err := os.Stat(filepath.Join(buildDir, "image-files/etc/squid/allowed_domains.kit.txt")); err != nil {
		t.Fatalf("kit allow-list must always be written: %v", err)
	}
}

func TestSquidConfReferencesKitFile(t *testing.T) {
	got := read(t, "hardening/image-files/etc/squid/squid.conf")
	if !strings.Contains(got, "allowed_domains.kit.txt") {
		t.Fatalf("squid.conf must reference the kit allow-list: %q", got)
	}
}

// The sealed squid.conf must reference the per-session allow-list (COV-58 / COV-39
// §4), so at-cove can widen a session's egress by rewriting that one file and
// reconfiguring squid — additive on top of the sealed base + kit lists.
func TestSquidConfReferencesSessionFile(t *testing.T) {
	got := read(t, "hardening/image-files/etc/squid/squid.conf")
	if !strings.Contains(got, "allowed_domains.session.txt") {
		t.Fatalf("squid.conf must reference the session allow-list: %q", got)
	}
}

// The hardening layer bakes an empty, header-only session allow-list so the ACL
// never dangles and a no-class session works (COV-39 §4). It must exist and carry
// no domain entries (only comment/blank lines) in the sealed embed.
func TestSessionAllowlistBakedEmpty(t *testing.T) {
	got := read(t, "hardening/image-files/etc/squid/allowed_domains.session.txt")
	for _, line := range strings.Split(got, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		t.Fatalf("baked session allow-list must be empty (header only); found entry %q in:\n%s", trimmed, got)
	}
}

func TestCollaboratorRoleFileSeeded(t *testing.T) {
	b, err := os.ReadFile(baseInitAgentData("CLAUDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "@COLLABORATOR.md") {
		t.Fatalf("base CLAUDE.md must @-include COLLABORATOR.md:\n%s", b)
	}
	if _, err := os.Stat(baseInitAgentData("COLLABORATOR.md")); err != nil {
		t.Fatalf("default COLLABORATOR.md missing from the base seed: %v", err)
	}
}

// Assemble must bake the Vertex provider's derived GCP egress domains into the
// always-on infra allow-list, not just the kit's own image.allowed-domains, so a
// Vertex kit can reach aiplatform + the ADC auth endpoints without a manual
// allowed-domains entry (COV egress task 2).
func TestAssemble_VertexDomainsBaked(t *testing.T) {
	kitDir := t.TempDir()
	buildDir := filepath.Join(kitDir, ".build")
	cfg, err := kit.ParseConfig([]byte(`
name: k
model-provider:
  vertex:
    env:
      ANTHROPIC_VERTEX_PROJECT_ID: p
      CLOUD_ML_REGION: us-east5
`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if err := Assemble(kitDir, buildDir, []byte("k\n"), Egress{Policy: cfg.Image.AllowedDomains, Infra: kit.InfraDomains(cfg)}, ""); err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(buildDir, "image-files/etc/squid/allowed_domains.infra.txt"))
	if err != nil {
		t.Fatalf("read baked domains: %v", err)
	}
	if !strings.Contains(string(b), "us-east5-aiplatform.googleapis.com") ||
		!strings.Contains(string(b), "oauth2.googleapis.com") {
		t.Fatalf("baked kit domains missing vertex hosts:\n%s", b)
	}
}

// A GitLab kit must get a generated gitconfig include baking the host's ssh/git→
// https insteadOf rewrites and scoping the credential helper to that host, so an
// interactive collaborator git over HTTPS authenticates with GITLAB_TOKEN — the
// GitHub-only static gitconfig cannot do this for a (possibly self-hosted) host.
func TestAssembleGeneratesGitLabGitConfig(t *testing.T) {
	buildDir := filepath.Join(t.TempDir(), ".build")
	if err := Assemble(t.TempDir(), buildDir, []byte("k\n"), Egress{}, "gitlab.example.com"); err != nil {
		t.Fatal(err)
	}
	got := read(t, filepath.Join(buildDir, "image-files/etc/gitconfig-gitlab.inc"))
	for _, want := range []string{
		`[url "https://gitlab.example.com/"]`,
		"insteadOf = git@gitlab.example.com:",
		"insteadOf = ssh://git@gitlab.example.com/",
		`[credential "https://gitlab.example.com"]`,
		"helper = /usr/local/bin/cove-git-credential.sh",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("generated GitLab gitconfig missing %q; got:\n%s", want, got)
		}
	}
}

// For a non-GitLab kit the include must still exist (the sealed /etc/gitconfig
// includes it unconditionally) but carry no host config — only the header — so it
// is a well-formed no-op rather than a dangling include.
func TestAssembleGitLabGitConfigHeaderOnlyForGitHub(t *testing.T) {
	buildDir := filepath.Join(t.TempDir(), ".build")
	if err := Assemble(t.TempDir(), buildDir, []byte("k\n"), Egress{}, ""); err != nil {
		t.Fatal(err)
	}
	got := read(t, filepath.Join(buildDir, "image-files/etc/gitconfig-gitlab.inc"))
	if strings.Contains(got, "[url ") || strings.Contains(got, "[credential ") {
		t.Fatalf("non-GitLab include must be header-only; got:\n%s", got)
	}
}

// The sealed /etc/gitconfig must include the per-kit GitLab drop-in that assemble
// generates, or the baked host config would never load.
func TestGitConfigIncludesGitLabDropin(t *testing.T) {
	got := read(t, "hardening/image-files/etc/gitconfig")
	if !strings.Contains(got, "path = /etc/gitconfig-gitlab.inc") {
		t.Fatalf("gitconfig must include the generated GitLab drop-in; got:\n%s", got)
	}
}

// The sealed base allow-list must reach gitlab.com out of the box, the same
// way it already reaches github.com — self-hosted GitLab hosts are handled
// per-kit via source-control.gitlab.host, not by this sealed base entry.
func TestSealedBaseAllowsGitLab(t *testing.T) {
	b, err := fs.ReadFile(HardeningFS(), "hardening/image-files/etc/squid/allowed_domains.txt")
	if err != nil {
		t.Fatalf("read sealed allow-list: %v", err)
	}
	if !strings.Contains(string(b), "gitlab.com") {
		t.Fatalf("sealed base must allow gitlab.com:\n%s", b)
	}
}

// domainLines returns the non-comment, non-blank lines of a baked list.
func domainLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}

// Assemble splits the kit's egress into the active policy list (kit.txt =
// image.allowed-domains), the always-on infra list (provider, GitLab, Jam), and
// the immutable ceiling a Jam role's list must fit inside (= image.allowed-domains).
func TestAssembleSplitsEgressLists(t *testing.T) {
	kitDir := t.TempDir()
	buildDir := filepath.Join(kitDir, ".build")
	eg := Egress{Policy: []string{"pkg.go.dev", ".example.com"}, Infra: []string{"jam.example"}}
	if err := Assemble(kitDir, buildDir, []byte("k\n"), eg, ""); err != nil {
		t.Fatal(err)
	}
	squidDir := filepath.Join(buildDir, "image-files/etc/squid")
	for file, want := range map[string]string{
		"allowed_domains.kit.txt":   ".example.com,pkg.go.dev",
		"allowed_domains.infra.txt": "jam.example",
		"egress_ceiling.txt":        ".example.com,pkg.go.dev",
	} {
		got := read(t, filepath.Join(squidDir, file))
		if !strings.HasPrefix(got, "#") {
			t.Errorf("%s must start with a header comment:\n%s", file, got)
		}
		if g := strings.Join(domainLines(got), ","); g != want {
			t.Errorf("%s domains = %q, want %q", file, g, want)
		}
	}
}

// Every baked list is written (header only when empty) so squid never references a
// missing ACL file and the helper always finds a ceiling.
func TestAssembleEgressListsAlwaysWritten(t *testing.T) {
	buildDir := filepath.Join(t.TempDir(), ".build")
	if err := Assemble(t.TempDir(), buildDir, []byte("k\n"), Egress{}, ""); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"allowed_domains.kit.txt", "allowed_domains.infra.txt", "egress_ceiling.txt"} {
		got := read(t, filepath.Join(buildDir, "image-files/etc/squid", file))
		if !strings.HasPrefix(got, "#") || len(domainLines(got)) != 0 {
			t.Errorf("%s must be header-only when empty:\n%s", file, got)
		}
	}
}

// The split must not change what a dev sandbox can reach: kit.txt ∪ infra.txt is
// exactly the old single kit list (kit.RootDomains).
func TestAssembleEgressUnionUnchanged(t *testing.T) {
	// Jam and model-provider are mutually exclusive, so cover a provider +
	// GitLab kit and a Jam + GitLab kit.
	for name, yml := range map[string]string{
		"provider+gitlab": `
name: k
image:
  allowed-domains: [pkg.go.dev, .example.com]
model-provider:
  vertex:
    env:
      ANTHROPIC_VERTEX_PROJECT_ID: p
      CLOUD_ML_REGION: us-east5
source-control:
  gitlab:
    host: gitlab.example.com
    project: g/app
`,
		"jam+gitlab": `
name: k
image:
  allowed-domains: [pkg.go.dev, .example.com]
source-control:
  gitlab:
    host: gitlab.example.com
    project: g/app
jam:
  host: jam.example
`,
	} {
		t.Run(name, func(t *testing.T) {
			cfg, err := kit.ParseConfig([]byte(yml))
			if err != nil {
				t.Fatalf("ParseConfig: %v", err)
			}
			kitDir := t.TempDir()
			buildDir := filepath.Join(kitDir, ".build")
			if err := Assemble(kitDir, buildDir, []byte("k\n"), EgressFor(cfg), ""); err != nil {
				t.Fatal(err)
			}
			squidDir := filepath.Join(buildDir, "image-files/etc/squid")
			union := map[string]bool{}
			for _, f := range []string{"allowed_domains.kit.txt", "allowed_domains.infra.txt"} {
				for _, d := range domainLines(read(t, filepath.Join(squidDir, f))) {
					union[d] = true
				}
			}
			root := kit.RootDomains(cfg)
			if len(union) != len(root) {
				t.Fatalf("kit ∪ infra = %v, want RootDomains %v", union, root)
			}
			for _, d := range root {
				if !union[d] {
					t.Fatalf("kit ∪ infra missing %q (RootDomains %v)", d, root)
				}
			}
		})
	}
}

// squid.conf must allow the always-on infra list, and must NOT reference the
// ceiling: the ceiling is a bound for apply-role-egress.sh, never an allow-list.
func TestSquidConfInfraAndNoCeiling(t *testing.T) {
	got := read(t, "hardening/image-files/etc/squid/squid.conf")
	if !strings.Contains(got, `acl allowed_infra_domains dstdomain "/etc/squid/allowed_domains.infra.txt"`) ||
		!strings.Contains(got, "http_access allow allowed_infra_domains") {
		t.Fatalf("squid.conf must allow the infra list:\n%s", got)
	}
	if strings.Contains(got, "egress_ceiling") {
		t.Fatalf("squid.conf must not reference the egress ceiling:\n%s", got)
	}
}

// The kit's mcp-servers are baked (non-secret: env references only) at
// kit.MCPServersImagePath for the cove's agent harness to merge with its own
// messaging server (COV-240).
func TestAssembleContextBakesMCPServers(t *testing.T) {
	buildDir := filepath.Join(t.TempDir(), ".build")
	servers := map[string]kit.MCPServer{
		"linear": {Type: "http", URL: "${LINEAR_MCP_URL}", Headers: map[string]string{"Authorization": "Bearer ${LINEAR_TOKEN}"}},
	}
	if err := AssembleContext(buildDir, []byte("k\n"), Egress{}, "", servers, harnessinstall.Default()); err != nil {
		t.Fatal(err)
	}
	got := read(t, filepath.Join(buildDir, "image-files", kit.MCPServersImagePath))
	want := `{"linear":{"type":"http","url":"${LINEAR_MCP_URL}","headers":{"Authorization":"Bearer ${LINEAR_TOKEN}"}}}` + "\n"
	if got != want {
		t.Fatalf("mcp-servers file:\n got %s\nwant %s", got, want)
	}
}

// Always written (an empty object for a kit with none), mirroring the egress
// lists, so the harness can tell "no kit servers" from a stale image.
func TestAssembleContextBakesEmptyMCPServers(t *testing.T) {
	buildDir := filepath.Join(t.TempDir(), ".build")
	if err := Assemble(t.TempDir(), buildDir, []byte("k\n"), Egress{}, ""); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(buildDir, "image-files", kit.MCPServersImagePath)); got != "{}\n" {
		t.Fatalf("mcp-servers file = %q, want {}", got)
	}
}
