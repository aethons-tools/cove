// Asciidoctor tree processor: drops every block or section with the role
// `internal` (`[.internal]`) when the document lacks the `internal` attribute
// (the customer build). The team build keeps them; manual/ui/css/site.css marks
// them. Inline `[.internal]#…#` is dropped by internal.js, which also hard-unsets
// `internal` in the customer build so no component or page can set it.
// See docs/manual.md.
'use strict'

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
    })
  })
}

module.exports = { register }
