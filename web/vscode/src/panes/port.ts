// The wiring every Jam webview shares: the host answers the pane's requests
// (Router), feeds it live events (PaneHub) and Thinking streams
// (ThinkingStreams), posts its state and acts on its open intents. No message
// to the pane carries a token, the Jam URL or a cookie.
import * as vscode from "vscode";
import type { ExtToPane, PaneName, PaneState, PaneToExt } from "../../../app/src/bridge/protocol.js" with { "resolution-mode": "import" };
import { intentAction } from "../bridge/intents";
import type { IntentAction } from "../bridge/intents";
import { ID_RE } from "../bridge/router";
import type { Router } from "../bridge/router";
import { STREAM_RE } from "../thinking";
import type { ThinkingStreams } from "../thinking";
import type { PaneHub } from "./hub";
import { newNonce, webviewHtml } from "./html";

// THINKING_SUBS_PER_PANE is the most Thinking feeds one pane holds open.
export const THINKING_SUBS_PER_PANE = 2;

export type PortDeps = {
  hub: Pick<PaneHub, "attach">;
  router: Pick<Router, "handle">;
  thinking: Pick<ThinkingStreams, "subscribe">;
  jamUrl: () => string | Error;
  windowStudio: string | undefined;
  perform: (a: IntentAction) => void;
  executeCommand: (c: string, ...a: unknown[]) => Thenable<unknown>;
};

export type PaneHooks = {
  state: () => Promise<PaneState>;
  onViewing?: (channel: string | null) => void;
  onTitle?: (title: string) => void;
};

export type Port = { post(m: ExtToPane): void; refresh(): Promise<PaneState | undefined>; reset(): void; dispose(): void };

let routerSeq = 0; // request ids are the pane's own; the shared Router gets unique ones

const quietly = (t: unknown): void => void Promise.resolve(t).catch(() => undefined);

// paneHtml sets the webview's options and renders the pane bundle for pane.
export function paneHtml(w: vscode.Webview, extensionUri: vscode.Uri, pane: PaneName, extra?: { board?: string }): string {
  const root = vscode.Uri.joinPath(extensionUri, "dist", "webview");
  w.options = { enableScripts: true, enableCommandUris: false, localResourceRoots: [root] };
  return webviewHtml({
    cspSource: w.cspSource,
    nonce: newNonce(),
    script: w.asWebviewUri(vscode.Uri.joinPath(root, "pane.js")).toString(),
    style: w.asWebviewUri(vscode.Uri.joinPath(root, "pane.css")).toString(),
    pane,
    // joinPath drops the trailing slash, and Excalidraw appends "fonts/…" to it
    ...(extra?.board === undefined ? {} : { board: extra.board, assets: `${w.asWebviewUri(vscode.Uri.joinPath(root, "excalidraw")).toString()}/` }),
  });
}

