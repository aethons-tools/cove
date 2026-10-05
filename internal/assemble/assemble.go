package assemble

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/aethons-tools/cove/internal/atswitchboard"
	"github.com/aethons-tools/cove/internal/attask"
	"github.com/aethons-tools/cove/internal/covemasterbin"
	"github.com/aethons-tools/cove/internal/harnessinstall"
	"github.com/aethons-tools/cove/internal/kit"
)

// Egress is the kit's baked egress beyond the sealed base, split by purpose.
// Policy is image.allowed-domains: baked as the active policy list
// (allowed_domains.kit.txt, which a Jam role's list replaces at raise) and as
// the immutable ceiling that role list must fit inside (egress_ceiling.txt).
// Infra is kit.InfraDomains (provider, self-hosted GitLab, Jam hosts): always
// on, baked into allowed_domains.infra.txt.
type Egress struct {
	Policy []string
	Infra  []string
}

// EgressFor derives a kit config's baked Egress.
func EgressFor(c kit.Config) Egress {
	return Egress{Policy: c.Image.AllowedDomains, Infra: kit.InfraDomains(c)}
}

// HarnessFor derives a kit config's harness install: the harness layer of its
// model-spec: block (version + plugins), or claude-default's when it has none
// (kit.Config.EffectiveModelSpec).
func HarnessFor(c kit.Config) harnessinstall.Install {
	s := c.EffectiveModelSpec()
	return harnessinstall.FromSpec(&s)
}

// Assemble builds the context in buildDir: the sealed hardening layer, the
// injected at-task, the kit's egress lists, the per-kit GitLab gitconfig, and
// the managed public key. The kit's image/ is the Dockerfile build context
// (resolved elsewhere), not overlaid. gitlabHost is the kit's resolved GitLab
// source-control host (from Config.GitLabHost) or "" for a non-GitLab kit.
//
// Assemble keeps the kit's .gitignore current (a dev-repo housekeeping side
// effect on kitDir) and then assembles the build context. The build context
// itself needs no kitDir — see AssembleContext, which the managed-cove launcher
// uses to build from a kit communicated as data, with no host directory.
func Assemble(kitDir, buildDir string, pub []byte, egress Egress, gitlabHost string, harness harnessinstall.Install) error {
	// Any path that assembles a build context (build/create/work) keeps the kit's
	// .gitignore current, so generated .build/.state artifacts never leak into git.
	if err := kit.EnsureGitignore(kitDir); err != nil {
		return err
	}
	// A full kit's agent never runs cove-master (only Jam-raised studio coves
	// do), so it bakes no MCP servers — just the empty file. Its harness is
	// the caller's — HarnessFor: the kit's model-spec: block, else
	// claude-default's install (COV-241).
	return AssembleContext(buildDir, pub, egress, gitlabHost, nil, harness)
}

// AssembleContext stages the docker build context into buildDir with NO source
// kit directory: everything comes from resources compiled into this binary (the
// sealed hardening layer + Dockerfile via the embedded FS, and the injected
// at-task/at-switchboard/cove-master binaries), plus data the caller supplies —
// the kit's egress lists, its per-kit GitLab gitconfig, its MCP servers
// (kit.MCPServersImagePath), the public key baked into authorized_keys, and the
// harness install (the model-spec's CLI version + plugins). Because it takes no directory, a Launcher can build a
// managed cove from a kit communicated purely as data (config + key), which is
// what lets that build run wherever the substrate builds (locally today; a
// remote substrate later). See
// docs/superpowers/specs/2026-09-29-cove-launcher-abstraction-design.md.
//
// The assembled Dockerfile is the generated harness stage (`ARG BASE`, `FROM
// ${BASE} AS harness`, the CLI install + plugin seed — internal/harnessinstall)
// followed by the sealed hardening Dockerfile (`FROM harness`), so the image is
// FROM ${BASE} → harness → hardening and hardening is always applied last.
func AssembleContext(buildDir string, pub []byte, egress Egress, gitlabHost string, mcpServers map[string]kit.MCPServer, harness harnessinstall.Install) error {
	if err := os.RemoveAll(buildDir); err != nil {
		return err
	}
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		return err
	}

	// The kit's image/ is only the Dockerfile build context now (COV-34) — it is
	// not overlaid, and the overridable defaults ship in cove-base-image. So the
	// build context is the harness layer, the sealed hardening layer, the
	// injected at-task, the kit's egress lists, and the managed key.
	if err := copyEmbed(hardeningFS, "hardening", buildDir); err != nil {
		return err
	}

	if err := writeDockerfile(buildDir, harness); err != nil {
		return err
	}

	if err := writeAtTask(buildDir); err != nil {
		return err
	}

	if err := writeSwitchboard(buildDir); err != nil {
		return err
	}

	if err := writeCoveMaster(buildDir); err != nil {
		return err
	}

	if err := writeEgressLists(buildDir, egress); err != nil {
		return err
	}

	if err := writeGitLabGitConfig(buildDir, gitlabHost); err != nil {
		return err
	}

	if err := writeMCPServers(buildDir, mcpServers); err != nil {
		return err
	}

	// Managed key injection.
	ak := filepath.Join(buildDir, "image-files/home/agent/.ssh/authorized_keys")
	if err := os.MkdirAll(filepath.Dir(ak), 0o700); err != nil {
		return err
	}
	return os.WriteFile(ak, pub, 0o600)
}

