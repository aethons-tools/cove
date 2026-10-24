package jam

import (
	"cmp"
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"
)

// seedBody renders what an image seeds into /agent-data: CLAUDE.md and the
// files it imports in full, skills by name and description, settings.json
// redacted, the .refresh list, every other file by path and size.
func seedBody(f ImageFacts) string {
	byPath := map[string]SeedFile{}
	for _, s := range f.Seed {
		byPath[s.Path] = s
	}
	shown := map[string]bool{}
	notShown := map[string]bool{}
	var sb strings.Builder
	fmt.Fprintf(&sb, "image: %s\n", flat(shortDigest(f.ID)))
	if c, ok := byPath["CLAUDE.md"]; ok && c.HasText {
		shown["CLAUDE.md"] = true
		fmt.Fprintf(&sb, "\n--- /agent-data/CLAUDE.md\n%s", ensureNL(redactText(c.Text)))
		for _, imp := range claudeImports(c.Text) {
			if !strings.HasSuffix(imp, ".md") {
				notShown[imp] = true
				continue
			}
			if s, ok := byPath[imp]; ok && s.HasText && !shown[imp] {
				shown[imp] = true
				fmt.Fprintf(&sb, "\n--- /agent-data/%s (imported)\n%s", flat(imp), ensureNL(redactText(s.Text)))
			}
		}
	} else {
		sb.WriteString("\nCLAUDE.md: none\n")
	}
	var skills [][]string
	for _, s := range f.Seed {
		if dir, ok := skillDir(s.Path); ok && s.HasText {
			shown[s.Path] = true
			name, desc := skillMeta(s.Text)
			skills = append(skills, []string{"  " + flat(cmp.Or(name, dir)), flat(redactLines(desc))})
		}
	}
	if len(skills) == 0 {
		sb.WriteString("\nskills: none\n")
	} else {
		sb.WriteString("\nskills:\n")
		writeTable(&sb, []string{"  NAME", "DESCRIPTION"}, skills)
	}
	if s, ok := byPath["settings.json"]; ok {
		shown["settings.json"] = true
		if js, ok := RedactJSON([]byte(s.Text)); s.HasText && ok {
			fmt.Fprintf(&sb, "\n--- /agent-data/settings.json (secret-named values masked)\n%s\n", escapeText(js, true))
		} else {
			fmt.Fprintf(&sb, "\nsettings.json: not valid JSON (%d B)\n", s.Size)
		}
	}
	if s, ok := byPath[".refresh"]; ok && s.HasText {
		shown[".refresh"] = true
		fmt.Fprintf(&sb, "\nre-copied every boot (.refresh): %s\n", joinOr(refreshEntries(s.Text), "none"))
	}
	var rest [][]string
	for _, s := range f.Seed {
		if !shown[s.Path] {
			size := fmt.Sprintf("%d B", s.Size)
			if notShown[s.Path] {
				size += " (import not shown)"
			}
			rest = append(rest, []string{"  /agent-data/" + flat(s.Path), size})
		}
	}
	if len(rest) > 0 {
		sb.WriteString("\nother files:\n")
		writeTable(&sb, []string{"  PATH", "SIZE"}, rest)
	}
	if f.SeedNote != "" {
		fmt.Fprintf(&sb, "\n(%s)\n", flat(f.SeedNote))
	}
	return sb.String()
}

// redactLines passes each line of seeded prose through RedactEnv with no key.
func redactLines(text string) string {
	var sb strings.Builder
	for line := range strings.Lines(text) {
		body := strings.TrimSuffix(line, "\n")
		sb.WriteString(RedactEnv("", body))
		if len(body) != len(line) {
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

// redactText is redactLines made inert, keeping newlines and tabs.
func redactText(text string) string { return escapeText(redactLines(text), true) }

// claudeImports are CLAUDE.md's "@path" lines that stay inside the seed.
func claudeImports(text string) []string {
	var out []string
	for line := range strings.Lines(text) {
		p, ok := strings.CutPrefix(strings.TrimSpace(line), "@")
		if !ok || p == "" || strings.HasPrefix(p, "/") {
			continue
		}
		if c := path.Clean(p); !strings.HasPrefix(c, "..") {
			out = append(out, c)
		}
	}
	return out
}

func skillDir(p string) (string, bool) {
	parts := strings.Split(p, "/")
	if len(parts) == 3 && parts[0] == "skills" && parts[2] == "SKILL.md" {
		return parts[1], true
	}
	return "", false
}

// skillMeta reads name and description from a SKILL.md frontmatter.
func skillMeta(text string) (name, desc string) {
	body, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		return "", ""
	}
	front, _, _ := strings.Cut(body, "\n---")
	for line := range strings.Lines(front) {
		k, v, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		switch k {
		case "name":
			name = v
		case "description":
			desc = v
		}
	}
	return name, desc
}

func refreshEntries(text string) []string {
	var out []string
	for line := range strings.Lines(text) {
		if l := strings.TrimSpace(line); l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, flat(l))
		}
	}
	return out
}

