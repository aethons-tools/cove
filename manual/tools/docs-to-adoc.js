#!/usr/bin/env node
// docs-to-adoc: convert one of the repo's Markdown docs to AsciiDoc, for the
// architecture book (docs/modules/ROOT/pages/docs-asciidoc.adoc).
//
// Line-based on purpose, not a Markdown AST: prose keeps its line breaks
// (semantic line breaks survive), and only the GFM subset our docs use is
// handled. What it can't convert faithfully it reports on stderr, so a
// converted page is reviewed rendered, never trusted blindly.
//
//   front matter     → header attributes after the title (:summary: … :updated:)
//   # Title          → = Title; ## → ==, …; a heading another doc links to by
//                      its GitHub anchor keeps that anchor as an explicit [#id]
//   lists (nested)   → * / . items, deeper levels repeat the marker, blocks in
//                      an item attach with +
//   pipe tables      → [%header] |=== tables (alignment kept in cols)
//   ```lang fences   → [source,lang] ---- blocks
//   > quotes         → ____ blocks; > **Note:** / > [!NOTE] → NOTE: admonitions
//   `code`           → `code`, or a literal `+code+` when AsciiDoc would format it
//   **b** *i* _i_    → *b* _i_ (doubled when inside a word)
//   [t](x.md#a)      → xref to x's page in the target layout (below)
//   [t](https://u)   → https://u[t]; ![alt](p) → image:p[alt]
//
// The target layout is a parameter. By default docs/ is an Antora component
// and a doc at <prefix>/<path>.md lands in module ROOT at <prefix>/<path>.adoc;
// `--module usage=usage` maps docs/usage/** to module `usage` (the prefix is
// dropped from the page path). `--plain` targets a plain AsciiDoc tree instead
// (xrefs relative to the file). Links to targets that stay Markdown
// (`--keep-md <glob>`, e.g. superpowers/**) or leave the docs root become links
// to the file on GitHub (`--repo-url`).
//
// Usage: node manual/tools/docs-to-adoc.js [options] <doc.md>...
//   --docs-root DIR        the docs tree (default: docs)
//   --repo-root DIR        the repository root (default: .)
//   --repo-url URL         base of links to repo files
//                          (default: https://github.com/aethons-tools/cove/blob/main)
//   --module PREFIX=NAME   docs under PREFIX/ go to Antora module NAME (repeatable)
//   --plain                plain AsciiDoc tree: no modules, relative xrefs
//   --keep-md GLOB         docs-root-relative targets that stay Markdown (repeatable)
//   -o FILE                write the (single) result to FILE
//   --out-dir DIR          write each result to its place in the layout under DIR
//                          (DIR/modules/<module>/pages/<page>.adoc, or DIR/<path>.adoc)
// Without -o/--out-dir the AsciiDoc goes to stdout. Exit 1 on a usage error.
'use strict'

const fs = require('node:fs')
const path = require('node:path')

const DEFAULT_REPO_URL = 'https://github.com/aethons-tools/cove/blob/main'

// ---------- layout ----------

// globRe compiles a docs-root-relative glob (* within a segment, ** across).
function globRe (glob) {
  let re = ''
  for (let i = 0; i < glob.length; i++) {
    const c = glob[i]
    if (c === '*' && glob[i + 1] === '*') {
      re += '.*'
      i++
      if (glob[i + 1] === '/') i++
    } else if (c === '*') re += '[^/]*'
    else if (c === '?') re += '[^/]'
    else re += c.replace(/[.+^${}()|[\]\\]/g, '\\$&')
  }
  return new RegExp(`^${re}$`)
}

function posix (p) {
  return p.split(path.sep).join('/')
}

