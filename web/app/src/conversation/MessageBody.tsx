import { useEffect, useRef } from "react";
import { copyText } from "./copy";
import "./body.css";

// ICONS are the copy button's two states (a constant: never message content).
const ICONS = '<svg class="cp" width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" aria-hidden="true"><rect x="5" y="5" width="9" height="9" rx="1.5"/><path d="M11 5V3.5A1.5 1.5 0 0 0 9.5 2h-6A1.5 1.5 0 0 0 2 3.5v6A1.5 1.5 0 0 0 3.5 11H5"/></svg>'
  + '<svg class="ok" width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><path d="M3 8.5l3.2 3L13 4.5"/></svg>';

// addCodeButtons gives each code block a copy button, in a wrapper so the
// button stays put while the block scrolls sideways (the old meCodeButtons).
function addCodeButtons(root: HTMLElement) {
  root.querySelectorAll("pre").forEach((pre) => {
    if (pre.parentElement?.classList.contains("codebox")) return;
    const box = document.createElement("div");
    box.className = "codebox";
    const b = document.createElement("button");
    b.type = "button";
    b.className = "copy-code";
    b.title = "Copy code";
    b.setAttribute("aria-label", "Copy code");
    // Icon only: no text of its own to leak into the message's "Text" copy.
    b.innerHTML = ICONS;
    b.onclick = () => void copyText((pre.textContent ?? "").replace(/\n$/, "")).then((ok) => {
      if (ok) { b.classList.add("copied"); setTimeout(() => b.classList.remove("copied"), 1200); }
    });
    pre.parentNode?.insertBefore(box, pre);
    box.append(pre, b);
  });
}

// MessageBody shows a squawk body as squawkrender rendered it on the server.
export function MessageBody({ html, plain }: { html: string; plain: boolean }) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => { if (ref.current && !plain) addCodeButtons(ref.current); }, [html, plain]);
  return (
    // eslint-disable-next-line no-restricted-syntax -- the one allowed site: squawkrender's sanitized HTML (spec §4, docs/web-app.md)
    <div ref={ref} className={plain ? "jam-body plain" : "jam-body md"} dangerouslySetInnerHTML={{ __html: html }} />
  );
}
