// internal/studio/prompt.go
package studio

import "strings"

// JamBoilerplate is the first, always-present prompt layer for a studio session.
const JamBoilerplate = "You are operating inside an at-cove hardened sandbox: an isolated filesystem " +
	"and an allow-listed network. Work within it; reach external services only through approved egress."

// PromptLayers are the raise-time layers composed into a studio session's prompt,
// each from its own source. Kit is the StudioKit's orienting prompt; Project and
// Role come from their configs (may be empty until those carry a prompt field);
// Launch is the per-raise workload prompt (RaiseSpec.Prompt).
type PromptLayers struct {
	Kit     string
	Project string
	Role    string
	Launch  string
}

// ComposePrompt joins JamBoilerplate and the non-empty layers, in fixed order,
// separated by a blank line. Assembled at RAISE (never baked into the image), so
// it is outside the build-digest.
func ComposePrompt(l PromptLayers) string {
	parts := []string{JamBoilerplate}
	for _, layer := range []string{l.Kit, l.Project, l.Role, l.Launch} {
		if strings.TrimSpace(layer) != "" {
			parts = append(parts, layer)
		}
	}
	return strings.Join(parts, "\n\n")
}
