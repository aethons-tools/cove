// Fails when an area's initial JS exceeds the budget gzipped
// (docs/superpowers/specs/2026-10-26-jam-spa-design.md §5,
// 2026-10-28-admin-spa-design.md §1): the user area's is the entry module plus
// its modulepreloads, as index.html loads them; the admin area's is that plus
// the admin start chunk and its static imports. Lazy chunks don't count.
import { readFileSync, rmSync } from "node:fs";
import { gzipSync } from "node:zlib";
import { join } from "node:path";

const dir = process.argv[2] ?? "dist";
const budget = 350 * 1024;
const html = readFileSync(join(dir, "index.html"), "utf8");
const srcs = [...html.matchAll(/<(?:script[^>]+src|link[^>]+rel="modulepreload"[^>]+href)="\/app\/(assets\/[^"]+\.js)"/g)].map((m) => m[1]);
if (srcs.length === 0) throw new Error("size-budget: no scripts found in index.html");
const manifest = JSON.parse(readFileSync(join(dir, ".vite", "manifest.json"), "utf8"));
const admin = Object.values(manifest).find((c) => c.src === "src/admin/start.tsx");
if (!admin) throw new Error("size-budget: no src/admin/start.tsx chunk in the manifest");
// staticFiles is a chunk's file and those of its static imports, transitively.
const staticFiles = (key, out = new Set()) => {
  const c = manifest[key];
  if (!c || out.has(c.file)) return out;
  out.add(c.file);
  for (const i of c.imports ?? []) staticFiles(i, out);
  return out;
};
const adminKey = Object.keys(manifest).find((k) => manifest[k] === admin);
const size = (files) => [...files].reduce((n, f) => n + gzipSync(readFileSync(join(dir, f))).length, 0);
const user = new Set(srcs);
let failed = false;
for (const [area, files] of [["user", user], ["admin", new Set([...user, ...staticFiles(adminKey)])]]) {
  const total = size(files);
  console.log(`size-budget: ${area} initial JS ${(total / 1024).toFixed(1)} KB gzipped (budget ${budget / 1024} KB)`);
  if (total > budget) failed = true;
}
// The manifest is a build input to this check only: never embedded.
rmSync(join(dir, ".vite"), { recursive: true, force: true });
if (failed) {
  console.error("size-budget: over budget — lazy-load the new code (React.lazy) or drop a dependency");
  process.exit(1);
}
