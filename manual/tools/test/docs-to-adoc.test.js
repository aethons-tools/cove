// docs-to-adoc: the Markdown → AsciiDoc conversion of the repo's docs
// (docs/modules/ROOT/pages/docs-asciidoc.adoc). Hermetic: converts the
// sample tree under fixture/docs and renders the result with Asciidoctor.
'use strict'

const assert = require('node:assert/strict')
const { execFileSync, spawnSync } = require('node:child_process')
const fs = require('node:fs')
const os = require('node:os')
const path = require('node:path')
const { test } = require('node:test')
const Asciidoctor = require('@asciidoctor/core')
const { convert, outPathOf, layoutOf } = require('../docs-to-adoc')

const fixture = path.join(__dirname, 'fixture')
const docs = path.join(fixture, 'docs')
const script = path.join(__dirname, '..', 'docs-to-adoc.js')
const layout = { repoRoot: fixture, modules: { usage: 'usage' }, keepMd: ['superpowers/**'] }

function convertDoc (rel, opts = layout) {
  const src = path.join(docs, rel)
  return convert(fs.readFileSync(src, 'utf8'), { ...opts, srcPath: src })
}

// render converts AsciiDoc to HTML and returns it with Asciidoctor's log.
function render (adoc) {
  const asciidoctor = Asciidoctor()
  const logger = asciidoctor.MemoryLogger.create()
  asciidoctor.LoggerManager.setLogger(logger)
  const html = asciidoctor.convert(adoc, { safe: 'safe', standalone: false, attributes: { 'attribute-missing': 'warn' } })
  return { html, log: logger.getMessages().map((m) => m.getText()) }
}

// After an intended change, regenerate the golden file (from the repo root):
//   node manual/tools/docs-to-adoc.js --repo-root manual/tools/test/fixture --module usage=usage \
//     --keep-md 'superpowers/**' -o manual/tools/test/fixture/sample.adoc manual/tools/test/fixture/docs/sample.md
test('the sample doc converts to the expected AsciiDoc', () => {
  const { adoc, warnings } = convertDoc('sample.md')
  assert.equal(adoc, fs.readFileSync(path.join(fixture, 'sample.adoc'), 'utf8'))
  assert.deepEqual(warnings, ['image images/pic.png: an Antora page\'s images live in its module\'s images/ — move it and fix the target'])
})

test('front matter becomes header attributes, values verbatim', () => {
  const { adoc } = convertDoc('sample.md')
  const head = adoc.split('\n\n')[0].split('\n')
  assert.equal(head[0], '= Sample doc (`docs-to-adoc`)')
  assert.deepEqual(head.slice(1).map((l) => l.match(/^:([\w-]+):/)[1]), ['summary', 'read_when', 'owns', 'prereqs', 'tier', 'updated'])
  assert.equal(head[1], ':summary: A sample doc: every construct the converter handles, with `code` and a \\{placeholder}.')
  const doc = Asciidoctor().load(adoc)
  assert.equal(doc.getAttribute('read_when'), 'You are testing docs-to-adoc and need one doc that uses every Markdown construct our docs do.')
  assert.equal(doc.getAttribute('summary'), 'A sample doc: every construct the converter handles, with `code` and a {placeholder}.')
})

test('the converted sample renders cleanly and keeps its content', () => {
  const { html, log } = render(convertDoc('sample.md').adoc)
  assert.deepEqual(log, [])
  for (const want of [
    '<strong>bold</strong>', '<em>italic</em>', '<em>also italic</em>', '<code>inline code</code>',
    '<code>{Targets, Timeout}</code>', '<code>a*b*c</code>', '<code>x_y_z</code>', '<code>--flag</code>',
    '<code>a -&gt; b</code>', '<code>jam</code>\'s', 'Intra<strong>word</strong>', 'placeholder {name} in prose',
    'line<br>', 'escaped &#42;star&#42; stays literal',
    'href="other.html#the-section"', 'href="#lists-and-code"', 'href="https://go.dev"',
    'href="https://github.com/aethons-tools/cove/blob/main/internal/foo.go"',
    '<code class="language-go" data-lang="go">func main() {}</code>', 'plain listing',
    '<code>a|b</code>', 'halign-center', '<div class="title">Note</div>', 'GitHub&#8217;s form.',
    '<div class="quoteblock">', '<hr>', '<img src="images/pic.png" alt="A picture">',
  ]) assert.ok(html.includes(want), `missing ${want}`)
  assert.match(html, /<li>\s*<p>two, with a nested list:<\/p>\s*<div class="olist arabic">/)
  assert.match(html, /<p>three, with a code block:<\/p>\s*<div class="listingblock">[\s\S]*?<\/div>\s*<\/div>\s*<\/li>/)
})

