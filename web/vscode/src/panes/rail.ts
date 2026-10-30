// The rail view (jam.rail): web/app's rail in a webview, in every window.
// Its state carries the sessions with an open studio window (▣), refreshed
// every CONNECTED_BEAT_MS; the needs-you badge sits on it.
import type * as vscode from "vscode";
import type { PaneState } from "../../../app/src/bridge/protocol.js" with { "resolution-mode": "import" };
import type { Auth } from "../auth";
import { CONNECTED_BEAT_MS } from "../open";
import { paneHtml, wirePane } from "./port";
import type { PortDeps, Port } from "./port";

export type RailDeps = PortDeps & {
  extensionUri: vscode.Uri;
  auth: Pick<Auth, "session" | "onChange">;
  windows: () => string[];
  // describe is the view's description (the window's Jam, with several).
  describe?: () => string;
};

type Badge = { value: number; tooltip: string } | undefined;

let railSeq = 0;

export class RailViewProvider implements vscode.WebviewViewProvider {
  readonly viewType = "jam.rail";
  private views = new Set<vscode.WebviewView>();
  private ports = new Set<Port>();
  private badge: Badge;

  constructor(private d: RailDeps) {}

  setBadge(b: Badge): void {
    this.badge = b;
    for (const v of this.views) v.badge = b;
  }

  refresh(): void {
    for (const v of this.views) v.description = this.d.describe?.() || undefined;
    for (const p of this.ports) void p.refresh().catch(() => undefined);
  }

  private async state(): Promise<PaneState> {
    const url = this.d.jamUrl();
    const signedIn = !(url instanceof Error) && (await this.d.auth.session(url).catch(() => undefined)) !== undefined;
    return { signedIn, target: null, windows: this.d.windows() };
  }

  resolveWebviewView(view: vscode.WebviewView, _context?: vscode.WebviewViewResolveContext, _token?: vscode.CancellationToken): void {
    const w = view.webview;
    w.html = paneHtml(w, this.d.extensionUri, "rail");
    view.badge = this.badge;
    view.description = this.d.describe?.() || undefined;
    const port = wirePane(w, `rail-${++railSeq}`, this.d, { state: () => this.state() });
    const refreshSoon = (): void => void port.refresh().catch(() => undefined);
    this.views.add(view);
    this.ports.add(port);
    const offAuth = this.d.auth.onChange(refreshSoon);
    const beat = setInterval(refreshSoon, CONNECTED_BEAT_MS);
    const subs = [
      // Without retainContextWhenHidden a hidden view's JS is gone.
      view.onDidChangeVisibility(() => {
        if (!view.visible) port.reset();
      }),
    ];
    subs.push(
      view.onDidDispose(() => {
        clearInterval(beat);
        offAuth();
        port.dispose();
        this.views.delete(view);
        this.ports.delete(port);
        for (const x of subs) x.dispose();
      }),
    );
  }
}
