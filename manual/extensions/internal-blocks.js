// Asciidoctor tree processor: drops every block or section with the role
// `internal` (`[.internal]`) when the document lacks the `internal` attribute
// (the customer build). The team build keeps them; manual/ui/css/site.css marks
// them. Inline `[.internal]#…#` is dropped by internal.js, which also hard-unsets
// `internal` in the customer build so no component or page can set it.
// The customer build also fails on a kept section whose generated id would
// carry an internal phrase of its title. See docs/manual.md.
'use strict'

const asciidoctor = require('@asciidoctor/core') // the instance Antora loads

// A class attribute with the token `internal` (as in internal.js).
const INTERNAL_CLASS = /\sclass=(["'])(?:(?!\1).)*?(?<![\w-])internal(?![\w-])(?:(?!\1).)*\1/

function register (registry) {
  registry.treeProcessor(function () {
    this.process((doc) => {
      if (doc.hasAttribute('internal')) return
      // Outermost first, so a block inside an already dropped one is skipped.
      // traverse_documents: also the nested documents of AsciiDoc (a|) table cells.
      const internal = doc.findBy({ traverse_documents: true }, (node) => node !== doc && node.hasRole('internal'))
      for (const block of internal) {
        const siblings = block.getParent().getBlocks()
        const at = siblings.indexOf(block)
        if (at >= 0) siblings.splice(at, 1)
      }
      // A section's generated id is a slug of its whole title, internal phrases
      // included: fail rather than publish it. An explicit id ([#id]) is kept as is.
      for (const section of doc.findBy({ traverse_documents: true, context: 'section' })) {
        const title = section.getTitle()
        if (!section.getId() || !INTERNAL_CLASS.test(title ?? '') || !generatedId(section)) continue
        const where = doc.getAttribute('docfile')
        throw new Error(
          `${where ? where + ': ' : ''}the section "${publicText(title)}" has an internal phrase in its title ` +
            `and no explicit id, so its generated id ${section.getId()} carries the phrase: ` +
            'give the section an explicit id ([#some-id] on the line above it)'
        )
      }
    })
  })
}

// generatedId: whether section's id is the one Asciidoctor generates from its
// title (the slug, or the slug plus a separator and a number when it repeats).
function generatedId (section) {
  const doc = section.getDocument()
  const attributes = {}
  for (const name of ['idprefix', 'idseparator']) {
    if (doc.hasAttribute(name)) attributes[name] = doc.getAttribute(name)
  }
  // A scratch document, so the slug is not deduplicated against this one's ids.
  const slug = section.constructor.$generate_id(section.getTitle(), asciidoctor().load('', { attributes }))
  const id = section.getId()
  const separator = attributes.idseparator ?? '_'
  return id === slug || (id.startsWith(slug + separator) && /^\d+$/.test(id.slice(slug.length + separator.length)))
}

// publicText: html's text with its internal elements, and all tags, removed.
function publicText (html) {
  return html
    .replace(/<(\w+)\b[^>]*\sclass="[^"]*\binternal\b[^"]*"[^>]*>.*?<\/\1>/g, '')
    .replace(/<[^>]*>/g, '')
    .replace(/\s+/g, ' ')
    .trim()
}

module.exports = { register }
