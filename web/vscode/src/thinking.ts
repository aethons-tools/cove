// Host-side Thinking streams: one SSE connection per (session, stream) that
// some Thinking pane in the window shows, shared by those panes. The router
// refuses /thinking/events from panes; only this class opens it. A pane that
// joins late is sent what the connection has seen (the replay), and a network
// drop resumes with Last-Event-ID.
import type { ThinkingEventName } from "../../app/src/bridge/protocol.js" with { "resolution-mode": "import" };
import { LIVE_BACKOFF_MAX_MS, LIVE_BACKOFF_MIN_MS, LIVE_STALL_MS, SseParser, type LiveDeps } from "./live.js";
import { checkPath, ID_RE } from "./bridge/router.js";

export const THINKING_REPLAY_MAX = 2000;
export const STREAM_RE = /^[0-9a-f]{32}$/;

export type ThinkingSignal = { event: ThinkingEventName; data: string } | { error: true; closed: boolean };

const ALLOWED = new Set<string>(["stream", "totals", "ev", "more", "skipped", "newstream"]);

// pending: a late subscriber whose replay hasn't been delivered yet. It gets no
// live signal until then; the replay already holds any event it skipped.
type Sub = { cb: (s: ThinkingSignal) => void; onEnd?: () => void; pending?: boolean };
type Item = { event: ThinkingEventName; data: string };

class Key {
  subs = new Set<Sub>();
  state: "connecting" | "open" | "retrying" = "connecting";
  gen = 0;
  attempt = 0;
  lastId: string | undefined;
  ac: AbortController | undefined;
  retryTimer: ReturnType<typeof setTimeout> | undefined;
  stallTimer: ReturnType<typeof setTimeout> | undefined;
  replay: Item[] = [];
  evCount = 0;
  synthetic = false; // a synthetic "more" replaced the server's
  constructor(
    readonly session: string,
    readonly stream: string,
  ) {}

  remember(e: Item): void {
    const r = this.replay;
    switch (e.event) {
      case "stream":
      case "totals":
        this.replay = r.filter((i) => i.event !== e.event);
        this.replay.push(e);
        return;
      case "more":
        if (this.synthetic) return;
        this.replay = r.filter((i) => i.event !== "more");
        this.replay.push(e);
        return;
      case "skipped":
        r.push(e);
        return;
      case "ev": {
        r.push(e);
        if (++this.evCount <= THINKING_REPLAY_MAX) return;
        r.splice(r.findIndex((i) => i.event === "ev"), 1);
        this.evCount--;
        const at = r.findIndex((i) => i.event === "ev");
        let seq: unknown;
        try {
          seq = (JSON.parse(r[at]!.data) as { seq?: unknown }).seq;
        } catch {
          return;
        }
        if (typeof seq !== "number") return;
        this.replay = r.filter((i) => i.event !== "more");
        this.replay.splice(this.replay.findIndex((i) => i.event === "ev"), 0, { event: "more", data: String(seq) });
        this.synthetic = true;
        return;
      }
    }
  }
}

export class ThinkingStreams {
  private keys = new Map<string, Key>();
  private notified = new Set<string>();

  constructor(private d: LiveDeps) {}

  get connections(): number {
    return this.keys.size;
  }

  subscribe(session: string, stream: string, cb: (s: ThinkingSignal) => void, onEnd?: () => void): () => void {
    if (!ID_RE.ses.test(session)) throw new Error("Not a session id.");
    if (stream !== "" && !STREAM_RE.test(stream)) throw new Error("Not a stream id.");
    const id = `${session}\n${stream}`;
    let k = this.keys.get(id);
    const first = !k;
    if (!k) {
      k = new Key(session, stream);
      this.keys.set(id, k);
    }
    const key = k;
    const sub: Sub = { cb, onEnd, pending: !first };
    key.subs.add(sub);
    if (first) void this.connect(id, key, key.gen);
    else {
      queueMicrotask(() => {
        sub.pending = false;
        if (!key.subs.has(sub)) return;
        if (key.state === "open") this.deliver(sub, { event: "open", data: "" });
        for (const i of [...key.replay]) {
          if (!key.subs.has(sub)) return;
          this.deliver(sub, i);
        }
      });
    }
    return () => {
      if (!key.subs.delete(sub)) return;
      if (key.subs.size === 0 && this.keys.get(id) === key) {
        this.halt(key);
        this.keys.delete(id);
      }
    };
  }

  // restart ends every key: sign-in or sign-out changed the token.
  restart(): void {
    for (const [id, k] of [...this.keys]) this.end(id, k, true);
  }

  private deliver(s: Sub, sig: ThinkingSignal): void {
    try {
      s.cb(sig);
    } catch {
      // one bad subscriber must not starve the rest or leak the connection
    }
  }