test('a heading another doc links to keeps that anchor', () => {
  const { adoc } = convertDoc('sample.md')
  assert.match(adoc, /\[#tables\]\n== Tables/) // other.md links sample.md#tables
  assert.match(adoc, /\[#lists-and-code\]\n== Lists and code/) // sample links itself
  assert.match(adoc, /\n\n== Quotes and callouts/) // nobody links here: no explicit id
  const { warnings } = convert('# T\n\n## A & b\n', { anchors: new Set(['a--b', 'gone']) })
  assert.deepEqual(warnings, ['a link uses anchor #gone, but no heading has it'])
  assert.match(convert('# T\n\n## A & b\n', { anchors: new Set(['a--b']) }).adoc, /\[#a--b\]/)
})

test('links target the layout: modules, plain trees, kept Markdown, repo files', () => {
  const md = '# T\n\n[a](../sample.md#x) [b](../other.md) [c](u.md) [d](../superpowers/s.md) [e](../../go.mod#L1)\n'
  const src = path.join(docs, 'usage', 'u.md')
  const body = (opts) => convert(md, { ...opts, srcPath: src, anchors: new Set() }).adoc.split('\n')[2]

  assert.equal(body(layout), 'xref:ROOT:sample.adoc#x[a] xref:ROOT:other.adoc[b] xref:u.adoc[c] ' +
    'https://github.com/aethons-tools/cove/blob/main/docs/superpowers/s.md[d] https://github.com/aethons-tools/cove/blob/main/go.mod#L1[e]')
  // no modules: one ROOT module keeps the subdirectories
  assert.equal(body({ repoRoot: fixture }).split(' ').slice(0, 3).join(' '), 'xref:sample.adoc#x[a] xref:other.adoc[b] xref:usage/u.adoc[c]')
  assert.match(body({ repoRoot: fixture }), /xref:superpowers\/s\.adoc\[d\]/) // not kept: converted too
  // a plain AsciiDoc tree: xrefs relative to the page
  assert.equal(body({ repoRoot: fixture, plain: true }).split(' ').slice(0, 3).join(' '), 'xref:../sample.adoc#x[a] xref:../other.adoc[b] xref:u.adoc[c]')
  assert.match(body({ ...layout, repoUrl: 'https://example.com/r/blob/goal/' }), /https:\/\/example\.com\/r\/blob\/goal\/go\.mod#L1\[e\]/)
})

test('outPathOf places a doc in the layout', () => {
  const l = layoutOf(layout)
  assert.equal(outPathOf('usage/jam/x.md', l, 'out'), path.join('out', 'modules', 'usage', 'pages', 'jam', 'x.adoc'))
  assert.equal(outPathOf('OVERVIEW.md', l, 'out'), path.join('out', 'modules', 'ROOT', 'pages', 'OVERVIEW.adoc'))
  assert.equal(outPathOf('usage/x.md', layoutOf({ plain: true }), 'out'), path.join('out', 'usage', 'x.adoc'))
})

test('what it cannot convert faithfully, it reports', () => {
  const { warnings } = convert('# One\n\n# Two\n\n#### Deep\n\nSee [x][ref].\n\n[ref]: https://e.com\n')
  assert.deepEqual(warnings, [
    'reference-style link left as text: [x][ref]',
    'link reference definition left as text: [ref]: https://e.com',
    'a second level-1 heading became a section: Two',
    'heading skips a level (AsciiDoc warns): Deep',
  ])
  assert.deepEqual(convert('text\n').warnings, ['no level-1 heading: the document has no title'])
})

test('text AsciiDoc would read as markup stays text', () => {
  const md = '# T\n\nA line\n. that starts with a dot\n[and one in brackets]\nNOTE: not an admonition\n2024. was a year\n\n3. starts at three\n'
  const { adoc } = convert(md)
  const { html, log } = render(adoc)
  assert.deepEqual(log, [])
  assert.ok(html.includes('. that starts with a dot'))
  assert.ok(html.includes('[and one in brackets]'))
  assert.ok(html.includes('NOTE: not an admonition'))
  assert.ok(html.includes('2024. was a year'))
  assert.ok(html.includes('<ol class="arabic" start="3">'))
})

test('the CLI writes into the layout under --out-dir and rejects bad usage', () => {
  const out = fs.mkdtempSync(path.join(os.tmpdir(), 'docs-to-adoc-'))
  execFileSync(process.execPath, [script, '--repo-root', fixture, '--module', 'usage=usage', '--out-dir', out,
    path.join(docs, 'usage', 'u.md'), path.join(docs, 'other.md')], { stdio: 'pipe' })
  const u = fs.readFileSync(path.join(out, 'modules', 'usage', 'pages', 'u.adoc'), 'utf8')
  assert.match(u, /^= Usage\n:summary: A usage doc\./)
  assert.match(u, /xref:ROOT:sample\.adoc\[the sample\]/)
  assert.ok(fs.existsSync(path.join(out, 'modules', 'ROOT', 'pages', 'other.adoc')))

  const bad = spawnSync(process.execPath, [script, '--nope', 'x.md'], { encoding: 'utf8' })
  assert.equal(bad.status, 1)
  assert.match(bad.stderr, /unknown option --nope/)
  const two = spawnSync(process.execPath, [script, '-o', 'x.adoc', 'a.md', 'b.md'], { encoding: 'utf8' })
  assert.equal(two.status, 1)
})

test('a converted tree passes the docs-audit checker', () => {
  const audit = path.join(__dirname, '..', '..', '..', 'images', 'jam-studio-base', 'image-files', 'home', 'agent',
    '.init-agent-data', 'skills', 'docs-audit', 'scripts', 'docs_audit.py')
  const out = fs.mkdtempSync(path.join(os.tmpdir(), 'docs-to-adoc-audit-'))
  fs.writeFileSync(path.join(out, 'antora.yml'), 'name: architecture\nversion: ~\n')
  fs.cpSync(path.join(docs, 'superpowers'), path.join(out, 'superpowers'), { recursive: true })
  execFileSync(process.execPath, [script, '--repo-root', fixture, '--module', 'usage=usage', '--keep-md', 'superpowers/**',
    '--out-dir', out, ...['sample.md', 'other.md', 'usage/u.md'].map((f) => path.join(docs, f))], { stdio: 'pipe' })
  const res = spawnSync('python3', [audit, out, '--index', 'modules/ROOT/pages/sample.adoc', '--exclude', 'superpowers/*'], { encoding: 'utf8' })
  assert.equal(res.status, 0, res.stdout + res.stderr)
  assert.match(res.stdout, /3 docs checked — 0 error\(s\)/)
})
