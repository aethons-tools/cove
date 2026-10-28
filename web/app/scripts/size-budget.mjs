// Fails when the shell route's initial JS (the entry module plus its
// modulepreloads, as index.html loads them) exceeds the budget gzipped
// (docs/superpowers/specs/2026-10-26-jam-spa-design.md §5). Lazy chunks don't count.
import { readFileSync } from "node:fs";
import { gzipSync } from "node:zlib";
import { join } from "node:path";

const dir = process.argv[2] ?? "dist";
const budget = 350 * 1024;
const html = readFileSync(join(dir, "index.html"), "utf8");
const srcs = [...html.matchAll(/<(?:script[^>]+src|link[^>]+rel="modulepreload"[^>]+href)="\/app\/(assets\/[^"]+\.js)"/g)].map((m) => m[1]);
if (srcs.length === 0) throw new Error("size-budget: no scripts found in index.html");
let total = 0;
for (const s of new Set(srcs)) total += gzipSync(readFileSync(join(dir, s))).length;
console.log(`size-budget: initial JS ${(total / 1024).toFixed(1)} KB gzipped (budget ${budget / 1024} KB)`);
if (total > budget) {
  console.error("size-budget: over budget — lazy-load the new code (React.lazy) or drop a dependency");
  process.exit(1);
}
