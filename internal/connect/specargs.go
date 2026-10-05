package connect

import (
	"encoding/json"

	"github.com/aethons-tools/cove/internal/jam/modelspec"
)

// SpecArgs renders the runtime parts of a plain at-cove kit's model-spec that
// an interactive session applies as claude argv (COV-241) — StdinScript.Args.
// The build-time parts (version, plugins) are the harness layer's, and the
// provider env is the session env's (kit.Config.SessionEnv), so neither is
// here:
//
//   - model.id / model.effort → --model ID / --effort LEVEL;
//   - policy.mode → --permission-mode=MODE, except empty or bypassPermissions,
//     which is already the image's interactive default (the harness layer's
//     managed permissions.defaultMode) and adds nothing;
//   - policy.allow / deny → one --allowedTools=RULE / --disallowedTools=RULE
//     element each (the = form keeps a rule one element);
//   - claude.settings → --settings JSON (preferences only — Validate refused
//     everything else; never a secret).
//
// nil spec (a kit without model-spec:) or nothing to render → nil, so the
// launch is byte-identical to the pre-model-spec one.
func SpecArgs(s *modelspec.Spec) []string {
	if s == nil {
		return nil
	}
	var args []string
	if s.Model.ID != "" {
		args = append(args, "--model", s.Model.ID)
	}
	if s.Model.Effort != "" {
		args = append(args, "--effort", s.Model.Effort)
	}
	if m := s.Policy.Mode; m != "" && m != modelspec.ModeBypassPermissions {
		args = append(args, "--permission-mode="+m)
	}
	for _, r := range s.Policy.Allow {
		args = append(args, "--allowedTools="+r)
	}
	for _, r := range s.Policy.Deny {
		args = append(args, "--disallowedTools="+r)
	}
	if s.Claude != nil && len(s.Claude.Settings) > 0 {
		if b, err := json.Marshal(s.Claude.Settings); err == nil { // Validate: a JSON object
			args = append(args, "--settings", string(b))
		}
	}
	return args
}
