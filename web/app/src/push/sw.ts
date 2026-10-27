// registerSW registers the /app/ service worker (appui's sw.js: installable,
// Web Push notifications, network-only) from the bundle — the old pages'
// inline registration would break the app's CSP.
export function registerSW(nav: Navigator = navigator): void {
  nav.serviceWorker?.register("/app/sw.js", { scope: "/app/" }).catch(() => {});
}
