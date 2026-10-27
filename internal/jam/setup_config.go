package jam

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/jam/snippet"
	"github.com/aethons-tools/cove/internal/kit"
)

// IntercomTools are the tools the guaranteed intercom MCP server
// (`jam-studio-agent mcp`) offers every session, sorted. cmd/jam-studio-agent pins it
// (TestIntercomToolsMatchJam).
var IntercomTools = []string{"alarm_clear", "alarm_list", "alarm_set", "amend", "ask", "asks", "board_apply", "board_create",
	"board_read", "board_snapshot", "boards", "call_in", "commit", "end", "escalate", "expose", "exposures", "fetch",
	"idle_timeout", "leave", "list_targets", "mission", "read", "report", "send", "unexpose", "withdraw"}

// destinationsBody renders each destination in a's scopes: how it is reached
// and authenticated, by credential NAME, and which env keys it sets.
func destinationsBody(store Store, a Actor) string {
	byName := map[string]Destination{}
	for _, d := range store.ListDestinations() {
		byName[d.Name] = d
	}
	inScope, creds := map[string]bool{}, map[string]string{}
	for _, s := range ScopesFor(store, a) {
		for _, n := range s.Destinations {
			inScope[n] = true
			if c := s.Credentials[n]; c != "" {
				creds[n] = c
			}
		}
	}
	if len(inScope) == 0 {
		return "no destinations in scope\n"
	}
	var sb strings.Builder
	for _, n := range slices.Sorted(maps.Keys(inScope)) {
		d, ok := byName[n]
		if !ok {
			fmt.Fprintf(&sb, "%s: not configured (skipped)\n", SafeText(n))
			continue
		}
		git := "no"
		if d.GitRouted() {
			git = "routed (" + flat(snippet.RewriteTargetFor(d.Upstream)) + ")"
		}
		fmt.Fprintf(&sb, "%s\n", SafeText(d.Name))
		fmt.Fprintf(&sb, "  route:       %s\n", SafeText(d.Route))
		fmt.Fprintf(&sb, "  upstream:    %s\n", displayURL(d.Upstream))
		fmt.Fprintf(&sb, "  identity-in: %s\n", inboundText(d.IdentityIn, d.IdentityInSpec))
		fmt.Fprintf(&sb, "  apply:       %s\n", outboundText(d.Apply, d.ApplySpec))
		fmt.Fprintf(&sb, "  credential:  %s\n", cmp.Or(SafeText(cmp.Or(creds[n], d.CredName)), "none"))
		fmt.Fprintf(&sb, "  git:         %s\n", git)
		fmt.Fprintf(&sb, "  allow-paths: %s\n", joinOr(flatAll(d.AllowPaths), "any"))
		fmt.Fprintf(&sb, "  env keys:    %s\n", joinOr(slices.Sorted(maps.Keys(d.ClientEnv())), "none"))
	}
	return sb.String()
}

func inboundText(m ApplyMethod, s *InboundSpec) string {
	if m == ApplyCustom && s != nil {
		return fmt.Sprintf("custom (header %s, encoding %s)", SafeText(s.Header), cmp.Or(string(s.Encoding), "raw"))
	}
	return cmp.Or(string(m), "none")
}

func outboundText(m ApplyMethod, s *OutboundSpec) string {
	if m == ApplyCustom && s != nil {
		return fmt.Sprintf("custom (header %s, template %q)", SafeText(s.Header), templateText(s.Template))
	}
	return cmp.Or(string(m), "none")
}

// templateText shows an outbound template only when, once {cred} is removed,
// nothing but an optional scheme word remains; any literal text is masked.
func templateText(t string) string {
	rest := strings.TrimSpace(strings.ReplaceAll(t, "{cred}", ""))
	switch strings.ToLower(rest) {
	case "", "bearer", "basic", "token":
		return SafeText(t)
	}
	return MarkMasked
}

