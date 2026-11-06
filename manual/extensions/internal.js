// Antora extension: internal content in the manual (docs/manual.md, "Marking
// content internal").
//
// The site attribute `internal` says which build this is: set, the team build;
// unset, the customer build. In the customer build this
//   - hard-unsets `internal`, so no component descriptor or page header can
//     set it (that would keep `[.internal]` blocks and `ifdef::internal[]`);
//   - drops every page with `:page-internal:` and every page in a module listed
//     in this extension's `modules` config, with its nav entries and aliases,
//     and the internal modules' nav menus whole;
//   - drops inline elements with the role `internal` (`[.internal]#…#`,
//     `[.internal]*…*`, …), titles included; internal-blocks.js drops blocks;
//   - drops every image and attachment no published page references;
//   - fails when a kept page links to a dropped page (a leak) or to a fragment
//     of its own that no element has (a dropped section or block), when the site or
//     a component start page is internal, and, as a backstop, when any
//     published page still has an element with the class `internal`.
// In the team build it drops nothing; nav entries of internal pages and the
// menus of internal modules get `internal: true`, which manual/ui renders as a
// "Team only" badge.
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

// A class attribute with the token `internal`; and the start tag of an element with one.
const INTERNAL_CLASS_ATTR = String.raw`\sclass=(?<q>["'])(?:(?!\k<q>).)*?(?<![\w-])internal(?![\w-])(?:(?!\k<q>).)*\k<q>`
const INTERNAL_CLASS = new RegExp(INTERNAL_CLASS_ATTR)
const INTERNAL_START_TAG = new RegExp(String.raw`<(?<name>[a-zA-Z][\w-]*)\b[^>]*?` + INTERNAL_CLASS_ATTR + '[^>]*>', 'g')
const VOID_ELEMENTS = new Set(['area', 'br', 'col', 'embed', 'hr', 'img', 'input', 'link', 'meta', 'source', 'track', 'wbr'])