// writeDockerfile prepends the harness stage (staging its payload) to the
// sealed hardening Dockerfile copyEmbed just wrote.
func writeDockerfile(buildDir string, harness harnessinstall.Install) error {
	stage, err := harnessinstall.Stage(buildDir, harness)
	if err != nil {
		return err
	}
	df := filepath.Join(buildDir, "Dockerfile")
	sealed, err := os.ReadFile(df)
	if err != nil {
		return err
	}
	return os.WriteFile(df, []byte(stage+"\n# --- sealed hardening layer (internal/assemble/hardening) ---\n"+string(sealed)), 0o644)
}

// writeAtTask stages the embedded linux at-task binaries into the build context
// (buildDir/attask/at-task-linux-<arch>), so the sealed hardening layer can
// install the arch-matching one — the at-cove that orchestrates a dispatch ships
// the exact at-task it expects (COV-34/COV-36). When the embed was not staged (a
// plain `go build` without scripts/build.sh — never the release path), a 0-byte
// placeholder is written instead; hardening installs the binary only when it is
// non-empty, so it falls back to the base image's at-task rather than a broken one.
func writeAtTask(buildDir string) error {
	dir := filepath.Join(buildDir, "attask")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, arch := range []string{"amd64", "arm64"} {
		b, err := attask.Binary(arch)
		if err != nil {
			b = nil // not staged → placeholder; hardening keeps the base's at-task
		}
		if err := os.WriteFile(filepath.Join(dir, "at-task-linux-"+arch), b, 0o755); err != nil {
			return err
		}
	}
	return nil
}

// writeSwitchboard stages the embedded linux at-switchboard binaries into the
// build context (buildDir/switchboard/at-switchboard-linux-<arch>), so the
// sealed hardening layer can install the arch-matching one — mirrors
// writeAtTask. Unlike at-task there is no base-image fallback binary to keep:
// when the embed was not staged (a plain `go build` without scripts/build.sh),
// a 0-byte placeholder is written instead, and hardening's install guard skips
// it; `at-cove teammate` then errors clearly at launch if the binary is absent.
func writeSwitchboard(buildDir string) error {
	dir := filepath.Join(buildDir, "switchboard")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, arch := range []string{"amd64", "arm64"} {
		b, err := atswitchboard.Binary(arch)
		if err != nil {
			b = nil // not staged → placeholder; caught at launch, not build
		}
		if err := os.WriteFile(filepath.Join(dir, "at-switchboard-linux-"+arch), b, 0o755); err != nil {
			return err
		}
	}
	return nil
}

// writeCoveMaster stages the embedded linux cove-master binaries into the build
// context (buildDir/covemaster/cove-master-linux-<arch>), so the sealed hardening
// layer can install the arch-matching one — mirrors writeSwitchboard (COV-158).
// When the embed was not staged (a plain `go build` without scripts/stage-attask.sh),
// a 0-byte placeholder is written instead and hardening's install guard skips it;
// a raised cove then has no cove-master and the launcher's start step fails clearly.
func writeCoveMaster(buildDir string) error {
	dir := filepath.Join(buildDir, "covemaster")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, arch := range []string{"amd64", "arm64"} {
		b, err := covemasterbin.Binary(arch)
		if err != nil {
			b = nil // not staged → placeholder; caught at launch, not build
		}
		if err := os.WriteFile(filepath.Join(dir, "cove-master-linux-"+arch), b, 0o755); err != nil {
			return err
		}
	}
	return nil
}

