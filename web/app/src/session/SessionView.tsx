import { ActionIcon, Group, Loader, Text } from "@mantine/core";
import { lazy, Suspense, useRef, useState, type CSSProperties, type KeyboardEvent, type PointerEvent, type ReactNode } from "react";
import { SessionHeader } from "./SessionHeader";
import { useSessionHead } from "./useSession";
import { usePaneLayout } from "./usePaneLayout";
import { clampSize, KEY_STEP, MIN_MAIN, MIN_PANE, TAB_LABEL, type PaneTab } from "./layout";
import { PaneBody } from "./PaneTabs";

// StudioTab is its own chunk, loaded when the tab first opens.
const StudioTab = lazy(() => import("./StudioTab"));

const GROW: Record<string, number> = { ArrowLeft: 1, ArrowUp: 1, ArrowRight: -1, ArrowDown: -1 };
const border = "1px solid var(--mantine-color-default-border)";

// SessionView lays out a conversation: when it is a session's home (id),
// the session's header, then the conversation (header, then messages)
// beside or above the session pane. Without a session, or once it's gone
// (its head 404s), it is the plain conversation. Its shape never changes
// with id, so the conversation (composer, reading place) stays mounted
// when a session ends or appears.
export function SessionView({ id, channel, linked, onAsk, header, children }: {
  id: string | null; channel: string; linked: string | null; onAsk: (id: string) => void; header: ReactNode; children: ReactNode;
}) {
  const q = useSessionHead(id);
  // A failed refetch keeps the last data; a 404 means the session is gone.
  const h = id && q.error?.code !== "not_found" ? q.data : undefined;
  const pane = usePaneLayout(id ?? "", { studio: !!h?.studio, linked });
  const split = useRef<HTMLDivElement>(null);
  const right = pane.place === "right";
  const tab = h ? pane.tab : null;
  // The Studio stays mounted (hidden) once shown, so the IDE keeps its
  // state across tabs, until the pane closes.
  const [studioKept, setStudioKept] = useState(false);
  if (tab === "studio" && !studioKept) setStudioKept(true);
  if (tab === null && studioKept) setStudioKept(false);
  // Studio's first open, with no size saved for the placement, takes 60%.
  const onTab = (t: PaneTab) => {
    if (t === "studio" && tab !== "studio" && !pane.hasSize && split.current) {
      const r = split.current.getBoundingClientRect();
      pane.toggle(t, (right ? r.width : r.height) * 0.6);
      return;
    }
    pane.toggle(t);
  };
  const total = () => {
    const r = split.current!.getBoundingClientRect();
    return right ? r.width : r.height;
  };
  const onKey = (e: KeyboardEvent) => {
    const grow = GROW[e.key];
    if (!grow) return;
    e.preventDefault();
    pane.resize(clampSize(pane.size + grow * KEY_STEP, total()), true);
  };
  const onDown = (e: PointerEvent<HTMLDivElement>) => {
    if (e.button !== 0) return;
    e.preventDefault();
    const div = e.currentTarget;
    div.setPointerCapture(e.pointerId);
    const move = (ev: globalThis.PointerEvent) => {
      const r = split.current!.getBoundingClientRect();
      pane.resize(clampSize(right ? r.right - ev.clientX : r.bottom - ev.clientY, right ? r.width : r.height));
    };
    const up = () => {
      div.removeEventListener("pointermove", move);
      div.removeEventListener("pointerup", up);
      div.removeEventListener("pointercancel", up);
      pane.commit();
    };
    div.addEventListener("pointermove", move);
    div.addEventListener("pointerup", up);
    div.addEventListener("pointercancel", up);
  };
  // The size is clamped in CSS too, so a window smaller than when it was
  // saved still leaves the messages room.
  const paneStyle: CSSProperties = right
    ? { flex: `0 0 ${pane.size}px`, minWidth: MIN_PANE, maxWidth: `calc(100% - ${MIN_MAIN}px)`, minHeight: 0 }
    : { flex: `0 0 ${pane.size}px`, minHeight: MIN_PANE, maxHeight: `calc(100% - ${MIN_MAIN}px)` };
  return (
    <>
      {h && <SessionHeader key={h.id} head={h} tab={tab} onTab={onTab} />}
      {header}
      <div ref={split} data-place={pane.place} style={{ flex: 1, minHeight: 0, display: "flex", flexDirection: right ? "row" : "column" }}>
        <div style={{ flex: 1, minWidth: 0, minHeight: 0, display: "flex", flexDirection: "column" }}>{children}</div>
        {tab && (
          <>
            <div role="separator" tabIndex={0} aria-label="Resize the session pane" aria-orientation={right ? "vertical" : "horizontal"}
              onKeyDown={onKey} onPointerDown={onDown}
              style={{ flex: "0 0 5px", cursor: right ? "col-resize" : "row-resize", background: "var(--mantine-color-default-border)", touchAction: "none" }} />
            <aside aria-label="Session pane" style={{ ...paneStyle, display: "flex", flexDirection: "column", overflow: "hidden" }}>
              <Group gap={6} px="sm" py={6} style={{ borderBottom: border }}>
                <Text fw={700} size="sm">{TAB_LABEL[tab]}</Text>
                <span style={{ flex: 1 }} />
                {!pane.narrow && <ActionIcon variant="subtle" size="sm" title="Move the pane right or below" aria-label="Move the pane right or below" onClick={pane.flip}>⇄</ActionIcon>}
                <ActionIcon variant="subtle" size="sm" aria-label="Close the pane" onClick={pane.close}>×</ActionIcon>
              </Group>
              {/* Thinking scrolls its own list; Studio lives in its keep below. */}
              {tab !== "studio" && (
                <div id="session-pane-body" role="tabpanel" aria-label={TAB_LABEL[tab]}
                  style={tab === "thinking" ? { flex: 1, minHeight: 0, overflow: "hidden" } : { flex: 1, minHeight: 0, overflow: "auto", padding: "8px 12px 14px" }}>
                  <PaneBody id={h!.id} channel={channel} tab={tab} busy={h!.state.class === "busy"} onAsk={onAsk} />
                </div>
              )}
              {studioKept && (
                <div data-keep role={tab === "studio" ? "tabpanel" : undefined} aria-label="Studio"
                  style={{ display: tab === "studio" ? "flex" : "none", flex: 1, minHeight: 0, flexDirection: "column" }}>
                  <Suspense fallback={<Loader size="sm" m="sm" />}><StudioTab id={h!.id} /></Suspense>
                </div>
              )}
            </aside>
          </>
        )}
      </div>
    </>
  );
}
