// The pane never navigates: every link click becomes an open intent for the
// extension, a scroll within the pane, or nothing. That covers router Links
// and the server-rendered message HTML alike.
import type { OpenIntent } from "../bridge/protocol";

export type LinkAction = { intent: OpenIntent } | { scrollTo: string } | { allow: true } | { drop: true };

// HERE stands in for the Jam origin a link's path is relative to.
const HERE = "https://jam.invalid/";
const EXTERNAL = new Set(["http:", "https:", "mailto:"]);

// linkAction is what a click on href (a link's href attribute) does in the
// pane showing paneChannel.
export function linkAction(href: string, paneChannel: string): LinkAction {
  const h = href.trim();
  if (h.startsWith("#")) return { allow: true };
  let u: URL;
  try {
    u = new URL(h, HERE);
  } catch {
    return { drop: true };
  }
  if (u.origin !== new URL(HERE).origin || u.protocol !== "https:") {
    return EXTERNAL.has(u.protocol) ? { intent: { kind: "external", url: u.href } } : { drop: true };
  }
  const conv = /^\/app\/user\/c\/([^/]+)$/.exec(u.pathname);
  if (conv) {
    const channel = decoded(conv[1]!);
    if (channel === null) return { drop: true };
    if (channel !== paneChannel) return { intent: { kind: "conversation", channel } };
    return u.hash.startsWith("#ask-") ? { scrollTo: decoded(u.hash.slice(1)) ?? "" } : { drop: true };
  }
  const board = /^\/app\/user\/boards\/([^/]+)$/.exec(u.pathname);
  if (board) {
    const id = decoded(board[1]!);
    return id === null ? { drop: true } : { intent: { kind: "board", id } };
  }
  if (u.pathname.startsWith("/app/")) return { intent: { kind: "web", path: u.pathname + u.search + u.hash } };
  return { drop: true };
}

function decoded(s: string): string | null {
  try {
    return decodeURIComponent(s);
  } catch {
    return null; // a malformed escape
  }
}

// interceptLinks handles every link click in doc (capture phase, so before
// the router or the browser): channel is the pane's, post sends an intent.
// It returns the uninstall.
export function interceptLinks(doc: Document, channel: () => string, post: (i: OpenIntent) => void): () => void {
  const onClick = (e: MouseEvent) => {
    if (e.type === "auxclick" && e.button !== 1) return; // only a middle click opens
    const a = e.target instanceof Element ? e.target.closest("a[href], area[href]") : null;
    if (!a) return;
    const act = linkAction(a.getAttribute("href") ?? "", channel());
    if ("allow" in act) return;
    e.preventDefault();
    e.stopPropagation();
    if ("intent" in act) post(act.intent);
    else if ("scrollTo" in act) doc.getElementById(act.scrollTo)?.scrollIntoView({ behavior: "smooth", block: "center" });
  };
  doc.addEventListener("click", onClick, true);
  doc.addEventListener("auxclick", onClick, true);
  return () => {
    doc.removeEventListener("click", onClick, true);
    doc.removeEventListener("auxclick", onClick, true);
  };
}
