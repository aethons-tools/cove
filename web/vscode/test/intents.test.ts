import { describe, expect, it } from "vitest";
import type { OpenIntent } from "../../app/src/bridge/protocol.js" with { "resolution-mode": "import" };
import { intentAction } from "../src/bridge/intents.js";

const JAM = "https://jam.example.com";
const BRD = "brd_0123456789abcdefghjkmnpqrs";
const CHN = "chn_0123456789abcdefghjkmnpqrs";

describe("intentAction", () => {
  const errs: OpenIntent[] = [
    { kind: "external", url: "javascript:alert(1)" },
    { kind: "external", url: "command:x" },
    { kind: "external", url: "file:///x" },
    { kind: "external", url: "vscode://x/y" },
    { kind: "external", url: "not a url" },
    { kind: "board", id: "../x" },
    { kind: "board", id: CHN },
    { kind: "conversation", channel: BRD },
    { kind: "web", path: "/me/studios/x" },
    { kind: "web", path: "/app/user/../../me" },
    { kind: "nope" } as unknown as OpenIntent,
  ];
  for (const i of errs) it(`refuses ${JSON.stringify(i)}`, () => expect(intentAction(i, JAM)).toBeInstanceOf(Error));

  it("maps the allowed ones", () => {
    expect(intentAction({ kind: "external", url: "https://a.example/b" }, JAM)).toEqual({ openExternal: "https://a.example/b" });
    expect(intentAction({ kind: "external", url: "mailto:a@b.example" }, JAM)).toEqual({ openExternal: "mailto:a@b.example" });
    expect(intentAction({ kind: "board", id: BRD }, JAM)).toEqual({ openBoard: BRD });
    expect(intentAction({ kind: "conversation", channel: CHN }, JAM)).toEqual({ openExternal: `${JAM}/app/user/c/${CHN}` });
    expect(intentAction({ kind: "web", path: "/app/user/settings" }, JAM)).toEqual({ openExternal: `${JAM}/app/user/settings` });
    expect(intentAction({ kind: "focusSession" } as never, JAM)).toEqual(new Error("Unknown intent."));
  });

  const W = "ses_0abcdefghjkmnpqrstvwxyz012";
  const O = "ses_1abcdefghjkmnpqrstvwxyz012";
  const pv = (session: string, port: unknown, url: string): OpenIntent => ({ kind: "preview", session, port, url, inBrowser: false }) as OpenIntent;
  it("maps previews by the window's studio", () => {
    expect(intentAction(pv(W, 5173, "https://p.example/x"), JAM, W)).toEqual({ forwardPort: 5173, inBrowser: false, url: "https://p.example/x" });
    expect(intentAction(pv(O, 5173, "https://p.example/x"), JAM, W)).toEqual({ openExternal: "https://p.example/x" });
    expect(intentAction(pv(W, 5173, "https://p.example/x"), JAM)).toEqual({ openExternal: "https://p.example/x" });
  });
  for (const [s, port, url] of [
    [W, 5173, "javascript:alert(1)"],
    [O, 5173, "javascript:alert(1)"],
    [W, 0, "https://p.example/"],
    [W, 70000, "https://p.example/"],
    [W, 1.5, "https://p.example/"],
    [W, "22", "https://p.example/"],
    ["../x", 5173, "https://p.example/"],
  ] as const) it(`refuses preview ${s} ${port} ${url}`, () => expect(intentAction(pv(s, port, url), JAM, W)).toBeInstanceOf(Error));

  it("maps reveal and session", () => {
    expect(intentAction({ kind: "reveal", anchor: "ask-ask_1" }, JAM)).toEqual({ revealConversation: "ask-ask_1" });
    expect(intentAction({ kind: "reveal" }, JAM)).toEqual({ revealConversation: null });
    expect(intentAction({ kind: "reveal", anchor: 'x" onmouseover="' }, JAM)).toBeInstanceOf(Error);
    expect(intentAction({ kind: "session", id: W }, JAM)).toEqual({ openSession: W });
    expect(intentAction({ kind: "session", id: "../x" }, JAM)).toBeInstanceOf(Error);
  });
});
