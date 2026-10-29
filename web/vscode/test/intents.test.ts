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
    expect(intentAction({ kind: "board", id: BRD }, JAM)).toEqual({ openExternal: `${JAM}/app/user/boards/${BRD}` });
    expect(intentAction({ kind: "conversation", channel: CHN }, JAM)).toEqual({ openExternal: `${JAM}/app/user/c/${CHN}` });
    expect(intentAction({ kind: "web", path: "/app/user/settings" }, JAM)).toEqual({ openExternal: `${JAM}/app/user/settings` });
    expect(intentAction({ kind: "focusSession" }, JAM)).toEqual({ command: "jam.focusSession" });
  });
});
