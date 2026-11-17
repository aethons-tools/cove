// Builds the diagram fixture (diagrams/content) with Antora and checks what
// extensions/diagrams.js publishes for its Mermaid blocks. Hermetic: mmdc is
// diagrams/fake-mmdc.js, which needs no browser; the content is read from
// this repo's working tree and the UI from manual/ui. The last test renders
// with the real mmdc and skips without a headless Chrome. See
// docs/modules/ROOT/pages/manual-diagrams.adoc.
'use strict'

const assert = require('node:assert/strict')
const fs = require('node:fs')
const os = require('node:os')
const path = require('node:path')
const { test } = require('node:test')
const generateSite = require('@antora/site-generator')

const here = path.join(__dirname, 'diagrams')
const manual = path.join(__dirname, '..', '..')
const repo = path.join(manual, '..')
const fakeMmdc = path.join(here, 'fake-mmdc.js')
const kitBrowser = '/opt/chrome-headless-shell/chrome-headless-shell'

// build builds the fixture as the team (or, with customer, as the customer)
// into a fresh temp dir and returns that dir. diagrams.js caches in cacheDir
// and runs mmdc (fake-mmdc.js unless null); env is the build's environment.
async function build ({ customer = false, cacheDir = tmp(), mmdc = fakeMmdc, env = {}, attributes = {} } = {}) {
  const out = tmp()
  const playbook = {
    site: { title: 'Diagrams', start_page: 'diagrams::index.adoc' },
    content: {
      sources: [{ url: repo, start_path: path.relative(repo, path.join(here, 'content')), branches: ['HEAD'] }],
    },
    antora: {
      extensions: [
        { require: path.join(manual, 'extensions', 'internal.js') },
        { require: path.join(manual, 'extensions', 'diagrams.js'), cache_dir: cacheDir, ...(mmdc && { mmdc }) },
      ],
    },
    asciidoc: {
      attributes: { ...(!customer && { internal: '' }), ...attributes },
      extensions: [path.join(manual, 'extensions', 'internal-blocks.js')],
    },
    ui: { bundle: { url: path.join(manual, 'ui'), snapshot: true } },
    runtime: { fetch: false },
  }
  const file = path.join(out, 'antora-playbook.json')
  fs.writeFileSync(file, JSON.stringify(playbook))
  await generateSite(
    ['--playbook', file, '--to-dir', path.join(out, 'site'), '--cache-dir', path.join(out, '.cache'), '--quiet'],
    { JAM_BROWSER: '/fake/chrome', ...env }
  )
  return path.join(out, 'site')
}

function tmp () {
  return fs.mkdtempSync(path.join(os.tmpdir(), 'manual-diagrams-'))
}

// site reads every published file under dir into one { relpath: text } map.
function site (dir) {
  const files = {}
  for (const entry of fs.readdirSync(dir, { recursive: true, withFileTypes: true })) {
    if (!entry.isFile()) continue
    const abs = path.join(entry.parentPath, entry.name)
    files[path.relative(dir, abs)] = fs.readFileSync(abs, 'utf8')
  }
  return files
}

// renders runs fn with $FAKE_MMDC_LOG set and returns what fake-mmdc.js rendered.
async function renders (fn, env = {}) {
  const log = path.join(tmp(), 'mmdc.log')
  const set = { FAKE_MMDC_LOG: log, ...env }
  const saved = Object.fromEntries(Object.keys(set).map((name) => [name, process.env[name]]))
  Object.assign(process.env, set)
  try {
    await fn()
  } finally {
    // Key by key: a process.env replaced whole no longer reaches child processes.
    for (const [name, value] of Object.entries(saved)) {
      if (value === undefined) delete process.env[name]
      else process.env[name] = value
    }
  }
  if (!fs.existsSync(log)) return []
  return fs.readFileSync(log, 'utf8').trim().split('\n').map((line) => JSON.parse(line))
}

// lineOf returns the 1-based line of the fixture page that is text, the first after the line after.
function lineOf (page, text, after) {
  const lines = fs.readFileSync(path.join(here, 'content', 'modules', 'ROOT', 'pages', page), 'utf8').split('\n')
  return lines.indexOf(text, lines.indexOf(after)) + 1
}

const svgs = (files) => Object.keys(files).filter((name) => name.endsWith('.svg'))
const imgSrc = (html) => [...html.matchAll(/<img src="([^"]+\.svg)"/g)].map(([, src]) => src)

