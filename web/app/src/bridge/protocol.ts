// The webview bridge protocol between the VS Code extension host and a pane.
// Types only: no DOM or React imports, so web/vscode can type-import it.
export type SessionPaneName = "conversation" | "thinking" | "previews" | "status" | "artifacts";
// session is a hub's session tab: the session view, without its pane.
export type PaneName = SessionPaneName | "board" | "rail" | "session";
export type ThinkingEventName = "open" | "stream" | "totals" | "ev" | "more" | "skipped" | "newstream";
export type Method = "GET" | "POST" | "PUT" | "DELETE";
export type BridgeRequest = {
  method: Method;
  path: string;
  json?: unknown;
  bytes?: ArrayBuffer;
  headers?: Record<string, string>;
  accept?: "json" | "bytes";
  progress?: boolean;
};
export type BridgeResponse =
  | { kind: "http"; status: number; contentType: string; json?: unknown; text?: string; bytes?: ArrayBuffer }
  | { kind: "unreachable" }
  | { kind: "signedOut" }
  | { kind: "refused"; reason: string };
export type SessionTarget = { session: string; label: string; channel: string } | { session: string; gone: true };
// error: the focused session couldn't be looked up (not a 404); the pane offers
// Try again, which is a new ready.
// windows: the sessions with an open studio window (the rail's ▣).
// windows, active and activeSession are the rail's: the sessions with a
// studio window open, and the window's focused conversation (its channel) or
// session (a hub's active session tab).
export type PaneState = { signedIn: boolean; target: SessionTarget | null; error?: string; windows?: string[]; active?: string; activeSession?: string };
export type OpenIntent =
  | { kind: "board"; id: string }
  | { kind: "conversation"; channel: string }
  | { kind: "web"; path: string }
  | { kind: "external"; url: string }
  | { kind: "preview"; session: string; port: number; url: string; inBrowser: boolean }
  | { kind: "reveal"; anchor?: string } // show the Conversation view, then scroll to anchor
  | { kind: "session"; id: string } // open that session's code window (the extension link)
  | { kind: "go"; channel: string; session: string } // a rail row's click: the conversation's tab (a double-click is "session")
  | { kind: "sessionTab"; id: string }; // an Agents card's click: the session's tab in the hub
export type PaneToExt =
  | { type: "ready"; pane: PaneName }
  | { type: "request"; id: number; req: BridgeRequest }
  | { type: "abort"; id: number }
  | { type: "viewing"; channel: string | null }
  | { type: "open"; intent: OpenIntent }
  | { type: "signIn" }
  | { type: "thinkingOpen"; sub: number; session: string; stream: string }
  | { type: "thinkingClose"; sub: number }
  | { type: "title"; title: string }; // board tabs only
export type ExtToPane =
  | { type: "state"; state: PaneState } // on ready and on every change
  | { type: "response"; id: number; res: BridgeResponse }
  | { type: "progress"; id: number; loaded: number; total: number }
  | { type: "live"; event: "changed" | "presence" | "board" | "open"; data: string }
  | { type: "thinking"; sub: number; event: ThinkingEventName; data: string }
  | { type: "thinkingError"; sub: number; closed: boolean }
  | { type: "scrollTo"; anchor: string };
