import { describe, expect, it } from "vitest";
import { ChannelGate, NOTIFY_BODY_MAX, parseNotify, notifyTarget, targetLink } from "../src/notify/event";
import type { NotifyEvent } from "../src/notify/event";

const SES = "ses_0123456789abcdefghjkmnpqrs";
const CHN = "chn_0123456789abcdefghjkmnpqrs";
const base = { title: "a · b", body: "hi", channel: CHN, id: "sqk_1", unread: 2, kind: "messages", session: SES };
const parse = (o: object) => parseNotify(JSON.stringify(o));

describe("parseNotify", () => {
  it("parses the server's event", () => {
    expect(parse(base)).toEqual({ id: "sqk_1", title: "a · b", body: "hi", channel: CHN, session: SES, kind: "messages" });
  });

  it.each([
    ["not json", "{"],
    ["array", "[]"],
    ["number title", JSON.stringify({ ...base, title: 5 })],
    ["number body", JSON.stringify({ ...base, body: 5 })],
    ["bad channel", JSON.stringify({ ...base, channel: "javascript:alert(1)" })],
    ["empty id", JSON.stringify({ ...base, id: "" })],
    ["long id", JSON.stringify({ ...base, id: "x".repeat(201) })],
    ["non-string session", JSON.stringify({ ...base, session: 3 })],
  ])("refuses bad input: %s", (_n, data) => {
    expect(parseNotify(data)).toBeUndefined();
  });

  it("cuts a 5,000-rune body to 200 with an ellipsis and drops control characters", () => {
    const e = parse({ ...base, body: "é".repeat(5000) })!;
    expect([...e.body]).toHaveLength(NOTIFY_BODY_MAX);
    expect(e.body.endsWith("…")).toBe(true);
    const c = parse({ ...base, title: "  a\u0007b\u001b[1m  ", body: "x\ny\tz\u0000" })!;
    expect(c.title).toBe("a b [1m");
    expect(c.body).toBe("x\ny z");
  });

  it("unknown kind is messages", () => {
    expect(parse({ ...base, kind: "weird" })!.kind).toBe("messages");
    expect(parse({ ...base, kind: "needs" })!.kind).toBe("needs");
  });

  it('a session failing SESSION_ID_RE becomes "" and the event is kept', () => {
    for (const s of ["../x", "alice-bot", ""]) {
      const e = parse({ ...base, session: s })!;
      expect(e.session).toBe("");
      expect(notifyTarget(e, "https://jam.example")).toEqual({ web: `https://jam.example/app/user/c/${CHN}` });
    }
  });
});

describe("ChannelGate", () => {
  it("admits one messages event per channel per 5 s and every needs event", () => {
    const g = new ChannelGate();
    const ev = (channel: string, kind: "needs" | "messages"): NotifyEvent => ({ id: "i", title: "t", body: "b", channel, session: "", kind });
    const admitted: number[] = [];
    for (let i = 0; i < 20; i++) {
      const kind = i === 7 ? "needs" : "messages";
      if (g.admit(ev(CHN, kind), i * 500)) admitted.push(i);
    }
    expect(admitted).toEqual([0, 7, 10]);
    const other = "chn_1123456789abcdefghjkmnpqrs";
    expect(g.admit(ev(other, "messages"), 500)).toBe(true);
  });
});

describe("targets", () => {
  it("notifyTarget and targetLink", () => {
    const e = parse(base)!;
    const t = notifyTarget(e, "https://jam.example");
    expect(t).toEqual({ session: SES });
    expect(targetLink(t, "vscode-insiders")).toBe(`vscode-insiders://aethons.jam/session/${SES}`);
    const chat = notifyTarget({ ...e, session: "" }, "https://jam.example");
    expect(targetLink(chat, "vscode")).toBe(`https://jam.example/app/user/c/${CHN}`);
  });
});
