// Whiteboards as editor tabs: one webview panel per board, kept alive while
// hidden (the board editor holds unsaved drawing state) and restored after a
// window reload from the {board} state the pane saves with setState.
import * as vscode from "vscode";
import type { Auth } from "../auth";
import { ID_RE } from "../bridge/router";
import { paneHtml, wirePane } from "./port";
import type { Port, PortDeps } from "./port";

export const TITLE_MAX = 60;

export type BoardDeps = PortDeps & {
  extensionUri: vscode.Uri;
  auth: Pick<Auth, "session" | "onChange">;
  createPanel: typeof vscode.window.createWebviewPanel;
};

// tabTitle is the board's title for its tab: control, bidi and zero-width
// characters dropped (they could reorder or hide part of the label), trimmed, at most TITLE_MAX characters, "Whiteboard" when nothing is left.
export function tabTitle(t: string): string {
  // eslint-disable-next-line no-control-regex
  const s = t.replace(/[\u0000-\u001f\u007f-\u009f\u200b-\u200f\u202a-\u202e\u2060-\u2069\ufeff]/g, "").trim();
  return Array.from(s).slice(0, TITLE_MAX).join("").trim() || "Whiteboard";
}

export class BoardPanels {
  static readonly viewType = "jam.board";

  private panels = new Map<string, vscode.WebviewPanel>();

  constructor(private d: BoardDeps) {}

  // open reveals the tab for id, else makes a new one in the active column.
  open(id: string): void {
    if (typeof id !== "string" || !ID_RE.brd.test(id)) return;
    const have = this.panels.get(id);
    if (have) return have.reveal();
    const panel = this.d.createPanel(BoardPanels.viewType, tabTitle(""), vscode.ViewColumn.Active, {
      enableScripts: true,
      enableCommandUris: false,
      enableFindWidget: false,
      retainContextWhenHidden: true,
      localResourceRoots: [vscode.Uri.joinPath(this.d.extensionUri, "dist", "webview")],
    });
    this.attach(panel, id);
  }

  // restore is the serializer: a panel VS Code revived after a reload. VS Code
  // revives a background tab only when it is first shown, so the board may
  // already be open (from a card or Artifacts): then the revived tab closes and
  // the open one is revealed, so the user's click still lands on the board.
  restore(panel: vscode.WebviewPanel, state: unknown): void {
    const id = state && typeof state === "object" ? (state as { board?: unknown }).board : undefined;
    if (typeof id !== "string" || !ID_RE.brd.test(id)) return panel.dispose();
    const have = this.panels.get(id);
    if (have) {
      panel.dispose();
      return have.reveal();
    }
    this.attach(panel, id);
  }

  private attach(panel: vscode.WebviewPanel, id: string): void {
    this.panels.set(id, panel);
    panel.iconPath = vscode.Uri.joinPath(this.d.extensionUri, "media", "jam.svg");
    const w = panel.webview;
    w.html = paneHtml(w, this.d.extensionUri, "board", { board: id });

    const port: Port = wirePane(w, `board-${id}`, this.d, {
      state: async () => {
        const url = this.d.jamUrl();
        const signedIn = !(url instanceof Error) && (await this.d.auth.session(url)) !== undefined;
        return { signedIn, target: null, pinned: false, elsewhere: false };
      },
      onTitle: (t) => {
        panel.title = tabTitle(t);
      },
    });
    const off = this.d.auth.onChange(() => void port.refresh().catch(() => undefined));
    panel.onDidDispose(() => {
      off();
      port.dispose();
      if (this.panels.get(id) === panel) this.panels.delete(id);
    });
  }
}
