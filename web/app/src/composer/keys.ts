type Keyish = { key: string; code: string; metaKey: boolean; ctrlKey: boolean; shiftKey: boolean; altKey: boolean; isComposing?: boolean };

export const isMac = () => /Mac|iPhone|iPad/.test(navigator.platform);

// isSendKey: Cmd+Enter on a Mac, Ctrl+Enter elsewhere; never with Shift or
// Alt, nor mid-composition (an IME's Enter commits the text).
export function isSendKey(e: Keyish, mac: boolean): boolean {
  const mod = mac ? e.metaKey && !e.ctrlKey : e.ctrlKey && !e.metaKey;
  return e.key === "Enter" && mod && !e.shiftKey && !e.altKey && !e.isComposing;
}

// isPasteAsCodeKey: the browsers' own paste-as-plain-text shortcut, so a real
// paste event (no permission prompt) follows.
export function isPasteAsCodeKey(e: Keyish, mac: boolean): boolean {
  return e.code === "KeyV" && e.shiftKey && (mac ? e.metaKey : e.ctrlKey && !e.altKey);
}

export function isDraftKey(e: Keyish, mac: boolean): "push" | "pop" | null {
  if (!(mac ? e.metaKey : e.ctrlKey) || e.shiftKey || e.altKey) return null;
  return e.key === "ArrowDown" ? "push" : e.key === "ArrowUp" ? "pop" : null;
}

export const hint = (mac: boolean) => mac
  ? "⌘↵ to send · ⇧⌘V pastes as code · ⌘↓ stacks a draft"
  : "Ctrl+↵ to send · ⇧Ctrl+V pastes as code · Ctrl+↓ stacks a draft";
