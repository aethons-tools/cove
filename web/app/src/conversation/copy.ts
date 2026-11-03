// copyText puts text on the clipboard: the async API in a secure context,
// else (or when it refuses) a hidden textarea and execCommand (the old
// Inbox's meCopy).
export async function copyText(text: string): Promise<boolean> {
  if (navigator.clipboard && window.isSecureContext) {
    try { await navigator.clipboard.writeText(text); return true; } catch { /* try execCommand */ }
  }
  const ta = document.createElement("textarea");
  ta.value = text;
  ta.style.position = "fixed";
  ta.style.opacity = "0";
  document.body.appendChild(ta);
  ta.select();
  try { return document.execCommand?.("copy") ?? false; } catch { return false; } finally { ta.remove(); }
}
