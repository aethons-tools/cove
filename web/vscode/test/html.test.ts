import { describe, expect, it } from "vitest";
import { newNonce, webviewHtml } from "../src/panes/html";

const CDN = "https://x.vscode-cdn.net";
const html = (over: Partial<Parameters<typeof webviewHtml>[0]> = {}) =>
  webviewHtml({ cspSource: CDN, nonce: "N", script: `${CDN}/ext/dist/webview/pane.js`, style: `${CDN}/ext/dist/webview/pane.css`, pane: "conversation", ...over });
const unescape = (s: string) => s.replace(/&quot;/g, '"').replace(/&lt;/g, "<").replace(/&gt;/g, ">").replace(/&#39;/g, "'").replace(/&amp;/g, "&");

describe("webviewHtml", () => {
  it("the CSP is exactly the Constraints' string", () => {
    const m = /<meta http-equiv="Content-Security-Policy" content="([^"]*)">/.exec(html());
    expect(m).not.toBeNull();
    expect(unescape(m![1]!)).toBe(
      `default-src 'none'; script-src 'nonce-N' ${CDN}; style-src ${CDN} 'unsafe-inline'; font-src ${CDN}; img-src ${CDN} blob: data:; connect-src 'none'; frame-src 'none'; base-uri 'none'; form-action 'none'`,
    );
    expect(html().match(/Content-Security-Policy/g)).toHaveLength(1);
  });

  it("script carries the nonce and the asWebviewUri path", () => {
    const h = html();
    expect(h).toContain(`<script type="module" nonce="N" src="${CDN}/ext/dist/webview/pane.js"></script>`);
    expect(h).toContain(`<link rel="stylesheet" href="${CDN}/ext/dist/webview/pane.css">`);
    expect(h).toContain(`<div id="root" data-pane="conversation"></div>`);
    expect(h.match(/<script/g)).toHaveLength(1);
  });

  it("a quote in a path is escaped", () => {
    const h = html({ script: `${CDN}/a"b<c>&.js`, style: `${CDN}/s"><script>x</script>.css` });
    expect(h).toContain(`src="${CDN}/a&quot;b&lt;c&gt;&amp;.js"`);
    expect(h).toContain(`href="${CDN}/s&quot;&gt;&lt;script&gt;x&lt;/script&gt;.css"`);
    expect(h.match(/<script/g)).toHaveLength(1);
  });

  it("two nonces differ and are 44 base64 chars", () => {
    const a = newNonce();
    const b = newNonce();
    expect(a).not.toBe(b);
    for (const n of [a, b]) expect(n).toMatch(/^[A-Za-z0-9+/]{43}=$/);
  });
});
