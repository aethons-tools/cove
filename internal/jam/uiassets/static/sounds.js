// Jam notification sounds: the principal menu's per-device Sounds settings
// (#sounds-toggle) and the chimes the Inbox plays while a Jam window is open.
// Two kinds — "messages" (a new message) and "needs" (something needs you:
// an ask, a call-in, an escalation) — each Off or one of a few synthesized
// chimes, with one volume. Settings live in this browser: localStorage for
// the page, and IndexedDB for the service worker, which silences a push when
// a Jam window already chimed or the kind's sound is Off (a web push can only
// play the OS's sound or none).
(function () {
  'use strict';
  var KEY = 'jam-sounds';
  var DEFAULTS = { messages: 'pop', needs: 'chime', volume: 0.6 };
  var CHIMES = {
    // name: [[frequency Hz, start s, duration s], …], an exponential decay each
    pop: [[880, 0, 0.12]],
    chime: [[660, 0, 0.35], [990, 0.12, 0.45]],
    ping: [[1320, 0, 0.25]],
    bell: [[523, 0, 0.6], [784, 0, 0.6], [1046, 0.02, 0.5]],
  };

  function load() {
    try {
      var s = JSON.parse(localStorage.getItem(KEY) || '{}');
      return {
        messages: s.messages in CHIMES || s.messages === 'off' ? s.messages : DEFAULTS.messages,
        needs: s.needs in CHIMES || s.needs === 'off' ? s.needs : DEFAULTS.needs,
        volume: typeof s.volume === 'number' && s.volume >= 0 && s.volume <= 1 ? s.volume : DEFAULTS.volume,
      };
    } catch (_) { return Object.assign({}, DEFAULTS); }
  }

  // put writes value under key in IndexedDB "jam"/"prefs", where the service
  // worker reads it.
  function put(key, value) {
    try {
      var req = indexedDB.open('jam', 1);
      req.onupgradeneeded = function () { req.result.createObjectStore('prefs'); };
      req.onsuccess = function () {
        try {
          var tx = req.result.transaction('prefs', 'readwrite');
          tx.objectStore('prefs').put(value, key);
          tx.oncomplete = function () { req.result.close(); };
        } catch (_) { req.result.close(); }
      };
    } catch (_) {}
  }

  // store saves the settings for the page and for the service worker.
  function store(s) {
    try { localStorage.setItem(KEY, JSON.stringify(s)); } catch (_) {}
    put('sounds', s);
  }

  // While this page can actually chime (its audio unlocked), it says so every
  // 20 s; the service worker silences a push only while such a heartbeat is
  // fresh, so a frozen or never-clicked tab never swallows a push's sound.
  var beating = false;
  function heartbeat() {
    if (beating) return;
    beating = true;
    var beat = function () { if (ctx && ctx.state === 'running') put('chimer', Date.now()); };
    beat();
    setInterval(beat, 20000);
  }

  var ctx = null;
  function audio() {
    if (!ctx) {
      var AC = window.AudioContext || window.webkitAudioContext;
      if (!AC) return null;
      ctx = new AC();
    }
    return ctx;
  }
  // Browsers start audio only after a user gesture: unlock on the first one.
  ['pointerdown', 'keydown'].forEach(function (ev) {
    document.addEventListener(ev, function () {
      var ac = audio();
      if (ac && ac.state !== 'running') ac.resume().then(heartbeat).catch(function () {});
      else if (ac) heartbeat();
    }, { once: true, capture: true });
  });

  // tone plays chime name at volume, only if audio is running now — a chime
  // scheduled on a locked context would play late, in a burst, on the first
  // click.
  function tone(name, volume) {
    var notes = CHIMES[name];
    var ac = notes && volume > 0 && audio();
    if (!ac || ac.state !== 'running') return false;
    var now = ac.currentTime;
    notes.forEach(function (n) {
      var osc = ac.createOscillator(), gain = ac.createGain();
      osc.type = 'sine';
      osc.frequency.value = n[0];
      gain.gain.setValueAtTime(0.0001, now + n[1]);
      gain.gain.exponentialRampToValueAtTime(0.35 * volume, now + n[1] + 0.01);
      gain.gain.exponentialRampToValueAtTime(0.0001, now + n[1] + n[2]);
      osc.connect(gain).connect(ac.destination);
      osc.start(now + n[1]);
      osc.stop(now + n[1] + n[2] + 0.05);
    });
    return true;
  }

  // recent reports whether any Jam tab in this browser chimed in the last
  // second, and at which priority (a Needs you chime is never held back by a
  // Messages one).
  function recent(kind) {
    try {
      var l = JSON.parse(localStorage.getItem(KEY + '-last') || '{}');
      if (Date.now() - (l.at || 0) >= 1000) return false;
      return !(kind === 'needs' && l.kind !== 'needs');
    } catch (_) { return false; }
  }
  // jamSound.play plays kind's chime ("messages" or "needs") — at most one a
  // second across this browser's Jam tabs; it reports whether one played.
  window.jamSound = {
    play: function (kind) {
      var s = load(), name = s[kind];
      if (!name || name === 'off' || recent(kind)) return false;
      if (!tone(name, s.volume)) return false;
      try { localStorage.setItem(KEY + '-last', JSON.stringify({ at: Date.now(), kind: kind })); } catch (_) {}
      return true;
    },
    settings: load,
  };
  store(load()); // the service worker reads the current settings

  // The menu item and its panel.
  var btn = document.getElementById('sounds-toggle');
  if (!btn) return;
  var panel = document.getElementById('sounds-panel');
  btn.hidden = false;
  btn.addEventListener('click', function () {
    panel.hidden = !panel.hidden;
    btn.setAttribute('aria-expanded', String(!panel.hidden));
  });
  function opts(sel, value) {
    sel.innerHTML = '';
    ['off'].concat(Object.keys(CHIMES)).forEach(function (n) {
      var o = document.createElement('option');
      o.value = n; o.textContent = n === 'off' ? 'Off' : n[0].toUpperCase() + n.slice(1);
      if (n === value) o.selected = true;
      sel.appendChild(o);
    });
  }
  var s = load();
  var msg = panel.querySelector('[name=messages]'), needs = panel.querySelector('[name=needs]'), vol = panel.querySelector('[name=volume]');
  opts(msg, s.messages); opts(needs, s.needs); vol.value = String(Math.round(s.volume * 100));
  function save() {
    store({ messages: msg.value, needs: needs.value, volume: Number(vol.value) / 100 });
  }
  [msg, needs, vol].forEach(function (el) { el.addEventListener('change', save); });
  panel.querySelectorAll('[data-preview]').forEach(function (b) {
    b.addEventListener('click', function () {
      var sel = b.dataset.preview === 'needs' ? needs : msg;
      var ac = audio(); // a click: audio may start now
      var go = function () { tone(sel.value, Number(vol.value) / 100); heartbeat(); };
      if (ac && ac.state !== 'running') ac.resume().then(go).catch(function () {}); else go();
    });
  });
})();
