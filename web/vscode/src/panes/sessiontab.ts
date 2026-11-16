// Session tabs in the hub (vscode-hub.adoc#session-tabs): one webview panel
// per session, running the session view without its pane, restored after a
// window reload from the panel's {session} state. The session's activity
// decides whether it opens: a 404 there is a gone session.
import * as vscode from "vscode";
import type { SessionTarget } from "../../../app/src/bridge/protocol.js" with { "resolution-mode": "import" };
import type { Auth } from "../auth";
import { ID_RE } from "../bridge/router";
import type { FocusReporter } from "../focusreport";
import { tabTitle } from "./board";
import { paneHtml, wirePane } from "./port";
import type { PortDeps } from "./port";

export type SessionTabDeps = PortDeps & {
  extensionUri: vscode.Uri;
  auth: Pick<Auth, "session" | "onChange">;
  createPanel: typeof vscode.window.createWebviewPanel;
  // resolve is the session's label, or "gone" (its activity 404s).
  resolve: (session: string) => Promise<{ label: string } | "gone">;
  reporter: Pick<FocusReporter, "pane" | "drop">;
  // onActive is the session of a tab that became the active editor.
  onActive: (session: string) => void;
};

const validSession = (s: unknown): s is string => typeof s === "string" && ID_RE.ses.test(s);

let tabSeq = 0;

export class SessionPanels {
  static readonly viewType = "jam.sessionTab";

  private tabs = new Map<string, vscode.WebviewPanel>();

  constructor(private d: SessionTabDeps) {}

  has(session: string): boolean {
    return this.tabs.has(session);
  }

  // open reveals the tab for session, else makes a new one in the active column.
  open(session: string): void {
    if (!validSession(session)) return;
    const have = this.tabs.get(session);
    if (have) return have.reveal();
    const panel = this.d.createPanel(SessionPanels.viewType, "Session", vscode.ViewColumn.Active, {
      enableScripts: true,
      enableCommandUris: false,
      enableFindWidget: true,
      retainContextWhenHidden: false,
      localResourceRoots: [vscode.Uri.joinPath(this.d.extensionUri, "dist", "webview")],
    });
    this.attach(panel, session);
  }

  // restore is the serializer: as ConversationPanels.restore, a revived tab
  // for a session already open closes and the open one is revealed.
  restore(panel: vscode.WebviewPanel, state: unknown): void {
    const session = state && typeof state === "object" ? (state as { session?: unknown }).session : undefined;
    if (!validSession(session)) return panel.dispose();
    const have = this.tabs.get(session);
    if (have) {
      panel.dispose();
      return have.reveal();
    }
    this.attach(panel, session);
  }

  private attach(panel: vscode.WebviewPanel, session: string): void {
    panel.iconPath = vscode.Uri.joinPath(this.d.extensionUri, "media", "jam-color.svg");
    const w = panel.webview;
    w.html = paneHtml(w, this.d.extensionUri, "session", { session });
    const id = `session-tab-${++tabSeq}`;
    // viewing is the pane's dm with the session while it runs (null: none).
    let viewing: string | null = null;
    let found = false;
    const report = (): void => this.d.reporter.pane(id, { visible: panel.visible && panel.active, channel: viewing });
    const lookup = async (): Promise<SessionTarget> => {
      const r = await this.d.resolve(session);
      if (r === "gone") return { session, gone: true };
      panel.title = tabTitle(r.label);
      // A tab already active (a new one, or one restored in front) gets no
      // view-state change: tell onActive now.
      if (!found && panel.active) this.d.onActive(session);
      found = true;
      return { session, label: r.label, channel: "" };
    };
    const port = wirePane(w, id, this.d, {
      state: async () => {
        const url = this.d.jamUrl();
        const signedIn = !(url instanceof Error) && (await this.d.auth.session(url)) !== undefined;
        return { signedIn, target: signedIn ? await lookup() : null };
      },
      onViewing: (c) => {
        viewing = c;
        report();
      },
    });
    this.tabs.set(session, panel);
    const off = this.d.auth.onChange(() => void port.refresh().catch(() => undefined));
    const sub = panel.onDidChangeViewState(() => {
      report();
      if (!panel.active) return;
      // Not found yet: the lookup tells onActive once it is.
      if (found) this.d.onActive(session);
      else void lookup().catch(() => undefined);
    });
    panel.onDidDispose(() => {
      off();
      port.dispose();
      sub.dispose();
      this.d.reporter.drop(id);
      if (this.tabs.get(session) === panel) this.tabs.delete(session);
    });
  }
}
