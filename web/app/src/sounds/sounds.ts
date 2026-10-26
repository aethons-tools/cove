// Jam notification sounds: the chimes the app plays while a Jam window is
// open, and their per-device settings. A port of internal/jam/uiassets/
// static/sounds.js that keeps its storage contract: localStorage "jam-sounds"
// (settings) and "jam-sounds-last" (the last chime, across tabs) for pages,
// and IndexedDB "jam"/"prefs" ("sounds", "chimer") for the service worker,
// which silences a push when a Jam window already chimed or the kind's sound
// is Off (a web push can only play the OS's sound or none).

export const CHIMES: Record<string, [number, number, number][]> = {
  // name: [[frequency Hz, start s, duration s], …], an exponential decay each
  pop: [[880, 0, 0.12]],
  chime: [[660, 0, 0.35], [990, 0.12, 0.45]],
  ping: [[1320, 0, 0.25]],
  bell: [[523, 0, 0.6], [784, 0, 0.6], [1046, 0.02, 0.5]],
};

export type SoundKind = "messages" | "needs";
export type SoundSettings = { messages: string; needs: string; volume: number };

const KEY = "jam-sounds";
const DEFAULTS: SoundSettings = { messages: "pop", needs: "chime", volume: 0.6 };
const valid = (v: unknown) => typeof v === "string" && (v in CHIMES || v === "off");

export function loadSounds(): SoundSettings {
  try {
    const s = JSON.parse(localStorage.getItem(KEY) ?? "{}") as Partial<Record<keyof SoundSettings, unknown>>;
    return {
      messages: valid(s.messages) ? (s.messages as string) : DEFAULTS.messages,
      needs: valid(s.needs) ? (s.needs as string) : DEFAULTS.needs,
      volume: typeof s.volume === "number" && s.volume >= 0 && s.volume <= 1 ? s.volume : DEFAULTS.volume,
    };
  } catch {
    return { ...DEFAULTS };
  }
}

// put writes value under key in IndexedDB "jam"/"prefs", where the service
// worker reads it; any failure (no IndexedDB, private mode) is ignored.
function put(key: string, value: unknown) {
  try {
    const req = indexedDB.open("jam", 1);
    req.onupgradeneeded = () => { req.result.createObjectStore("prefs"); };
    req.onsuccess = () => {
      try {
        const tx = req.result.transaction("prefs", "readwrite");
        tx.objectStore("prefs").put(value, key);
        tx.oncomplete = () => req.result.close();
      } catch { req.result.close(); }
    };
  } catch { /* no IndexedDB */ }
}

// storeSounds saves the settings for the page and for the service worker.
export function storeSounds(s: SoundSettings) {
  try { localStorage.setItem(KEY, JSON.stringify(s)); } catch { /* storage off */ }
  put("sounds", s);
}

let ctx: AudioContext | null = null;
function audio(): AudioContext | null {
  if (!ctx) {
    const AC = window.AudioContext ?? (window as unknown as { webkitAudioContext?: typeof AudioContext }).webkitAudioContext;
    if (!AC) return null;
    ctx = new AC();
  }
  return ctx;
}

// While this page can actually chime (its audio unlocked), it says so every
// 20 s; the service worker silences a push only while such a heartbeat is
// fresh, so a frozen or never-clicked tab never swallows a push's sound.
let beating = false;
function heartbeat() {
  if (beating) return;
  beating = true;
  const beat = () => { if (ctx && ctx.state === "running") put("chimer", Date.now()); };
  beat();
  setInterval(beat, 20000);
}

// tone plays chime name at volume, only if audio is running now — a chime
// scheduled on a locked context would play late, in a burst, on the first
// click.
function tone(name: string, volume: number): boolean {
  const notes = CHIMES[name];
  const ac = notes && volume > 0 ? audio() : null;
  if (!notes || !ac || ac.state !== "running") return false;
  const now = ac.currentTime;
  for (const [freq, start, dur] of notes) {
    const osc = ac.createOscillator(), gain = ac.createGain();
    osc.type = "sine";
    osc.frequency.value = freq;
    gain.gain.setValueAtTime(0.0001, now + start);
    gain.gain.exponentialRampToValueAtTime(0.35 * volume, now + start + 0.01);
    gain.gain.exponentialRampToValueAtTime(0.0001, now + start + dur);
    osc.connect(gain).connect(ac.destination);
    osc.start(now + start);
    osc.stop(now + start + dur + 0.05);
  }
  return true;
}

// recent reports whether any Jam tab in this browser chimed in the last
// second, and at which priority (a Needs you chime is never held back by a
// Messages one).
function recent(kind: SoundKind): boolean {
  try {
    const l = JSON.parse(localStorage.getItem(KEY + "-last") ?? "{}") as { at?: number; kind?: string };
    if (Date.now() - (l.at ?? 0) >= 1000) return false;
    return !(kind === "needs" && l.kind !== "needs");
  } catch { return false; }
}

// play plays kind's chime — at most one a second across this browser's Jam
// tabs; it reports whether one played.
export function play(kind: SoundKind): boolean {
  const s = loadSounds(), name = s[kind];
  if (!name || name === "off" || recent(kind)) return false;
  if (!tone(name, s.volume)) return false;
  try { localStorage.setItem(KEY + "-last", JSON.stringify({ at: Date.now(), kind })); } catch { /* storage off */ }
  return true;
}

// preview plays name at volume now (a click: audio may start here).
export function preview(name: string, volume: number) {
  const ac = audio();
  const go = () => { tone(name, volume); heartbeat(); };
  if (ac && ac.state !== "running") ac.resume().then(go).catch(() => {}); else go();
}

// initSounds unlocks audio on the first gesture (browsers start audio only
// after one) and publishes the current settings to the service worker.
export function initSounds() {
  for (const ev of ["pointerdown", "keydown"]) {
    document.addEventListener(ev, () => {
      const ac = audio();
      if (ac && ac.state !== "running") ac.resume().then(heartbeat).catch(() => {});
      else if (ac) heartbeat();
    }, { once: true, capture: true });
  }
  storeSounds(loadSounds());
}
