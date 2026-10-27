// Excalidraw loads its fonts from window.EXCALIDRAW_ASSET_PATH + "fonts/…";
// scripts/app.sh copies them into the bundle there. Imported before
// Excalidraw itself, so it is set when Excalidraw's modules evaluate.
declare global {
  interface Window { EXCALIDRAW_ASSET_PATH?: string | string[] }
}
window.EXCALIDRAW_ASSET_PATH = `${location.origin}/app/next/assets/excalidraw/`;
export {};
