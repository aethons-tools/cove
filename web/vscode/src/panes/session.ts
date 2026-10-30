// The session views (Conversation, Thinking, Previews, Status, Artifacts):
// one webview each running web/app's pane bundle, each following the window's
// focused session (FocusModel) with its own pin.
import type * as vscode from "vscode";
import type { ExtToPane, PaneState, SessionPaneName } from "../../../app/src/bridge/protocol.js" with { "resolution-mode": "import" };
import { ApiError, SignedOutError } from "../api";
import type { Auth } from "../auth";
import type { FocusModel } from "../focus";
import type { FocusReporter } from "../focusreport";
import { paneHtml, wirePane } from "./port";
import type { PortDeps } from "./port";

export const SESSION_PANES: readonly SessionPaneName[] = ["conversation", "thinking", "previews", "status", "artifacts"];

export type SessionPaneDeps = PortDeps & {
  extensionUri: vscode.Uri;
  model: Pick<FocusModel, "stateOf" | "isPinned" | "windowSession" | "onChange">;
  reporter: Pick<FocusReporter, "pane" | "drop">;
  auth: Pick<Auth, "session" | "onChange">;
};

let paneSeq = 0;

const quietly = (t: unknown): void => void Promise.resolve(t).catch(() => undefined);

// sessionPaneState is what the view should show: signed out, the view's
// target (its pin, else the window's session), or an error once.
export async function sessionPaneState(d: SessionPaneDeps, view: string): Promise<PaneState> {
  const pinned = d.model.isPinned(view);
  const out: PaneState = { signedIn: false, target: null, pinned, elsewhere: false };
  const url = d.jamUrl();
  if (url instanceof Error) return out;
  try {
    if (!(await d.auth.session(url))) return out;
    const s = await d.model.stateOf(view);
    return { signedIn: true, target: s.target, pinned: s.pinned, elsewhere: s.elsewhere };
  } catch (e) {
    if (e instanceof SignedOutError) return out;
    // Not a 404 (that is a gone target): say so once; Try again is a new ready.
    return { signedIn: true, target: null, pinned, elsewhere: false, error: errorCopy(e) };
  }
}

export class SessionPaneProvider implements vscode.WebviewViewProvider {
  readonly viewType: string;

  private posters = new Set<(m: ExtToPane) => void>();
  private pending: string | undefined; // one anchor awaiting a Conversation pane's ready

  constructor(
    private name: SessionPaneName,
    private d: SessionPaneDeps,
  ) {
    this.viewType = `jam.${name}`;
  }

  // scrollTo asks this provider's views to scroll; a view whose JS isn't
  // running yet (or was dropped on hide) gets it on its next ready.
  scrollTo(anchor: string): void {
    this.pending = anchor;
    for (const post of this.posters) post({ type: "scrollTo", anchor });
  }

  resolveWebviewView(view: vscode.WebviewView, _context?: vscode.WebviewViewResolveContext, _token?: vscode.CancellationToken): void {
    const w = view.webview;
    w.html = paneHtml(w, this.d.extensionUri, this.name);

    const id = `${this.name}-${++paneSeq}`;
    const reports = this.name === "conversation"; // only Conversation tells the server what you're viewing
    let disposed = false;
    let channel: string | null = null;
    let seq = 0;

    const report = (): void => {
      if (reports) this.d.reporter.pane(id, { visible: view.visible, channel });
    };
    const setContext = (): void => {
      quietly(this.d.executeCommand("setContext", `jam.${this.name}.pinned`, this.d.model.isPinned(this.viewType)));
      quietly(this.d.executeCommand("setContext", `jam.${this.name}.hasSession`, this.d.model.windowSession() !== undefined));
    };

    const port = wirePane(w, id, this.d, {
      // The description and context keys follow the newest lookup, as the
      // port's refresh posts only the newest state.
      state: async () => {
        const my = ++seq;
        const s = await sessionPaneState(this.d, this.viewType);
        if (!disposed && my === seq) {
          view.description = s.elsewhere && s.target && "label" in s.target ? s.target.label : "";
          setContext();
        }
        return s;
      },
      onViewing: (c) => {
        channel = c;
        report();
      },
    });
    const refreshSoon = (): void => void port.refresh().catch(() => undefined);

    this.posters.add(port.post);
    const subs: (() => void)[] = [this.d.model.onChange(refreshSoon), this.d.auth.onChange(refreshSoon)];
    const disposables = [
      // after the port's own ready handling (reset, then refresh)
      w.onDidReceiveMessage((m: { type?: unknown } | undefined) => {
        if (!m || typeof m !== "object" || m.type !== "ready" || this.pending === undefined) return;
        port.post({ type: "scrollTo", anchor: this.pending });
        this.pending = undefined;
      }),
      // Without retainContextWhenHidden a hidden view's JS is gone: it holds
      // no requests and no Thinking feeds.
      view.onDidChangeVisibility(() => {
        if (!view.visible) port.reset();
        report();
      }),
    ];
    disposables.push(
      view.onDidDispose(() => {
        disposed = true;
        port.dispose();
        this.posters.delete(port.post);
        for (const u of subs) u();
        for (const x of disposables) x.dispose();
        if (reports) this.d.reporter.drop(id);
      }),
    );
    setContext();
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