// writeGitLabGitConfig writes the per-kit GitLab gitconfig include that the sealed
// /etc/gitconfig unconditionally includes. For a GitLab kit it bakes the host's
// ssh/git→https insteadOf rewrites (only HTTPS egress is permitted, so ssh and
// git:// remotes must be rewritten — the same treatment github.com gets statically)
// and scopes the credential helper to that host so an interactive collaborator git
// authenticates with GITLAB_TOKEN. Always written (header-only when host is "") so
// the include never dangles — mirroring writeEgressLists.
func writeGitLabGitConfig(buildDir, host string) error {
	dst := filepath.Join(buildDir, "image-files/etc/gitconfig-gitlab.inc")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("# Per-kit GitLab source-control gitconfig, generated by at-cove assemble and\n")
	b.WriteString("# included by /etc/gitconfig. Empty (header only) for a non-GitLab kit.\n")
	if host != "" {
		b.WriteString("\n")
		b.WriteString("[url \"https://" + host + "/\"]\n")
		b.WriteString("\tinsteadOf = git@" + host + ":\n")
		b.WriteString("\tinsteadOf = ssh://git@" + host + "/\n")
		b.WriteString("\tinsteadOf = git://" + host + "/\n")
		b.WriteString("[credential \"https://" + host + "\"]\n")
		b.WriteString("\thelper = \"\"\n")
		b.WriteString("\thelper = /usr/local/bin/cove-git-credential.sh\n")
	}
	return os.WriteFile(dst, []byte(b.String()), 0o644)
}

// writeMCPServers bakes the kit's MCP servers (validated kit data: env
// references, never secret values) at kit.MCPServersImagePath, where the cove's
// agent harness merges them with its guaranteed messaging server (COV-240).
// Always written ({} when the kit declares none), mirroring writeEgressLists.
func writeMCPServers(buildDir string, servers map[string]kit.MCPServer) error {
	if servers == nil {
		servers = map[string]kit.MCPServer{}
	}
	b, err := json.Marshal(servers)
	if err != nil {
		return err
	}
	dst := filepath.Join(buildDir, "image-files", kit.MCPServersImagePath)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, append(b, '\n'), 0o644)
}

// copyEmbed copies efs under root into dst, stripping the root prefix.
func copyEmbed(efs fs.FS, root, dst string) error {
	return fs.WalkDir(efs, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := fs.ReadFile(efs, p)
		if err != nil {
			return err
		}
		mode := fs.FileMode(0o644)
		if filepath.Ext(p) == ".sh" || filepath.Base(p) == "entrypoint.sh" {
			mode = 0o755
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, b, mode)
	})
}

// writeEgressLists writes the kit's baked squid lists: the active policy list
// (kit.txt), the always-on infra list (infra.txt), and the ceiling a Jam role's
// policy must fit inside (egress_ceiling.txt — read only by the sealed
// apply-role-egress.sh, never by squid). Each is always written (empty → header
// only) so the sealed squid.conf never references a missing ACL file.
func writeEgressLists(buildDir string, e Egress) error {
	policy := sortedUnique(e.Policy)
	files := []struct {
		name    string
		header  []string
		domains []string
	}{
		{"allowed_domains.kit.txt", []string{
			"# Active egress policy list: the kit's image.allowed-domains by default;",
			"# replaced (root-only) by a Jam role's list at raise, within egress_ceiling.txt.",
			"# Additive to the sealed base + infra lists; leading dot = subdomains.",
		}, policy},
		{"allowed_domains.infra.txt", []string{
			"# Kit infrastructure egress domains (model provider, self-hosted GitLab, Jam).",
			"# Always on; a Jam role's egress policy cannot remove these.",
		}, sortedUnique(e.Infra)},
		{"egress_ceiling.txt", []string{
			"# Egress ceiling: the kit's image.allowed-domains, baked immutable.",
			"# NOT an allow-list (squid never reads it). apply-role-egress.sh refuses any",
			"# role domain this list does not cover; leading dot = subdomains.",
		}, policy},
	}
	dir := filepath.Join(buildDir, "image-files/etc/squid")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, f := range files {
		var b strings.Builder
		for _, h := range f.header {
			b.WriteString(h)
			b.WriteString("\n")
		}
		for _, d := range f.domains {
			b.WriteString(d)
			b.WriteString("\n")
		}
		if err := os.WriteFile(filepath.Join(dir, f.name), []byte(b.String()), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// sortedUnique returns the deduped, sorted copy of domains.
func sortedUnique(domains []string) []string {
	out := slices.Clone(domains)
	slices.Sort(out)
	return slices.Compact(out)
}
