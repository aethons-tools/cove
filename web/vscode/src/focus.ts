// The window's focused session and per-pane pins. Pure: state and the resolver
// are injected, so the VS Code layer only supplies workspaceState and the API.
import type { JamApi } from "./api";
import { ApiError } from "./api";
import { sessionOfPick, sessionPicks } from "./open";
import type { SessionPick, Ui } from "./open";

export type SessionTarget = { session: string; label: string; channel: string; gone?: undefined } | { session: string; gone: true };

export interface StateLike {
  get<T>(k: string): T | undefined;
  update(k: string, v: unknown): Thenable<void>;
}

const FOCUS = "jam.focus";
const PINS = "jam.pins";

// sessionHead -> target; a 404 is a gone session, anything else propagates.
export function sessionResolver(api: Pick<JamApi, "sessionHead">): (session: string) => Promise<SessionTarget> {
  return async (session) => {
    try {
      const h = await api.sessionHead(session);
      return { session, label: h.label, channel: h.channel };
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) return { session, gone: true };
      throw e;
    }
  };
}

export class FocusModel {
  private cache = new Map<string, Promise<SessionTarget>>();
  private subs = new Set<() => void>();
  constructor(private d: { windowDefault: string | undefined; state: StateLike; resolve: (session: string) => Promise<SessionTarget> }) {}

  windowSession(): string | undefined {
    return this.d.state.get<string>(FOCUS) ?? this.d.windowDefault;
  }

  async focus(session: string | undefined): Promise<void> {
    await this.d.state.update(FOCUS, session === undefined || session === this.d.windowDefault ? undefined : session);
    this.fire();
  }

  private pins(): Record<string, string> {
    return this.d.state.get<Record<string, string>>(PINS) ?? {};
  }

  isPinned(view: string): boolean {
    return view in this.pins();
  }

  async togglePin(view: string): Promise<void> {
    const pins = { ...this.pins() };
    if (view in pins) delete pins[view];
    else {
      const s = this.windowSession();
      if (!s) return;
      pins[view] = s;
    }
    await this.d.state.update(PINS, pins);
    this.fire();
  }

  async stateOf(view: string): Promise<{ target: SessionTarget | null; pinned: boolean; elsewhere: boolean }> {
    const pinned = this.isPinned(view);
    const session = this.pins()[view] ?? this.windowSession();
    if (!session) return { target: null, pinned, elsewhere: false };
    let p = this.cache.get(session);
    if (!p) {
      p = this.d.resolve(session);
      this.cache.set(session, p);
      p.catch(() => this.cache.delete(session));
    }
    return { target: await p, pinned, elsewhere: session !== this.windowSession() };
  }

  invalidate(): void {
    this.cache.clear();
    this.fire();
  }

  onChange(cb: () => void): () => void {
    this.subs.add(cb);
    return () => void this.subs.delete(cb);
  }

  private fire(): void {
    for (const cb of [...this.subs]) cb();
  }
}

type Item = { label: string; pick?: SessionPick };

export async function focusSessionCommand(d: { api: JamApi; ui: Ui; model: FocusModel; hasDefault: boolean }): Promise<void> {
  const items: Item[] = d.hasDefault ? [{ label: "$(home) This window's studio" }] : [];
  items.push(...(await sessionPicks(d.api)).map((pick) => ({ label: pick.label, description: pick.description, detail: pick.detail, pick })));
  const p = await d.ui.pick(items, "Focus a session (unpinned Jam panes follow it)");
  if (!p) return;
  if (!p.pick) return d.model.focus(undefined);
  const session = await sessionOfPick(d.api, p.pick);
  if (!session) {
    await d.ui.error("That conversation has no session to focus");
    return;
  }
  await d.model.focus(session);
}