test('team build shows each diagram as an SVG image file', async () => {
  let files
  const rendered = await renders(async () => {
    files = site(await build())
  })
  // The diagram on two pages renders once.
  assert.deepEqual(
    rendered.map(({ source }) => source.match(/[A-Z]+-DIAGRAM/)[0]).sort(),
    ['PUBLIC-DIAGRAM', 'SECRET-DIAGRAM', 'TEAM-DIAGRAM'],
    'each distinct source renders once'
  )
  assert.ok(rendered.every(({ browser }) => browser === '/fake/chrome'), 'mmdc runs with $JAM_BROWSER')
  const index = files['diagrams/index.html']
  assert.match(
    index,
    /<div id="flow" class="imageblock wide">\s*<div class="content">\s*<img src="_images\/_diagrams\/[0-9a-f]+\.svg" alt="Start &lt;then&gt; done"[^>]*>\s*<\/div>\s*<div class="title">Figure 1\. The flow<\/div>/,
    'the block becomes an image with its id, role, title and alt'
  )
  assert.doesNotMatch(index, /<svg|mermaid|PUBLIC-DIAGRAM/, 'no inline SVG or Mermaid source on the page')
  assert.match(index, /<div class="imageblock internal">/, 'an internal diagram keeps its role')
  const [src] = imgSrc(index)
  const svg = files[path.join('diagrams', src)]
  assert.ok(svg, `${src} is published`)
  assert.match(svg, /^<svg [^>]*><title>Start &lt;then&gt; done<\/title>/, 'alt is the SVG\'s title')
  assert.match(svg, /^<svg [^>]*\bwidth="120" height="40"/, 'the SVG has its size, not width="100%"')
  assert.match(svg, /PUBLIC-DIAGRAM/)
  // Another alt for the same source: another file, no other render.
  const [againSrc] = imgSrc(files['diagrams/again.html'])
  assert.notEqual(againSrc, src)
  assert.match(files[path.join('diagrams', againSrc)], /<title>The same flow<\/title>/)
  assert.equal(svgs(files).length, 4)
})

test('customer build drops internal diagrams', async () => {
  const files = site(await build({ customer: true }))
  assert.equal(svgs(files).length, 2, 'only the public diagrams are published')
  const all = Object.values(files).join('\n')
  assert.ok(all.includes('PUBLIC-DIAGRAM'))
  for (const secret of ['SECRET-DIAGRAM', 'TEAM-DIAGRAM']) assert.ok(!all.includes(secret), `${secret} is dropped`)
})

test('rendered diagrams are cached by source and mmdc version', async () => {
  const cacheDir = tmp()
  assert.equal((await renders(() => build({ cacheDir }))).length, 3)
  assert.equal(fs.readdirSync(cacheDir).filter((name) => name.endsWith('.svg')).length, 3, 'one file per source')
  assert.equal((await renders(() => build({ cacheDir }))).length, 0, 'a second build renders nothing')
  assert.equal(
    (await renders(() => build({ cacheDir }), { FAKE_MMDC_VERSION: '0.0.1-fake' })).length,
    3,
    'another mmdc version renders again'
  )
})

test('a diagram without alt fails the build with its page and line', async () => {
  await assert.rejects(
    build({ attributes: { noalt: '' } }),
    new RegExp(`diagrams:ROOT:index\\.adoc, line ${lineOf('index.adoc', '----', '[mermaid]')}: a mermaid block needs alt=`)
  )
})

test('a diagram mmdc cannot render fails the build with its page, line and mmdc\'s error', async () => {
  await assert.rejects(
    build({ attributes: { broken: '' } }),
    new RegExp(
      `diagrams:ROOT:index\\.adoc, line ${lineOf('index.adoc', '----', '[mermaid,alt="Broken"]')}: mmdc failed to render the diagram:[\\s\\S]*Parse error on line 2`
    )
  )
})

const browser = process.env.JAM_BROWSER || (fs.existsSync(kitBrowser) && kitBrowser)
test('mmdc renders a diagram with headless Chrome', { skip: !browser && 'no headless Chrome (set JAM_BROWSER)' }, async () => {
  const files = site(await build({ customer: true, mmdc: null, env: { JAM_BROWSER: browser } }))
  const [src] = imgSrc(files['diagrams/index.html'])
  const svg = files[path.join('diagrams', src)]
  assert.match(svg, /^<svg [^>]*><title>Start &lt;then&gt; done<\/title>/)
  assert.match(svg, /PUBLIC-DIAGRAM/)
})
