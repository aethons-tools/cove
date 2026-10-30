// The session views (Conversation, Thinking, Previews, Status, Artifacts):
// one webview each running web/app's pane bundle, each showing the window's
// session (FocusModel).
import type * as vscode from "vscode";
import type { PaneState, SessionPaneName } from "../../../app/src/bridge/protocol.js" with { "resolution-mode": "import" };
import { ApiError, SignedOutError } from "../api";
import type { Auth } from "../auth";
import type { FocusModel } from "../focus";
import type { FocusReporter } from "../focusreport";
import { paneHtml, wirePane } from "./port";
import type { PortDeps } from "./port";

// The conversation is a tab (panes/conversation.ts), not a view.
export const SESSION_PANES: readonly SessionPaneName[] = ["thinking", "previews", "status", "artifacts"];

export type SessionPaneDeps = PortDeps & {
  extensionUri: vscode.Uri;
  model: Pick<FocusModel, "target" | "onChange">;
  reporter: Pick<FocusReporter, "pane" | "drop">;
  auth: Pick<Auth, "session" | "onChange">;
};

let paneSeq = 0;

// sessionPaneState is what the view should show: signed out, the window's
// session, or an error once.
export async function sessionPaneState(d: SessionPaneDeps): Promise<PaneState> {
  const out: PaneState = { signedIn: false, target: null };
  const url = d.jamUrl();
  if (url instanceof Error) return out;
  try {
    if (!(await d.auth.session(url))) return out;
    return { signedIn: true, target: await d.model.target() };
  } catch (e) {
    if (e instanceof SignedOutError) return out;
    // Not a 404 (that is a gone target): say so once; Try again is a new ready.
    return { signedIn: true, target: null, error: errorCopy(e) };
  }
}

export class SessionPaneProvider implements vscode.WebviewViewProvider {
  readonly viewType: string;


  constructor(
    private name: SessionPaneName,
    private d: SessionPaneDeps,
  ) {
    this.viewType = `jam.${name}`;
  }

  resolveWebviewView(view: vscode.WebviewView, _context?: vscode.WebviewViewResolveContext, _token?: vscode.CancellationToken): void {
    const w = view.webview;
    w.html = paneHtml(w, this.d.extensionUri, this.name);

    const id = `${this.name}-${++paneSeq}`;
    const reports = this.name === "conversation"; // only Conversation tells the server what you're viewing
    let channel: string | null = null;

    const report = (): void => {
      if (reports) this.d.reporter.pane(id, { visible: view.visible, channel });
    };

    const port = wirePane(w, id, this.d, {
      state: () => sessionPaneState(this.d),
      onViewing: (c) => {
        channel = c;
        report();
      },
    });
    const refreshSoon = (): void => void port.refresh().catch(() => undefined);

    const subs: (() => void)[] = [this.d.model.onChange(refreshSoon), this.d.auth.onChange(refreshSoon)];
    const disposables = [
      // Without retainContextWhenHidden a hidden view's JS is gone: it holds
      // no requests and no Thinking feeds.
      view.onDidChangeVisibility(() => {
        if (!view.visible) port.reset();
        report();
      }),
    ];
    disposables.push(
      view.onDidDispose(() => {
        port.dispose();
        for (const u of subs) u();
        for (const x of disposables) x.dispose();
        if (reports) this.d.reporter.drop(id);
      }),
    );
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
