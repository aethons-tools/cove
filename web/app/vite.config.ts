/// <reference types="vitest/config" />
import { defineConfig, type ProxyOptions } from "vite";
import react from "@vitejs/plugin-react";
import { excalidrawFonts } from "./build/excalidrawFonts.ts";

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

export default defineConfig({
  base: "/app/user/",
  plugins: [react(), excalidrawFonts()],
  build: {
    outDir: process.env.APP_OUT_DIR ?? "dist",
    emptyOutDir: true,
    assetsDir: "assets",
    sourcemap: false,
  },
  server: {
    // The app's pages are this server's (base /app/user/); only the routes
    // it reads beside them go to Jam.
    proxy: {
      "^/app/user/(api|events|session|uploads|blobs|push)(/|$)": toJam,
      "^/app/user/boards/[^/]+/(elements|thumb\\.png)": toJam,
      "/app/auth": toJam,
      "/app/static": toJam,
    },
  },
  // Full-app renders (Mantine + router + MSW) run seconds each under a
  // parallel run: a 15 s ceiling instead of the 5 s default.
  test: { environment: "jsdom", setupFiles: ["./src/test/setup.ts"], css: false, testTimeout: 15_000 },
});
