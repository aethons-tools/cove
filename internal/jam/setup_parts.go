package jam

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
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
		rows = append(rows, []string{flat(sessionctx.Dir + "/" + p), fmt.Sprint(len(files[p]))})
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
		fmt.Fprintf(&sb, "\n--- %s/%s\n%s", sessionctx.Dir, flat(p), ensureNL(files[p]))
	}
	return sb.String()
}

// ContextBodyFile is one file of a context section body: its real path and
// its text.
type ContextBodyFile struct{ Path, Body string }

// SplitContextBody splits a context section body (contextBody's output, a
// snapshot or a recompute) back into its head — the file table, the budgets,
// the warnings — and its files, in the table's order. Marker searches start
// after the table's bytes, and each file's extent comes from the table's
// size when the next marker sits exactly there, so a file holding a later
// file's marker line stays its own. A truncated body yields the files it
// still holds (the last one carrying the truncation marker). ok is false when
// body does not start with the table or holds none of its files (an
// unavailable or empty section).
func SplitContextBody(body string) (head string, files []ContextBodyFile, ok bool) {
	lines := strings.Split(body, "\n")
	if !strings.HasPrefix(lines[0], "FILE") {
		return "", nil, false
	}
	var paths []string
	var sizes []int
	tableEnd := len(lines[0]) + 1
	for _, l := range lines[1:] {
		f := strings.Fields(l)
		if len(f) != 2 || !strings.HasPrefix(f[0], sessionctx.Dir+"/") {
			break
		}
		n, err := strconv.Atoi(f[1])
		if err != nil || n < 0 || n > len(body) {
			n = -1 // unusable size: this file falls back to its markers
		}
		paths = append(paths, f[0])
		sizes = append(sizes, n)
		tableEnd += len(l) + 1
	}
	tableEnd = min(tableEnd, len(body))
	marker := func(p string) string { return "\n--- " + p + "\n" }
	start := -1 // where the current file's text begins
	for i, p := range paths {
		var at int // where this file's marker begins
		if start < 0 {
			j := strings.Index(body[tableEnd:], marker(p))
			if j < 0 {
				break
			}
			at = tableEnd + j
			head = body[:at]
		} else {
			// The previous file's text is ensureNL(content): its size, plus
			// a newline when the content lacked one.
			end := -1
			for _, extra := range []int{0, 1} {
				e := start + sizes[i-1] + extra
				if sizes[i-1] >= 0 && e >= start && e <= len(body) && strings.HasPrefix(body[e:], marker(p)) && (extra == 0 || body[e-1] == '\n') {
					end = e
					break
				}
			}
			if end < 0 { // sizes do not fit: first marker after the file starts
				j := strings.Index(body[start:], marker(p))
				if j < 0 {
					break // truncated before this file
				}
				end = start + j
			}
			files[len(files)-1].Body = body[start:end]
			at = end
		}
		files = append(files, ContextBodyFile{Path: p})
		start = at + len(marker(p))
	}
	if start < 0 {
		return "", nil, false
	}
	files[len(files)-1].Body = body[start:]
	return head, files, true
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
		fmt.Fprintf(&sb, "%s=%s\n", SafeText(k), flat(RedactEnv(k, env[k])))
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
		names[i] = flat(r.String())
	}
	if imageTag == "" {
		imageTag = "unknown"
	}
	return fmt.Sprintf("kit: %s digest %s\nchain (root first): %s\nimage tag: %s\n", flat(kit.String()), flat(shortDigest(kit.Digest)), strings.Join(names, " → "), flat(imageTag))
}

// instanceKitRefsBody is an instance's kit refs: kitRefsBody, or for a
// draft studio its kit as KitDraftRef.String() ("<kit> (draft @<tree[:7]>)")
// and its own chain link as "<kit> (draft)", never "<kit>@v0".
func instanceKitRefsBody(inst Instance) string {
	if inst.KitDraft == nil {
		return kitRefsBody(inst.Kit, inst.KitChain, inst.ImageTag)
	}
	refs := chainOrSelf(inst.KitChain, inst.Kit)
	names := make([]string, len(refs))
	for i, r := range refs {
		names[i] = flat(r.String())
	}
	names[len(names)-1] = flat(inst.KitDraft.Kit) + " (draft)"
	tag := inst.ImageTag
	if tag == "" {
		tag = "unknown"
	}
	return fmt.Sprintf("kit: %s digest %s\nchain (root first): %s\nimage tag: %s\n",
		flat(inst.KitDraft.String()), flat(shortDigest(inst.Kit.Digest)), strings.Join(names, " → "), flat(tag))
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
