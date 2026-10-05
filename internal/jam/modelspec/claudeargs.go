package modelspec

import "encoding/json"

// The claude-harness runtime renderings of a spec, shared by every claude
// launcher — Jam's headless harness (internal/agentrun), plain at-cove's
// interactive chat (internal/connect) and its dispatched workers
// (internal/dispatchrun) — so a spec means the same flags everywhere.

// ClaudeModelArgs renders model.id / model.effort as --model ID / --effort
// LEVEL; nil when neither is set (or no spec).
func ClaudeModelArgs(s *Spec) []string {
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
	return args
}

// ClaudePolicyArgs renders spec.policy as claude flags (COV-239):
//
//   - mode empty (or no spec) or ModeBypassPermissions → --dangerously-skip-permissions
//     when explicitBypass (a headless launch: the legacy argv, byte-identical
//     to before model-specs); nothing otherwise (an interactive session, where
//     bypassPermissions is already the image's managed default);
//   - any other mode → one --permission-mode=MODE element, then alwaysAllowed
//     (the launcher's own must-work tools) as --allowedTools=RULE;
//   - each allow / deny rule → one --allowedTools=RULE / --disallowedTools=RULE
//     element (claude accumulates repeated flags). The = form keeps a rule one
//     argv element that can never be read as a flag. Deny applies in every
//     mode, bypassPermissions included.
//
// Flags, not a permissions block in a settings file: Validate refuses
// permissions in claude.settings (policy owns it), and the rules stay visible.
func ClaudePolicyArgs(s *Spec, explicitBypass bool, alwaysAllowed []string) []string {
	var p Policy
	if s != nil {
		p = s.Policy
	}
	var args []string
	if p.Mode == "" || p.Mode == ModeBypassPermissions {
		if explicitBypass {
			args = append(args, "--dangerously-skip-permissions")
		}
	} else {
		args = append(args, "--permission-mode="+p.Mode)
		for _, r := range alwaysAllowed {
			args = append(args, "--allowedTools="+r)
		}
	}
	for _, r := range p.Allow {
		args = append(args, "--allowedTools="+r)
	}
	for _, r := range p.Deny {
		args = append(args, "--disallowedTools="+r)
	}
	return args
}

// ClaudeSettingsJSON renders claude.settings as the JSON a --settings argument
// takes inline; ok is false when there are none. Preferences only (Validate),
// never a secret.
func ClaudeSettingsJSON(s *Spec) (string, bool) {
	if s == nil || s.Claude == nil || len(s.Claude.Settings) == 0 {
		return "", false
	}
	b, err := json.Marshal(s.Claude.Settings)
	if err != nil { // Validate: a JSON object
		return "", false
	}
	return string(b), true
}
