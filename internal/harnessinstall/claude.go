package harnessinstall

import (
	"encoding/json"
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
//     marketplace and installs the plugins into the first-boot seed — BEFORE
//     the managed settings exist, so their update controls never apply to the
//     build-time `claude plugin` commands;
//   - the baseline preferences (BaselineSettings, from
//     modelspec.DefaultClaudeSettings — what the sealed managed settings used
//     to force, COV-245) merged UNDER the first-boot seed settings.json by
//     merge-baseline-settings.sh: the lowest-precedence user settings of
//     every session (interactive, spec-less, any spec, any kit base); a
//     model-spec's claude.settings overrides them per run;
//   - Claude Code's managed settings (payload/claude/managed-settings.json)
//     at /etc/claude-code/managed-settings.json, root-owned 0644: ONLY
//     sandbox-wide policy every claude session in the image gets — update
//     control, remote control, the bypass-mode acceptance, disableAutoMode,
//     and permissions.defaultMode bypassPermissions for interactive sessions
//     (at-cove connect/chat run claude without the harness argv; a Jam cove's
//     --dangerously-skip-permissions / --permission-mode flag overrides it,
//     COV-239). It lives here, not in the sealed hardening layer, because it
//     is Claude-specific; the kit still cannot override it (the harness stage
//     builds on top of the kit base and only hardening follows).
//
// Each of the last three is its own COPY/RUN layer naming only its own files,
// so editing one (say the managed settings) never re-runs another (the
// network-bound plugin seed).
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
		fmt.Fprintf(&b, "COPY %s/seed-plugins.sh /tmp/cove-seed/\n", ContextDir)
		fmt.Fprintf(&b, "RUN bash /tmp/cove-seed/seed-plugins.sh %s \\\n && rm -rf /tmp/cove-seed\n", strings.Join(args, " "))
	}
	b.WriteString("\n# Baseline Claude preferences, merged UNDER the first-boot user settings\n")
	b.WriteString("# (lowest precedence; a model-spec's claude.settings overrides them per run).\n")
	fmt.Fprintf(&b, "COPY %[1]s/merge-baseline-settings.sh %[1]s/%[2]s /tmp/cove-baseline/\n", ContextDir, baselineFile)
	fmt.Fprintf(&b, "RUN bash /tmp/cove-baseline/merge-baseline-settings.sh /tmp/cove-baseline/%s \\\n && rm -rf /tmp/cove-baseline\n", baselineFile)
	b.WriteString("\n# Claude Code's managed settings: sandbox-wide policy only, root-owned and\n")
	b.WriteString("# world-readable. Last, so no build-time claude command runs under them.\n")
	fmt.Fprintf(&b, "COPY %s/managed-settings.json /tmp/cove-managed/\n", ContextDir)
	b.WriteString("RUN install -D -o root -g root -m 0644 /tmp/cove-managed/managed-settings.json /etc/claude-code/managed-settings.json \\\n && rm -rf /tmp/cove-managed\n")
	b.WriteString("\n")
	return b.String()
}

// baselineFile is the build-context name of the rendered BaselineSettings.
const baselineFile = "baseline-settings.json"

// BaselineSettings is the harness layer's baseline Claude preferences file,
// rendered from modelspec.DefaultClaudeSettings (the one source). It is
// generated, not embedded payload, so at-cove's build identity hashes it
// explicitly (internal/install.AtCoveIdentity).
func BaselineSettings() []byte {
	b, _ := json.MarshalIndent(modelspec.DefaultClaudeSettings(), "", "  ") // bools and strings; never errors
	return append(b, '\n')
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
