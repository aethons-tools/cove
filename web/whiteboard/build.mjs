// Bundles the whiteboard editor into ../../internal/jam/uiassets/whiteboard/
// (git-ignored; embedded by the Jam binary): whiteboard.js, whiteboard.css
// and Excalidraw's fonts (without the 13 MB CJK Xiaolai set: those glyphs
// fall back to system fonts).
import { build } from "esbuild";
import { cpSync, mkdirSync, rmSync, readdirSync, renameSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const out = join(here, "../../internal/jam/uiassets/whiteboard");
const keep = "Placeholder: the whiteboard bundle is built here by `just whiteboard` (git-ignored).\n";
// Build into a scratch directory and swap it in only on success, so a failed
// build never leaves the embedded directory empty (go:embed needs .keep).
const tmp = out + ".tmp";
rmSync(tmp, { recursive: true, force: true });
mkdirSync(tmp, { recursive: true });

await build({
  entryPoints: [join(here, "entry.jsx")],
  bundle: true,
  minify: true,
  // ESM with code splitting: Excalidraw's lazily loaded parts (locales,
  // Mermaid, …) become chunks fetched on demand, not one 8 MB script.
  format: "esm",
  splitting: true,
  target: "es2020",
  outdir: tmp,
  entryNames: "whiteboard",
  chunkNames: "chunks/[name]-[hash]",
  assetNames: "assets/[name]-[hash]",
  define: { "process.env.NODE_ENV": '"production"', "import.meta.env": "{}" },
  conditions: ["production"],
  loader: { ".woff2": "file", ".png": "file", ".svg": "file" },
  jsx: "automatic",
  legalComments: "linked",
  logLevel: "warning",
});

const fonts = join(here, "node_modules/@excalidraw/excalidraw/dist/prod/fonts");
for (const f of readdirSync(fonts)) {
  if (f !== "Xiaolai") cpSync(join(fonts, f), join(tmp, "fonts", f), { recursive: true });
}
for (const [pkg, file] of [["@excalidraw/excalidraw", "LICENSE"], ["react", "LICENSE"], ["react-dom", "LICENSE"]]) {
  try { cpSync(join(here, "node_modules", pkg, file), join(tmp, "licenses", pkg.replace("/", "_") + ".txt"), { recursive: true }); } catch {}
}
writeFileSync(join(tmp, ".keep"), keep);
rmSync(out, { recursive: true, force: true });
renameSync(tmp, out);
