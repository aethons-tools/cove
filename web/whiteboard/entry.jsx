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

const SEND_DELAY = 300; // ms: one POST per burst of edits
const POLL = 30000; // ms: catch up if a live push was missed

function Board({ base, boardId, readOnly }) {
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
    // For tests and debugging: how many live elements the editor shows.
    window.__jamBoardCount = () => api.getSceneElements().length;
    const pull = () => fetchSince().then(apply).catch(() => {});
    const onPush = (ev) => { if (ev.detail === boardId) pull(); };
    window.addEventListener("jam-board", onPush);
    const poll = setInterval(pull, POLL);
    return () => { window.removeEventListener("jam-board", onPush); clearInterval(poll); };
  }, [api, boardId]);

  const send = async () => {
    timer.current = null;
    const changed = api.getSceneElementsIncludingDeleted().filter((e) => sent.current.get(e.id) !== e.version);
    if (changed.length === 0) return;
    for (let i = 0; i < changed.length; i += 500) {
      const batch = changed.slice(i, i + 500);
      const r = await fetch(url, {
        method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ elements: batch }),
      });
      if (!r.ok) { setError(`save failed (${r.status})`); return; }
      setError("");
      remember(batch);
      const body = await r.json();
      const lost = restoreElements(body.lost, null);
      remember(lost);
      apply(lost);
    }
  };

  const onChange = () => {
    if (readOnly || !api || timer.current) return;
    timer.current = setTimeout(send, SEND_DELAY);
  };

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
    root.render(<Board base={base} boardId={boardId} readOnly={readOnly} />);
    return () => root.unmount();
  },
};
