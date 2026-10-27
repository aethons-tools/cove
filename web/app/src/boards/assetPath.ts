// Excalidraw loads its fonts from window.EXCALIDRAW_ASSET_PATH + "fonts/…",
// then from its fallback (rewritten from a CDN to this same bundle by
// vite.config.ts's excalidrawFonts); scripts/app.sh copies them there.
declare global {
  interface Window { EXCALIDRAW_ASSET_PATH?: string | string[] }
}
window.EXCALIDRAW_ASSET_PATH = `${location.origin}/app/user/assets/excalidraw/`;
export {};
