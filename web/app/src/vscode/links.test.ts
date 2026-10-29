import { afterEach, describe, expect, it, vi } from "vitest";
import type { OpenIntent } from "../bridge/protocol";
import { interceptLinks, linkAction, type LinkAction } from "./links";

const BRD = "brd_0abcdefghjkmnpqrstvwxyz01";
const BLB = "blb_0abcdefghjkmnpqrstvwxyz01";

describe("linkAction (Review Focus 4), in the pane for chn_a", () => {
  const table: [string, LinkAction][] = [
    ["javascript:alert(1)", { drop: true }],
    [" JavaScript:alert(1)", { drop: true }],
    ["command:workbench.action.reload", { drop: true }],
    ["vscode://x", { drop: true }],
    ["file:///etc/passwd", { drop: true }],
    ["data:text/html,<p>x</p>", { drop: true }],
    ["https://example.com/x", { intent: { kind: "external", url: "https://example.com/x" } }],
    ["http://example.com/", { intent: { kind: "external", url: "http://example.com/" } }],
    ["mailto:a@example.com", { intent: { kind: "external", url: "mailto:a@example.com" } }],
    ["/app/user/c/chn_b", { intent: { kind: "conversation", channel: "chn_b" } }],
    ["/app/user/c/chn_a#ask-ask_1", { scrollTo: "ask-ask_1" }],
    ["/app/user/c/chn_a", { drop: true }],
    [`/app/user/boards/${BRD}`, { intent: { kind: "board", id: BRD } }],
    [`/app/user/blobs/${BLB}`, { intent: { kind: "web", path: `/app/user/blobs/${BLB}` } }],
    ["/app/user/?q=x#y", { intent: { kind: "web", path: "/app/user/?q=x#y" } }],
    ["/elsewhere", { drop: true }],
    ["#x", { allow: true }],
  ];
  it.each(table)("%s", (href, want) => {
    expect(linkAction(href, "chn_a")).toEqual(want);
  });
});

describe("linkAction session links", () => {
  const SES = "ses_0abcdefghjkmnpqrstvwxyz012";
  it.each<[string, LinkAction]>([
    [`vscode://aethons.jam/session/${SES}`, { intent: { kind: "session", id: SES } }],
    [`vscode-insiders://aethons.jam/session/${SES}`, { intent: { kind: "session", id: SES } }],
    ["vscode://aethons.jam/session/../x", { drop: true }],
    ["vscode://aethons.jam/session/ses_x", { drop: true }],
    ["vscode://other/x", { drop: true }],
  ])("%s", (href, want) => {
    expect(linkAction(href, "chn_a")).toEqual(want);
  });
});

describe("interceptLinks", () => {
  let stop: (() => void) | undefined;
  afterEach(() => { stop?.(); document.body.innerHTML = ""; });

  const setup = (html: string) => {
    document.body.innerHTML = `<div class="jam-body">${html}</div>`;
    const post = vi.fn<(i: OpenIntent) => void>();
    stop = interceptLinks(document, () => "chn_a", post);
    const a = document.querySelector("a")!;
    const ev = new MouseEvent("click", { bubbles: true, cancelable: true });
    const below = vi.fn();
    a.parentElement!.addEventListener("click", below);
    (a.querySelector("span") ?? a).dispatchEvent(ev);
    return { post, ev, below };
  };

  it("interceptLinks prevents the click's default", () => {
    const { post, ev, below } = setup('<a href="https://example.com/x"><span>x</span></a>');
    expect(ev.defaultPrevented).toBe(true);
    expect(below).not.toHaveBeenCalled();
    expect(post).toHaveBeenCalledWith({ kind: "external", url: "https://example.com/x" });
  });

  it("drops a javascript: link without posting", () => {
    const { post, ev } = setup('<a href="javascript:alert(1)">x</a>');
    expect(ev.defaultPrevented).toBe(true);
    expect(post).not.toHaveBeenCalled();
  });

  it("scrolls to an ask in the pane's own conversation", () => {
    const scroll = vi.fn();
    Element.prototype.scrollIntoView = scroll; // jsdom has none
    const { post, ev } = setup('<a href="/app/user/c/chn_a#ask-ask_1">re</a><div id="ask-ask_1"></div>');
    expect(ev.defaultPrevented).toBe(true);
    expect(post).not.toHaveBeenCalled();
    expect(scroll).toHaveBeenCalledTimes(1);
    expect(scroll.mock.contexts[0]).toBe(document.getElementById("ask-ask_1"));
  });

  it("leaves a #fragment link alone", () => {
    const { post, ev } = setup('<a href="#x">x</a>');
    expect(ev.defaultPrevented).toBe(false);
    expect(post).not.toHaveBeenCalled();
  });
});