// layoutOf resolves the options into the layout the links target.
function layoutOf (opts = {}) {
  const repoRoot = path.resolve(opts.repoRoot || '.')
  return {
    repoRoot,
    docsRoot: path.resolve(repoRoot, opts.docsRoot || 'docs'),
    repoUrl: (opts.repoUrl || DEFAULT_REPO_URL).replace(/\/+$/, ''),
    plain: !!opts.plain,
    modules: Object.entries(opts.modules || {})
      .map(([prefix, name]) => ({ prefix: prefix.replace(/\/+$/, ''), name }))
      .sort((a, b) => b.prefix.length - a.prefix.length),
    keepMd: (opts.keepMd || []).map(globRe),
  }
}

// pageOf places a docs-root-relative .md path in the layout.
function pageOf (rel, layout) {
  const adoc = rel.replace(/\.md$/, '.adoc')
  if (layout.plain) return { module: null, page: adoc }
  for (const m of layout.modules) {
    if (adoc.startsWith(m.prefix + '/')) return { module: m.name, page: adoc.slice(m.prefix.length + 1) }
  }
  return { module: 'ROOT', page: adoc }
}

// outPathOf is where --out-dir writes a converted doc.
function outPathOf (rel, layout, outDir) {
  const { module, page } = pageOf(rel, layout)
  return module === null ? path.join(outDir, page) : path.join(outDir, 'modules', module, 'pages', page)
}

// ---------- anchors ----------

// githubSlug is a heading's anchor as our .md links spell it: as
// docs_audit.py's slugify does (runs of whitespace collapsed), or exactly as
// GitHub does (collapse false: every space a hyphen).
function githubSlug (text, collapse = true) {
  return plainText(text).trim().toLowerCase()
    .replace(/[^\p{L}\p{N}\s_-]/gu, '')
    .replace(collapse ? /\s+/g : /\s/g, '-')
}

// plainText strips the inline Markdown a heading's anchor ignores.
function plainText (md) {
  return md
    .replace(/!?\[([^\]]*)\]\([^)]*\)/g, '$1')
    .replace(/\\(.)/g, '$1')
}

// inboundAnchors finds the anchors other docs (and the doc itself) use to link
// into srcPath, so the converted headings can keep them.
function inboundAnchors (srcPath, layout) {
  const want = path.resolve(srcPath)
  const anchors = new Set()
  const files = []
  const walk = (dir) => {
    for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
      const p = path.join(dir, e.name)
      if (e.isDirectory()) walk(p)
      else if (e.name.endsWith('.md')) files.push(p)
    }
  }
  if (fs.existsSync(layout.docsRoot)) walk(layout.docsRoot)
  for (const f of files) {
    const text = stripFences(fs.readFileSync(f, 'utf8'))
    for (const m of text.matchAll(/\]\(([^)\s]*)#([^)\s]+)\)/g)) {
      const target = m[1] === '' ? f : path.resolve(path.dirname(f), m[1])
      if (target === want) anchors.add(m[2])
    }
  }
  return anchors
}