// displayURL renders a URL for display without its userinfo, query or
// fragment (via the one URL redaction in setup_redact.go); an unparsable one
// is MarkInvalid. (Named displayURL: stripUserinfo already exists there.)
func displayURL(raw string) string {
	u, ok := stripUserinfo(raw)
	if !ok {
		return MarkInvalid
	}
	return SafeText(u)
}

// modelBody renders a resolved model-spec (names only — never a secret).
func modelBody(ms *ModelSpec) string {
	if ms == nil {
		return "model-spec: none (the harness's built-in defaults)\n"
	}
	raw, err := json.Marshal(ms)
	var v map[string]any
	if err == nil {
		err = json.Unmarshal(raw, &v)
	}
	if err != nil {
		return "model-spec: " + flat(ms.Name) + " (unrenderable)\n"
	}
	// The principal's credential is a NAME, not a value: keep it readable.
	var credName any
	if p, ok := v["principal"].(map[string]any); ok {
		credName = p["credential"]
		if hs, ok := p["headers"].([]any); ok {
			for _, x := range hs {
				if m, ok := x.(map[string]any); ok {
					for _, k := range []string{"set", "ensure-list-item"} {
						if val, ok := m[k].(string); ok {
							// injected auth by nature: only placeholders show
							m[k] = RedactEnv("authorization", val)
						}
					}
				}
			}
		}
	}
	v = redactJSONValue("", v).(map[string]any)
	if p, ok := v["principal"].(map[string]any); ok {
		if n, ok := credName.(string); ok {
			if valueLooksSecret(n) {
				n = MarkMasked
			}
			p["credential"] = SafeText(n)
		}
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return "model-spec: " + flat(ms.Name) + " (unrenderable)\n"
	}
	return out.String()
}

// mcpBody renders the kit's MCP servers (no args, no header values) and the
// intercom tools every session is offered.
func mcpBody(servers map[string]kit.MCPServer, kitKnown bool) string {
	var sb strings.Builder
	switch {
	case !kitKnown:
		sb.WriteString("kit servers: unknown (no kit resolved)\n")
	case len(servers) == 0:
		sb.WriteString("kit servers: none\n")
	default:
		sb.WriteString("kit servers:\n")
		rows := make([][]string, 0, len(servers))
		for _, n := range slices.Sorted(maps.Keys(servers)) {
			s := servers[n]
			where := displayURL(s.URL)
			if s.Type == "stdio" {
				where = flat(RedactEnv("command", s.Command))
				if len(s.Args) > 0 {
					where += fmt.Sprintf(" (+%d args)", len(s.Args))
				}
			}
			rows = append(rows, []string{"  " + SafeText(n), SafeText(s.Type), where, joinOr(slices.Sorted(maps.Keys(s.Headers)), "-")})
		}
		writeTable(&sb, []string{"  NAME", "TYPE", "URL/COMMAND", "HEADERS"}, rows)
	}
	fmt.Fprintf(&sb, "intercom (jam-studio-agent mcp, always loaded): %s\n", strings.Join(IntercomTools, ", "))
	return sb.String()
}

