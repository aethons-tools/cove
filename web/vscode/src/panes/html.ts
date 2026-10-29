// The webview's HTML: the pane bundle (dist/webview, by asWebviewUri) under a
// nonce CSP that allows no network at all, so every byte goes through the host.
import { randomBytes } from "node:crypto";
import type { PaneName } from "../../../app/src/bridge/protocol.js" with { "resolution-mode": "import" };

// Attributes are all double-quoted, so these are the characters that matter.
const attr = (s: string): string => s.replace(/&/g, "&amp;").replace(/"/g, "&quot;").replace(/</g, "&lt;").replace(/>/g, "&gt;");

// newNonce is 32 random bytes in base64, for one render.
export function newNonce(): string {
  return randomBytes(32).toString("base64");
}

export function csp(cspSource: string, nonce: string): string {
  return [
    "default-src 'none'",
    `script-src 'nonce-${nonce}' ${cspSource}`,
    `style-src ${cspSource} 'unsafe-inline'`,
    `font-src ${cspSource}`,
    `img-src ${cspSource} blob: data:`,
    "connect-src 'none'",
    "frame-src 'none'",
    "base-uri 'none'",
    "form-action 'none'",
  ].join("; ");
}

export function webviewHtml(o: { cspSource: string; nonce: string; script: string; style: string; pane: PaneName }): string {
  return `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta http-equiv="Content-Security-Policy" content="${attr(csp(o.cspSource, o.nonce))}">
<meta name="viewport" content="width=device-width, initial-scale=1">
<link rel="stylesheet" href="${attr(o.style)}">
</head>
<body>
<div id="root" data-pane="${attr(o.pane)}"></div>
<script type="module" nonce="${attr(o.nonce)}" src="${attr(o.script)}"></script>
</body>
</html>
`;
}