function stripFences (text) {
  let fence = null
  return text.split('\n').filter((line) => {
    const m = line.match(/^\s*(`{3,}|~{3,})/)
    if (m && (!fence || m[1].startsWith(fence))) {
      fence = fence ? null : m[1]
      return false
    }
    return !fence
  }).join('\n')
}

// ---------- front matter ----------

function unquote (v) {
  if (/^".*"$/.test(v)) return v.slice(1, -1).replace(/\\(["\\])/g, '$1')
  if (/^'.*'$/.test(v)) return v.slice(1, -1).replace(/''/g, "'")
  return v
}

// frontMatter splits YAML front matter (flat key: value, as our docs use) off
// the document.
function frontMatter (lines, warn) {
  if (lines[0] !== '---') return { attrs: [], rest: lines }
  const end = lines.indexOf('---', 1)
  if (end < 0) {
    warn('front matter is not closed; left as text')
    return { attrs: [], rest: lines }
  }
  const attrs = []
  for (const line of lines.slice(1, end)) {
    const m = line.match(/^([A-Za-z0-9_][\w-]*):\s*(.*)$/)
    if (m) attrs.push([m[1], unquote(m[2].trim())])
    else if (/^\s+\S/.test(line) && attrs.length) attrs[attrs.length - 1][1] += ' ' + line.trim()
    else if (line.trim()) warn(`front matter line not understood: ${line}`)
  }
  return { attrs, rest: lines.slice(end + 1) }
}

// ---------- inline ----------

// codeSpan renders inline code so AsciiDoc doesn't format inside it: plain
// backticks when the content is inert, else a literal passthrough.
function codeSpan (code, before, after) {
  const inert = /^[A-Za-z0-9 .,/@=%!?()&;$"|<>-]*$/.test(code) && !/--|->|<-|=>|<=|\.\.\.|<<|^\s|\s$/.test(code)
  // single backticks only between spaces or plain punctuation; a quote
  // beside them would also make a curved quote, so {empty} parts them
  const tight = /[\p{L}\p{N}_'";:}`*]/u.test(before) || /[\p{L}\p{N}_'";:`*]/u.test(after)
  const tick = tight ? '``' : '`'
  const pre = /['"]/.test(before) ? '{empty}' : ''
  const post = after === '"' ? '{empty}' : ''
  if (inert) return pre + tick + code + tick + post
  if (!code.includes('+') && !/^\s|\s$/.test(code)) return `${pre}${tick}+${code}+${tick}${post}`
  return `${pre}${tick}pass:c[${code.replace(/]/g, '\\]')}]${tick}${post}`
}

// inline converts a run of Markdown text (a paragraph, a cell, a heading).
function inline (text, ctx, held = []) {
  const hold = (s) => `\uE000${held.push(s) - 1}\uE001`
  const restore = (s) => s.replace(/\uE000(\d+)\uE001/g, (_, i) => restore(held[+i]))

  // code spans first: nothing inside them is Markdown
  text = text.replace(/(`+)([\s\S]*?[^`])\1(?!`)/g, (m, ticks, code, off, all) => {
    let c = code.replace(/\n/g, ' ')
    if (/^ .* $/.test(c) && c.trim()) c = c.slice(1, -1)
    return hold(codeSpan(c, all[off - 1] || '', all[off + m.length] || ''))
  })
  // backslash escapes: the character itself, kept away from the passes below
  text = text.replace(/\\([!-/:-@[-`{-~])/g, (_, c) => hold(/[*_`#^~+[\]{}<>|']/.test(c) ? `&#${c.charCodeAt(0)};` : c))
  // HTML anchors
  text = text.replace(/<a\s+(?:id|name)="([^"]+)"\s*><\/a>/g, (_, id) => hold(`[[${id}]]`))
  // {name} is an attribute reference in AsciiDoc, text in Markdown
  text = text.replace(/\{[\w-]+\}/g, (m) => hold(`\\${m}`))
  // autolinks and bare URLs: no emphasis inside them
  text = text.replace(/<((?:https?|mailto):[^>\s]+)>/g, (_, u) => hold(u))
  text = text.replace(/(?<![(\w])https?:\/\/[^\s<>()[\]`]+[^\s<>()[\]`.,;:!?'"]/g, (u) => hold(u))
  // images, then links (their text is converted too)
  text = text.replace(/!\[([^\]]*)\]\(\s*([^)\s]+)(?:\s+"[^"]*")?\s*\)/g, (_, alt, src) => {
    if (ctx.layout && !ctx.layout.plain && !/^[a-z]+:/.test(src)) ctx.warn(`image ${src}: an Antora page's images live in its module's images/ — move it and fix the target`)
    return hold(`image:${src}[${alt.replace(/]/g, '\\]')}]`)
  })
  text = text.replace(/\[((?:[^\][]|\[[^\]]*\])*)\]\(\s*([^)\s]*)(?:\s+"[^"]*")?\s*\)/g, (_, label, href) =>
    hold(link(inline(label, ctx, held), href, ctx)))
  text = text.replace(/\[([^\]]+)\]\[([^\]]*)\]/g, (m) => {
    ctx.warn(`reference-style link left as text: ${m}`)
    return m
  })

  // emphasis, marked with sentinels and given its AsciiDoc form at the end
  const S = '\uE002'
  const E = '\uE003'
  text = text.replace(/\*\*(?=\S)([\s\S]*?\S)\*\*/g, `${S}$1${S}`)
  text = text.replace(/(?<![\p{L}\p{N}\\])__(?=\S)([\s\S]*?\S)__(?![\p{L}\p{N}])/gu, `${S}$1${S}`)
  text = text.replace(/(?<![*\\])\*([^\s*](?:[\s\S]*?[^\s*\\])??)\*(?!\*)/g, `${E}$1${E}`)
  text = text.replace(/(?<![\p{L}\p{N}_\\])_([^\s_](?:[\s\S]*?[^\s_\\])??)_(?![\p{L}\p{N}_])/gu, `${E}$1${E}`)
  const word = /[\p{L}\p{N}_]/u
  // a pair is doubled (unconstrained) when either end touches a word
  const pair = new RegExp(`${S}([^${S}]*)${S}|${E}([^${E}]*)${E}`, 'g')
  while (pair.test(text)) {
    text = text.replace(pair, (m, b, it, off, all) => {
      const ch = b !== undefined ? '*' : '_'
      const tight = word.test(all[off - 1] || '') || word.test(all[off + m.length] || '')
      const mark = tight ? ch + ch : ch
      return mark + (b !== undefined ? b : it) + mark
    })
  }

  // hard line breaks
  text = text.replace(/(?: {2,}|\\)\n/g, ' +\n').replace(/[ \t]+\n/g, '\n')
  return restore(text)
}

// link renders a Markdown link in the target layout.
function link (label, href, ctx) {
  // a URL macro's text that looks like name=value would be read as attributes
  const named = /^\s*[\w-]+\s*=|,\s*[\w-]+\s*=/.test(label)
  const text = named && /^[a-z][a-z0-9+.-]*:/i.test(href) ? `"${label.replace(/"/g, '\\"')}"` : label.replace(/]/g, '\\]')
  if (/^[a-z][a-z0-9+.-]*:/i.test(href)) return label === href ? href : `${href}[${text}]`
  const [target, frag] = href.split('#', 2)
  const hash = frag ? `#${frag}` : ''
  if (target === '') return label ? `<<${frag},${label}>>` : `<<${frag}>>`
  const { layout, srcPath } = ctx
  if (!layout || !srcPath) return `link:${href}[${text}]`
  const abs = target.startsWith('/') ? path.join(layout.repoRoot, target) : path.resolve(path.dirname(srcPath), target)
  const rel = posix(path.relative(layout.docsRoot, abs))
  const inDocs = !rel.startsWith('..') && !path.isAbsolute(rel)
  if (inDocs && rel.endsWith('.md') && !layout.keepMd.some((re) => re.test(rel))) {
    if (layout.plain) {
      const from = path.dirname(posix(path.relative(layout.docsRoot, srcPath)))
      return `xref:${path.posix.relative(from, rel.replace(/\.md$/, '.adoc'))}${hash}[${text}]`
    }
    const here = pageOf(posix(path.relative(layout.docsRoot, srcPath)), layout)
    const there = pageOf(rel, layout)
    const id = there.module === here.module ? there.page : `${there.module}:${there.page}`
    return `xref:${id}${hash}[${text}]`
  }
  const repoRel = posix(path.relative(layout.repoRoot, abs))
  if (repoRel.startsWith('..')) {
    ctx.warn(`link outside the repository left relative: ${href}`)
    return `link:${href}[${text}]`
  }
  return `${layout.repoUrl}/${repoRel}${hash}[${text}]`
}

// ---------- blocks ----------

const FENCE = /^(\s*)(`{3,}|~{3,})\s*([^\s`]*)[^`]*$/
const HEADING = /^ {0,3}(#{1,6})\s+(.*?)(?:\s+#+)?\s*$/
const HR = /^ {0,3}(?:(?:\*\s*){3,}|(?:-\s*){3,}|(?:_\s*){3,})$/
const ITEM = /^(\s*)([-*+]|\d{1,9}[.)])(\s+)(.*)$/
const QUOTE = /^ {0,3}>/
const TABLE_DELIM = /^\s*\|?\s*:?-+:?\s*(?:\|\s*:?-+:?\s*)*\|?\s*$/
const ADMONITIONS = { note: 'NOTE', tip: 'TIP', important: 'IMPORTANT', warning: 'WARNING', caution: 'CAUTION' }

// a line AsciiDoc would read as block syntax where Markdown means text
const ADOC_BLOCKISH = /^(?:[*.]+\s|-\s|\d+\.\s|[a-zA-Z]\.\s|\[.*\]$|\.[^\s.]|:[\w-]+:|\/\/|=+\s|(?:-{4,}|\.{4,}|={4,}|\*{4,}|_{4,}|\+{4,}|\/{4,}|\|===)$|\+$|'''|<<<|(?:NOTE|TIP|IMPORTANT|WARNING|CAUTION):\s|.*\S::(?:\s|$)|\|)/

function indentOf (line) {
  return line.match(/^\s*/)[0].replace(/\t/g, '    ').length
}

function dedent (line, n) {
  let i = 0
  let col = 0
  while (i < line.length && col < n && (line[i] === ' ' || line[i] === '\t')) {
    col += line[i] === '\t' ? 4 : 1
    i++
  }
  return line.slice(i)
}

function startsBlock (line, next) {
  return FENCE.test(line) || HEADING.test(line) || HR.test(line) || QUOTE.test(line) ||
    /^\s*([-*+]|1[.)])\s+\S/.test(line) || /^\s*<!--/.test(line) ||
    (line.includes('|') && next !== undefined && TABLE_DELIM.test(next) && next.includes('-'))
}

function splitRow (line) {
  let s = line.trim()
  if (s.startsWith('|')) s = s.slice(1)
  if (s.endsWith('|') && !s.endsWith('\\|')) s = s.slice(0, -1)
  const cells = []
  let cur = ''
  for (let i = 0; i < s.length; i++) {
    if (s[i] === '\\' && s[i + 1] === '|') {
      cur += '|'
      i++
    } else if (s[i] === '|') {
      cells.push(cur.trim())
      cur = ''
    } else cur += s[i]
  }
  cells.push(cur.trim())
  return cells
}

// paragraph converts text lines, keeping their breaks.
function paragraph (lines, ctx) {
  const text = lines.map((l) => l.trimStart()).join('\n')
  const img = text.trim().match(/^!\[([^\]]*)\]\(\s*([^)\s]+)(?:\s+"[^"]*")?\s*\)$/)
  if (img) return [inline(text.trim(), ctx).replace(/^image:/, 'image::')]
  const out = inline(text.replace(/[ \t]+$/, ''), ctx).split('\n')
  return out.map((l) => (ADOC_BLOCKISH.test(l) ? `{empty}${l}` : l))
}

// blocks converts Markdown lines into a list of AsciiDoc blocks
// ({ type, lines }); depth is the enclosing list depth.
function blocks (lines, ctx, depth = 0) {
  const out = []
  let i = 0
  while (i < lines.length) {
    const line = lines[i]
    if (!line.trim()) {
      i++
      continue
    }
    let m
    if ((m = line.match(FENCE))) {
      const [, ind, fence, lang] = m
      const body = []
      i++
      while (i < lines.length && !new RegExp(`^\\s*${fence[0]}{${fence.length},}\\s*$`).test(lines[i])) body.push(dedent(lines[i++], ind.length))
      i++
      let delim = '----'
      while (body.some((b) => b.trim() === delim)) delim += '-'
      out.push({ type: 'code', lines: [...(lang ? [`[source,${lang}]`] : []), delim, ...body, delim] })
      continue
    }
    if (/^\s*<!--/.test(line)) {
      const body = []
      while (i < lines.length) {
        body.push(lines[i])
        if (lines[i++].includes('-->')) break
      }
      const text = body.join('\n').replace(/^\s*<!--\s?/, '').replace(/\s?-->\s*$/, '')
      out.push({ type: 'comment', lines: ['////', ...text.split('\n'), '////'] })
      continue
    }
    if ((m = line.match(HEADING))) {
      out.push({ type: 'heading', level: m[1].length, text: m[2] })
      i++
      continue
    }
    if (HR.test(line)) {
      out.push({ type: 'hr', lines: ["'''"] })
      i++
      continue
    }
    if (QUOTE.test(line)) {
      const body = []
      while (i < lines.length && lines[i].trim() && (QUOTE.test(lines[i]) || !startsBlock(lines[i], lines[i + 1]))) {
        body.push(lines[i++].replace(/^ {0,3}> ?/, ''))
      }
      out.push(quote(body, ctx, depth))
      continue
    }
    if (line.includes('|') && TABLE_DELIM.test(lines[i + 1] || '') && (lines[i + 1] || '').includes('-')) {
      const head = splitRow(line)
      const aligns = splitRow(lines[i + 1]).map((c) => (c.startsWith(':') && c.endsWith(':') ? '^' : c.endsWith(':') ? '>' : '<'))
      i += 2
      const rows = []
      while (i < lines.length && lines[i].trim() && lines[i].includes('|')) rows.push(splitRow(lines[i++]))
      const cell = (c) => inline(c, ctx).replace(/\|/g, '\\|')
      const row = (cells) => head.map((_, k) => `|${cell(cells[k] || '')}`).join(' ')
      const cols = aligns.some((a) => a !== '<') ? `,cols="${aligns.join(',')}"` : ''
      out.push({ type: 'table', lines: [`[%header${cols}]`, '|===', row(head), '', ...rows.map(row), '|==='] })
      continue
    }
    if ((m = line.match(ITEM))) {
      const res = list(lines, i, ctx, depth)
      out.push(res.block)
      i = res.end
      continue
    }
    if (/^\[[^\]]+\]:\s/.test(line)) ctx.warn(`link reference definition left as text: ${line.trim()}`)
    if (/^\s*<\/?[a-z][^>]*>\s*$/i.test(line)) ctx.warn(`raw HTML left as text: ${line.trim()}`)
    const para = []
    while (i < lines.length && lines[i].trim() && !(para.length && startsBlock(lines[i], lines[i + 1]))) {
      if (para.length && /^\s*(=+|-+)\s*$/.test(lines[i])) ctx.warn('setext heading treated as text')
      para.push(lines[i++])
    }
    out.push({ type: 'para', lines: paragraph(para, ctx) })
  }
  return out
}

// quote turns a blockquote body into a quote block, or an admonition when it
// opens with **Note:** (any ADMONITIONS key) or GitHub's [!NOTE].
function quote (body, ctx, depth) {
  let kind = null
  const first = body[0] || ''
  let m
  if ((m = first.match(/^\s*\[!([A-Za-z]+)\]\s*$/)) && ADMONITIONS[m[1].toLowerCase()]) {
    kind = ADMONITIONS[m[1].toLowerCase()]
    body = body.slice(1)
  } else if ((m = first.match(/^\s*\*\*([A-Za-z]+):?\*\*:?\s*(.*)$/)) && ADMONITIONS[m[1].toLowerCase()]) {
    kind = ADMONITIONS[m[1].toLowerCase()]
    body = [m[2], ...body.slice(1)].filter((l, k) => k > 0 || l.trim())
  }
  const bs = blocks(body, ctx, depth)
  const inner = render(bs)
  if (kind && bs.length === 1 && bs[0].type === 'para') {
    return { type: 'admonition', lines: [`${kind}: ${inner[0]}`, ...inner.slice(1)] }
  }
  return { type: 'quote', lines: [...(kind ? [`[${kind}]`, '===='] : ['____']), ...inner, kind ? '====' : '____'] }
}

// list parses the list starting at lines[start].
function list (lines, start, ctx, depth) {
  const first = lines[start].match(ITEM)
  const base = indentOf(lines[start])
  const ordered = /\d/.test(first[2])
  const marker = (ordered ? '.' : '*').repeat(depth + 1)
  // Markdown numbers every ordered list 1, 2, 3 from its first number;
  // AsciiDoc letters nested ones unless told otherwise
  const from = ordered ? parseInt(first[2], 10) : 1
  const style = [...(ordered && depth ? ['arabic'] : []), ...(from !== 1 ? [`start=${from}`] : [])]
  const out = style.length ? [`[${style.join(',')}]`] : []
  let i = start
  while (i < lines.length) {
    const m = lines[i].match(ITEM)
    if (!m || indentOf(lines[i]) !== base || /\d/.test(m[2]) !== ordered) break
    const contentIndent = base + m[2].length + Math.min(m[3].length, 4)
    const body = [m[4]]
    i++
    let blank = false
    while (i < lines.length) {
      const l = lines[i]
      if (!l.trim()) {
        blank = true
        body.push('')
        i++
        continue
      }
      if (indentOf(l) >= contentIndent || (ITEM.test(l) && indentOf(l) > base)) {
        body.push(dedent(l, Math.min(contentIndent, indentOf(l))))
        blank = false
        i++
        continue
      }
      if (!blank && !startsBlock(l, lines[i + 1]) && !ITEM.test(l)) {
        body.push(l.trim()) // lazy continuation
        i++
        continue
      }
      break
    }
    while (body.length && !body[body.length - 1].trim()) body.pop()
    const parts = blocks(body, ctx, depth + 1)
    const head = parts[0] && parts[0].type === 'para' ? parts.shift().lines : ['{empty}']
    out.push(`${marker} ${head[0]}`, ...head.slice(1))
    for (const p of parts) {
      if (p.type === 'list') out.push(...p.lines)
      else out.push('+', ...renderBlock(p))
    }
    // a blank line between items unless the next line is another of ours
    if (i < lines.length && !lines[i].trim()) {
      let j = i
      while (j < lines.length && !lines[j].trim()) j++
      const n = lines[j] && lines[j].match(ITEM)
      if (n && indentOf(lines[j]) === base && /\d/.test(n[2]) === ordered) i = j
    }
  }
  return { block: { type: 'list', lines: out }, end: i }
}

function renderBlock (b) {
  if (b.type === 'heading') return [`${'='.repeat(b.level)} ${b.text}`]
  return b.lines
}

// render joins blocks with blank lines (an empty comment keeps two lists apart).
function render (bs) {
  const out = []
  bs.forEach((b, k) => {
    if (k) out.push('')
    if (k && b.type === 'list' && bs[k - 1].type === 'list') out.push('//', '')
    out.push(...renderBlock(b))
  })
  return out
}

// ---------- document ----------

// convert turns a Markdown doc into AsciiDoc. opts: srcPath (the doc's path,
// for links and inbound anchors), the layout options (see the header), and
// anchors (a Set to use instead of scanning the docs root). Returns
// { adoc, warnings }.
function convert (md, opts = {}) {
  const warnings = []
  const warn = (w) => warnings.push(w)
  const layout = opts.srcPath ? layoutOf(opts) : null
  const srcPath = opts.srcPath ? path.resolve(opts.srcPath) : null
  const ctx = { layout, srcPath, warn }
  const anchors = opts.anchors || (layout ? inboundAnchors(srcPath, layout) : new Set())

  const { attrs, rest } = frontMatter(md.replace(/\r\n/g, '\n').split('\n'), warn)
  const bs = blocks(rest, ctx)
  let title = null
  let prev = 1
  const used = new Map()
  for (const b of bs) {
    if (b.type !== 'heading') continue
    let slug = githubSlug(b.text)
    let gh = githubSlug(b.text, false)
    const n = used.get(slug) || 0
    used.set(slug, n + 1)
    if (n) {
      slug += `-${n}`
      gh += `-${n}`
    }
    if (!anchors.has(slug) && anchors.has(gh)) slug = gh
    const text = inline(b.text, ctx)
    if (title === null && b.level === 1) {
      title = text
      b.drop = true
      continue
    }
    if (b.level === 1) warn(`a second level-1 heading became a section: ${b.text}`)
    const level = Math.max(b.level, 2)
    if (level > prev + 1) warn(`heading skips a level (AsciiDoc warns): ${b.text}`)
    prev = level
    b.lines = [...(anchors.has(slug) ? [`[#${slug}]`] : []), `${'='.repeat(level)} ${text}`]
    b.type = 'section'
    anchors.delete(slug)
  }
  for (const a of anchors) warn(`a link uses anchor #${a}, but no heading has it`)
  if (title === null) warn('no level-1 heading: the document has no title')

  const head = [...(title !== null ? [`= ${title}`] : []), ...attrs.map(([k, v]) => (v ? `:${k}: ${v.replace(/\{[\w-]+\}/g, '\\$&')}` : `:${k}:`))]
  const body = render(bs.filter((b) => !b.drop))
  const adoc = [...head, ...(head.length && body.length ? [''] : []), ...body].join('\n') + '\n'
  return { adoc, warnings }
}

// ---------- CLI ----------

function main (argv) {
  const opts = { modules: {}, keepMd: [] }
  const files = []
  let out = null
  let outDir = null
  for (let k = 0; k < argv.length; k++) {
    const a = argv[k]
    const val = () => {
      if (k + 1 >= argv.length) throw new Error(`${a} needs a value`)
      return argv[++k]
    }
    if (a === '--docs-root') opts.docsRoot = val()
    else if (a === '--repo-root') opts.repoRoot = val()
    else if (a === '--repo-url') opts.repoUrl = val()
    else if (a === '--plain') opts.plain = true
    else if (a === '--keep-md') opts.keepMd.push(val())
    else if (a === '--module') {
      const [prefix, name] = val().split('=')
      if (!prefix || !name) throw new Error('--module needs PREFIX=NAME')
      opts.modules[prefix] = name
    } else if (a === '-o') out = val()
    else if (a === '--out-dir') outDir = val()
    else if (a === '-h' || a === '--help') {
      process.stdout.write(fs.readFileSync(__filename, 'utf8').split('\n').filter((l) => l.startsWith('//')).map((l) => l.slice(3)).join('\n') + '\n')
      return 0
    } else if (a.startsWith('-')) throw new Error(`unknown option ${a}`)
    else files.push(a)
  }
  if (!files.length) throw new Error('no input: give one or more .md docs')
  if (out && files.length > 1) throw new Error('-o takes a single input; use --out-dir')
  const layout = layoutOf(opts)
  let warned = 0
  for (const f of files) {
    const { adoc, warnings } = convert(fs.readFileSync(f, 'utf8'), { ...opts, srcPath: f })
    for (const w of warnings) process.stderr.write(`${f}: warning: ${w}\n`)
    warned += warnings.length
    if (outDir) {
      const dest = outPathOf(posix(path.relative(layout.docsRoot, path.resolve(f))), layout, outDir)
      fs.mkdirSync(path.dirname(dest), { recursive: true })
      fs.writeFileSync(dest, adoc)
      process.stderr.write(`${f} -> ${dest}\n`)
    } else if (out) fs.writeFileSync(out, adoc)
    else process.stdout.write(adoc)
  }
  if (warned) process.stderr.write(`${warned} warning(s): review the rendered pages\n`)
  return 0
}

if (require.main === module) {
  try {
    process.exitCode = main(process.argv.slice(2))
  } catch (e) {
    process.stderr.write(`docs-to-adoc: ${e.message}\n`)
    process.exitCode = 1
  }
}

module.exports = { convert, githubSlug, layoutOf, pageOf, outPathOf, main }