export function wirePane(w: vscode.Webview, id: string, d: PortDeps, h: PaneHooks): Port {
  let disposed = false;
  let seq = 0;
  const ids = new Map<number, number>(); // pane request id -> Router id
  const feeds = new Map<number, () => void>(); // pane sub id -> unsubscribe
  const used = new Set<number>(); // sub ids this context has opened

  const post = (m: ExtToPane): void => {
    if (disposed) return;
    try {
      quietly(w.postMessage(m));
    } catch {
      // a webview going away
    }
  };

  // refresh posts the current state; an answer overtaken by a newer refresh is dropped.
  const refresh = async (): Promise<PaneState | undefined> => {
    const my = ++seq;
    const s = await h.state();
    if (disposed || my !== seq) return undefined;
    post({ type: "state", state: s });
    return s;
  };
  const refreshSoon = (): void => void refresh().catch(() => undefined);

  // An answer is posted only while its pane id still maps to its Router id:
  // an aborted or superseded request's answer is dropped.
  const request = (m: Extract<PaneToExt, { type: "request" }>): void => {
    const rid = ++routerSeq;
    ids.set(m.id, rid);
    d.router.handle({ type: "request", id: rid, req: m.req }, (r) => {
      if (r.type !== "response" && r.type !== "progress") return;
      if (ids.get(m.id) !== rid) return;
      if (r.type === "response") ids.delete(m.id);
      post({ ...r, id: m.id });
    });
  };
  const abort = (paneId: number): void => {
    const rid = ids.get(paneId);
    if (rid === undefined) return;
    ids.delete(paneId);
    d.router.handle({ type: "abort", id: rid }, () => undefined);
  };
  const abortAll = (): void => {
    for (const paneId of [...ids.keys()]) abort(paneId);
  };

  const closeFeed = (sub: number): void => {
    const off = feeds.get(sub);
    if (!off) return;
    feeds.delete(sub);
    off();
  };
  // thinkingOpen is untrusted: a bad sub, session or stream, a sub this
  // context already used, or one feed too many is refused with no subscribe.
  const thinkingOpen = (m: Extract<PaneToExt, { type: "thinkingOpen" }>): void => {
    const sub = m.sub;
    if (typeof sub !== "number") return;
    const refuse = () => post({ type: "thinkingError", sub, closed: true });
    if (!Number.isSafeInteger(sub) || sub < 1 || used.has(sub) || feeds.size >= THINKING_SUBS_PER_PANE) return refuse();
    if (typeof m.session !== "string" || !ID_RE.ses.test(m.session)) return refuse();
    if (typeof m.stream !== "string" || (m.stream !== "" && !STREAM_RE.test(m.stream))) return refuse();
    used.add(sub);
    let live = true; // false once closed by the pane or ended by the key
    const feed = { close: () => {} };
    try {
      const off = d.thinking.subscribe(
        m.session,
        m.stream,
        (s) => {
          if (!live) return;
          if ("error" in s) post({ type: "thinkingError", sub, closed: s.closed });
          else post({ type: "thinking", sub, event: s.event, data: s.data });
        },
        // the key ended: its slot is free
        () => {
          live = false;
          if (feeds.get(sub) === feed.close) feeds.delete(sub);
        },
      );
      feed.close = () => {
        live = false;
        off();
      };
    } catch {
      return refuse();
    }
    if (live) feeds.set(sub, feed.close);
  };

  // reset forgets the pane's context: its requests are aborted and its feeds
  // closed. Without retainContextWhenHidden a new context numbers its request
  // and sub ids from 1 again.
  const reset = (): void => {
    abortAll();
    for (const sub of [...feeds.keys()]) closeFeed(sub);
    used.clear();
  };

  const detach = d.hub.attach({ id, post });
  const listener = w.onDidReceiveMessage((m: PaneToExt) => {
    if (disposed || !m || typeof m !== "object") return;
    switch (m.type) {
      case "ready":
        reset();
        return refreshSoon();
      case "request":
        return request(m);
      case "abort":
        return abort(m.id);
      case "viewing":
        return h.onViewing?.(typeof m.channel === "string" && ID_RE.chn.test(m.channel) ? m.channel : null);
      case "title":
        if (typeof m.title === "string") h.onTitle?.(m.title);
        return;
      case "open": {
        const url = d.jamUrl();
        if (url instanceof Error || !m.intent || typeof m.intent !== "object") return;
        const a = intentAction(m.intent, url, d.windowStudio);
        if (a instanceof Error) return;
        try {
          quietly(d.perform(a));
        } catch {
          // perform shows its own errors
        }
        return;
      }
      case "signIn":
        return quietly(d.executeCommand("jam.signIn"));
      case "thinkingOpen":
        return thinkingOpen(m);
      case "thinkingClose":
        if (typeof m.sub === "number") closeFeed(m.sub);
        return;
    }
  });

  return {
    post,
    refresh,
    reset,
    dispose: () => {
      if (disposed) return;
      reset();
      disposed = true;
      detach();
      listener.dispose();
    },
  };
}
