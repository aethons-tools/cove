import { useMediaQuery } from "@mantine/hooks";
import { useEffect, useRef, useState } from "react";
import { DEFAULT_SIZE, isTab, loadLayout, NARROW, saveLayout, type PaneLayout, type PaneState, type PaneTab, type Place } from "./layout";

// usePaneLayout is one session's pane layout: loaded from storage (a linked
// ?tab= opens that tab and is saved), saved on each change. Narrow windows
// put the pane below without changing the saved placement. A Studio tab the
// viewer lacks (opts.studio false) shows closed, and the saved layout is
// left as it was.
export function usePaneLayout(id: string, opts: { studio: boolean; linked: string | null }): PaneState {
  const [layout, setLayout] = useState<PaneLayout>(() => {
    const l = loadLayout(id);
    return isTab(opts.linked) ? { ...l, tab: opts.linked } : l;
  });
  const narrow = useMediaQuery(NARROW) ?? false;
  const linked = isTab(opts.linked);
  useEffect(() => {
    if (linked && layout.tab !== "studio") saveLayout(id, layout);
    // Only the first layout: later changes save themselves.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  // latest is the newest layout set, for a drag's pointerup: its listener
  // was made in an earlier render, whose layout is the pre-drag one.
  const latest = useRef<PaneLayout | null>(null);
  const update = (next: PaneLayout, save = true) => {
    latest.current = next;
    setLayout(next);
    if (save) saveLayout(id, next);
  };
  const tab = layout.tab === "studio" && !opts.studio ? null : layout.tab;
  const place: Place = narrow ? "below" : layout.place;
  const key = place === "right" ? "w" : "h";
  return {
    tab, place, narrow,
    size: layout[key] ?? DEFAULT_SIZE[place],
    toggle: (t: PaneTab) => update({ ...layout, tab: tab === t ? null : t }),
    close: () => update({ ...layout, tab: null }),
    flip: () => update({ ...layout, place: layout.place === "right" ? "below" : "right" }),
    resize: (px: number, save = false) => update({ ...layout, [key]: Math.round(px) }, save),
    commit: () => saveLayout(id, latest.current ?? layout),
  };
}
