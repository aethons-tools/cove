// Antora extension: internal content in the manual (docs/manual.md, "Marking
// content internal").
//
// The site attribute `internal` says which build this is: set, the team build;
// unset, the customer build. In the customer build this drops
//   - every page with `:page-internal:` and every page in a module listed in
//     this extension's `modules` config, with its nav entries and page aliases;
//   - inline `[.internal]#…#` phrases (internal-blocks.js drops the blocks);
//   - every image and attachment no published page references;
// and fails when a kept page links to a dropped page (a leak).
// In the team build it drops nothing; nav entries of internal pages get
// `internal: true`, which manual/ui renders as a "Team only" badge.
//
// Playbook:
//   antora:
//     extensions:
//     - require: ./extensions/internal.js
//       modules: [<module>, …]   # optional: whole modules that are internal
//   asciidoc:
//     extensions:
//     - ./extensions/internal-blocks.js
'use strict'

const path = require('node:path').posix
const blocks = require('./internal-blocks')

function register ({ config }) {
  const logger = this.getLogger('internal')
  const internalModules = new Set(config.modules || [])
  let team = false
  // pub.url of every internal page: dropped in the customer build, badged in the team build.
  const internalUrls = new Set()

  this.once('beforeProcess', ({ siteAsciiDocConfig }) => {
    const value = siteAsciiDocConfig.attributes?.internal
    team = value != null && value !== false
    if (!siteAsciiDocConfig.extensions?.includes(blocks)) {
      throw new Error(
        'internal.js needs internal-blocks.js in the playbook\'s asciidoc.extensions, ' +
          'or [.internal] blocks would be published'
      )
    }
  })

  this.once('documentsConverted', ({ contentCatalog }) => {
    const pages = contentCatalog.getPages((page) => page.out)
    const internal = pages.filter(
      (page) => page.asciidoc?.attributes?.['page-internal'] != null || internalModules.has(page.src.module)
    )
    for (const page of internal) internalUrls.add(page.pub.url)
    if (team) return
    const leaks = []
    for (const page of pages) {
      if (internalUrls.has(page.pub.url)) continue
      page.contents = Buffer.from(dropInternalSpans(page.contents.toString()))
      for (const url of linkedUrls(page)) {
        if (!internalUrls.has(url)) continue
        const target = internal.find((candidate) => candidate.pub.url === url)
        const msg = `${pageName(page)} links to internal page ${pageName(target)}, which the customer build drops`
        logger.error({ file: page.src }, msg)
        leaks.push(msg)
      }
    }
    if (leaks.length) throw new Error('Internal content leaks into the customer build:\n' + leaks.join('\n'))
  })

  this.once('navigationBuilt', ({ contentCatalog, navigationCatalog }) => {
    for (const component of contentCatalog.getComponents()) {
      for (const { version } of component.versions) {
        const trees = navigationCatalog.getNavigation(component.name, version)
        if (!trees) continue
        if (team) markInternal(trees, internalUrls)
        else trees.splice(0, trees.length, ...pruneInternal(trees, internalUrls))
      }
    }
    if (team) return
    for (const page of contentCatalog.getPages()) {
      if (page.pub && internalUrls.has(page.pub.url)) contentCatalog.removeFile(page)
    }
    for (const alias of contentCatalog.findBy({ family: 'alias' })) {
      if (alias.rel?.pub && internalUrls.has(alias.rel.pub.url)) contentCatalog.removeFile(alias)
    }
  })

  this.once('beforePublish', ({ contentCatalog }) => {
    if (team) return
    const referenced = new Set()
    for (const page of contentCatalog.getPages((page) => page.out)) {
      for (const url of linkedUrls(page)) referenced.add(url)
    }
    for (const family of ['image', 'attachment']) {
      for (const file of contentCatalog.findBy({ family })) {
        if (file.out && !referenced.has(file.pub.url)) contentCatalog.removeFile(file)
      }
    }
  })
}

// dropInternalSpans removes every <span class="… internal …">…</span>, with
// whatever spans it contains, from html.
function dropInternalSpans (html) {
  const open = /<span class="(?:[^"]*\s)?internal(?:\s[^"]*)?">/g
  const tag = /<span\b|<\/span>/g
  let out = ''
  let from = 0
  let match
  while ((match = open.exec(html))) {
    out += html.slice(from, match.index)
    let depth = 1
    let next
    tag.lastIndex = open.lastIndex
    while (depth && (next = tag.exec(html))) depth += next[0] === '</span>' ? -1 : 1
    from = open.lastIndex = depth ? html.length : tag.lastIndex
  }
  return out + html.slice(from)
}

// linkedUrls returns the site URLs (no query or fragment) that page's HTML
// links to or embeds, resolved against the page's own URL.
function linkedUrls (page) {
  const urls = []
  const base = path.dirname(page.pub.url)
  for (const [, ref] of page.contents.toString().matchAll(/\b(?:href|src|data)="([^"#?]+)/g)) {
    if (/^[a-z][a-z0-9+.-]*:|^\/\//i.test(ref)) continue // another site, or mailto: and the like
    urls.push(ref.startsWith('/') ? path.normalize(ref) : path.resolve(base, ref))
  }
  return urls
}

function pageName (page) {
  const { component, version, module: module_, relative } = page.src
  return `${version ? version + '@' : ''}${component}:${module_}:${relative}`
}

function markInternal (items, internalUrls) {
  for (const item of items) {
    if (item.urlType === 'internal' && internalUrls.has(item.url)) item.internal = true
    if (item.items) markInternal(item.items, internalUrls)
  }
}

// pruneInternal returns items without the entries for internal pages. An entry
// for an internal page with kept children stays as plain text; a text-only
// entry whose children were all dropped goes too.
function pruneInternal (items, internalUrls) {
  return items.filter((item) => {
    const hadChildren = item.items?.length > 0
    if (hadChildren) item.items = pruneInternal(item.items, internalUrls)
    const keptChildren = item.items?.length > 0
    if (item.urlType === 'internal' && internalUrls.has(item.url)) {
      if (!keptChildren) return false
      delete item.url
      delete item.urlType
      delete item.hash
    }
    return item.url || keptChildren || !hadChildren
  })
}

module.exports = { register }
