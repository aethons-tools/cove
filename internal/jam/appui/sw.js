// Jam app service worker: present so the app is installable and so push
// (a later slice) has a home. It caches nothing and has no fetch handler,
// so every request goes straight to the network.
self.addEventListener('install', () => self.skipWaiting());
self.addEventListener('activate', (e) => e.waitUntil(self.clients.claim()));
