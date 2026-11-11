// books.js: which books a team build adds from local clones, and which of
// their branches. Hermetic: builds throwaway git repos in a temp dir.
'use strict'

const assert = require('node:assert/strict')
const { execFileSync } = require('node:child_process')
const fs = require('node:fs')
const os = require('node:os')
const path = require('node:path')
const { test } = require('node:test')
const { bookSources, markPrereleases } = require('../books')

const book = {
  name: 'process',
  dirEnv: 'JAM_CONFIG_DIR',
  dir: 'jam-config',
  startPath: 'process',
  branches: ['main', 'goal'],
  version: { main: 'current', goal: 'goal' },
}

function git (dir, ...args) {
  execFileSync('git', ['-C', dir, '-c', 'user.name=t', '-c', 'user.email=t@t', ...args], { stdio: 'ignore' })
}

// repo makes a clone at <tmp>/jam-config with `main` checked out holding
// process/antora.yml (when withBook), and returns <tmp>.
function repo (withBook = true) {
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'manual-books-'))
  const dir = path.join(tmp, 'jam-config')
  fs.mkdirSync(path.join(dir, 'process'), { recursive: true })
  git(dir, 'init', '-q', '-b', 'main')
  fs.writeFileSync(path.join(dir, withBook ? 'process/antora.yml' : 'README'), 'name: process\n')
  git(dir, 'add', '-A')
  git(dir, 'commit', '-q', '-m', 'init')
  return tmp
}

test('no clone at the default path: skipped with a warning', () => {
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'manual-books-'))
  const { sources, warnings } = bookSources([book], { env: {}, playbookDir: tmp })
  assert.deepEqual(sources, [])
  assert.match(warnings.join('\n'), /no clone at .*jam-config \(set JAM_CONFIG_DIR\); building without the process book/)
})

test('an env var naming a missing clone fails the build', () => {
  assert.throws(
    () => bookSources([book], { env: { JAM_CONFIG_DIR: '/nonexistent' }, playbookDir: '.' }),
    /JAM_CONFIG_DIR=\/nonexistent is not a git clone/
  )
})

test('a clone at the default path: its existing branches with the book', () => {
  const tmp = repo()
  const { sources, warnings } = bookSources([book], { env: {}, playbookDir: tmp })
  assert.deepEqual(warnings, [])
  assert.deepEqual(sources, [
    { url: path.join(tmp, 'jam-config'), startPath: 'process', branches: ['main'], version: book.version },
  ])
})

test('the env var wins over the default path; origin branches count', () => {
  const tmp = repo()
  const dir = path.join(tmp, 'jam-config')
  git(dir, 'update-ref', 'refs/remotes/origin/goal', 'HEAD')
  const { sources } = bookSources([book], { env: { JAM_CONFIG_DIR: dir }, playbookDir: '/elsewhere' })
  assert.deepEqual(sources[0].branches, ['main', 'goal'])
})

test('a branch without the book is dropped; none left skips the book', () => {
  const tmp = repo(false)
  const { sources, warnings } = bookSources([book], { env: {}, playbookDir: tmp })
  assert.deepEqual(sources, [])
  assert.match(warnings.join('\n'), /main in .* has no process\//)
  assert.match(warnings.join('\n'), /no branch of .* has process\/; building without the process book/)
})

test('a local branch wins over origin: checked against the local one', () => {
  const tmp = repo()
  const dir = path.join(tmp, 'jam-config')
  git(dir, 'update-ref', 'refs/remotes/origin/goal', 'HEAD')
  git(dir, 'checkout', '-q', '--orphan', 'goal')
  git(dir, 'rm', '-rq', '--cached', '.')
  git(dir, 'commit', '-q', '--allow-empty', '-m', 'empty goal')
  git(dir, 'checkout', '-q', '-f', 'main')
  const { sources, warnings } = bookSources([book], { env: {}, playbookDir: tmp })
  assert.deepEqual(sources[0].branches, ['main'])
  assert.match(warnings.join('\n'), /goal in .* has no process\//)
})

test('prerelease versions of the book are flagged, nothing else', () => {
  const aggregate = [
    { name: 'process', version: 'goal' },
    { name: 'process', version: 'current' },
    { name: 'jam', version: 'goal' },
  ]
  markPrereleases(aggregate, [{ ...book, prerelease: ['goal'] }])
  assert.deepEqual(
    aggregate.map((cv) => !!cv.prerelease),
    [true, false, false]
  )
})

test('the customer playbook never adds books', () => {
  const playbook = fs.readFileSync(path.join(__dirname, '../../antora-playbook.jam.yml'), 'utf8')
  assert.doesNotMatch(playbook, /books\.js|jam-config|start_path: process/)
})
