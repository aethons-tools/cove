import { afterEach, describe, expect, it } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MantineProvider } from "@mantine/core";
import { http, HttpResponse } from "msw";
import { server } from "../../test/server";
import { theme } from "../../theme";
import { FrameView } from "./FrameView";

// mint answers the frame route: answers in turn (then the last); a number
// is a refusal with that status.
function mint(...answers: (string | number)[]) {
  const names: string[] = [];
  let i = 0;
  server.use(http.post("/app/user/api/sessions/:id/frame", async ({ request }) => {
    names.push(((await request.json()) as { name: string }).name);
    const a = answers[Math.min(i++, answers.length - 1)]!;
    return typeof a === "number"
      ? HttpResponse.json({ error: "no", code: "x" }, { status: a })
      : HttpResponse.json({ url: a, open: "https://app--s.p.example/" });
  }));
  return names;
}
const show = (open?: string) =>
  render(<MantineProvider theme={theme} env="test"><FrameView sid="ses_1" name="app" open={open} reload /></MantineProvider>);
const frame = () => document.querySelector<HTMLIFrameElement>("iframe.st-iframe");
const status = () => document.querySelector(".st-status")!.textContent;
// post sends a frame-error from source (default: the current frame's window).
const post = (st: unknown, source: MessageEventSource | null = frame()!.contentWindow) =>
  act(() => { window.dispatchEvent(new MessageEvent("message", { data: { jam: "frame-error", status: st }, source })); });
const pageshow = (persisted: boolean) =>
  act(() => { window.dispatchEvent(Object.assign(new Event("pageshow"), { persisted })); });

afterEach(() => server.resetHandlers());

describe("a framed view", () => {
  it("frames the minted URL, as the boundary wants it, and clears the status on load", async () => {
    const names = mint("https://app--s.p.example/__jam/frame?t=1");
    show();
    expect(status()).toBe("Loading…");
    await waitFor(() => expect(frame()).not.toBeNull());
    const f = frame()!;
    expect(f.getAttribute("src")).toBe("https://app--s.p.example/__jam/frame?t=1");
    expect(f.getAttribute("referrerpolicy")).toBe("origin");
    expect(f.getAttribute("allow")).toBe("clipboard-read; clipboard-write");
    expect(f.getAttribute("title")).toBe("app");
    expect(screen.getByRole("link", { name: "Open in new tab" })).toHaveAttribute("href", "https://app--s.p.example/");
    fireEvent.load(f);
    expect(status()).toBe("");
    expect(names).toEqual(["app"]);
  });

  it("Reload mints a fresh frame; a late message from the replaced frame is ignored", async () => {
    const names = mint("https://a.p.example/1", "https://a.p.example/2");
    show();
    await waitFor(() => expect(frame()).not.toBeNull());
    const old = frame()!.contentWindow;
    await userEvent.click(screen.getByRole("button", { name: "Reload" }));
    await waitFor(() => expect(frame()!.getAttribute("src")).toBe("https://a.p.example/2"));
    expect(frame()!.contentWindow).not.toBe(old);
    fireEvent.load(frame()!);
    post(403, old);
    expect(status()).toBe("");
    expect(names).toHaveLength(2);
  });

  it("ignores frame-errors from anywhere but its frame", async () => {
    const names = mint("https://a.p.example/1");
    show();
    await waitFor(() => expect(frame()).not.toBeNull());
    fireEvent.load(frame()!);
    post(403, window);
    post("signin", null);
    const other = document.createElement("iframe");
    document.body.append(other);
    post("signin", other.contentWindow);
    other.remove();
    expect(status()).toBe("");
    expect(names).toHaveLength(1);
  });

  it("shows a frame's refusal, and the load doesn't clear it", async () => {
    mint("https://a.p.example/1");
    show();
    await waitFor(() => expect(frame()).not.toBeNull());
    post(403);
    expect(status()).toBe("No longer shared with you.");
    fireEvent.load(frame()!);
    expect(status()).toBe("No longer shared with you.");
    post(404);
    expect(status()).toBe("Nothing to show: the studio isn't running.");
    post(502);
    expect(status()).toBe("The preview isn't answering.");
    post("weird");
    expect(status()).toBe("Couldn't show this view.");
  });

  it("re-mints once on signin, then offers Reopen, which re-arms it", async () => {
    const names = mint("https://a.p.example/1", "https://a.p.example/2", "https://a.p.example/3", "https://a.p.example/4");
    show();
    await waitFor(() => expect(frame()).not.toBeNull());
    post("signin");
    await waitFor(() => expect(frame()!.getAttribute("src")).toBe("https://a.p.example/2"));
    post("signin");
    expect(await screen.findByRole("button", { name: "Reopen this view" })).toBeInTheDocument();
    expect(names).toHaveLength(2);
    await userEvent.click(screen.getByRole("button", { name: "Reopen this view" }));
    await waitFor(() => expect(frame()!.getAttribute("src")).toBe("https://a.p.example/3"));
    post("signin");
    await waitFor(() => expect(names).toHaveLength(4)); // re-armed: one more re-mint
  });

  it("re-mints once on cookies, then says to use a new tab", async () => {
    const names = mint("https://a.p.example/1", "https://a.p.example/2");
    show();
    await waitFor(() => expect(frame()).not.toBeNull());
    post("cookies");
    await waitFor(() => expect(frame()!.getAttribute("src")).toBe("https://a.p.example/2"));
    post("cookies");
    expect(status()).toBe("This browser can't sign in inside the pane — use Open in new tab.");
    expect(names).toHaveLength(2);
  });

  it("says why a mint was refused, keeps the plain URL, and frames only http(s)", async () => {
    mint(429);
    show("https://app--s.p.example/");
    await waitFor(() => expect(status()).toBe("Too many views opened; wait a minute."));
    expect(frame()).toBeNull();
    expect(screen.getByRole("link", { name: "Open in new tab" })).toHaveAttribute("href", "https://app--s.p.example/");
    mint(403);
    await userEvent.click(screen.getByRole("button", { name: "Reload" }));
    await waitFor(() => expect(status()).toBe("No longer shared with you."));
    mint("javascript:alert(1)");
    await userEvent.click(screen.getByRole("button", { name: "Reload" }));
    await waitFor(() => expect(status()).toBe("Couldn't open this view."));
    expect(frame()).toBeNull();
  });

  it("a preview loads as one, and its 502 says it isn't answering", async () => {
    mint("https://app--s.p.example/__jam/frame?t=1");
    show("preview");
    expect(status()).toBe("Loading…");
    await waitFor(() => expect(frame()).not.toBeNull());
    expect(frame()!.getAttribute("title")).toBe("app");
    post(502);
    expect(status()).toBe("The preview isn't answering.");
  });

  it("re-mints when restored from the back/forward cache (its ticket is spent)", async () => {
    const names = mint("https://a.p.example/1", "https://a.p.example/2");
    show();
    await waitFor(() => expect(frame()).not.toBeNull());
    pageshow(true);
    await waitFor(() => expect(frame()!.getAttribute("src")).toBe("https://a.p.example/2"));
    pageshow(false);
    expect(names).toHaveLength(2);
  });
});
