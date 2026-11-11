// Antora extension: books from local clones of other repos (docs/modules/ROOT/pages/manual-books.adoc).
// A playbook can't read the environment and fails on a
// content source whose url is missing, so a book that lives in a repo not
// everyone has (the process book, in the private aethons-tools/jam-config) is
// listed here instead of under content.sources, and added only when a clone exists:
//   - the env var `dir_env` names the clone: it must then exist (CI sets it
//     only after checking the repo out), or the build fails;
//   - else `dir` (relative to the playbook): if there is a git clone there, it
//     is added, else the book is skipped with a warning.
// `prerelease` lists versions that are never the book's default (Antora sorts
// non-semver versions in reverse alphabetical order and opens a book on the
// first, so without it the process book would open on goal, not current).
// Antora reads each branch from the clone: the local branch of that name (the
// working tree if it is checked out), else origin's; and fails if that has no
// `start_path`. So a branch is kept only when the one Antora will read has it;
// one that doesn't exist is dropped quietly, one without the book with a
// warning, and a book with no branches left is skipped with a warning.
//
// Playbook (team playbooks only; never the customer one):
//   antora:
//     extensions:
//     - require: ./extensions/books.js
//       books:
//       - name: process            # the component name, from its antora.yml
//         dir_env: JAM_CONFIG_DIR
//         dir: ../../jam-config
//         start_path: process
//         branches: [main, goal]
//         version: {main: current, goal: goal}
//         prerelease: [goal]
'use strict'

const { execFileSync } = require('node:child_process')
const fs = require('node:fs')
const path = require('node:path')

// git runs git in the clone and returns its trimmed output, or undefined when it fails.
function git (clone, ...args) {
  try {
    return execFileSync('git', ['-C', clone, ...args], { stdio: ['ignore', 'pipe', 'ignore'] }).toString().trim()
  } catch {}
}

// branchState says what Antora will find for `branch` in the clone: 'missing'
// (no local or origin branch: skipped), 'empty' (no startPath in it) or 'ok'.
function branchState (clone, branch, startPath) {
  const descriptor = `${startPath}/antora.yml`
  if (git(clone, 'symbolic-ref', '--short', '-q', 'HEAD') === branch) {
    return fs.existsSync(path.join(clone, descriptor)) ? 'ok' : 'empty'
  }
  for (const ref of [`refs/heads/${branch}`, `refs/remotes/origin/${branch}`]) {
    if (git(clone, 'rev-parse', '--verify', '-q', ref) === undefined) continue
    return git(clone, 'cat-file', '-e', `${ref}:${descriptor}`) === undefined ? 'empty' : 'ok'
  }
  return 'missing'
}

// bookSources resolves each book to a content source, or to a warning when its
// clone isn't there. It throws when an env var names a clone that doesn't exist.
function bookSources (books = [], { env = {}, playbookDir = '.' } = {}) {
  const sources = []
  const warnings = []
  for (const { name, dirEnv, dir, startPath, branches, version } of books) {
    const fromEnv = dirEnv && env[dirEnv]
    const clone = path.resolve(playbookDir, fromEnv || dir || '')
    if (!fs.existsSync(path.join(clone, '.git'))) {
      if (fromEnv) throw new Error(`books.js: ${dirEnv}=${fromEnv} is not a git clone (${name} book)`)
      warnings.push(
        `no clone at ${clone}${dirEnv ? ` (set ${dirEnv})` : ''}; building without the ${name} book`
      )
      continue
    }
    const kept = []
    for (const b of branches) {
      const state = branchState(clone, b, startPath)
      if (state === 'ok') kept.push(b)
      else if (state === 'empty') warnings.push(`${b} in ${clone} has no ${startPath}/; the ${name} book skips it`)
    }
    if (!kept.length) {
      warnings.push(`no branch of ${clone} has ${startPath}/; building without the ${name} book`)
      continue
    }
    sources.push({ url: clone, startPath, branches: kept, ...(version && { version }) })
  }
  return { sources, warnings }
}

// markPrereleases flags the versions each book lists under `prerelease`.
function markPrereleases (contentAggregate, books = []) {
  for (const { name, prerelease = [] } of books) {
    for (const cv of contentAggregate) {
      if (cv.name === name && prerelease.includes(cv.version)) cv.prerelease = true
    }
  }
}

function register ({ config }) {
  const logger = this.getLogger('books')
  this.once('playbookBuilt', ({ playbook }) => {
    const { sources, warnings } = bookSources(config.books, { env: playbook.env, playbookDir: playbook.dir })
    warnings.forEach((w) => logger.warn(w))
    for (const s of sources) logger.info(`adding a book from ${s.url}`)
    playbook.content.sources.push(...sources)
  })
  this.once('contentAggregated', ({ contentAggregate }) => markPrereleases(contentAggregate, config.books))
}

module.exports = { register, bookSources, markPrereleases }
