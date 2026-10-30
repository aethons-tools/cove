// Conversations as editor tabs: one webview panel per channel, running the
// conversation pane on that channel, restored after a window reload from the
// panel's {channel} state. The hub opens any conversation; a studio window
// only its own session's.
import * as vscode from "vscode";
import type { SessionTarget } from "../../../app/src/bridge/protocol.js" with { "resolution-mode": "import" };
import type { Auth } from "../auth";
import { LEGACY_RE } from "../bridge/intents";
import { ID_RE } from "../bridge/router";
import type { FocusReporter } from "../focusreport";
import { tabTitle } from "./board";
import { paneHtml, wirePane } from "./port";
import type { Port, PortDeps } from "./port";

export type ConversationDeps = PortDeps & {
  extensionUri: vscode.Uri;
  auth: Pick<Auth, "session" | "onChange">;
  createPanel: typeof vscode.window.createWebviewPanel;
  // resolve is the channel's target ({session: ""} for a chat), or "gone".
  resolve: (channel: string) => Promise<SessionTarget | "gone">;
  reporter: Pick<FocusReporter, "pane" | "drop">;
  // onActive is the target of a tab that became the active editor.
  onActive: (t: SessionTarget) => void;
};

const validChannel = (c: unknown): c is string => typeof c === "string" && (ID_RE.chn.test(c) || LEGACY_RE.test(c));

type Tab = { panel: vscode.WebviewPanel; port: Port; pending?: string };

let tabSeq = 0;

export class ConversationPanels {
  static readonly viewType = "jam.conversationTab";

  private tabs = new Map<string, Tab>();

  constructor(private d: ConversationDeps) {}

  has(channel: string): boolean {
    return this.tabs.has(channel);
  }

  // open reveals the tab for channel, else makes a new one in the active column.
  open(channel: string): void {
    if (!validChannel(channel)) return;
    const have = this.tabs.get(channel);
    if (have) return have.panel.reveal();
    const panel = this.d.createPanel(ConversationPanels.viewType, "Conversation", vscode.ViewColumn.Active, {
      enableScripts: true,
      enableCommandUris: false,
      enableFindWidget: true,
      retainContextWhenHidden: false,
      localResourceRoots: [vscode.Uri.joinPath(this.d.extensionUri, "dist", "webview")],
    });
    this.attach(panel, channel);
  }

  // restore is the serializer: as BoardPanels.restore, a revived tab for a
  // channel already open closes and the open one is revealed.
  restore(panel: vscode.WebviewPanel, state: unknown): void {
    const channel = state && typeof state === "object" ? (state as { channel?: unknown }).channel : undefined;
    if (!validChannel(channel)) return panel.dispose();
    const have = this.tabs.get(channel);
    if (have) {
      panel.dispose();
      return have.panel.reveal();
    }
    this.attach(panel, channel);
  }

  // reveal shows channel's tab and scrolls it to anchor (or on its next
  // ready, when its JS isn't running); false when there is no such tab.
  reveal(channel: string, anchor?: string): boolean {
    const t = this.tabs.get(channel);
    if (!t) return false;
    t.panel.reveal();
    if (anchor) {
      t.pending = anchor;
      t.port.post({ type: "scrollTo", anchor });
    }
    return true;
  }

  private attach(panel: vscode.WebviewPanel, channel: string): void {
    panel.iconPath = vscode.Uri.joinPath(this.d.extensionUri, "media", "jam-color.svg");
    const w = panel.webview;
    w.html = paneHtml(w, this.d.extensionUri, "conversation", { channel });
    const id = `conversation-tab-${++tabSeq}`;
    let viewing: string | null = null;
    let target: SessionTarget | undefined;
    const report = (): void => this.d.reporter.pane(id, { visible: panel.visible && panel.active, channel: viewing });
    const lookup = async (): Promise<SessionTarget | null> => {
      const t = await this.d.resolve(channel);
      if (t === "gone") return null;
      target = t;
      if ("label" in t) panel.title = tabTitle(t.label);
      return t;
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
    const tab: Tab = { panel, port };
    this.tabs.set(channel, tab);
    const off = this.d.auth.onChange(() => void port.refresh().catch(() => undefined));
    const subs = [
      w.onDidReceiveMessage((m: { type?: unknown } | undefined) => {
        if (!m || typeof m !== "object" || m.type !== "ready" || tab.pending === undefined) return;
        port.post({ type: "scrollTo", anchor: tab.pending });
        tab.pending = undefined;
      }),
      panel.onDidChangeViewState(() => {
        report();
        if (!panel.active) return;
        if (target) this.d.onActive(target);
        else void lookup().then((t) => t && panel.active && this.d.onActive(t), () => undefined);
      }),
    ];
    panel.onDidDispose(() => {
      off();
      port.dispose();
      for (const s of subs) s.dispose();
      this.d.reporter.drop(id);
      if (this.tabs.get(channel) === tab) this.tabs.delete(channel);
    });
  }
}
