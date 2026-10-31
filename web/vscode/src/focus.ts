// The window's session: its studio's, resolved and cached. Pure: the
// resolver is injected, so the VS Code layer only supplies the API.
import type { JamApi } from "./api";
import { ApiError } from "./api";

export type SessionTarget = { session: string; label: string; channel: string; gone?: undefined } | { session: string; gone: true };

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
  private session: string | undefined;

  constructor(private d: { windowSession: string | undefined; resolve: (session: string) => Promise<SessionTarget> }) {
    this.session = d.windowSession;
  }

  windowSession(): string | undefined {
    return this.session;
  }

  // setSession changes the window's session (the hub follows its active
  // conversation tab); views refresh on the change.
  setSession(s: string | undefined): void {
    if (s === this.session) return;
    this.session = s;
    this.fire();
  }

  // target is the window's session (null when it has none); a failed lookup
  // isn't cached.
  async target(): Promise<SessionTarget | null> {
    const session = this.session;
    if (!session) return null;
    let p = this.cache.get(session);
    if (!p) {
      p = this.d.resolve(session);
      this.cache.set(session, p);
      p.catch(() => {
        if (this.cache.get(session) === p) this.cache.delete(session);
      });
    }
    return p;
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
