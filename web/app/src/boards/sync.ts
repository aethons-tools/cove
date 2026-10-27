// The board sync's rules (web/whiteboard/entry.jsx, ported): Jam merges per
// element; the editor sends only versions Jam lacks, in batches, never an
// element over Jam's per-element cap (64 KB; 60 000 bytes of JSON here).
export const SEND_DELAY = 300; // ms after the last change
export const MAX_WAIT = 1000; // ms: save at least this often during a long drag
export const MAX_ELEMENT = 60000;
export const BATCH = 500;
export const POLL = 30000; // ms: catch up if a live push was missed
export const RETRY_MAX = 30000;

export interface El { id: string; version: number }
export interface Snap { id: string; version: number; json: string }

export const pending = <T extends El>(els: readonly T[], sent: Map<string, number>): T[] => els.filter((e) => sent.get(e.id) !== e.version);

// snapshot freezes what to send now (Excalidraw mutates elements in place,
// so a later edit is never mistaken for one already saved). An oversized
// element counts as sent, so it never blocks the rest.
export function snapshot(els: readonly El[], sent: Map<string, number>): { batches: Snap[][]; tooLarge: boolean } {
  const out: Snap[] = [];
  let tooLarge = false;
  for (const e of pending(els, sent)) {
    const json = JSON.stringify(e);
    if (json.length > MAX_ELEMENT) {
      sent.set(e.id, e.version);
      tooLarge = true;
      continue;
    }
    out.push({ id: e.id, version: e.version, json });
  }
  const batches: Snap[][] = [];
  for (let i = 0; i < out.length; i += BATCH) batches.push(out.slice(i, i + BATCH));
  return { batches, tooLarge };
}

export const body = (batch: Snap[]) => '{"elements":[' + batch.map((b) => b.json).join(",") + "]}";
export const nextRetry = (ms: number) => Math.min(ms * 2, RETRY_MAX);
