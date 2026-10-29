// The Conversation view: a webview running web/app's pane bundle. The host
// answers its requests (Router), feeds it live events (PaneHub), tells it which
// session to show (FocusModel) and acts on its open intents. No message to the
// pane carries a token, the Jam URL or a cookie.
import * as vscode from "vscode";
import type { ExtToPane, OpenIntent, PaneState, PaneToExt } from "../../../app/src/bridge/protocol.js" with { "resolution-mode": "import" };
import { ApiError, SignedOutError } from "../api";
import type { Auth } from "../auth";
import { intentAction } from "../bridge/intents";
import { ID_RE } from "../bridge/router";
import type { Router } from "../bridge/router";
import type { FocusModel } from "../focus";
import type { FocusReporter } from "../focusreport";
import type { PaneHub } from "./hub";
import { newNonce, webviewHtml } from "./html";

export type ConversationDeps = {
  extensionUri: vscode.Uri;
  hub: Pick<PaneHub, "attach">;
  router: Pick<Router, "handle">;
  model: Pick<FocusModel, "stateOf" | "isPinned" | "windowSession" | "onChange">;
  reporter: Pick<FocusReporter, "pane" | "drop">;
  auth: Pick<Auth, "session" | "onChange">;
  jamUrl: () => string | Error;
  openExternal: (url: string) => unknown;
  executeCommand: (command: string, ...rest: unknown[]) => Thenable<unknown>;
};

let paneSeq = 0;
let routerSeq = 0; // request ids are the pane's own; the shared Router gets unique ones

const quietly = (t: unknown): void => void Promise.resolve(t).catch(() => undefined);

export class ConversationViewProvider implements vscode.WebviewViewProvider {
  static readonly viewType = "jam.conversation";

  constructor(private d: ConversationDeps) {}

  resolveWebviewView(view: vscode.WebviewView, _context?: vscode.WebviewViewResolveContext, _token?: vscode.CancellationToken): void {
    const w = view.webview;
    const root = vscode.Uri.joinPath(this.d.extensionUri, "dist", "webview");
    w.options = { enableScripts: true, enableCommandUris: false, localResourceRoots: [root] };
    w.html = webviewHtml({
      cspSource: w.cspSource,
      nonce: newNonce(),
      script: w.asWebviewUri(vscode.Uri.joinPath(root, "pane.js")).toString(),
      style: w.asWebviewUri(vscode.Uri.joinPath(root, "pane.css")).toString(),
      pane: "conversation",
    });

    const id = `conversation-${++paneSeq}`;
    let disposed = false;
    let channel: string | null = null;
    let seq = 0;
    const ids = new Map<number, number>(); // pane request id -> Router id

    const post = (m: ExtToPane): void => {
      if (disposed) return;
      try {
        quietly(w.postMessage(m));
      } catch {
        // a webview going away
      }
    };
    const report = (): void => this.d.reporter.pane(id, { visible: view.visible, channel });
    const setContext = (): void => {
      quietly(this.d.executeCommand("setContext", "jam.conversation.pinned", this.d.model.isPinned(ConversationViewProvider.viewType)));
      quietly(this.d.executeCommand("setContext", "jam.conversation.hasSession", this.d.model.windowSession() !== undefined));
    };
    // refresh posts the current state; an answer overtaken by a newer refresh is dropped.
    const refresh = async (): Promise<void> => {
      const my = ++seq;
      const s = await this.state();
      if (disposed || my !== seq) return;
      post({ type: "state", state: s });
      view.description = s.elsewhere && s.target && "label" in s.target ? s.target.label : "";
      setContext();
    };
    const refreshSoon = (): void => void refresh().catch(() => undefined);

    // An answer is posted only while its pane id still maps to its Router id:
    // an aborted or superseded request's answer is dropped.
    const request = (m: Extract<PaneToExt, { type: "request" }>): void => {
      const rid = ++routerSeq;
      ids.set(m.id, rid);
      this.d.router.handle({ type: "request", id: rid, req: m.req }, (r) => {
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
      this.d.router.handle({ type: "abort", id: rid }, () => undefined);
    };
    // Without retainContextWhenHidden, hiding the view destroys its JS, and a
    // new context numbers its requests from 1 again: the old context's
    // requests are aborted on hide and on every ready.
    const abortAll = (): void => {
      for (const paneId of [...ids.keys()]) abort(paneId);
    };

    const detach = this.d.hub.attach({ id, post });
    const subs: (() => void)[] = [this.d.model.onChange(refreshSoon), this.d.auth.onChange(refreshSoon)];
    const disposables = [
      w.onDidReceiveMessage((m: PaneToExt) => {
        if (!m || typeof m !== "object") return;
        switch (m.type) {
          case "ready":
            abortAll();
            return refreshSoon();
          case "request":
            return request(m);
          case "abort":
            return abort(m.id);
          case "viewing":
            channel = typeof m.channel === "string" && ID_RE.chn.test(m.channel) ? m.channel : null;
            return report();
          case "open":
            return this.open(m.intent);
          case "signIn":
            return quietly(this.d.executeCommand("jam.signIn"));
        }
      }),
      view.onDidChangeVisibility(() => {
        if (!view.visible) abortAll();
        report();
      }),
    ];
    disposables.push(
      view.onDidDispose(() => {
        disposed = true;
        abortAll();
        detach();
        for (const u of subs) u();
        for (const x of disposables) x.dispose();
        this.d.reporter.drop(id);
      }),
    );
    setContext();
  }

  private open(intent: OpenIntent): void {
    const url = this.d.jamUrl();
    if (url instanceof Error || !intent || typeof intent !== "object") return;
    const a = intentAction(intent, url);
    if (a instanceof Error) return;
    if ("openExternal" in a) quietly(this.d.openExternal(a.openExternal));
    else quietly(this.d.executeCommand(a.command));
  }

  private async state(): Promise<PaneState> {
    const view = ConversationViewProvider.viewType;
    const pinned = this.d.model.isPinned(view);
    const out: PaneState = { signedIn: false, target: null, pinned, elsewhere: false };
    const url = this.d.jamUrl();
    if (url instanceof Error) return out;
    try {
      if (!(await this.d.auth.session(url))) return out;
      const s = await this.d.model.stateOf(view);
      return { signedIn: true, target: s.target, pinned: s.pinned, elsewhere: s.elsewhere };
    } catch (e) {
      if (e instanceof SignedOutError) return out;
      // Not a 404 (that is a gone target): say so once; Try again is a new ready.
      return { signedIn: true, target: null, pinned, elsewhere: false, error: errorCopy(e) };
    }
  }
}

// ERROR_CAP is the most of a server's message the pane shows.
const ERROR_CAP = 200;

// errorCopy is what the pane says about a failed lookup: a JSON error's
// message (capped), never a raw non-JSON body such as a proxy's HTML page.
function errorCopy(e: unknown): string {
  if (!(e instanceof ApiError)) return "Jam couldn't be reached.";
  if (!e.code) return `Jam answered ${e.status}.`;
  const msg = e.message.length > ERROR_CAP ? `${e.message.slice(0, ERROR_CAP)}…` : e.message;
  return `${msg} (HTTP ${e.status})`;
}
