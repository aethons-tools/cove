// The session panes' bodies: what each of the Jam views shows for the
// focused session, inside Pane's shell.
import { useCallback, useContext, useMemo, type JSX, type ReactNode } from "react";
import type { PreviewLink } from "../api/types";
import type { SessionPaneName } from "../bridge/protocol";
import { ConversationView } from "../conversation/ConversationView";
import { HostContext, type Host } from "../host";
import { PaneBody } from "../session/PaneTabs";
import { SessionHeader } from "../session/SessionHeader";
import { useSessionHead } from "../session/useSession";
import type { Bridge } from "./bridge";
import { Copy, copyFor } from "./Pane";

const noAsk = () => {};
const padded = { padding: "8px 12px 14px" };

// SessionGate shows a session pane's body until the session's head 404s
// (it ended, or the viewer lost it): then the gone copy.
export function SessionGate({ session, name, onFocus, children }: {
  session: string; name: SessionPaneName; onFocus: () => void; children: ReactNode;
}): JSX.Element {
  const head = useSessionHead(session);
  if (head.error?.code === "not_found") return <Copy text={copyFor(name).gone} action="Focus a session…" onClick={onFocus} />;
  return <>{children}</>;
}

// SessionBody is the pane named name for session (whose home is channel).
export function SessionBody({ bridge, name, session, channel }: {
  bridge: Bridge; name: SessionPaneName; session: string; channel: string;
}): JSX.Element {
  // Conversation reads no session head (SessionGate's cached query elsewhere).
  const head = useSessionHead(name === "conversation" ? null : session);
  const host = useContext(HostContext);
  const openPreview = useCallback((_: string, v: PreviewLink, inBrowser: boolean) =>
    bridge.post({ type: "open", intent: { kind: "preview", session, port: v.port, url: v.url, inBrowser } }), [bridge, session]);
  const previewHost = useMemo<Host>(() => ({ ...host, openPreview }), [host, openPreview]);
  const onAsk = useCallback((id: string) => bridge.post({ type: "open", intent: { kind: "reveal", anchor: `ask-${id}` } }), [bridge]);
  switch (name) {
    case "conversation":
      return <ConversationView key={channel} id={channel} withSession={false} />;
    case "thinking":
      return (
        <div style={{ height: "100vh", display: "flex", flexDirection: "column" }}>
          <PaneBody id={session} tab="thinking" busy={head.data?.state.class === "busy"} onAsk={noAsk} />
        </div>
      );
    case "previews":
      return (
        <HostContext.Provider value={previewHost}>
          <div style={padded}><PaneBody id={session} tab="previews" busy={false} onAsk={noAsk} /></div>
        </HostContext.Provider>
      );
    case "status":
      return (
        <>
          {head.data && <SessionHeader key={head.data.id} head={head.data} />}
          <div style={padded}><PaneBody id={session} tab="status" busy={false} onAsk={noAsk} /></div>
        </>
      );
    case "artifacts":
      return <div style={padded}><PaneBody id={session} tab="artifacts" busy={false} onAsk={onAsk} /></div>;
  }
}
