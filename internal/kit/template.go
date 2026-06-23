package kit

import "strings"

// Substitute performs GNU envsubst-style expansion: ${VAR} and $VAR where
// VAR is a shell identifier ([A-Za-z_][A-Za-z0-9_]*) are replaced with the
// looked-up value; an unset variable becomes the empty string. Anything
// that is not a valid reference is emitted verbatim. No escaping, command
// substitution, or arithmetic is performed.
func Substitute(input string, lookup func(string) (string, bool)) string {
	var b strings.Builder
	for i := 0; i < len(input); {
		if input[i] != '$' {
			b.WriteByte(input[i])
			i++
			continue
		}
		// Braced form ${VAR}
		if i+1 < len(input) && input[i+1] == '{' {
			if end := strings.IndexByte(input[i+2:], '}'); end >= 0 {
				name := input[i+2 : i+2+end]
				if isIdent(name) {
					val, _ := lookup(name)
					b.WriteString(val)
					i = i + 2 + end + 1
					continue
				}
			}
			// Not a valid ${ident}: emit the literal '$' and rescan from '{'.
			b.WriteByte('$')
			i++
			continue
		}
		// Bare form $VAR
		j := i + 1
		for j < len(input) && isIdentByte(input[j], j == i+1) {
			j++
		}
		if j > i+1 {
			val, _ := lookup(input[i+1 : j])
			b.WriteString(val)
			i = j
			continue
		}
		// Lone dollar.
		b.WriteByte('$')
		i++
	}
	return b.String()
}

func isIdentByte(c byte, first bool) bool {
	switch {
	case c == '_':
		return true
	case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z':
		return true
	case !first && c >= '0' && c <= '9':
		return true
	}
	return false
}

func isIdent(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isIdentByte(s[i], i == 0) {
			return false
		}
	}
	return true
}
