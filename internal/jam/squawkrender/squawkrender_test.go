package squawkrender

import (
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/intercom"
)

func TestMarkdownRenders(t *testing.T) {
	got := string(Body(intercom.ContentMarkdown, "**bold** and `code`\nnext line\n\n- item\n\n| a | b |\n|---|---|\n| 1 | 2 |"))
	for _, want := range []string{"<strong>bold</strong>", "<code>code</code>", "<br", "<li>item</li>", "<table>"} {
		if !strings.Contains(got, want) {
			t.Errorf("markdown output missing %q:\n%s", want, got)
		}
	}
	// "" is the default content type: markdown.
	if !strings.Contains(string(Body("", "*em*")), "<em>em</em>") {
		t.Error(`content type "" should render as markdown`)
	}
}

func TestPlainIsLiteral(t *testing.T) {
	got := string(Body(intercom.ContentPlain, "**not bold** <b>x</b> & a_b"))
	want := "**not bold** &lt;b&gt;x&lt;/b&gt; &amp; a_b"
	if got != want {
		t.Fatalf("plain = %q, want %q", got, want)
	}
}

// TestMarkdownIsSanitized: squawk bodies come from agents and relays, so the
// rendered HTML must never carry script, raw HTML, or script URLs.
func TestMarkdownIsSanitized(t *testing.T) {
	for _, in := range []string{
		"<script>alert(1)</script>",
		`<img src=x onerror="alert(1)">`,
		"[click](javascript:alert(1))",
		"![img](javascript:alert(1))",
		"<javascript:alert(1)>",
		`<a href="javascript:alert(1)">x</a>`,
		"[x](vbscript:msgbox(1))",
		`<iframe src="https://evil.example"></iframe>`,
	} {
		got := strings.ToLower(string(Body(intercom.ContentMarkdown, in)))
		// Script URLs may survive as visible link text; never in an attribute.
		for _, bad := range []string{"<script", "onerror", `href="javascript:`, `src="javascript:`, `href="vbscript:`, "<iframe", "<img src=x"} {
			if strings.Contains(got, bad) {
				t.Errorf("input %q rendered %q (contains %q)", in, got, bad)
			}
		}
	}
}

func TestLinksOpenSafelyInNewTab(t *testing.T) {
	got := string(Body(intercom.ContentMarkdown, "see https://example.com and [docs](https://docs.example)"))
	if strings.Count(got, `target="_blank"`) != 2 || strings.Count(got, `rel="noopener noreferrer"`) != 2 {
		t.Fatalf("links should open in a new tab with noopener: %s", got)
	}
}
