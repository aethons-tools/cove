// Package textesc makes untrusted text inert for a terminal, a plan or a page:
// what could forge structure or drive a terminal is shown escaped.
package textesc

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Escape escapes every control, format, line or paragraph separator
// character and every invalid UTF-8 byte (\x1b, \u202e, \xff), tabs and
// newlines included. With keepLayout, \n and \t pass through (multi-line
// bodies); \r is always escaped.
func Escape(s string, keepLayout bool) string {
	var sb strings.Builder
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && n == 1:
			fmt.Fprintf(&sb, `\x%02x`, s[i])
		case keepLayout && (r == '\n' || r == '\t'):
			sb.WriteRune(r)
		case r == '\n':
			sb.WriteString(`\n`)
		case r == '\r':
			sb.WriteString(`\r`)
		case r == '\t':
			sb.WriteString(`\t`)
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == 0x2028 || r == 0x2029:
			switch {
			case r < 0x100:
				fmt.Fprintf(&sb, `\x%02x`, r)
			case r < 0x10000:
				fmt.Fprintf(&sb, `\u%04x`, r)
			default:
				fmt.Fprintf(&sb, `\U%08x`, r)
			}
		default:
			sb.WriteString(s[i : i+n])
		}
		i += n
	}
	return sb.String()
}
