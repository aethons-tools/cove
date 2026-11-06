// Asciidoctor tree processor: drops every block or section with the role
// `internal` (`[.internal]`) when the document lacks the `internal` attribute
// (the customer build). The team build keeps them; manual/ui/css/site.css marks
// them. Inline `[.internal]#…#` is dropped by internal.js. See docs/manual.md.
'use strict'

function register (registry) {
  registry.treeProcessor(function () {
    this.process((doc) => {
      if (doc.hasAttribute('internal')) return
      // Outermost first, so a block inside an already dropped one is skipped.
      for (const block of doc.findBy((candidate) => candidate !== doc && candidate.hasRole('internal'))) {
        const siblings = block.getParent().getBlocks()
        const at = siblings.indexOf(block)
        if (at >= 0) siblings.splice(at, 1)
      }
    })
  })
}

module.exports = { register }