// imageEnvBody renders an image's baked ENV (redacted) and the names its
// COVE_SSHENV transfers into sessions.
func imageEnvBody(f ImageFacts) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "image env (%s):\n", flat(shortDigest(f.ID)))
	var transfer string
	for _, kv := range f.Env {
		k, v, _ := strings.Cut(kv, "=")
		if k == "COVE_SSHENV" {
			transfer = v
		}
		fmt.Fprintf(&sb, "  %s=%s\n", flat(k), flat(RedactEnv(k, v)))
	}
	names := strings.FieldsFunc(transfer, func(r rune) bool { return r == ':' })
	for i, n := range names {
		names[i] = flat(n)
	}
	fmt.Fprintf(&sb, "transferred into sessions (COVE_SSHENV): %s\n", joinOr(names, "none"))
	return sb.String()
}

// imageLabelsBody renders an image's id and labels, each re-parsed.
func imageLabelsBody(f ImageFacts) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "image id: %s\n", flat(f.ID))
	if len(f.Labels) == 0 {
		sb.WriteString("labels: none\n")
		return sb.String()
	}
	sb.WriteString("labels:\n")
	for _, k := range slices.Sorted(maps.Keys(f.Labels)) {
		dk, dv := RedactLabel(k, f.Labels[k])
		fmt.Fprintf(&sb, "  %s=%s\n", flat(dk), flat(dv))
	}
	return sb.String()
}

// kitDetailBody renders each chain link's provenance (from the registry) and
// the merged build args (redacted).
func kitDetailBody(store Store, chain []KitRef, buildArgs map[string]string) string {
	var sb strings.Builder
	sb.WriteString("provenance (root first):\n")
	rows := make([][]string, 0, len(chain))
	for _, r := range chain {
		src := "unknown (not in the registry)"
		if k, ok := store.GetKit(r.ID); ok {
			if m, ok := k.Meta[r.Version]; ok {
				src = "local file"
				if m.Source != nil {
					src = sourceText(*m.Source)
				}
				src += ", pushed " + m.PushedAt.UTC().Format(time.RFC3339)
			} else if k.HasVersion(r.Version) {
				src = "legacy version (no provenance)"
			}
		}
		rows = append(rows, []string{"  " + flat(r.String()), src})
	}
	writeTable(&sb, []string{"  KIT", "SOURCE"}, rows)
	if len(buildArgs) == 0 {
		sb.WriteString("build args: none\n")
		return sb.String()
	}
	sb.WriteString("build args:\n")
	for _, k := range slices.Sorted(maps.Keys(buildArgs)) {
		fmt.Fprintf(&sb, "  %s=%s\n", flat(k), flat(RedactEnv(k, buildArgs[k])))
	}
	return sb.String()
}

func sourceText(s KitSource) string {
	out := displayRepo(s.Repo)
	if s.Dir != "" {
		out += "//" + flat(s.Dir)
	}
	out += "@" + flat(s.Ref)
	if s.Commit != "" {
		out += " (commit " + flat(shortDigest(s.Commit)) + ")"
	}
	return out
}

// displayRepo is displayURL, but an scp-style repo (user@host:path, no
// password possible) shows as written instead of invalid.
func displayRepo(raw string) string {
	if !strings.Contains(raw, "://") && scpRepoRE.MatchString(raw) {
		return SafeText(raw)
	}
	return displayURL(raw)
}

var scpRepoRE = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:[A-Za-z0-9._/~-]+$`)
