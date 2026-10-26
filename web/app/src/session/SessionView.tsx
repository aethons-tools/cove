import { ActionIcon, Group, Text } from "@mantine/core";
import { useRef, type CSSProperties, type KeyboardEvent, type PointerEvent, type ReactNode } from "react";
import { SessionHeader } from "./SessionHeader";
import { useSessionHead } from "./useSession";
import { usePaneLayout } from "./usePaneLayout";
import { clampSize, KEY_STEP, MIN_MAIN, MIN_PANE, TAB_LABEL } from "./layout";
import { PaneBody } from "./PaneTabs";

const GROW: Record<string, number> = { ArrowLeft: 1, ArrowUp: 1, ArrowRight: -1, ArrowDown: -1 };
const border = "1px solid var(--mantine-color-default-border)";

// SessionView is a session's home channel: its header, then the
// conversation (header, then messages) beside or above the session pane.
// A session that's gone (its head 404s) shows as a plain conversation.
export function SessionView({ id, channel, linked, onAsk, header, children }: {
  id: string; channel: string; linked: string | null; onAsk: (id: string) => void; header: ReactNode; children: ReactNode;
}) {
  const q = useSessionHead(id);
  // A failed refetch keeps the last data; a 404 means the session is gone.
  const h = q.error?.code === "not_found" ? undefined : q.data;
  const pane = usePaneLayout(id, { studio: !!h?.studio, linked });
  const split = useRef<HTMLDivElement>(null);
  const right = pane.place === "right";
  const tab = h ? pane.tab : null;
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
      {h && <SessionHeader head={h} tab={tab} onTab={pane.toggle} />}
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
              <div id="session-pane-body" role="tabpanel" aria-label={TAB_LABEL[tab]} style={{ flex: 1, minHeight: 0, overflow: "auto", padding: "8px 12px 14px" }}>
                <PaneBody id={id} channel={channel} tab={tab} onAsk={onAsk} />
              </div>
            </aside>
          </>
        )}
      </div>
    </>
  );
}
