#!/usr/bin/env node
// A stand-in for mmdc in the diagram tests (diagrams.test.js), so they need no
// browser: `--version` prints a version; otherwise it writes an SVG of the
// input to -o, like mmdc's (width="100%", an inline style), or fails when the
// input has FAIL in it. Each render appends the input and the browser named
// by the -p puppeteer config, as a JSON line, to $FAKE_MMDC_LOG.
'use strict'

const fs = require('node:fs')

const args = process.argv.slice(2)
if (args.includes('--version')) {
  console.log(process.env.FAKE_MMDC_VERSION || '0.0.0-fake')
  process.exit(0)
}
const opt = (name) => args[args.indexOf(name) + 1]
const source = fs.readFileSync(opt('-i'), 'utf8')
const browser = JSON.parse(fs.readFileSync(opt('-p'), 'utf8')).executablePath
if (process.env.FAKE_MMDC_LOG) fs.appendFileSync(process.env.FAKE_MMDC_LOG, JSON.stringify({ source, browser }) + '\n')
if (source.includes('FAIL')) {
  console.error('Error: Parse error on line 2: FAIL')
  process.exit(1)
}
const text = source.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
fs.writeFileSync(
  opt('-o'),
  '<svg id="my-svg" width="100%" xmlns="http://www.w3.org/2000/svg" style="max-width: 120px;" viewBox="0 0 120 40">' +
    `<style>#my-svg{fill:#333;}</style><text>${text}</text></svg>`
)
