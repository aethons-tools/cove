// Jam push notifications: the principal menu's per-device Notifications
// toggle (#push-toggle). On subscribes this browser with the push service
// under Jam's VAPID key and stores the subscription with Jam; Off
// unsubscribes and deletes it. The server only renders the item for a member
// when push is configured.
(function () {
  'use strict';
  var btn = document.getElementById('push-toggle');
  if (!btn) return;

  function standalone() {
    return navigator.standalone === true ||
      (window.matchMedia && window.matchMedia('(display-mode: standalone)').matches);
  }
  function ios() {
    return /iPad|iPhone|iPod/.test(navigator.userAgent) ||
      (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1);
  }

  if (!('serviceWorker' in navigator && 'PushManager' in window && 'Notification' in window)) {
    // iOS offers Web Push only to the Home Screen app; elsewhere there is
    // nothing this device can do, so the item goes away.
    if (ios() && !standalone()) {
      btn.textContent = 'Add Jam to your Home Screen to get notifications';
      btn.disabled = true;
      btn.hidden = false;
    }
    return;
  }

  var on = false;
  var busy = false;

  function reflect() {
    btn.hidden = false;
    btn.setAttribute('aria-pressed', on ? 'true' : 'false');
    if (Notification.permission === 'denied') {
      btn.textContent = 'Notifications: Blocked in browser settings';
      btn.disabled = true;
      return;
    }
    btn.textContent = on ? 'Notifications: On' : 'Notifications: Off';
    btn.disabled = busy;
  }

  // urlBase64ToUint8Array decodes the VAPID public key for subscribe().
  function urlBase64ToUint8Array(s) {
    var b64 = (s + '='.repeat((4 - s.length % 4) % 4)).replace(/-/g, '+').replace(/_/g, '/');
    var raw = atob(b64);
    var out = new Uint8Array(raw.length);
    for (var i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i);
    return out;
  }

  async function send(method, body) {
    var r = await fetch('/app/user/push/subscriptions', {
      method: method,
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body)
    });
    if (!r.ok) throw new Error(method + ' subscription: ' + r.status);
    markSynced(method === 'POST' ? body.endpoint : '');
  }

  // The endpoint this page session last registered for this user, so the
  // load-time re-send happens once per session, not on every page.
  var syncKey = 'jam.push.synced:' + (btn.dataset.user || '');
  function synced() {
    try { return sessionStorage.getItem(syncKey); } catch (e) { return null; }
  }
  function markSynced(endpoint) {
    try {
      if (endpoint) sessionStorage.setItem(syncKey, endpoint);
      else sessionStorage.removeItem(syncKey);
    } catch (e) { /* storage off: re-send next page */ }
  }

  function subJSON(s) {
    return typeof s.toJSON === 'function' ? s.toJSON() : s;
  }

  async function fetchKey() {
    var r = await fetch('/app/user/push/key', { credentials: 'same-origin' });
    if (!r.ok) throw new Error('push key: ' + r.status);
    return (await r.json()).key;
  }

  // sameKey reports whether s was made under key (a subscription that can't
  // say is taken to match).
  function sameKey(s, key) {
    var have = s.options && s.options.applicationServerKey;
    if (!have) return true;
    var a = new Uint8Array(have), b = urlBase64ToUint8Array(key);
    if (a.length !== b.length) return false;
    for (var i = 0; i < a.length; i++) if (a[i] !== b[i]) return false;
    return true;
  }

  // drop unsubscribes s and tells Jam (best effort: the push service's 410
  // prunes a row Jam missed).
  async function drop(s) {
    var endpoint = s.endpoint;
    await s.unsubscribe();
    try { await send('DELETE', { endpoint: endpoint }); } catch (e) { /* pruned later */ }
  }

  async function subscribe(reg, key) {
    var s = await reg.pushManager.subscribe({
      userVisibleOnly: true,
      applicationServerKey: urlBase64ToUint8Array(key)
    });
    await send('POST', subJSON(s));
    on = true;
  }

  async function turnOn() {
    var perm = await Notification.requestPermission();
    if (perm !== 'granted') return;
    var key = await fetchKey();
    var reg = await navigator.serviceWorker.ready;
    // subscribe() refuses while a subscription under another key exists.
    var old = await reg.pushManager.getSubscription();
    if (old) await drop(old);
    await subscribe(reg, key);
  }

  async function turnOff() {
    var reg = await navigator.serviceWorker.ready;
    var s = await reg.pushManager.getSubscription();
    on = false;
    if (s) {
      var endpoint = s.endpoint;
      await s.unsubscribe();
      await send('DELETE', { endpoint: endpoint });
    }
  }

  btn.addEventListener('click', async function () {
    if (busy) return;
    busy = true;
    btn.title = '';
    reflect();
    try {
      await (on ? turnOff() : turnOn());
    } catch (e) {
      btn.title = 'Could not change notifications for this device';
    }
    busy = false;
    reflect();
  });

  // Reflect this device's state. A live subscription is re-sent, so the
  // endpoint belongs to whoever is signed in here now (a device that
  // switched users moves to the new one); one made under a since-rotated
  // VAPID key is replaced.
  (async function () {
    busy = true;
    reflect();
    try {
      var reg = await navigator.serviceWorker.ready;
      var s = await reg.pushManager.getSubscription();
      if (s && Notification.permission === 'granted') {
        // Once per page session per user: later pages trust the first.
        if (synced() === s.endpoint) {
          on = true;
          return;
        }
        var key = await fetchKey();
        if (sameKey(s, key)) {
          await send('POST', subJSON(s));
          on = true;
        } else {
          await drop(s);
          await subscribe(reg, key);
        }
      }
    } catch (e) { /* leave it Off */ } finally {
      busy = false;
      reflect();
    }
  })();
})();
