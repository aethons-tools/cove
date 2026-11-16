// Antora extension: Mermaid diagrams as SVG image files (docs/modules/ROOT/pages/manual-diagrams.adoc).
//
// Each `[mermaid,alt="…"]` listing block becomes an image block (keeping the
// block's id, role and title) that shows an SVG file rendered by mmdc
// (@mermaid-js/mermaid-cli, pinned in manual/package.json). Jam's CSP for its
// manual blocks inline styles, and Mermaid styles its SVG with one, so the SVG
// is an image file, never inline. `alt` is required: it is the image's alt
// text and the SVG's <title>. The file is added to the page's module as
// `_diagrams/<hash>.svg`; in the customer build internal.js drops it, as any
// image, when no published page shows it (an internal page or block).
//
// mmdc runs with $JAM_BROWSER, else the kit's /opt/chrome-headless-shell
// (scripts/manual-browser.sh provides one elsewhere). Renders are cached in
// `cache_dir` (default build/diagrams, relative to the playbook), keyed by the
// source and mmdc's version. A block without alt, or one mmdc fails to
// render, fails the build with its page and line.
//
// Playbook (every one, the customer one included):
//   antora:
//     extensions:
//     - require: ./extensions/diagrams.js
//       cache_dir: build/diagrams   # optional
//       mmdc: <path>                # optional, another mmdc (the tests' fake)
'use strict'

const { execFileSync } = require('node:child_process')
const crypto = require('node:crypto')
const fs = require('node:fs')
const os = require('node:os')
const path = require('node:path')

const KIT_BROWSER = '/opt/chrome-headless-shell/chrome-headless-shell'
const MMDC = path.join(__dirname, '..', 'node_modules', '@mermaid-js', 'mermaid-cli', 'src', 'cli.js')

function register ({ config, playbook }) {
  const cacheDir = path.resolve(playbook.dir, config.cacheDir ?? 'build/diagrams')
  const mmdc = config.mmdc ? path.resolve(playbook.dir, config.mmdc) : MMDC
  const env = playbook.env ?? process.env
  let version
  let puppeteerConfig

  // render returns mmdc's SVG of source, from the cache when it has it.
  function render (source) {
    version ??= execFileSync(process.execPath, [mmdc, '--version']).toString().trim()
    const key = sha256(`${version}\0${source}`)
    const cached = path.join(cacheDir, `${key}.svg`)
    if (fs.existsSync(cached)) return fs.readFileSync(cached, 'utf8')
    puppeteerConfig ??= writePuppeteerConfig(browser(env))
    fs.mkdirSync(cacheDir, { recursive: true })
    const input = path.join(cacheDir, `${key}.${process.pid}.mmd`)
    const output = path.join(cacheDir, `${key}.${process.pid}.tmp.svg`)
    fs.writeFileSync(input, source)
    try {
      execFileSync(process.execPath, [mmdc, '-q', '-i', input, '-o', output, '-p', puppeteerConfig], {
        stdio: ['ignore', 'pipe', 'pipe'],
      })
    } catch (err) {
      const why = (err.stderr?.toString() || err.message).trim()
      throw new Error(`mmdc failed to render the diagram:\n${why}`)
    } finally {
      fs.rmSync(input, { force: true })
    }
    fs.renameSync(output, cached)
    return fs.readFileSync(cached, 'utf8')
  }

  this.once('beforeProcess', ({ siteAsciiDocConfig }) => {
    siteAsciiDocConfig.extensions ??= []
    siteAsciiDocConfig.extensions.push({
      register (registry, { file, contentCatalog }) {
        registry.block('mermaid', function () {
          this.onContext('listing')
          this.parseContentAs('raw')
          this.process((parent, reader, attrs) => {
            const fail = (msg) => {
              throw new Error(`${pageName(file)}, line ${blockLine(parent)}: ${msg}`)
            }
            const { alt } = attrs
            if (!alt) fail('a mermaid block needs alt="…", the image\'s alt text and the SVG\'s title')
            let svg
            try {
              svg = withTitle(render(reader.getLines().join('\n') + '\n'), alt)
            } catch (err) {
              fail(err.message)
            }
            const { component, version, module: module_ } = file.src
            const relative = `_diagrams/${sha256(svg).slice(0, 20)}.svg`
            const src = { component, version, module: module_, family: 'image', relative }
            if (!contentCatalog.getById(src)) {
              contentCatalog.addFile({ contents: Buffer.from(svg), src: { ...src, mediaType: 'image/svg+xml' } })
            }
            const image = { target: relative, alt }
            for (const name of ['id', 'role']) if (attrs[name]) image[name] = attrs[name]
            const block = this.createImageBlock(parent, image)
            // The parser titles only the blocks it builds itself.
            if (attrs.title) {
              block.setTitle(attrs.title)
              block.assignCaption(attrs.caption, 'figure')
            }
            return block
          })
        })
      },
    })
  })
}

// browser returns the headless Chrome mmdc runs: $JAM_BROWSER, else the kit's.
function browser (env) {
  if (env.JAM_BROWSER) return env.JAM_BROWSER
  if (fs.existsSync(KIT_BROWSER)) return KIT_BROWSER
  throw new Error(
    `no headless Chrome to render diagrams: set JAM_BROWSER, or run scripts/manual-browser.sh (no ${KIT_BROWSER})`
  )
}

// writePuppeteerConfig writes the puppeteer config mmdc launches browser with
// to a temp file and returns its path.
function writePuppeteerConfig (browser) {
  const file = path.join(fs.mkdtempSync(path.join(os.tmpdir(), 'manual-diagrams-')), 'puppeteer.json')
  fs.writeFileSync(file, JSON.stringify({ executablePath: browser, headless: 'shell', args: ['--no-sandbox'] }))
  return file
}

// withTitle gives mmdc's svg alt as its <title>, and its size from its viewBox
// in place of width="100%" (an <img> of that has no natural size).
function withTitle (svg, alt) {
  const open = svg.match(/^\s*<svg\b[^>]*>/)
  if (!open) throw new Error('mmdc wrote no SVG')
  let tag = open[0].trim()
  const box = tag.match(/\sviewBox="\s*[-\d.]+[\s,]+[-\d.]+[\s,]+([\d.]+)[\s,]+([\d.]+)\s*"/)
  if (box) {
    tag = tag.replace(/\s(?:width|height)="[^"]*"/g, '').replace(/^<svg/, `<svg width="${box[1]}" height="${box[2]}"`)
  }
  return `${tag}<title>${escapeXml(alt)}</title>${svg.slice(open[0].length)}`
}

// blockLine returns the line of the page the block's opening delimiter is on
// (where the document's reader marked the block's start).
function blockLine (parent) {
  return parent.getDocument().getReader().$cursor_at_mark().getLineNumber()
}

function escapeXml (text) {
  return text.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;')
}

function sha256 (text) {
  return crypto.createHash('sha256').update(text).digest('hex')
}

function pageName (file) {
  const { component, version, module: module_, relative } = file.src
  return `${version ? version + '@' : ''}${component}:${module_}:${relative}`
}

module.exports = { register }
