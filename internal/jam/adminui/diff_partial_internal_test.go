package adminui

import (
	"bytes"
	"testing"
)

// legacyDiffMarkup is the inline markup config.html and kit.html carried
// before the diff-lines partial; the partial must render it byte for byte.
const legacyDiffMarkup = `{{define "legacy"}}<pre class="diff">{{range .}}{{if eq .Op "+"}}<span class="d-add">+ {{.Text}}</span>{{else if eq .Op "-"}}<span class="d-del">- {{.Text}}</span>{{else if eq .Op "…"}}<span class="d-skip">  … {{.Text}}</span>{{else}}<span>  {{.Text}}</span>{{end}}{{end}}</pre>{{end}}`

// Every page set carries the one line-diff partial (the Config, kit and Setup
// pages render through it), escaping each line's text.
func TestDiffLinesPartialOnEveryPage(t *testing.T) {
	lines := []diffLine{{Op: " ", Text: "same"}, {Op: "-", Text: "old <b>"}, {Op: "+", Text: "new"}, {Op: "…", Text: "4 unchanged lines"}}
	want := `<pre class="diff"><span>  same</span><span class="d-del">- old &lt;b&gt;</span><span class="d-add">+ new</span><span class="d-skip">  … 4 unchanged lines</span></pre>`
	for name, p := range pages {
		t2, err := p.t.Clone()
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if err := t2.ExecuteTemplate(&buf, "diff-lines", lines); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if buf.String() != want {
			t.Errorf("%s: diff-lines =\n%s\nwant\n%s", name, buf.String(), want)
		}
		// Byte-identical to the markup the pages used to inline.
		t3, err := p.t.Clone()
		if err != nil {
			t.Fatal(err)
		}
		old, err := t3.Parse(legacyDiffMarkup)
		if err != nil {
			t.Fatal(err)
		}
		var ob bytes.Buffer
		if err := old.ExecuteTemplate(&ob, "legacy", lines); err != nil {
			t.Fatal(err)
		}
		if ob.String() != buf.String() {
			t.Errorf("%s: partial differs from legacy inline markup", name)
		}
	}
}
