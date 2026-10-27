// Jam app service worker: makes the app installable and shows Web Push
// notifications. It caches nothing and has no fetch handler, so every
// request goes straight to the network.
self.addEventListener('install', () => self.skipWaiting());
self.addEventListener('activate', (e) => e.waitUntil(self.clients.claim()));

const ICON = '/app/static/icons/icon-192.png';
const BADGE = '/app/static/icons/favicon-32.png';

// jamStr is v if it is a string, else ''.
function jamStr(v) {
  return typeof v === 'string' ? v : '';
}

// jamPayload is a decoded push payload as an object: anything else (null,
// a number, a string, an array) reads as {}.
function jamPayload(v) {
  return v && typeof v === 'object' && !Array.isArray(v) ? v : {};
}

// jamNotice is the notification to show for push payload p (title, body,
// channel, id, unread) given prev, the data of the notification already
// showing for that conversation (null: none). A conversation keeps one
// notification: a newer push replaces it, counting the messages it stands
// for. Fields of the wrong type read as missing.
function jamNotice(p, prev, silent) {
  const n = ((prev && typeof prev.n === 'number' && prev.n > 0) ? prev.n : 0) + 1;
  const text = jamStr(p.body);
  const body = n > 1 ? n + ' new messages' + (text ? ' · latest: ' + text : '') : (text || 'New message');
  const channel = jamStr(p.channel);
  return {
    title: jamStr(p.title) || 'Jam',
    options: {
      body: body,
      tag: channel || 'jam',
      renotify: true,
      silent: !!silent,
      icon: ICON,
      badge: BADGE,
      data: { channel: channel, n: n },
    },
  };
}

// jamPref is key from IndexedDB "jam", store "prefs" (sounds.js writes
// "sounds", this device's settings, and "chimer", when a page last could
// chime), or null.
function jamPref(key) {
  return new Promise((resolve) => {
    try {
      const req = indexedDB.open('jam', 1);
      req.onupgradeneeded = () => req.result.createObjectStore('prefs');
      req.onerror = () => resolve(null);
      req.onsuccess = () => {
        try {
          const get = req.result.transaction('prefs').objectStore('prefs').get(key);
          get.onsuccess = () => { resolve(get.result || null); req.result.close(); };
          get.onerror = () => { resolve(null); req.result.close(); };
        } catch (_) { resolve(null); }
      };
    } catch (_) { resolve(null); }
  });
}

// jamSilent reports whether a push of kind should make no sound: a Jam page
// that can chime (its audio unlocked) said so in the last 90 s — chimerAt,
// from sounds.js — so it plays its own chime; or this device turned that
// kind's sound Off. Otherwise the OS plays its notification sound.
function jamSilent(kind, prefs, chimerAt, now) {
  if (typeof chimerAt === 'number' && now - chimerAt < 90000) return true;
  const k = kind === 'needs' ? 'needs' : 'messages';
  return !!(prefs && prefs[k] === 'off');
}

// jamFallback is the plain notification shown when building the real one
// failed: a push always ends in a notification.
function jamFallback(p) {
  return { title: 'Jam', options: { body: 'New message', icon: ICON, badge: BADGE, data: { channel: jamStr(p && p.channel), n: 1 } } };
}

// jamArea is the Jam area a window shows: 'next' for the Jam app
// ('/app/next/'), else 'user'.
function jamArea(url) {
  try { return new URL(url).pathname.startsWith('/app/next/') ? 'next' : 'user'; } catch (_) { return 'user'; }
}

// jamTarget is the URL that opens conversation channel in area: the Jam app
// ('next') or the Inbox (the default, until the app replaces it).
function jamTarget(channel, area) {
  if (area === 'next') return channel ? '/app/next/c/' + encodeURIComponent(channel) : '/app/next/';
  return channel ? '/app/user/?c=' + encodeURIComponent(channel) : '/app/user/';
}

// jamPickClient is the index of the window (by URL) a notification click
// should reuse — the first on the Jam app ('/app/next/') or the Inbox
// ('/app/user/') — or -1 to open a new one. An /app/admin/ (or any other)
// window is never navigated away.
function jamPickClient(urls) {
  for (let i = 0; i < urls.length; i++) {
    try {
      const p = new URL(urls[i]).pathname;
      if (p.startsWith('/app/next/') || p.startsWith('/app/user/')) return i;
    } catch (_) { /* not a URL: skip */ }
  }
  return -1;
}

self.addEventListener('push', (e) => {
  let p = {};
  try { p = jamPayload(e.data ? e.data.json() : {}); } catch (_) { p = { body: e.data ? e.data.text() : '' }; }
  e.waitUntil((async () => {
    try {
      const open = await self.registration.getNotifications({ tag: jamStr(p.channel) || 'jam' });
      const prev = open.length ? open[open.length - 1].data : null;
      const silent = jamSilent(jamStr(p.kind), await jamPref('sounds'), await jamPref('chimer'), Date.now());
      const nt = jamNotice(p, prev, silent);
      await self.registration.showNotification(nt.title, nt.options);
    } catch (_) {
      const nt = jamFallback(p);
      await self.registration.showNotification(nt.title, nt.options);
    }
    if (typeof p.unread === 'number' && self.navigator.setAppBadge) {
      try { await self.navigator.setAppBadge(p.unread); } catch (_) { /* unsupported here */ }
    }
  })());
});

self.addEventListener('notificationclick', (e) => {
  e.notification.close();
  const channel = jamStr(e.notification.data && e.notification.data.channel);
  e.waitUntil((async () => {
    const all = await self.clients.matchAll({ type: 'window', includeUncontrolled: true });
    const i = jamPickClient(all.map((c) => c.url));
    if (i >= 0) {
      const c = all[i];
      try {
        if ('focus' in c) await c.focus();
        if (!('navigate' in c)) return;
        await c.navigate(jamTarget(channel, jamArea(c.url)));
        return;
      } catch (_) { /* uncontrolled or gone: open instead */ }
    }
    await self.clients.openWindow(jamTarget(channel));
  })());
});
