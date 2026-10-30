import { describe, expect, it, vi } from "vitest";
import { ChannelGate } from "../src/notify/event";
import type { NotifyKind } from "../src/notify/event";
import { Notifier } from "../src/notify/notifier";
import type { NotifierDeps } from "../src/notify/notifier";
import type { OsNote } from "../src/notify/os";

const SES = "ses_0123456789abcdefghjkmnpqrs";
const CHN = "chn_0123456789abcdefghjkmnpqrs";
const JAM = "https://jam.example.com";
const base = { title: "Alice · build-bot", body: "done?", channel: CHN, id: "sqk_1", unread: 1, kind: "messages", session: SES };
const ev = (o: object = {}) => ({ event: "notify", data: JSON.stringify({ ...base, ...o }) });

function setup(o: { focused?: boolean; claim?: boolean; answer?: string; admit?: boolean } = {}, extra: Partial<NotifierDeps> = {}) {
  const shown: OsNote[] = [];
  const toasts: [NotifyKind, string, string][] = [];
  const claim = vi.fn(async () => o.claim ?? true);
  const admit = vi.fn(() => o.admit ?? true);
  const d = {
    gate: { admit },
    lock: { claim },
    os: { show: vi.fn(async (n: OsNote) => void shown.push(n)) },
    focused: () => o.focused ?? false,
    jamUrl: () => JAM,
    scheme: "vscode",
    now: () => 0,
    toast: vi.fn((kind: NotifyKind, m: string, a: "Open") => {
      toasts.push([kind, m, a]);
      return Promise.resolve(o.answer);
    }),
    goTo: vi.fn(),
    log: vi.fn(),
  } satisfies NotifierDeps;
  return { d, n: new Notifier({ ...d, ...extra }), shown, toasts, claim, admit };
}
const settle = () => new Promise((r) => setTimeout(r, 0));

