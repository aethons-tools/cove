import { Excalidraw, reconcileElements, restoreElements, CaptureUpdateAction } from "@excalidraw/excalidraw";
import type { ExcalidrawImperativeAPI } from "@excalidraw/excalidraw/types";
import "@excalidraw/excalidraw/index.css";
import { Loader, Text } from "@mantine/core";
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { MAX_WAIT, nextRetry, POLL, SEND_DELAY, snapshot } from "./sync";
import { loadElements, saveElements } from "./api";

type Scene = ReturnType<ExcalidrawImperativeAPI["getSceneElementsIncludingDeleted"]>;
type Remote = Parameters<typeof reconcileElements>[1];
type Restored = ReturnType<typeof restoreElements>;
interface Root extends HTMLDivElement { jamElementCount?: () => number }

// BoardEditor is a board in Excalidraw (a lazy chunk), synced with Jam by
// element as the old whiteboard bundle was: edits send only the elements whose
// version changed (debounced; one request in flight); Jam merges them and
// answers with the stored winners of any that lost, reconciled back in.
// Other people's and agents' edits arrive as jam-board window events (the
// events stream's board signal), with a 30 s poll behind them.
export default function BoardEditor({ id, readOnly }: { id: string; readOnly: boolean }) {
  const [api, setApi] = useState<ExcalidrawImperativeAPI | null>(null);
  const [initial, setInitial] = useState<{ elements: Restored; scrollToContent: boolean } | null>(null);
  const [error, setError] = useState("");
  const root = useRef<Root>(null);
  const apiRef = useRef<ExcalidrawImperativeAPI | null>(null);
  const sent = useRef(new Map<string, number>()); // element id → version Jam already has
  const seq = useRef(0);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const firstChange = useRef(0);
  const inFlight = useRef(false);
  const dirty = useRef(false);
  const retry = useRef(1000);
  // mounted: false from the moment the editor starts to go — before
  // Excalidraw's own teardown, which empties its scene — so a save sent on
  // leaving uses latest, the scene as it last was.
  const mounted = useRef(true);
  const latest = useRef<Scene>([]);
  const leftTries = useRef(0); // saves retried after leaving

  const remember = (els: readonly { id: string; version: number }[]) => els.forEach((e) => sent.current.set(e.id, e.version));

  const fetchSince = useCallback(async () => {
    const b = await loadElements(id, seq.current);
    seq.current = b.seq;
    const els = restoreElements(b.elements as Remote, null);
    remember(els);
    return els;
  }, [id]);

  useEffect(() => {
    fetchSince().then((els) => setInitial({ elements: els, scrollToContent: true }), (e: unknown) => setError(String((e as Error).message ?? e)));
  }, [fetchSince]);

  // Merge remote elements (pushed, polled, or merge losers) into the scene.
  const apply = useCallback((remote: Restored) => {
    const a = apiRef.current;
    if (!a || remote.length === 0) return;
    const merged = reconcileElements(a.getSceneElementsIncludingDeleted(), remote as unknown as Remote, a.getAppState());
    a.updateScene({ elements: merged, captureUpdate: CaptureUpdateAction.NEVER });
  }, []);

  useEffect(() => {
    if (!api) return;
    apiRef.current = api;
    if (root.current) root.current.jamElementCount = () => api.getSceneElements().length; // tests and debugging
    const pull = () => { fetchSince().then(apply, () => {}); };
    const onPush = (ev: Event) => { if ((ev as CustomEvent<string>).detail === id) pull(); };
    window.addEventListener("jam-board", onPush);
    const poll = setInterval(pull, POLL);
    return () => { window.removeEventListener("jam-board", onPush); clearInterval(poll); };
  }, [api, id, fetchSince, apply]);

  // Saving: one POST in flight at a time; each sends what Jam lacks,
  // snapshotted before the request. A failure retries with backoff.
  const send = useCallback(async () => {
    const a = apiRef.current;
    timer.current = null;
    if (!a) return;
    const scene = () => (mounted.current ? a.getSceneElementsIncludingDeleted() : latest.current);
    if (inFlight.current) { dirty.current = true; return; }
    inFlight.current = true;
    dirty.current = false;
    let failed = false;
    try {
      const s = snapshot(scene(), sent.current);
      if (s.tooLarge) setError("a drawing is too large to save; split it into smaller strokes");
      for (const batch of s.batches) {
        const res = await saveElements(id, batch);
        batch.forEach((b) => sent.current.set(b.id, b.version));
        const lost = restoreElements(res.lost as Remote, null);
        if (mounted.current) apply(lost);
        // Whatever the scene now holds for a lost element counts as settled,
        // so it isn't re-sent (and lost again) until it changes.
        const now = new Map(scene().map((e) => [e.id, e.version]));
        lost.forEach((e) => sent.current.set(e.id, now.get(e.id) ?? e.version));
      }
      retry.current = 1000;
      setError((e) => (e.startsWith("save failed") || e.startsWith("Failed") ? "" : e));
    } catch (e) {
      setError(String((e as Error).message ?? e));
      dirty.current = failed = true;
      retry.current = nextRetry(retry.current);
    } finally {
      inFlight.current = false;
      // After leaving, a failing save gets a few more tries, not forever.
      const giveUp = failed && !mounted.current && ++leftTries.current > 3;
      if (dirty.current && !timer.current && !giveUp) timer.current = setTimeout(() => void send(), failed ? retry.current : SEND_DELAY);
    }
  }, [id, apply]);

  // Debounced: SEND_DELAY after the last change, but at least every MAX_WAIT
  // during a long drag.
  const onChange = () => {
    if (readOnly || !apiRef.current) return;
    latest.current = apiRef.current.getSceneElementsIncludingDeleted();
    const now = Date.now();
    if (!timer.current) firstChange.current = now;
    if (timer.current) clearTimeout(timer.current);
    const wait = now - firstChange.current >= MAX_WAIT ? 0 : SEND_DELAY;
    timer.current = setTimeout(() => void send(), wait);
  };

  // Leaving: warn before unloading with edits not yet saved.
  useEffect(() => {
    if (readOnly) return;
    const warn = (ev: BeforeUnloadEvent) => {
      const a = apiRef.current;
      if (!a) return;
      const unsaved = timer.current || inFlight.current || a.getSceneElementsIncludingDeleted().some((e) => sent.current.get(e.id) !== e.version);
      if (unsaved) { ev.preventDefault(); ev.returnValue = ""; }
    };
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [readOnly]);

  // On an in-app navigation (unmount), send a pending save now rather than
  // drop it. A layout-effect cleanup runs before Excalidraw's teardown, while
  // its scene is still whole.
  useLayoutEffect(() => {
    mounted.current = true; // again, after a StrictMode remount
    return () => {
      const a = apiRef.current;
      if (a) latest.current = a.getSceneElementsIncludingDeleted();
      mounted.current = false;
      if (readOnly) return;
      if (timer.current) { clearTimeout(timer.current); timer.current = null; void send(); }
    };
  }, [readOnly, send]);

  if (error && !initial) return <Text c="red" m="md">{error}</Text>;
  if (!initial) return <Loader m="md" size="sm" />;
  return (
    <div ref={root} data-board-editor style={{ position: "absolute", inset: 0 }}>
      {error && <Text c="red" size="sm" style={{ position: "absolute", top: 8, left: "50%", transform: "translateX(-50%)", zIndex: 10 }}>{error}</Text>}
      {/* Keep Excalidraw's built-in embeddable allowlist (no validateEmbeddable
          override): this editor runs on the Jam UI origin, and a board element
          that could embed a preview host would pass the frame exchange's
          same-site Referer check (docs/usage/jam/previews.md, Framing). */}
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
