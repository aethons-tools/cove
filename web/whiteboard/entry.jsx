// The /app/user whiteboard editor (docs/usage/jam/whiteboards.md): Excalidraw
// mounted on a board, synced with Jam by element. Edits send only the
// elements whose version changed (debounced); Jam merges them per element and
// answers with the stored winners of any that lost, which are reconciled back
// in. Other people's and agents' edits arrive as `board` events on the page's
// live stream (window "jam-board" events), with a 30s poll as the fallback.
import React, { useEffect, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import { Excalidraw, reconcileElements, restoreElements, CaptureUpdateAction } from "@excalidraw/excalidraw";
import "@excalidraw/excalidraw/index.css";

const SEND_DELAY = 300; // ms after the last change
const MAX_WAIT = 1000; // ms: save at least this often during a long drag
const MAX_ELEMENT = 60000; // bytes: Jam's per-element cap is 64 KB
const POLL = 30000; // ms: catch up if a live push was missed

function Board({ el, base, boardId, readOnly }) {
  const [api, setApi] = useState(null);
  const [initial, setInitial] = useState(null);
  const [error, setError] = useState("");
  const sent = useRef(new Map()); // element id → version Jam already has
  const seq = useRef(0);
  const timer = useRef(null);
  const url = `${base}/boards/${boardId}/elements`;

  // Note what Jam has, so only newer versions are sent.
  const remember = (els) => els.forEach((e) => sent.current.set(e.id, e.version));

  const fetchSince = async () => {
    const r = await fetch(`${url}?since=${seq.current}`, { credentials: "same-origin" });
    if (!r.ok) throw new Error(`load failed (${r.status})`);
    const body = await r.json();
    seq.current = body.seq;
    const els = restoreElements(body.elements, null);
    remember(els);
    return els;
  };

  useEffect(() => {
    fetchSince().then((els) => setInitial({ elements: els, scrollToContent: true })).catch((e) => setError(String(e.message || e)));
  }, [boardId]);

  // Merge remote elements (pushed, polled, or merge losers) into the scene.
  const apply = (remote) => {
    if (!api || remote.length === 0) return;
    const merged = reconcileElements(api.getSceneElementsIncludingDeleted(), remote, api.getAppState());
    api.updateScene({ elements: merged, captureUpdate: CaptureUpdateAction.NEVER });
  };

  useEffect(() => {
    if (!api) return;
    el.jamElementCount = () => api.getSceneElements().length; // tests and debugging
    const pull = () => fetchSince().then(apply).catch(() => {});
    const onPush = (ev) => { if (ev.detail === boardId) pull(); };
    window.addEventListener("jam-board", onPush);
    const poll = setInterval(pull, POLL);
    return () => { window.removeEventListener("jam-board", onPush); clearInterval(poll); };
  }, [api, boardId]);

  // Saving: one POST in flight at a time; each sends the elements whose
  // version Jam doesn't have, snapshotted (id, version, JSON) before the
  // request — Excalidraw mutates elements in place, so a later edit is never
  // mistaken for one already saved. A failure retries with backoff.
  const inFlight = useRef(false);
  const dirty = useRef(false);
  const retry = useRef(1000);
  const send = async () => {
    timer.current = null;
    if (inFlight.current) { dirty.current = true; return; }
    inFlight.current = true;
    dirty.current = false;
    let failed = false;
    try {
      const changed = api.getSceneElementsIncludingDeleted().filter((e) => sent.current.get(e.id) !== e.version);
      const snap = [];
      for (const e of changed) {
        const json = JSON.stringify(e);
        if (json.length > MAX_ELEMENT) { // Jam refuses it; never let it block the rest
          sent.current.set(e.id, e.version);
          setError("a drawing is too large to save; split it into smaller strokes");
          continue;
        }
        snap.push({ id: e.id, version: e.version, json });
      }
      for (let i = 0; i < snap.length; i += 500) {
        const batch = snap.slice(i, i + 500);
        const r = await fetch(url, {
          method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json" },
          body: '{"elements":[' + batch.map((b) => b.json).join(",") + "]}",
        });
        if (!r.ok) throw new Error(`save failed (${r.status})`);
        batch.forEach((b) => sent.current.set(b.id, b.version));
        const body = await r.json();
        const lost = restoreElements(body.lost, null);
        remember(lost);
        apply(lost);
      }
      retry.current = 1000;
      setError((e) => (e.startsWith("save failed") || e.startsWith("Failed") ? "" : e));
    } catch (e) {
      setError(String(e.message || e));
      dirty.current = failed = true;
      retry.current = Math.min(retry.current * 2, 30000);
    } finally {
      inFlight.current = false;
      if (dirty.current && !timer.current) timer.current = setTimeout(send, failed ? retry.current : SEND_DELAY);
    }
  };

  // Debounced: SEND_DELAY after the last change, but at least every MAX_WAIT
  // during a long drag.
  const firstChange = useRef(0);
  const onChange = () => {
    if (readOnly || !api) return;
    const now = Date.now();
    if (!timer.current) firstChange.current = now;
    clearTimeout(timer.current);
    const wait = now - firstChange.current >= MAX_WAIT ? 0 : SEND_DELAY;
    timer.current = setTimeout(send, wait);
  };

  // Warn before leaving with edits not yet saved.
  useEffect(() => {
    const warn = (ev) => {
      if (!api || readOnly) return;
      const unsaved = timer.current || inFlight.current || api.getSceneElementsIncludingDeleted().some((e) => sent.current.get(e.id) !== e.version);
      if (unsaved) { ev.preventDefault(); ev.returnValue = ""; }
    };
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [api, readOnly]);

  if (error && !initial) return <div className="wb-error">{error}</div>;
  if (!initial) return <div className="wb-loading">Loading the board…</div>;
  return (
    <div className="wb-canvas">
      {error && <div className="wb-error wb-banner">{error}</div>}
      <Excalidraw
        excalidrawAPI={setApi}
        initialData={initial}
        onChange={onChange}
        viewModeEnabled={readOnly}
        UIOptions={{ tools: { image: false }, canvasActions: { loadScene: false, saveToActiveFile: false } }}
      />
    </div>
  );
}

// jamBoard.open mounts the editor for boardId in el (base: the user area's
// path, e.g. "/app/user"); it returns an unmount function.
window.jamBoard = {
  open(el, base, boardId, readOnly) {
    const root = createRoot(el);
    root.render(<Board el={el} base={base} boardId={boardId} readOnly={readOnly} />);
    return () => root.unmount();
  },
};
