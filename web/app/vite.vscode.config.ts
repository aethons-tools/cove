import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { excalidrawFonts } from "./build/excalidrawFonts.ts";

// The VS Code webview bundle (docs/superpowers/plans/2026-10-29-vscode-3-conversation.md):
// src/vscode/main.tsx as pane.js and pane.css with relative lazy chunks, for
// the extension's own HTML (asWebviewUri paths, a nonce CSP). No index.html.
export default defineConfig({
  base: "./",
  plugins: [react(), excalidrawFonts()],
  build: {
    outDir: process.env.VSCODE_WEBVIEW_OUT ?? "../vscode/dist/webview",
    emptyOutDir: true,
    cssCodeSplit: false,
    rollupOptions: {
      input: "src/vscode/main.tsx",
      output: {
        entryFileNames: "pane.js",
        chunkFileNames: "assets/[name]-[hash].js",
        assetFileNames: (a) => (a.names?.[0]?.endsWith(".css") ? "pane.css" : "assets/[name]-[hash][extname]"),
      },
    },
  },
});
