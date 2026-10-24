package jam

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/aethons-tools/cove/internal/jam/sessionctx"
	"github.com/aethons-tools/cove/internal/jam/snippet"
)

// contextBody renders a session-context bundle at its real paths: the file
// list with sizes, each layer's core budget used, the compile warnings, then
// every file in full (CORE.md first).
func contextBody(b sessionctx.Bundle) string {
	files := map[string]string{"CORE.md": b.Core}
	maps.Copy(files, b.Files)
	paths := append([]string{"CORE.md"}, slices.Sorted(maps.Keys(b.Files))...)
	var sb strings.Builder
	rows := make([][]string, 0, len(paths))
	for _, p := range paths {
		rows = append(rows, []string{sessionctx.Dir + "/" + p, fmt.Sprint(len(files[p]))})
	}
	writeTable(&sb, []string{"FILE", "BYTES"}, rows)
	if len(b.Budgets) > 0 {
		sb.WriteString("\ncore budget used:\n")
		for _, n := range sessionctx.LayerOrder() {
			if lb, ok := b.Budgets[n]; ok {
				fmt.Fprintf(&sb, "  %s %d/%d B\n", n, lb.Used, lb.Budget)
			}
		}
	}
	for _, w := range b.Warnings {
		fmt.Fprintf(&sb, "warning: %s\n", flat(w))
	}
	for _, p := range paths {
		fmt.Fprintf(&sb, "\n--- %s/%s\n%s", sessionctx.Dir, p, ensureNL(files[p]))
	}
	return sb.String()
}

// egressBody renders an egress policy as it applies in the box: a role list
// (each domain marked when the kit ceiling does not cover it — the in-box
// apply refuses those), or the kit default (the ceiling itself).
func egressBody(role *EgressPolicy, ceiling []string, ceilingKnown bool) string {
	var sb strings.Builder
	var rows [][]string
	if role == nil {
		sb.WriteString("policy: kit default\n")
		for _, h := range ceiling {
			rows = append(rows, []string{flat(h), "kit"})
		}
	} else {
		fmt.Fprintf(&sb, "policy: role (%d domain(s))\n", len(role.Domains))
		for _, h := range role.Domains {
			from := "role"
			if ceilingKnown && !slices.ContainsFunc(ceiling, func(c string) bool { return EgressCovers(c, h) }) {
				from = "role, outside the kit ceiling (refused in-box)"
			}
			rows = append(rows, []string{flat(h), from})
		}
	}
	if ceilingKnown {
		fmt.Fprintf(&sb, "kit ceiling: %d domain(s)\n", len(ceiling))
	} else {
		sb.WriteString("kit ceiling: unknown\n")
	}
	if len(rows) > 0 {
		writeTable(&sb, []string{"HOST", "FROM"}, rows)
	}
	return sb.String()
}

// sealedEgressBody is the sealed base list's rows, appended at render time
// (never stored in a snapshot): the hardening layer's always-on entries.
func sealedEgressBody(sealed []string) string {
	if len(sealed) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\nsealed base (always on):\n")
	rows := make([][]string, 0, len(sealed))
	for _, h := range sealed {
		rows = append(rows, []string{h, "sealed"})
	}
	writeTable(&sb, []string{"HOST", "FROM"}, rows)
	return sb.String()
}

// connectorBody renders the env an agent spawn gets from its connector, every
// value through RedactEnv (the identity-token vars included), then git
// routing and the delivered model-spec's name.
func connectorBody(c snippet.Connector) string {
	env := map[string]string{}
	for _, k := range snippet.IdentityTokenVars() {
		env[k] = "{token}"
	}
	maps.Copy(env, c.Env)
	var sb strings.Builder
	for _, k := range slices.Sorted(maps.Keys(env)) {
		fmt.Fprintf(&sb, "%s=%s\n", flat(k), flat(RedactEnv(k, env[k])))
	}
	if c.GitRoute != "" {
		fmt.Fprintf(&sb, "git: %s → {base}%s\n", flat(c.RewriteTarget()), flat(c.GitRoute))
	} else {
		sb.WriteString("git: not routed\n")
	}
	if c.ModelSpec != nil {
		fmt.Fprintf(&sb, "model-spec: %s\n", flat(c.ModelSpec.Name))
	} else {
		sb.WriteString("model-spec: none\n")
	}
	return sb.String()
}

// kitRefsBody is what a raise recorded of the kit it ran: the kit ref, its
// chain (root first), and the image tag.
func kitRefsBody(kit KitRef, chain []KitRef, imageTag string) string {
	if kit.ID == "" {
		return "kit: none (the launcher's static image)\n"
	}
	refs := chainOrSelf(chain, kit)
	names := make([]string, len(refs))
	for i, r := range refs {
		names[i] = r.String()
	}
	if imageTag == "" {
		imageTag = "unknown"
	}
	return fmt.Sprintf("kit: %s digest %s\nchain (root first): %s\nimage tag: %s\n", kit, shortDigest(kit.Digest), strings.Join(names, " → "), imageTag)
}

// kitFingerprint keys the kit snapshot part.
func kitFingerprint(kit KitRef, imageTag string) string {
	return kit.String() + "|" + kit.Digest + "|" + imageTag
}

// chainOrSelf is a recorded chain, or the kit alone when unchained.
func chainOrSelf(chain []KitRef, self KitRef) []KitRef {
	if len(chain) > 0 {
		return chain
	}
	return []KitRef{{ID: self.ID, Version: self.Version}}
}
