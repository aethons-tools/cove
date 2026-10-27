import { useEffect, useRef } from "react";

// useFocusReport tells Jam which conversation is open in a visible, focused
// window, so it doesn't push what you're reading (layout.html's meFocus): on
// focus, blur and visibility changes, every 30 s while focused (Jam forgets
// it after 60 s, so a crashed tab stops suppressing), and null on pagehide.
export function useFocusReport(channel: string | null, on: boolean) {
  const last = useRef<string | null>(null);
  useEffect(() => {
    if (!on) return;
    const post = (ch: string | null) => {
      last.current = ch;
      void fetch("/app/user/push/focus", {
        method: "POST", credentials: "same-origin", keepalive: true,
        headers: { "Content-Type": "application/json" }, body: JSON.stringify({ channel: ch }),
      }).catch(() => {});
    };
    const current = () => (channel && document.visibilityState === "visible" && document.hasFocus() ? channel : null);
    const check = () => { const ch = current(); if (ch !== last.current) post(ch); };
    const hide = () => { if (last.current) post(null); };
    window.addEventListener("focus", check);
    window.addEventListener("blur", check);
    document.addEventListener("visibilitychange", check);
    window.addEventListener("pagehide", hide);
    const tick = setInterval(() => { const ch = current(); if (ch) post(ch); else check(); }, 30_000);
    check();
    return () => {
      window.removeEventListener("focus", check);
      window.removeEventListener("blur", check);
      document.removeEventListener("visibilitychange", check);
      window.removeEventListener("pagehide", hide);
      clearInterval(tick);
    };
  }, [channel, on]);
}
