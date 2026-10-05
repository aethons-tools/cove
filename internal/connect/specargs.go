package connect

import (
	"github.com/aethons-tools/cove/internal/jam/modelspec"
)

// SpecArgs renders the runtime parts of a plain at-cove kit's model-spec that
// an interactive session applies as claude argv (COV-241) — StdinScript.Args —
// with the renderer every claude launcher shares (modelspec.ClaudeModelArgs,
// ClaudePolicyArgs, ClaudeSettingsJSON). The build-time parts (version,
// plugins) are the harness layer's, and the provider env is the session env's
// (kit.Config.SessionEnv), so neither is here:
//
//   - model.id / model.effort → --model ID / --effort LEVEL;
//   - policy → --permission-mode=MODE and --allowedTools= / --disallowedTools=
//     per rule; an empty or bypassPermissions mode adds no flag — it is
//     already the image's interactive default (the harness layer's managed
//     permissions.defaultMode);
//   - claude.settings → --settings JSON.
//
// nil spec (a kit without model-spec:) or nothing to render → nil, so the
// launch is byte-identical to the pre-model-spec one.
func SpecArgs(s *modelspec.Spec) []string {
	if s == nil {
		return nil
	}
	args := modelspec.ClaudeModelArgs(s)
	args = append(args, modelspec.ClaudePolicyArgs(s, false, nil)...)
	if js, ok := modelspec.ClaudeSettingsJSON(s); ok {
		args = append(args, "--settings", js)
	}
	return args
}
