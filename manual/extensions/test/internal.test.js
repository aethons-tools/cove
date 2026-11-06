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
const secrets = ['SECRET-BLOCK', 'SECRET-SECTION', 'SECRET-INLINE', 'SECRET-IFDEF', 'SECRET-PAGE', 'SECRET-MODULE']

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
  for (const secret of [...secrets, 'SECRET-ATTACHMENT']) assert.ok(!all.includes(secret), `${secret} is dropped`)
  assert.ok(!files['fixture/public.html'].includes('internal-page.html'), 'no nav link to internal-page.html')
  assert.ok(!files['fixture/public.html'].includes('Team area'), 'the internal module has no nav entry')
})

test('customer build fails on a leak', async () => {
  await assert.rejects(build('customer', '--attribute', 'leak'), /internal-page/)
})

test('team build keeps everything', async () => {
  const files = site(await build('team'))
  const all = Object.values(files).join('\n')
  for (const secret of secrets) assert.ok(all.includes(secret), `${secret} is kept`)
  assert.ok(files['fixture/internal-page.html'], 'internal-page.html is published')
  assert.ok(files['fixture/_images/internal-only.png'], 'internal-only.png is published')
  assert.match(files['fixture/internal-page.html'], /class="team-only"/, 'internal pages carry the nav badge')
})
