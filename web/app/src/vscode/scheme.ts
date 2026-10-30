import { useSyncExternalStore } from "react";

// schemeOf is the Mantine color scheme for VS Code's theme, which it names
// in the webview body's class (high-contrast light carries both
// vscode-high-contrast and vscode-high-contrast-light).
export function schemeOf(body: DOMTokenList): "light" | "dark" {
  if (body.contains("vscode-high-contrast-light")) return "light";
  return body.contains("vscode-dark") || body.contains("vscode-high-contrast") ? "dark" : "light";
}

function subscribe(changed: () => void): () => void {
  const o = new MutationObserver(changed);
  o.observe(document.body, { attributes: true, attributeFilter: ["class"] });
  return () => o.disconnect();
}

// useVscodeScheme follows the theme as VS Code switches it.
export function useVscodeScheme(): "light" | "dark" {
  return useSyncExternalStore(subscribe, () => schemeOf(document.body.classList));
}