// rolePolicyBody renders a role's policy and its project's escalation chain.
func rolePolicyBody(store Store, project string, r Role) string {
	var sb strings.Builder
	a := r.Allocation
	maxEph, perOwner := "unset", "pool only"
	if a.MaxEphemeral > 0 {
		maxEph = fmt.Sprint(a.MaxEphemeral)
	}
	if a.MaxPersonalPerOwner > 0 {
		perOwner = fmt.Sprint(a.MaxPersonalPerOwner)
	}
	fmt.Fprintf(&sb, "allocation: max-ephemeral %s, max-personal %d, max-personal-per-owner %s\n", maxEph, a.MaxPersonal, perOwner)
	idle, nag, reclaim := a.PersonalIdle()
	fmt.Fprintf(&sb, "personal idle: idle-after %s, nag-every %s, reclaim-after %s\n", idle, nag, durOr(reclaim, "never"))
	names := make([]string, 0, len(a.Standing))
	for _, s := range a.Standing {
		names = append(names, SafeText(s.Name))
	}
	fmt.Fprintf(&sb, "standing: %s\n", joinOr(names, "none"))
	te := r.TurnEnd
	fmt.Fprintf(&sb, "turn-end: idle-timeout %s, on-idle %s, time-zone %s\n", durOr(te.IdleTimeout, "none"), te.Action(), flat(cmp.Or(te.TimeZone, "UTC")))
	fmt.Fprintf(&sb, "collaborate: preview %s, ssh %s\n", flat(cmp.Or(r.Collaborate.Preview, "members")), flat(cmp.Or(r.Collaborate.SSH, "none")))
	p, _ := store.GetProject(orDefaultProject(project))
	fmt.Fprintf(&sb, "escalation (default): %s\n", tiersText(p.Escalation))
	for _, c := range slices.Sorted(maps.Keys(p.EscalationByCategory)) {
		fmt.Fprintf(&sb, "escalation (%s): %s\n", SafeText(c), tiersText(p.EscalationByCategory[c]))
	}
	return sb.String()
}

// instancePolicyBody renders a studio's own policy state: its idle override
// and its alarms (name, schedule, next fire, gated — never the gate or note).
func instancePolicyBody(inst Instance) string {
	var sb strings.Builder
	if o := inst.IdleOverride; o != nil {
		fmt.Fprintf(&sb, "idle override: %s (%s)\n", durOr(o.Duration, "off"), flat(string(o.Scope)))
	} else {
		sb.WriteString("idle override: none\n")
	}
	if len(inst.Alarms) == 0 {
		sb.WriteString("alarms: none\n")
		return sb.String()
	}
	sb.WriteString("alarms:\n")
	for _, al := range inst.Alarms {
		next, gate := "-", "no"
		if !al.NextAt.IsZero() {
			next = al.NextAt.UTC().Format(time.RFC3339)
		}
		if al.Gate != "" {
			gate = "yes"
		}
		fmt.Fprintf(&sb, "  %s  %s  next %s  gate %s\n", SafeText(al.Name), SafeText(al.Schedule), next, gate)
	}
	return sb.String()
}

func tiersText(ts []EscalationTier) string {
	if len(ts) == 0 {
		return "none"
	}
	parts := make([]string, len(ts))
	for i, t := range ts {
		parts[i] = fmt.Sprintf("%d. %s (after %s)", i+1, flat(strings.Join(t.Targets, ", ")), t.Timeout)
	}
	return strings.Join(parts, "; ")
}

func durOr(d time.Duration, zero string) string {
	if d <= 0 {
		return zero
	}
	return d.String()
}

func roleIdentityBody(store Store, project, role string) string {
	return fmt.Sprintf("project: %s\nrole: %s\nsession kind: ephemeral (a would-be raise: no unit, no owner)\n", ProjectName(store, orDefaultProject(project)), flat(role))
}

func instanceIdentityBody(store Store, inst Instance) string {
	kind := cmp.Or(inst.SessionKind, "ephemeral")
	switch inst.SessionKind {
	case SessionKindStanding:
		kind += " (name " + SafeText(inst.Name) + ")"
	case SessionKindPersonal:
		kind += " (owner " + SafeText(inst.Owner) + ")"
	}
	if inst.Unit != "" {
		kind += ", unit " + SafeText(inst.Unit)
	}
	return fmt.Sprintf("project: %s\nrole: %s\nsession kind: %s\nstudio: %s\nphase: %s\nraised at: %s\n",
		ProjectName(store, inst.Project), flat(inst.Role), kind, flat(inst.ActorID), flat(string(inst.Phase)), inst.RaisedAt.UTC().Format(time.RFC3339))
}

func flatAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, x := range ss {
		out[i] = flat(x)
	}
	return out
}
