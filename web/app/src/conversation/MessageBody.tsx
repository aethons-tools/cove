import { useEffect, useRef } from "react";
import { copyText } from "./copy";
import "./body.css";

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
    b.textContent = "⧉";
    b.onclick = () => void copyText((pre.textContent ?? "").replace(/\n$/, "")).then((ok) => {
      if (ok) { b.textContent = "✓"; setTimeout(() => { b.textContent = "⧉"; }, 1200); }
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
