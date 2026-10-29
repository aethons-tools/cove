// The Needs You tree view: the rows of the web app's rail "needs-you" group.
// Its count is the badge on the Jam activity-bar icon.
import * as vscode from "vscode";
import { SignedOutError } from "./api";
import type { JamApi } from "./api";
import type { LiveEvent } from "./live";
import type { RailRow } from "./open";
import type { RailResponse } from "../../app/src/api/types.js" with { "resolution-mode": "import" };

export const NEEDS_YOU_DEBOUNCE_MS = 3000;

// The rows of the group with bucket "needs-you" (the web app's Needs you group),
// the first occurrence of each id.
export function needsYouRows(r: RailResponse): RailRow[] {
  const seen = new Set<string>();
  const out: RailRow[] = [];
  for (const g of r.groups ?? []) {
    if (g.bucket !== "needs-you") continue;
    for (const row of g.rows ?? []) {
      if (seen.has(row.id)) continue;
      seen.add(row.id);
      out.push(row);
    }
  }
  return out;
}

export function badgeFor(n: number): { value: number; tooltip: string } | undefined {
  if (n <= 0) return undefined;
  return { value: n, tooltip: n === 1 ? "1 conversation needs you" : `${n} conversations need you` };
}

export type NeedsYouDeps = {
  session: () => Promise<{ url: string; token: string } | undefined>;
  api: (s: { url: string; token: string }) => Pick<JamApi, "rail">;
  setBadge: (b: { value: number; tooltip: string } | undefined) => void;
  setContext: (key: "jam.signedIn", v: boolean) => void;
  onSignedOut: (token: string) => void;
  setTimeout: typeof setTimeout;
  clearTimeout: typeof clearTimeout;
};

const LIVE_EVENTS = new Set(["open", "changed", "presence", "notify"]);

export class NeedsYou implements vscode.TreeDataProvider<RailRow> {
  private rows: RailRow[] = [];
  private timer: ReturnType<typeof setTimeout> | undefined;
  private running: Promise<void> | undefined;
  private again = false;
  private changed = new vscode.EventEmitter<void>();
  readonly onDidChangeTreeData: vscode.Event<void> = this.changed.event;

  constructor(private d: NeedsYouDeps) {}

  onLive(e: LiveEvent): void {
    if (LIVE_EVENTS.has(e.event)) this.poke();
  }

  poke(): void {
    if (this.timer !== undefined) return;
    this.timer = this.d.setTimeout(() => {
      this.timer = undefined;
      void this.refresh();
    }, NEEDS_YOU_DEBOUNCE_MS);
  }

  refresh(): Promise<void> {
    if (this.running) {
      this.again = true;
      return this.running;
    }
    const p = (async () => {
      do {
        this.again = false;
        await this.once();
      } while (this.again);
    })().finally(() => {
      this.running = undefined;
    });
    this.running = p;
    return p;
  }

  private signedOut(): void {
    this.rows = [];
    this.d.setBadge(undefined);
    this.d.setContext("jam.signedIn", false);
    this.changed.fire();
  }

  private async once(): Promise<void> {
    let s: { url: string; token: string } | undefined;
    try {
      s = await this.d.session();
    } catch {
      return;
    }
    if (!s) return this.signedOut();
    try {
      const rows = needsYouRows(await this.d.api(s).rail());
      this.rows = rows;
      this.d.setBadge(badgeFor(rows.length));
      this.d.setContext("jam.signedIn", true);
      this.changed.fire();
    } catch (e) {
      if (e instanceof SignedOutError) {
        this.d.onSignedOut(s.token);
        this.signedOut();
      }
      // any other error keeps the last rows and badge
    }
  }

  dispose(): void {
    if (this.timer !== undefined) this.d.clearTimeout(this.timer);
    this.timer = undefined;
  }

  getChildren(): RailRow[] {
    return this.rows;
  }

  getTreeItem(r: RailRow): vscode.TreeItem {
    const item = new vscode.TreeItem(r.label, vscode.TreeItemCollapsibleState.None);
    item.description = r.project;
    item.tooltip = r.mission;
    item.iconPath = new vscode.ThemeIcon("bell");
    item.command = { command: "jam.needsYou.open", title: "Open", arguments: [r] };
    return item;
  }
}
