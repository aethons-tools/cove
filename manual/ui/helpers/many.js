'use strict'

// many reports whether a collection (array or object map, like site.components)
// has more than one entry.
module.exports = (collection) => Object.keys(collection || {}).length > 1