  // emit sends a live signal. A pending subscriber still gets newstream, which
  // ends the key before its replay could run.
  private emit(k: Key, sig: ThinkingSignal): void {
    const always = "event" in sig && sig.event === "newstream";
    for (const s of [...k.subs]) if (always || !s.pending) this.deliver(s, sig);
  }

  private halt(k: Key): void {
    k.gen++;
    k.ac?.abort();
    k.ac = undefined;
    if (k.retryTimer !== undefined) this.d.clearTimeout(k.retryTimer);
    if (k.stallTimer !== undefined) this.d.clearTimeout(k.stallTimer);
    k.retryTimer = undefined;
    k.stallTimer = undefined;
  }

  // end drops the key, optionally telling its subscribers, then frees their slots.
  private end(id: string, k: Key, errorSignal: boolean): void {
    this.halt(k);
    if (this.keys.get(id) === k) this.keys.delete(id);
    const subs = [...k.subs];
    if (errorSignal) for (const s of subs) this.deliver(s, { error: true, closed: true });
    k.subs.clear();
    for (const s of subs) {
      try {
        s.onEnd?.();
      } catch {
        // as above
      }
    }
  }

  private armStall(k: Key, ac: AbortController, gen: number): void {
    if (k.stallTimer !== undefined) this.d.clearTimeout(k.stallTimer);
    k.stallTimer = this.d.setTimeout(() => {
      k.stallTimer = undefined;
      if (gen === k.gen) ac.abort();
    }, LIVE_STALL_MS);
  }

  private retry(k: Key, gen: number): void {
    if (gen !== k.gen) return;
    k.state = "retrying";
    this.emit(k, { error: true, closed: false });
    const base = Math.min(LIVE_BACKOFF_MAX_MS, LIVE_BACKOFF_MIN_MS * 2 ** k.attempt);
    k.attempt++;
    const delay = base * (0.8 + 0.4 * this.d.random());
    k.retryTimer = this.d.setTimeout(() => {
      k.retryTimer = undefined;
      void this.connect(`${k.session}\n${k.stream}`, k, gen);
    }, delay);
  }

  private async connect(id: string, k: Key, gen: number): Promise<void> {
    if (gen !== k.gen) return;
    let s: Awaited<ReturnType<LiveDeps["session"]>>;
    try {
      s = await this.d.session();
    } catch {
      return this.retry(k, gen);
    }
    if (gen !== k.gen) return;
    if (!s) return this.end(id, k, true);
    const path = `/app/user/session/${k.session}/thinking/events${k.stream ? `?stream=${k.stream}` : ""}`;
    const url = checkPath(path, s.url, { streams: true });
    if (url instanceof Error) return this.end(id, k, true);
    const ac = new AbortController();
    k.ac = ac;
    this.armStall(k, ac, gen);
    try {
      const headers: Record<string, string> = { Authorization: `Bearer ${s.token}`, Accept: "text/event-stream" };
      if (k.lastId !== undefined) headers["Last-Event-ID"] = k.lastId;
      const res = await this.d.fetch(url, { headers, redirect: "manual", signal: ac.signal });
      if (gen !== k.gen) return;
      if (res.status === 401) {
        await res.body?.cancel().catch(() => undefined);
        if (!this.notified.has(s.token)) {
          this.notified.add(s.token);
          try {
            this.d.onSignedOut(s.token);
          } catch {
            // the sign-out hook must not keep the key alive
          }
        }
        return this.end(id, k, true);
      }
      if (res.status !== 200 || !res.body) {
        await res.body?.cancel().catch(() => undefined);
        return this.end(id, k, true);
      }
      k.state = "open";
      k.attempt = 0;
      this.armStall(k, ac, gen);
      this.emit(k, { event: "open", data: "" });
      const parser = new SseParser();
      const dec = new TextDecoder();
      const reader = res.body.getReader();
      for (;;) {
        const { done, value } = await reader.read();
        if (gen !== k.gen) return;
        if (done) break;
        this.armStall(k, ac, gen);
        for (const e of parser.push(dec.decode(value, { stream: true }))) {
          if (gen !== k.gen) return;
          if (e.id !== undefined) k.lastId = e.id;
          if (!ALLOWED.has(e.event)) continue;
          const item: Item = { event: e.event as ThinkingEventName, data: e.data };
          if (item.event === "newstream") {
            this.emit(k, item);
            return this.end(id, k, false);
          }
          k.remember(item);
          this.emit(k, item);
        }
      }
    } catch {
      // aborted (stall or stop) or network error: fall through to retry
    }
    if (gen !== k.gen) return;
    ac.abort(); // never leave the old stream open behind a retry
    if (k.stallTimer !== undefined) this.d.clearTimeout(k.stallTimer);
    k.stallTimer = undefined;
    this.retry(k, gen);
  }
}
