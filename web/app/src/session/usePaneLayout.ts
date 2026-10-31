import { useMediaQuery } from "@mantine/hooks";
import { useEffect, useRef, useState } from "react";
import { DEFAULT_SIZE, isTab, loadLayout, NARROW, saveLayout, type PaneLayout, type PaneState, type PaneTab, type Place } from "./layout";

// usePaneLayout is one session's pane layout: loaded from storage (a linked
// ?tab= opens that tab and is saved), saved on each change. Narrow windows
// put the pane below without changing the saved placement. id "" is no
// session: nothing is saved.
export function usePaneLayout(id: string, opts: { linked: string | null }): PaneState {
  const [state, setState] = useState(() => {
    const l = loadLayout(id);
    return { id, layout: isTab(opts.linked) ? { ...l, tab: opts.linked } : l };
  });
  // Another session (the view stays mounted, so the conversation does):
  // start from its own layout.
  if (state.id !== id) setState({ id, layout: loadLayout(id) });
  const layout = state.layout;
  const setLayout = (next: PaneLayout) => setState({ id, layout: next });
  const narrow = useMediaQuery(NARROW) ?? false;
  const linked = isTab(opts.linked);
  useEffect(() => {
    if (id && linked) saveLayout(id, layout);
    // Only the first layout: later changes save themselves.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  // latest is the newest layout set, for a drag's pointerup: its listener
  // was made in an earlier render, whose layout is the pre-drag one.
  const latest = useRef<{ id: string; layout: PaneLayout } | null>(null);
  const update = (next: PaneLayout, save = true) => {
    latest.current = { id, layout: next };
    setLayout(next);
    if (save) saveLayout(id, next);
  };
  const tab = layout.tab;
  const place: Place = narrow ? "below" : layout.place;
  const key = place === "right" ? "w" : "h";
  return {
    tab, place, narrow,
    size: layout[key] ?? DEFAULT_SIZE[place],
    toggle: (t: PaneTab) => update({ ...layout, tab: tab === t ? null : t }),
    close: () => update({ ...layout, tab: null }),
    flip: () => update({ ...layout, place: layout.place === "right" ? "below" : "right" }),
    resize: (px: number, save = false) => update({ ...layout, [key]: Math.round(px) }, save),
    commit: () => saveLayout(id, latest.current?.id === id ? latest.current.layout : layout),
  };
}
