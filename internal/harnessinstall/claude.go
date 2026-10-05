package harnessinstall

import (
	"fmt"
	"slices"
	"strings"

	"github.com/aethons-tools/cove/internal/jam/modelspec"
)

// ClaudeInstallerURL is Claude Code's native installer. It accepts the
// release to install as its argument (`bash -s X.Y.Z`; see Claude Code's
// "Install a specific version" setup docs), so the image gets exactly the
// model-spec's version.
const ClaudeInstallerURL = "https://claude.ai/install.sh"

// claudeStage renders the claude harness steps (in is Validated):
//
//   - the native installer at in.Version, run as the agent user at BUILD time
//     (the builder has open network, before the runtime egress lock — the
//     desktop app then finds 'claude' present instead of bootstrapping through
//     the locked proxy);
//   - the /usr/local/bin/claude symlink, so the binary is on PATH however sshd
//     sets it for a session;
//   - DISABLE_AUTOUPDATER=1: no background updates from inside the sandbox — a
//     version change is a model-spec edit and an image rebuild (claude.ai is
//     still reached at runtime for the subscription OAuth login — see the
//     sealed squid allow-list);
//   - when in.Plugins is non-empty, seed-plugins.sh adds each named
//     marketplace and installs the plugins into the first-boot seed.
func claudeStage(in Install) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n# Claude Code %s (native installer, pinned by the model-spec's version).\n", in.Version)
	fmt.Fprintf(&b, "RUN su - agent -c 'curl -fsSL %s | bash -s %s'\n", ClaudeInstallerURL, in.Version)
	b.WriteString("\n# Make the binary findable regardless of how sshd sets PATH for the session.\n")
	b.WriteString("RUN ln -sf /home/agent/.local/bin/claude /usr/local/bin/claude\n")
	b.WriteString("\n# No background self-updates inside the sandbox: change the model-spec's\n")
	b.WriteString("# version and rebuild instead. (DISABLE_UPDATES=1 would also block manual ones.)\n")
	b.WriteString("ENV DISABLE_AUTOUPDATER=1\n")
	if len(in.Plugins) > 0 {
		var args []string
		for _, m := range claudeMarketplaces(in.Plugins) {
			src, _ := modelspec.ClaudeMarketplaceSource(m) // Validated: known
			args = append(args, "-m '"+m+"="+src+"'")
		}
		for _, p := range in.Plugins {
			args = append(args, "-p '"+p+"'")
		}
		b.WriteString("\n# Pre-install the model-spec's plugin marketplaces + plugins at BUILD time\n")
		b.WriteString("# (open network, before the runtime egress lock) into the first-boot seed, so\n")
		b.WriteString("# the sandbox never clones plugins through the locked proxy at runtime.\n")
		fmt.Fprintf(&b, "COPY %s/ /tmp/cove-harness/\n", ContextDir)
		fmt.Fprintf(&b, "RUN bash /tmp/cove-harness/seed-plugins.sh %s \\\n && rm -rf /tmp/cove-harness\n", strings.Join(args, " "))
	}
	b.WriteString("\n")
	return b.String()
}

// claudeMarketplaces returns the distinct marketplaces plugins name, sorted.
func claudeMarketplaces(plugins []string) []string {
	var out []string
	for _, p := range plugins {
		if _, m, ok := strings.Cut(p, "@"); ok {
			out = append(out, m)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}