function register ({ config }) {
  const logger = this.getLogger('internal')
  const internalModules = new Set(config.modules || [])
  let team = false
  // pub.url of every internal page: dropped in the customer build, badged in the team build.
  const internalUrls = new Set()

  this.once('beforeProcess', ({ siteAsciiDocConfig }) => {
    if (!siteAsciiDocConfig.extensions?.includes(blocks)) {
      throw new Error(
        'internal.js needs internal-blocks.js in the playbook\'s asciidoc.extensions, ' +
          'or [.internal] blocks would be published'
      )
    }
    const value = siteAsciiDocConfig.attributes?.internal
    team = value != null && value !== false
    // Hard unset, locked against component descriptors and page headers. null,
    // not false: Asciidoctor.js still takes a false-valued attribute as set for ifdef.
    if (!team) siteAsciiDocConfig.attributes.internal = null
  })

  this.once('documentsConverted', ({ contentCatalog }) => {
    const pages = contentCatalog.getPages((page) => page.out)
    const internal = pages.filter(
      (page) => page.asciidoc?.attributes?.['page-internal'] != null || internalModules.has(page.src.module)
    )
    for (const page of internal) internalUrls.add(page.pub.url)
    if (team) return
    const errors = []
    for (const page of pages) {
      if (internalUrls.has(page.pub.url)) continue
      page.contents = Buffer.from(dropInternal(page.contents.toString()))
      const { asciidoc } = page
      for (const key of ['doctitle', 'xreftext', 'navtitle']) {
        if (asciidoc?.[key]) asciidoc[key] = dropInternal(asciidoc[key]).trim()
      }
      // An in-page link to a dropped section or block keeps its title as the link text.
      for (const frag of danglingFragments(page.contents.toString())) {
        errors.push([page, `${pageName(page)} links to #${frag}, which is internal or missing`])
      }
      for (const url of linkedUrls(page)) {
        if (!internalUrls.has(url)) continue
        const target = internal.find((candidate) => candidate.pub.url === url)
        errors.push([page, `${pageName(page)} links to internal page ${pageName(target)}, which the customer build drops`])
      }
    }
    const startPages = [['site', contentCatalog.getSiteStartPage()]]
    for (const component of contentCatalog.getComponents()) {
      for (const cv of component.versions) startPages.push([`component ${component.name}`, { pub: { url: cv.url } }])
    }
    for (const [whose, start] of startPages) {
      if (!start?.pub || !internalUrls.has(start.pub.url)) continue
      const target = internal.find((candidate) => candidate.pub.url === start.pub.url)
      errors.push([target, `the ${whose} start page ${pageName(target)} is internal, which the customer build drops`])
    }
    fail(logger, 'Internal content leaks into the customer build', errors)
  })

  this.once('navigationBuilt', ({ contentCatalog, navigationCatalog }) => {
    for (const component of contentCatalog.getComponents()) {
      for (const { version } of component.versions) {
        const trees = navigationCatalog.getNavigation(component.name, version)
        if (!trees) continue
        // The nav files of internal modules: a tree's order is its file's nav index (+ a fraction).
        const internalNav = new Set(
          contentCatalog
            .findBy({ component: component.name, version, family: 'nav' })
            .filter((nav) => internalModules.has(nav.src.module) && nav.nav)
            .map((nav) => nav.nav.index)
        )
        const isInternalTree = (tree) => internalNav.has(Math.floor(tree.order))
        if (team) {
          for (const tree of trees) if (isInternalTree(tree)) tree.internal = true
          markInternal(trees, internalUrls)
        } else {
          const kept = trees.filter((tree) => !isInternalTree(tree))
          trees.splice(0, trees.length, ...pruneInternal(kept, internalUrls))
        }
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
    const published = contentCatalog.getPages((page) => page.out)
    // Backstop: whatever slipped past the steps above fails the build, not leaks.
    fail(
      logger,
      'Internal markup survives into the customer build',
      published
        .filter((page) => INTERNAL_CLASS.test(page.contents.toString()))
        .map((page) => [page, `internal markup survives into the customer build on ${pageName(page)}`])
    )
    const referenced = new Set()
    for (const page of published) {
      for (const url of linkedUrls(page)) referenced.add(url)
    }
    for (const family of ['image', 'attachment']) {
      for (const file of contentCatalog.findBy({ family })) {
        if (file.out && !referenced.has(file.pub.url)) contentCatalog.removeFile(file)
      }
    }
  })
}

// fail logs each [page, message] in errors and throws one error listing them.
function fail (logger, title, errors) {
  if (!errors.length) return
  for (const [page, msg] of errors) logger.error(page ? { file: page.src } : {}, msg)
  throw new Error(title + ':\n' + errors.map(([, msg]) => msg).join('\n'))
}

// dropInternal removes every element whose class has the token `internal`,
// with everything inside it, from html.
function dropInternal (html) {
  let out = ''
  let from = 0
  let match
  INTERNAL_START_TAG.lastIndex = 0
  while ((match = INTERNAL_START_TAG.exec(html))) {
    out += html.slice(from, match.index)
    const name = match.groups.name.toLowerCase()
    const tag = new RegExp(`<${name}\\b|</${name}\\s*>`, 'gi')
    tag.lastIndex = INTERNAL_START_TAG.lastIndex
    let depth = VOID_ELEMENTS.has(name) || match[0].endsWith('/>') ? 0 : 1
    let next
    while (depth && (next = tag.exec(html))) depth += next[0].startsWith('</') ? -1 : 1
    // An unclosed element drops the rest: fail closed.
    from = INTERNAL_START_TAG.lastIndex = depth ? html.length : tag.lastIndex
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

// danglingFragments returns the fragments html links to in itself
// (href="#frag") that no element in html has as its id.
function danglingFragments (html) {
  const ids = new Set()
  for (const [, , id] of html.matchAll(/\sid=(["'])(.*?)\1/g)) ids.add(id)
  const dangling = new Set()
  for (const [, , frag] of html.matchAll(/\shref=(["'])#(.+?)\1/g)) {
    if (!ids.has(frag)) dangling.add(frag)
  }
  return [...dangling]
}

function pageName (page) {
  const { component, version, module: module_, relative } = page.src
  return `${version ? version + '@' : ''}${component}:${module_}:${relative}`
}

// isInternalItem: a nav entry for an internal page, or one marked `[.internal]#…#` whole.
function isInternalItem (item, internalUrls) {
  return (item.urlType === 'internal' && internalUrls.has(item.url)) || !!item.roles?.includes('internal')
}

function markInternal (items, internalUrls) {
  for (const item of items) {
    if (isInternalItem(item, internalUrls)) item.internal = true
    if (item.items) markInternal(item.items, internalUrls)
  }
}

// pruneInternal returns items without internal entries and their children,
// and without entries for internal pages; such an entry with kept children
// stays as plain text. A text-only entry whose children all went goes too.
function pruneInternal (items, internalUrls) {
  return items.filter((item) => {
    if (item.roles?.includes('internal')) return false
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
