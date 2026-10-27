import type { Plugin } from "vite";

// excalidrawFonts points Excalidraw's font fallback (a CDN, esm.sh) at the
// app's own bundle, where scripts/app.sh copies the fonts: the app loads no
// font from another origin (its CSP is font-src 'self'; spec §4, no CDN at
// runtime). An Excalidraw upgrade can't silently bring the CDN back: the
// build fails if the fallback changed shape, if Excalidraw is in the build
// but nothing was rewritten (a rename), or if any chunk still names esm.sh.
const FALLBACK = /`https:\/\/esm\.sh\/\$\{[\w$]+\.PKG_NAME\?`\$\{[\w$]+\.PKG_NAME\}@\$\{[\w$]+\.PKG_VERSION\}`:"@excalidraw\/excalidraw"\}\/dist\/prod\/`/;

export function excalidrawFonts(): Plugin {
  let excalidraw = false;
  let rewrote = 0;
  return {
    name: "jam-excalidraw-fonts",
    transform(code, id) {
      if (!id.includes("@excalidraw/excalidraw")) return null;
      excalidraw = true;
      if (!code.includes("ASSETS_FALLBACK_URL")) return null;
      if (!FALLBACK.test(code)) throw new Error("excalidrawFonts: Excalidraw's font fallback changed shape; update build/excalidrawFonts.ts");
      rewrote++;
      return { code: code.replace(FALLBACK, "`${location.origin}/app/next/assets/excalidraw/`"), map: null };
    },
    generateBundle(_, bundle) {
      if (excalidraw && rewrote !== 1) this.error(`excalidrawFonts: rewrote ${rewrote} font fallbacks, want 1 (did Excalidraw rename ASSETS_FALLBACK_URL?)`);
      for (const [name, out] of Object.entries(bundle)) {
        if (out.type === "chunk" && out.code.includes("esm.sh")) this.error(`excalidrawFonts: ${name} still names esm.sh (a CDN the app must not load from)`);
      }
    },
  };
}
