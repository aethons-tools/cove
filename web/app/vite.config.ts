/// <reference types="vitest/config" />
import { defineConfig, type Plugin, type ProxyOptions } from "vite";
import react from "@vitejs/plugin-react";

// The dev server (`just app-dev`) proxies the API, auth and shared static
// routes to a running `at-jam serve` (JAM_URL; dev/jam.dev.yml's admin-listen),
// rewriting Origin so RequireSameOrigin admits the proxied writes.
const jam = process.env.JAM_URL ?? "http://localhost:8081";
const toJam: ProxyOptions = {
  target: jam,
  changeOrigin: true,
  configure: (proxy) => {
    proxy.on("proxyReq", (req) => req.setHeader("origin", jam));
  },
};

// excalidrawFonts points Excalidraw's font fallback (a CDN, esm.sh) at the
// app's own bundle, where scripts/app.sh copies the fonts: the app loads no
// font from another origin (its CSP is font-src 'self'; spec §4, no CDN at
// runtime). It fails the build if Excalidraw's fallback changes shape, so an
// upgrade can't silently bring the CDN back.
const FALLBACK = /`https:\/\/esm\.sh\/\$\{[\w$]+\.PKG_NAME\?`\$\{[\w$]+\.PKG_NAME\}@\$\{[\w$]+\.PKG_VERSION\}`:"@excalidraw\/excalidraw"\}\/dist\/prod\/`/;
function excalidrawFonts(): Plugin {
  return {
    name: "jam-excalidraw-fonts",
    transform(code, id) {
      if (!id.includes("@excalidraw/excalidraw") || !code.includes("ASSETS_FALLBACK_URL")) return null;
      if (!FALLBACK.test(code)) throw new Error("excalidrawFonts: Excalidraw's font fallback changed shape; update vite.config.ts");
      return { code: code.replace(FALLBACK, "`${location.origin}/app/next/assets/excalidraw/`"), map: null };
    },
  };
}

export default defineConfig({
  base: "/app/next/",
  plugins: [react(), excalidrawFonts()],
  build: {
    outDir: process.env.APP_OUT_DIR ?? "dist",
    emptyOutDir: true,
    assetsDir: "assets",
    sourcemap: false,
  },
  server: {
    proxy: { "/app/user": toJam, "/app/auth": toJam, "/app/static": toJam },
  },
  // Full-app renders (Mantine + router + MSW) run seconds each under a
  // parallel run: a 15 s ceiling instead of the 5 s default.
  test: { environment: "jsdom", setupFiles: ["./src/test/setup.ts"], css: false, testTimeout: 15_000 },
});
