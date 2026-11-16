// Puppeteer (mmdc's peer, for extensions/diagrams.js) never downloads a
// browser on install: diagrams render with the kit's headless Chrome or
// $JAM_BROWSER (scripts/manual-browser.sh). See docs/modules/ROOT/pages/manual-diagrams.adoc.
module.exports = { skipDownload: true }