describe("Notifier", () => {
  it("a notify event shows a toast and, unfocused, an OS notification with the session link", async () => {
    const { n, shown, toasts, claim } = setup();
    await n.handle(ev());
    expect(claim).toHaveBeenCalledWith("sqk_1", false);
    expect(toasts).toEqual([["messages", "Alice · build-bot: done?", "Open"]]);
    expect(shown).toHaveLength(1);
    expect(shown[0]).toMatchObject({ title: "Alice · build-bot", body: "done?", link: `vscode://aethons.jam/go/${CHN}?session=${SES}`, group: CHN });
  });

  it("focused: a toast and no OS notification (Review Focus 1)", async () => {
    const { n, shown, toasts, claim } = setup({ focused: true });
    await n.handle(ev());
    expect(claim).toHaveBeenCalledWith("sqk_1", true);
    expect(toasts).toHaveLength(1);
    expect(shown).toEqual([]);
  });

  it("a lost claim shows nothing", async () => {
    const { n, shown, toasts } = setup({ claim: false });
    await n.handle(ev());
    expect(toasts).toEqual([]);
    expect(shown).toEqual([]);
  });

  it("needs uses the warning toast", async () => {
    const { n, toasts } = setup();
    await n.handle(ev({ kind: "needs" }));
    expect(toasts[0]![0]).toBe("needs");
  });

  it("Open goes to the conversation (its window, else the hub)", async () => {
    const a = setup({ answer: "Open", focused: true });
    await a.n.handle(ev());
    await settle();
    expect(a.d.goTo).toHaveBeenCalledWith(CHN, SES);

    const b = setup({ answer: "Open", focused: true });
    await b.n.handle(ev({ session: "" }));
    await settle();
    expect(b.d.goTo).toHaveBeenCalledWith(CHN, "");

    const c = setup({ focused: true });
    await c.n.handle(ev());
    await settle();
    expect(c.d.goTo).not.toHaveBeenCalled();
  });

  it("an OS click (onClick) goes to the same target", async () => {
    const a = setup();
    await a.n.handle(ev());
    a.shown[0]!.onClick();
    await settle();
    expect(a.d.goTo).toHaveBeenCalledWith(CHN, SES);

    const b = setup();
    await b.n.handle(ev({ session: "" }));
    expect(b.shown[0]!.link).toBe(`vscode://aethons.jam/go/${CHN}`);
  });

  it("does not wait for the toast or the OS notification", async () => {
    const { d, n } = setup();
    d.toast.mockImplementation(() => new Promise(() => {}));
    d.os.show.mockImplementation(() => new Promise(() => {}));
    await n.handle(ev());
    expect(d.os.show).toHaveBeenCalledTimes(1);
  });

  it("other events and malformed data do nothing, and the data isn't logged", async () => {
    const { d, n, toasts, claim, admit } = setup();
    await n.handle({ event: "presence", data: JSON.stringify(base) });
    await n.handle({ event: "notify", data: '{"title":"secret words"' });
    await n.handle({ event: "notify", data: JSON.stringify({ ...base, title: 5, body: "secret words" }) });
    expect(toasts).toEqual([]);
    expect(claim).not.toHaveBeenCalled();
    expect(admit).not.toHaveBeenCalled();
    expect(d.log).toHaveBeenCalledTimes(2);
    for (const [m] of d.log.mock.calls) expect(m).not.toContain("secret");
  });

  it("a bad jam.url drops the event", async () => {
    const { n, toasts, claim } = setup({}, { jamUrl: () => new Error("Set jam.url to your Jam's address") });
    await n.handle(ev());
    expect(claim).not.toHaveBeenCalled();
    expect(toasts).toEqual([]);
  });

  it("the gate runs before the lock", async () => {
    const { n, claim, toasts } = setup({ admit: false });
    await n.handle(ev());
    expect(claim).not.toHaveBeenCalled();
    expect(toasts).toEqual([]);

    // with the real gate: a second messages event within 5 s never reaches the lock
    const r = setup({}, { gate: new ChannelGate() });
    await r.n.handle(ev({ id: "sqk_1" }));
    await r.n.handle(ev({ id: "sqk_2" }));
    expect(r.claim).toHaveBeenCalledTimes(1);
  });

  // VS Code's own legacy-link parser for extension notifications, verbatim from
  // microsoft/vscode src/vs/base/common/linkedText.ts. A toast must never match it.
  const LINK_REGEX = /\[([^\]]+)\]\(((?:https?:\/\/|command:|file:)[^\)\s]+)(?: (["'])(.+?)(\3))?\)/gi;
  const links = (m: string) => [...m.matchAll(LINK_REGEX)];

  it.each([
    ["plain command", "[Approve](command:workbench.action.terminal.sendSequence?%7B%22text%22%3A%22x%22%7D)"],
    ["nested", "[[x](y)](command:foo)"],
    ["https", "[docs](https://evil.example)"],
    ["file", "[f](file:///etc/passwd)"],
    ["titled", '[x](command:foo "t")'],
    ["upper case", "[x](COMMAND:foo)"],
    ["doubled", "[a](command:a)[b](command:b)]]((command:c)"],
    ["across a newline", "[x]\n(command:foo)"],
    ["encoded", "%5Bx%5D%28command:foo%29 [x](command%3Afoo) [x](command:foo%29"],
  ])("no link can form in the toast from title or body: %s", async (_n, payload) => {
    for (const o of [{ body: payload }, { title: payload }, { title: `${payload}]`, body: `(command:foo) ${payload}` }]) {
      const { n, toasts, shown } = setup();
      await n.handle(ev(o));
      expect(toasts).toHaveLength(1);
      const m = toasts[0]![1];
      expect(links(m)).toEqual([]);
      // VS Code folds newlines to spaces before parsing.
      expect(links(m.replace(/\r\n|\n|\r/g, " "))).toEqual([]);
      // The text still reads the same: only invisible separators were added.
      expect(m.replace(/\u200b/g, "")).toContain(payload.replace(/\n/g, "\n"));
      // OS notifiers don't parse links: they get the text as sent.
      expect(shown[0]!.body.replace(/\u200b/g, "")).toBe(shown[0]!.body);
    }
  });
});
