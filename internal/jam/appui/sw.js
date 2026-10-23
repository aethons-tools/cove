// Jam app service worker: makes the app installable and shows Web Push
// notifications. It caches nothing and has no fetch handler, so every
// request goes straight to the network.
self.addEventListener('install', () => self.skipWaiting());
self.addEventListener('activate', (e) => e.waitUntil(self.clients.claim()));

const ICON = '/app/static/icons/icon-192.png';
const BADGE = '/app/static/icons/favicon-32.png';

// jamNotice is the notification to show for push payload p (title, body,
// channel, id, unread) given prev, the data of the notification already
// showing for that conversation (null: none). A conversation keeps one
// notification: a newer push replaces it, counting the messages it stands
// for.
function jamNotice(p, prev) {
  const n = ((prev && prev.n) || 0) + 1;
  const body = n > 1 ? n + ' new messages · latest: ' + (p.body || '') : (p.body || '');
  return {
    title: p.title || 'Jam',
    options: {
      body: body,
      tag: p.channel || 'jam',
      renotify: true,
      icon: ICON,
      badge: BADGE,
      data: { channel: p.channel || '', n: n },
    },
  };
}

// jamTarget is the Inbox URL that opens conversation channel.
function jamTarget(channel) {
  return channel ? '/app/user/?c=' + encodeURIComponent(channel) : '/app/user/';
}

self.addEventListener('push', (e) => {
  let p = {};
  try { p = e.data ? e.data.json() : {}; } catch (_) { p = { body: e.data ? e.data.text() : '' }; }
  e.waitUntil((async () => {
    const tag = p.channel || 'jam';
    const open = await self.registration.getNotifications({ tag: tag });
    const prev = open.length ? open[open.length - 1].data : null;
    const nt = jamNotice(p, prev);
    await self.registration.showNotification(nt.title, nt.options);
    if (typeof p.unread === 'number' && self.navigator.setAppBadge) {
      try { await self.navigator.setAppBadge(p.unread); } catch (_) { /* unsupported here */ }
    }
  })());
});

self.addEventListener('notificationclick', (e) => {
  e.notification.close();
  const url = jamTarget(e.notification.data && e.notification.data.channel);
  e.waitUntil((async () => {
    const all = await self.clients.matchAll({ type: 'window', includeUncontrolled: true });
    for (const c of all) {
      if (new URL(c.url).pathname.startsWith('/app/') && 'focus' in c) {
        await c.focus();
        if ('navigate' in c) {
          try { await c.navigate(url); return; } catch (_) { /* uncontrolled: open instead */ }
        } else {
          return;
        }
      }
    }
    await self.clients.openWindow(url);
  })());
});
