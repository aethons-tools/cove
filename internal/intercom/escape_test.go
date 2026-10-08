package intercom

import "testing"

func TestEscapeMarkdownCommonMark(t *testing.T) {
	const nbsp = "\u00a0"
	for _, tc := range []struct{ name, in, want string }{
		{"inline emphasis and code", "2 * 3 = 6 and a_b_c `x`", `2 \* 3 = 6 and a\_b\_c \` + "`" + `x\` + "`"},
		{"links, html, entities", "[x](y) <b> &copy;", `\[x\](y) \<b\> \&copy;`},
		{"backslash, pipe, tilde", `a\b | ~~c~~`, `a\\b \| \~\~c\~\~`},
		{"line-start markers", "# h\n- item\n+ item\n> quote\n1. one\n2) two\n===", `\# h  ` + "\n" + `\- item  ` + "\n" + `\+ item  ` + "\n" + `\> quote  ` + "\n" + `1\. one  ` + "\n" + `2\) two  ` + "\n" + `\===`},
		{"newlines become hard breaks", "a\nb", "a  \nb"},
		{"indentation is not a code block", "    code", nbsp + nbsp + nbsp + nbsp + "code"},
		{"mid-line punctuation and urls stay readable", "see https://x.example/a-b.html - ok. 3.5", "see https://x.example/a-b.html - ok. 3.5"},
	} {
		if got := EscapeMarkdown(tc.in, FlavorCommonMark); got != tc.want {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
	}
}

func TestEscapeMarkdownDiscord(t *testing.T) {
	// Discord shows newlines and indentation literally, so only the markdown
	// characters are escaped.
	for _, tc := range []struct{ name, in, want string }{
		{"inline", "a_b *c* ||spoiler||", `a\_b \*c\* \|\|spoiler\|\|`},
		{"line-start", "# h\n> q\n- i", "\\# h\n\\> q\n\\- i"},
		{"indentation kept", "    x", "    x"},
	} {
		if got := EscapeMarkdown(tc.in, FlavorDiscord); got != tc.want {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
	}
}
