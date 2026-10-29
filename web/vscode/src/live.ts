// One /app/user/events stream per window. The host holds the token and the
// stream; panes only ever see the forwarded events (see panes/hub.ts).
import type { RouterDeps } from "./bridge/router.js";

export const LIVE_BACKOFF_MIN_MS = 1000;
export const LIVE_BACKOFF_MAX_MS = 30_000;
export const LIVE_STALL_MS = 60_000;

export type LiveEvent = { event: string; data: string };

// SseParser turns text chunks into events. Lines end in \n, \r\n or \r; lines
// starting with ":" are comments; "retry:" and "id:" are ignored.
export class SseParser {
  private buf = "";
  private event = "";
  private data: string[] = [];
  private hasData = false;

  push(chunk: string): LiveEvent[] {
    this.buf += chunk;
    const out: LiveEvent[] = [];
    for (;;) {
      const m = /\r\n|\n|\r/.exec(this.buf);
      if (!m) break;
      // A trailing "\r" may be the first half of "\r\n": wait for more.
      if (m[0] === "\r" && m.index + 1 === this.buf.length) break;
      const line = this.buf.slice(0, m.index);
      this.buf = this.buf.slice(m.index + m[0].length);
      this.line(line, out);
    }
    return out;
  }

  private line(line: string, out: LiveEvent[]): void {
    if (line === "") {
      if (this.hasData) out.push({ event: this.event || "message", data: this.data.join("\n") });
      this.event = "";
      this.data = [];
      this.hasData = false;
      return;
    }
    if (line.startsWith(":")) return;
    const i = line.indexOf(":");
    const field = i < 0 ? line : line.slice(0, i);
    let value = i < 0 ? "" : line.slice(i + 1);
    if (value.startsWith(" ")) value = value.slice(1);
    if (field === "event") this.event = value;
    else if (field === "data") {
      this.data.push(value);
      this.hasData = true;
    }
  }
}

export type LiveDeps = {
  session: RouterDeps["session"];
  fetch: typeof fetch;
  onSignedOut: () => void;
  setTimeout: typeof setTimeout;
  clearTimeout: typeof clearTimeout;
  random: () => number;
};

export type LiveState = "idle" | "connecting" | "open" | "off" | "signedOut";

export class LiveStream {
  private st: LiveState = "idle";
  private subs = new Set<(e: LiveEvent) => void>();
  private ac: AbortController | undefined;
  private gen = 0;
  private attempt = 0;
  private retryTimer: ReturnType<typeof setTimeout> | undefined;
  private stallTimer: ReturnType<typeof setTimeout> | undefined;
  private notified = new Set<string>();

  constructor(private d: LiveDeps) {}

  get state(): LiveState {
    return this.st;
  }

  subscribe(cb: (e: LiveEvent) => void): () => void {
    const wrapped = (e: LiveEvent): void => cb(e);
    this.subs.add(wrapped);
    if (this.subs.size === 1) this.start();
    return () => {
      if (!this.subs.delete(wrapped)) return;
      if (this.subs.size === 0) {
        this.stop();
        this.st = "idle";
      }
    };
  }

  // restart reconnects now (after sign-in), if anyone is subscribed.
  restart(): void {
    if (this.subs.size === 0) return;
    this.stop();
    this.start();
  }

  private stop(): void {
    this.gen++;
    this.ac?.abort();
    this.ac = undefined;
    this.clearTimers();
    this.attempt = 0;
  }

  private clearTimers(): void {
    if (this.retryTimer !== undefined) this.d.clearTimeout(this.retryTimer);
    if (this.stallTimer !== undefined) this.d.clearTimeout(this.stallTimer);
    this.retryTimer = undefined;
    this.stallTimer = undefined;
  }

  private start(): void {
    void this.connect(this.gen);
  }

  private retry(gen: number): void {
    if (gen !== this.gen) return;
    const base = Math.min(LIVE_BACKOFF_MAX_MS, LIVE_BACKOFF_MIN_MS * 2 ** this.attempt);
    this.attempt++;
    const delay = base * (0.8 + 0.4 * this.d.random());
    this.st = "connecting";
    this.retryTimer = this.d.setTimeout(() => {
      this.retryTimer = undefined;
      void this.connect(gen);
    }, delay);
  }

  private emit(e: LiveEvent): void {
    for (const cb of [...this.subs]) {
      try {
        cb(e);
      } catch {
        // one bad subscriber must not drop the event for the rest
      }
    }
  }

  private armStall(ac: AbortController, gen: number): void {
    if (this.stallTimer !== undefined) this.d.clearTimeout(this.stallTimer);
    this.stallTimer = this.d.setTimeout(() => {
      this.stallTimer = undefined;
      if (gen !== this.gen) return;
      ac.abort();
    }, LIVE_STALL_MS);
  }

  private async connect(gen: number): Promise<void> {
    if (gen !== this.gen) return;
    this.st = "connecting";
    let s: Awaited<ReturnType<LiveDeps["session"]>>;
    try {
      s = await this.d.session();
    } catch {
      return this.retry(gen);
    }
    if (gen !== this.gen) return;
    if (!s) {
      this.st = "signedOut";
      return;
    }
    const ac = new AbortController();
    this.ac = ac;
    this.armStall(ac, gen);
    try {
      const res = await this.d.fetch(new URL("/app/user/events", s.url).toString(), {
        headers: { Authorization: `Bearer ${s.token}`, Accept: "text/event-stream" },
        redirect: "manual",
        signal: ac.signal,
      });
      if (gen !== this.gen) return;
      if (res.status === 401) {
        this.clearTimers();
        this.st = "signedOut";
        if (!this.notified.has(s.token)) {
          this.notified.add(s.token);
          this.d.onSignedOut();
        }
        return;
      }
      if (res.status === 204) {
        this.clearTimers();
        this.st = "off";
        return;
      }
      if (res.status !== 200 || !res.body) {
        await res.body?.cancel().catch(() => undefined);
        this.clearTimers();
        return this.retry(gen);
      }
      this.st = "open";
      this.attempt = 0;
      this.armStall(ac, gen);
      this.emit({ event: "open", data: "" });
      const parser = new SseParser();
      const dec = new TextDecoder();
      const reader = res.body.getReader();
      for (;;) {
        const { done, value } = await reader.read();
        if (gen !== this.gen) return;
        if (done) break;
        this.armStall(ac, gen);
        for (const e of parser.push(dec.decode(value, { stream: true }))) {
          if (gen !== this.gen) return;
          this.emit(e);
        }
      }
    } catch {
      // aborted (stall or stop) or network error: fall through to retry
    }
    if (gen !== this.gen) return;
    ac.abort(); // never leave the old stream open behind a retry
    this.clearTimers();
    this.retry(gen);
  }
}
