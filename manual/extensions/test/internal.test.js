// Builds the fixture (fixture/content) with Antora, once as the customer and
// once as the team, and checks what internal content each one publishes.
// Hermetic: the content is read from this repo's working tree and the UI from
// manual/ui; nothing is fetched. See docs/manual.md ("Marking content internal").
'use strict'

const assert = require('node:assert/strict')
const fs = require('node:fs')
const os = require('node:os')
const path = require('node:path')
const { test } = require('node:test')
const generateSite = require('@antora/site-generator')

const fixture = path.join(__dirname, 'fixture')
const secrets = [
  'SECRET-BLOCK', // [.internal] paragraph
  'SECRET-SECTION', // [.internal] section
  'SECRET-INLINE', // [.internal]#…#
  'SECRET-IDSPAN', // [#id.internal]#…#
  'SECRET-STRONG', // [.internal]*…*
  'SECRET-BLOCKTITLE', // .title of an [.internal] block
  'SECRET-EXAMPLE',
  'SECRET-CELL', // [.internal] paragraph in an a| table cell
  'SECRET-TITLE', // [.internal]#…# in a doctitle
  'SECRET-IFDEF', // ifdef::internal[]
  'SECRET-PAGE', // :page-internal:
  'SECRET-MODULE', // a page in an internal module
  'SECRET-NAVTEXT', // a text-only nav entry in an internal module
  'SECRET-NAVROLE', // * [.internal]#…# as a whole nav entry
  'SECRET-COMPONENT-BLOCK', // the component descriptor sets `internal`
  'SECRET-COMPONENT-IFDEF',
  'SECRET-HEADER-BLOCK', // the page header sets `internal`
  'SECRET-HEADER-IFDEF',
]

// build runs the fixture playbook named `which` into a fresh temp dir and
// returns that dir; extra args go to Antora as on its command line.
async function build (which, ...extra) {
  const out = fs.mkdtempSync(path.join(os.tmpdir(), 'manual-internal-'))
  const playbook = path.join(fixture, `antora-playbook.${which}.yml`)
  await generateSite(
    ['--playbook', playbook, '--to-dir', out, '--cache-dir', path.join(out, '.cache'), '--quiet', ...extra],
    {}
  )
  return out
}

// site reads every published file under dir into one { relpath: text } map.
function site (dir) {
  const files = {}
  for (const entry of fs.readdirSync(dir, { recursive: true, withFileTypes: true })) {
    if (!entry.isFile()) continue
    const abs = path.join(entry.parentPath, entry.name)
    const rel = path.relative(dir, abs)
    if (!rel.startsWith('.cache')) files[rel] = fs.readFileSync(abs, 'utf8')
  }
  return files
}

test('customer build drops internal content', async () => {
  const files = site(await build('customer'))
  assert.ok(files['fixture/public.html'], 'public.html is published')
  assert.ok(files['fixture/_images/shared.png'], 'shared.png is published')
  assert.match(files['fixture/public.html'], /A sentence with\s+in it\./, 'the rest of an internal phrase\'s sentence is kept')
  assert.ok(files['fixture/public.html'].includes('KEPT-SECTION'), 'a public section after an internal one is kept')
  assert.equal(files['fixture/internal-page.html'], undefined, 'internal-page.html is dropped')
  assert.equal(files['fixture/team/secret.html'], undefined, 'the internal module is dropped')
  assert.equal(files['fixture/_images/internal-only.png'], undefined, 'an image only internal pages use is dropped')
  assert.equal(
    files['fixture/_attachments/internal-only.txt'],
    undefined,
    'an attachment only internal pages use is dropped'
  )
  const all = Object.values(files).join('\n')
  const leaked = [...secrets, 'SECRET-ATTACHMENT'].filter((secret) => all.includes(secret))
  assert.deepEqual(leaked, [], 'no secret is published')
  assert.ok(!files['fixture/public.html'].includes('internal-page.html'), 'no nav link to internal-page.html')
  assert.ok(!files['fixture/public.html'].includes('Team area'), 'the internal module has no nav entry')
})

test('customer build fails on a leak', async () => {
  await assert.rejects(
    build('customer', '--attribute', 'leak'),
    /leaks into the customer build:\nfixture:ROOT:public\.adoc links to internal page fixture:ROOT:internal-page\.adoc/
  )
})

test('customer build fails on a link to an internal fragment', async () => {
  await assert.rejects(build('customer', '--attribute', 'fragleak'), (err) => {
    assert.match(err.message, /fixture:ROOT:public\.adoc links to #secret-section, which is internal or missing/)
    assert.match(err.message, /fixture:ROOT:public\.adoc links to #secret-block, which is internal or missing/)
    return true
  })
})

test('customer build fails when internal markup survives', async () => {
  await assert.rejects(
    build('customer', '--attribute', 'navleak'),
    /internal markup survives into the customer build on fixture:ROOT:public\.adoc/
  )
})

test('customer build fails when the site start page is internal', async () => {
  await assert.rejects(
    build('customer', '--start-page', 'fixture::internal-page.adoc'),
    /start page fixture:ROOT:internal-page\.adoc is internal/
  )
})

test('team build keeps everything', async () => {
  const files = site(await build('team'))
  const all = Object.values(files).join('\n')
  for (const secret of secrets) assert.ok(all.includes(secret), `${secret} is kept`)
  assert.ok(files['fixture/internal-page.html'], 'internal-page.html is published')
  assert.ok(files['fixture/_images/internal-only.png'], 'internal-only.png is published')
  assert.match(
    files['fixture/public.html'],
    /href="internal-page\.html">Internal page<\/a> <span class="team-only">Team only<\/span>/,
    'an internal page carries the nav badge'
  )
  assert.match(
    files['fixture/public.html'],
    /href="team\/secret\.html">Team area page<\/a> <span class="team-only">Team only<\/span>/,
    'a page in an internal module carries the nav badge'
  )
  assert.match(
    files['fixture/public.html'],
    /<p class="nav-head">Team area <span class="team-only">Team only<\/span><\/p>/,
    'an internal module\'s nav menu carries the badge'
  )
  assert.match(
    files['fixture/public.html'],
    /SECRET-NAVROLE is an internal nav entry <span class="team-only">Team only<\/span>/,
    'an internal nav entry carries the nav badge'
  )
  assert.match(files['fixture/public.html'], /<div class="paragraph internal">/, 'internal blocks are marked')
  assert.match(files['fixture/public.html'], /<span class="internal">SECRET-INLINE<\/span>/, 'internal phrases are marked')
})
