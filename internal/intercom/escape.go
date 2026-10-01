package intercom

import "strings"

// Flavor is the markdown dialect a relay surface renders.
type Flavor int

const (
	// FlavorCommonMark is CommonMark/GFM (Linear comments): single newlines
	// join lines and leading indentation makes a code block.
	FlavorCommonMark Flavor = iota
	// FlavorDiscord is Discord's markdown: newlines and indentation are shown
	// literally.
	FlavorDiscord
)

// EscapeMarkdown makes plain text s display literally on a surface that renders
// markdown in flavor f — how a text/plain squawk is delivered to Linear or
// Discord. It backslash-escapes the characters that trigger markdown anywhere
// in a line and the block markers that only matter at a line's start, leaving
// other punctuation alone so bare URLs still auto-link. For CommonMark it also
// turns newlines into hard breaks and leading indentation into non-breaking
// spaces (so neither collapses nor becomes a code block).
func EscapeMarkdown(s string, f Flavor) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = escapeLine(line, f)
	}
	if f == FlavorCommonMark {
		return strings.Join(lines, "  \n")
	}
	return strings.Join(lines, "\n")
}

func escapeLine(line string, f Flavor) string {
	var b strings.Builder
	rest := strings.TrimLeft(line, " \t")
	indent := line[:len(line)-len(rest)]
	if f == FlavorCommonMark {
		for _, r := range indent {
			n := 1
			if r == '\t' {
				n = 4
			}
			b.WriteString(strings.Repeat(" ", n))
		}
	} else {
		b.WriteString(indent)
	}
	// Block markers: "#" headings, ">" quotes, "-"/"+" lists and thematic
	// breaks, "=" setext underlines, and "1." / "1)" ordered lists.
	if rest != "" && strings.ContainsRune("#>-+=", rune(rest[0])) {
		b.WriteByte('\\')
		b.WriteByte(rest[0])
		rest = rest[1:]
	} else if d := leadingDigits(rest); d > 0 && d < len(rest) && (rest[d] == '.' || rest[d] == ')') {
		b.WriteString(rest[:d])
		b.WriteByte('\\')
		b.WriteByte(rest[d])
		rest = rest[d+1:]
	}
	inline := "\\`*_[]<>|~"
	if f == FlavorCommonMark {
		inline += "&" // entity references (&copy;) would otherwise render
	}
	for i := 0; i < len(rest); i++ {
		if strings.IndexByte(inline, rest[i]) >= 0 {
			b.WriteByte('\\')
		}
		b.WriteByte(rest[i])
	}
	return b.String()
}

func leadingDigits(s string) int {
	n := 0
	for n < len(s) && s[n] >= '0' && s[n] <= '9' {
		n++
	}
	return n
}
