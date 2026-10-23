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
function jamNotice(p, prev) {
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
      icon: ICON,
      badge: BADGE,
      data: { channel: channel, n: n },
    },
  };
}

// jamFallback is the plain notification shown when building the real one
// failed: a push always ends in a notification.
function jamFallback(p) {
  return { title: 'Jam', options: { body: 'New message', icon: ICON, badge: BADGE, data: { channel: jamStr(p && p.channel), n: 1 } } };
}

// jamTarget is the Inbox URL that opens conversation channel.
function jamTarget(channel) {
  return channel ? '/app/user/?c=' + encodeURIComponent(channel) : '/app/user/';
}

// jamPickClient is the index of the window (by URL) a notification click
// should reuse — the first on the Inbox ('/app/user/') — or -1 to open a
// new one. An /app/admin/ (or any other) window is never navigated away.
function jamPickClient(urls) {
  for (let i = 0; i < urls.length; i++) {
    try {
      if (new URL(urls[i]).pathname.startsWith('/app/user/')) return i;
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
      const nt = jamNotice(p, prev);
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
  const url = jamTarget(jamStr(e.notification.data && e.notification.data.channel));
  e.waitUntil((async () => {
    const all = await self.clients.matchAll({ type: 'window', includeUncontrolled: true });
    const i = jamPickClient(all.map((c) => c.url));
    if (i >= 0) {
      const c = all[i];
      try {
        if ('focus' in c) await c.focus();
        if (!('navigate' in c)) return;
        await c.navigate(url);
        return;
      } catch (_) { /* uncontrolled or gone: open instead */ }
    }
    await self.clients.openWindow(url);
  })());
});
