// Package squawkrender turns a squawk body into safe HTML for the Jam UIs
// (/me and /ui), honoring its content type: markdown (the default) is rendered,
// text/plain is shown literally.
package squawkrender

import (
	"bytes"
	"html/template"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"

	"github.com/aethons-tools/cove/internal/intercom"
)

// md renders GitHub-flavored markdown with chat-style hard line breaks. It is
// NOT configured with html.WithUnsafe, so raw HTML is omitted and dangerous
// link destinations (javascript:, vbscript:, file:, non-image data:) are
// dropped — squawk bodies come from agents and relays, never trusted.
var md = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithParserOptions(parser.WithASTTransformers(util.Prioritized(newTabLinks{}, 100))),
	goldmark.WithRendererOptions(html.WithHardWraps()),
)

// Body renders body as HTML for contentType: text/plain is escaped as-is (the
// caller's CSS preserves its whitespace); anything else — text/markdown or ""
// — is rendered markdown. A render error falls back to the escaped text.
func Body(contentType, body string) template.HTML {
	if contentType == intercom.ContentPlain {
		return template.HTML(template.HTMLEscapeString(body))
	}
	var buf bytes.Buffer
	if err := md.Convert([]byte(body), &buf); err != nil {
		return template.HTML(template.HTMLEscapeString(body))
	}
	return template.HTML(buf.String())
}

// IsPlain reports whether contentType is shown literally (for a CSS class).
func IsPlain(contentType string) bool { return contentType == intercom.ContentPlain }

// newTabLinks makes every link open in a new tab without handing the opened
// page a reference back to the inbox.
type newTabLinks struct{}

func (newTabLinks) Transform(doc *ast.Document, _ text.Reader, _ parser.Context) {
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n.Kind() {
		case ast.KindLink, ast.KindAutoLink:
			n.SetAttributeString("target", []byte("_blank"))
			n.SetAttributeString("rel", []byte("noopener noreferrer"))
		}
		return ast.WalkContinue, nil
	})
}
