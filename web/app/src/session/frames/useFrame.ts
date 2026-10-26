import { useCallback, useEffect, useRef, useState, type RefObject } from "react";
import { apiPost, ApiError } from "../../api/client";
import type { FrameURL } from "../../api/types";

export type FrameKind = "ide" | "preview";

export interface FrameState {
  src: string; // the iframe's URL ("" = no frame)
  seq: number; // bumps on every mint: the iframe is keyed by it
  status: string;
  reopen: boolean; // offer Reopen this view (a second signin)
  open: string; // the mint's plain URL ("" = none yet)
  frame: RefObject<HTMLIFrameElement | null>;
  start(): void; // a user action: mint afresh, the retries re-armed
  loaded(): void;
}

const httpURL = (u: unknown): u is string => typeof u === "string" && /^https?:\/\//i.test(u);
const MINT_REFUSED: Record<number, string> = { 403: "No longer shared with you.", 404: "Nothing to show.", 429: "Too many views opened; wait a minute." };
const loading = (kind: FrameKind) => (kind === "ide" ? "Starting the IDE…" : "Loading…");

// useFrame is one framed view (the Studio's IDE, a preview's View here), as
// layout.html's meFrame* did: a URL comes only from the frame route, for this
// view's name; a frame-error is heeded only from this view's own current
// frame, and re-mints at most once per signin/cookies per user action.
export function useFrame(sid: string, name: string, kind: FrameKind): FrameState {
  const [s, set] = useState({ src: "", seq: 0, status: loading(kind), reopen: false, open: "" });
  const frame = useRef<HTMLIFrameElement | null>(null);
  const r = useRef({ seq: 0, err: false, signin: false, cookies: false, live: true });

  const mint = useCallback(() => {
    const c = r.current, n = ++c.seq;
    c.err = false;
    set((x) => ({ ...x, src: "", seq: n, reopen: false, status: loading(kind) }));
    apiPost<FrameURL>(`sessions/${encodeURIComponent(sid)}/frame`, { name }).then((j) => {
      if (!c.live || c.seq !== n) return;
      const open = httpURL(j.open) ? j.open : "";
      if (!httpURL(j.url)) {
        c.err = true;
        set((x) => ({ ...x, open: open || x.open, status: "Couldn't open this view." }));
        return;
      }
      set((x) => ({ ...x, src: j.url, open: open || x.open }));
    }, (e: unknown) => {
      if (!c.live || c.seq !== n) return;
      c.err = true;
      set((x) => ({ ...x, status: (e instanceof ApiError && MINT_REFUSED[e.status]) || "Couldn't open this view." }));
    });
  }, [sid, name, kind]);

  const start = useCallback(() => {
    r.current.signin = false;
    r.current.cookies = false;
    mint();
  }, [mint]);

  useEffect(() => {
    const c = r.current;
    c.live = true;
    start();
    const say = (status: string) => { c.err = true; set((x) => ({ ...x, status })); };
    const onMessage = (e: MessageEvent) => {
      const d = e.data as { jam?: unknown; status?: unknown } | null;
      if (!d || typeof d !== "object" || d.jam !== "frame-error") return;
      const w = frame.current?.contentWindow;
      if (!w || e.source !== w) return; // only this view's current frame
      const st = String(d.status);
      if (st === "signin") {
        if (!c.signin) { c.signin = true; mint(); return; }
        c.err = true;
        set((x) => ({ ...x, status: "", reopen: true }));
        return;
      }
      if (st === "cookies") { // an expired sign-in says "cookies" too: try once more first
        if (!c.cookies) { c.cookies = true; mint(); return; }
        say("This browser can't sign in inside the pane — use Open in new tab.");
        return;
      }
      if (st === "403") return say("No longer shared with you.");
      if (st === "404") return say("Nothing to show: the studio isn't running.");
      if (st === "502" || st === "503" || st === "504") {
        return say(kind === "ide" ? "The IDE didn't start. This studio's image may predate it: upgrade the studio, or expose an IDE as ide." : "The preview isn't answering.");
      }
      say("Couldn't show this view.");
    };
    // Restored from the back/forward cache: the frame's ticket is spent.
    const onShow = (e: Event) => { if ((e as PageTransitionEvent).persisted) start(); };
    window.addEventListener("message", onMessage);
    window.addEventListener("pageshow", onShow);
    return () => {
      c.live = false;
      window.removeEventListener("message", onMessage);
      window.removeEventListener("pageshow", onShow);
    };
  }, [start, mint, kind]);

  const loaded = useCallback(() => { if (!r.current.err) set((x) => ({ ...x, status: "" })); }, []);
  return { ...s, frame, start, loaded };
}
