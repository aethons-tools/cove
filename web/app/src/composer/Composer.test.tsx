import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MantineProvider } from "@mantine/core";
import { Notifications } from "@mantine/notifications";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { http, HttpResponse } from "msw";
import { server } from "../test/server";
import { theme } from "../theme";
import { Composer } from "./Composer";
import * as keys from "./keys";

function mount(to = "chn_a", onSent = vi.fn()) {
  const client = new QueryClient();
  const r = render(
    <MantineProvider theme={theme}><Notifications />
      <QueryClientProvider client={client}><Composer to={to} label="eng" attachMax={0} onSent={onSent} /></QueryClientProvider>
    </MantineProvider>,
  );
  return { ...r, box: screen.getByRole("textbox") as HTMLTextAreaElement, onSent };
}

beforeEach(() => { sessionStorage.clear(); vi.spyOn(keys, "isMac").mockReturnValue(false); });

describe("Composer", () => {
  it("sends on Ctrl+Enter, clears, and reports where it went", async () => {
    let got: unknown;
    server.use(http.post("/app/user/api/messages", async ({ request }) => { got = await request.json(); return HttpResponse.json({ channel: "chn_a", seq: 9 }, { status: 201 }); }));
    const { box, onSent } = mount();
    await userEvent.type(box, "  hello  ");
    fireEvent.keyDown(box, { key: "Enter", ctrlKey: true });
    await waitFor(() => expect(onSent).toHaveBeenCalledWith({ channel: "chn_a", seq: 9 }));
    expect(got).toEqual({ to: "chn_a", body: "hello" });
    expect(box.value).toBe("");
    expect(sessionStorage.getItem("me-reply:chn_a")).toBeNull();
  });

  it("sends plain text when asked", async () => {
    let got: { contentType?: string } = {};
    server.use(http.post("/app/user/api/messages", async ({ request }) => { got = (await request.json()) as typeof got; return HttpResponse.json({ channel: "chn_a", seq: 1 }, { status: 201 }); }));
    const { box } = mount();
    await userEvent.type(box, "*lit*");
    await userEvent.click(screen.getByLabelText("Plain text"));
    await userEvent.click(screen.getByRole("button", { name: /Send/ }));
    await waitFor(() => expect(got.contentType).toBe("text/plain"));
  });

  it("keeps the text and says why when a send fails", async () => {
    server.use(http.post("/app/user/api/messages", () => HttpResponse.json({ error: "not allowed", code: "forbidden" }, { status: 403 })));
    const { box, onSent } = mount();
    await userEvent.type(box, "keep me");
    fireEvent.keyDown(box, { key: "Enter", ctrlKey: true });
    expect(await screen.findByText("not allowed")).toBeInTheDocument();
    expect(box.value).toBe("keep me");
    expect(onSent).not.toHaveBeenCalled();
  });

  it("newlines on Enter / Shift+Enter, never sends mid-composition or on an empty box", async () => {
    let posts = 0;
    server.use(http.post("/app/user/api/messages", () => { posts++; return HttpResponse.json({ channel: "chn_a", seq: 1 }, { status: 201 }); }));
    const { box } = mount();
    fireEvent.keyDown(box, { key: "Enter", ctrlKey: true }); // empty
    await userEvent.type(box, "a{Enter}b{Shift>}{Enter}{/Shift}c");
    expect(box.value).toBe("a\nb\nc");
    fireEvent.keyDown(box, { key: "Enter", ctrlKey: true, isComposing: true });
    await new Promise((r) => setTimeout(r, 30));
    expect(posts).toBe(0);
  });

  it("pastes as code after Shift+Ctrl+V, a plain paste stays plain", () => {
    const { box } = mount();
    fireEvent.change(box, { target: { value: "see:" } });
    box.setSelectionRange(4, 4);
    fireEvent.keyDown(box, { code: "KeyV", key: "V", ctrlKey: true, shiftKey: true });
    fireEvent.paste(box, { clipboardData: { getData: () => "a ```b``` c", files: [] } });
    expect(box.value).toBe("see:\n````\na ```b``` c\n````\n");
    fireEvent.keyDown(box, { code: "KeyV", key: "v", ctrlKey: true });
    const plain = fireEvent.paste(box, { clipboardData: { getData: () => "z", files: [] } });
    expect(plain).toBe(true); // not prevented: the browser pastes it as is
  });

  it("keeps the reply per recipient across remounts", async () => {
    const first = mount("chn_a");
    await userEvent.type(first.box, "for a");
    first.unmount();
    const other = mount("chn_b");
    expect(other.box.value).toBe("");
    other.unmount();
    expect(mount("chn_a").box.value).toBe("for a");
  });

  it("stacks a draft on Ctrl+↓ and pops it back after a send, never over text", async () => {
    server.use(http.post("/app/user/api/messages", () => HttpResponse.json({ channel: "chn_a", seq: 1 }, { status: 201 })));
    const { box } = mount();
    await userEvent.type(box, "draft one");
    fireEvent.keyDown(box, { key: "ArrowDown", ctrlKey: true });
    expect(box.value).toBe("");
    expect(screen.getByRole("button", { name: /1 stacked draft/ })).toBeInTheDocument();
    await userEvent.type(box, "quick");
    fireEvent.keyDown(box, { key: "ArrowUp", ctrlKey: true }); // box not empty: no pop
    expect(box.value).toBe("quick");
    fireEvent.keyDown(box, { key: "Enter", ctrlKey: true });
    await waitFor(() => expect(box.value).toBe("draft one"));
    expect(screen.queryByRole("button", { name: /stacked draft/ })).toBeNull();
  });
});
describe("Composer attachments", () => {
  function mountWithFiles(onSent = vi.fn()) {
    const client = new QueryClient();
    render(
      <MantineProvider theme={theme}><Notifications />
        <QueryClientProvider client={client}><Composer to="chn_a" label="eng" attachMax={25 << 20} onSent={onSent} /></QueryClientProvider>
      </MantineProvider>,
    );
    return { box: screen.getByRole("textbox") as HTMLTextAreaElement, input: screen.getByLabelText(/Attach files/) as HTMLInputElement, onSent };
  }

  it("uploads picked files and sends their refs, with no text needed", async () => {
    let sent: { attachments?: string[]; body?: string } = {};
    server.use(
      http.post("/app/user/uploads", () => HttpResponse.json({ ref: "blb_9" }, { status: 201 })),
      http.post("/app/user/api/messages", async ({ request }) => { sent = (await request.json()) as typeof sent; return HttpResponse.json({ channel: "chn_a", seq: 3 }, { status: 201 }); }),
    );
    const { input, onSent } = mountWithFiles();
    await userEvent.upload(input, new File(["png"], "shot.png", { type: "image/png" }));
    expect(await screen.findByText("shot.png")).toBeInTheDocument();
    await waitFor(() => expect(screen.getByRole("button", { name: /Send/ })).toBeEnabled());
    await userEvent.click(screen.getByRole("button", { name: /Send/ }));
    await waitFor(() => expect(onSent).toHaveBeenCalled());
    expect(sent).toEqual({ to: "chn_a", body: "", attachments: ["blb_9"] });
    expect(screen.queryByText("shot.png")).toBeNull();
  });

  it("refuses to send while an upload is running; a failed chip is never sent", async () => {
    let release: () => void = () => {};
    const gate = new Promise<void>((r) => { release = r; });
    let posts = 0;
    server.use(
      http.post("/app/user/uploads", async () => { await gate; return new HttpResponse("upload failed", { status: 502 }); }),
      http.post("/app/user/api/messages", () => { posts++; return HttpResponse.json({ channel: "chn_a", seq: 3 }, { status: 201 }); }),
    );
    const { box, input } = mountWithFiles();
    await userEvent.type(box, "with file");
    await userEvent.upload(input, new File(["x"], "slow.txt"));
    expect(screen.getByRole("button", { name: /Send/ })).toBeDisabled();
    release();
    expect(await screen.findByText(/slow\.txt — upload failed/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /Send/ }));
    await waitFor(() => expect(posts).toBe(1));
  });

  it("attaches a pasted file, but text on the clipboard wins", async () => {
    let uploads = 0;
    server.use(http.post("/app/user/uploads", () => { uploads++; return HttpResponse.json({ ref: "blb_p" }, { status: 201 }); }));
    const { box } = mountWithFiles();
    const file = new File(["png"], "paste.png", { type: "image/png" });
    fireEvent.paste(box, { clipboardData: { getData: () => "words", files: [file] } });
    await new Promise((r) => setTimeout(r, 30));
    expect(uploads).toBe(0);
    fireEvent.paste(box, { clipboardData: { getData: () => "", files: [file] } });
    expect(await screen.findByText("paste.png")).toBeInTheDocument();
  });

  it("keeps the chips when a send fails", async () => {
    server.use(
      http.post("/app/user/uploads", () => HttpResponse.json({ ref: "blb_k" }, { status: 201 })),
      http.post("/app/user/api/messages", () => HttpResponse.error()),
    );
    const { box, input, onSent } = mountWithFiles();
    await userEvent.type(box, "with a file");
    await userEvent.upload(input, new File(["x"], "keep.txt"));
    await waitFor(() => expect(screen.getByRole("button", { name: /Send/ })).toBeEnabled());
    await userEvent.click(screen.getByRole("button", { name: /Send/ }));
    expect(await screen.findByText(/can't be reached/)).toBeInTheDocument();
    expect(screen.getByText("keep.txt")).toBeInTheDocument();
    expect(box.value).toBe("with a file");
    expect(onSent).not.toHaveBeenCalled();
  });

  it("aborts an upload when its chip is removed, and refuses Ctrl+Enter while one runs", async () => {
    let aborted = false;
    server.use(http.post("/app/user/uploads", ({ request }) => new Promise<Response>((_, reject) => {
      request.signal.addEventListener("abort", () => { aborted = true; reject(new DOMException("aborted", "AbortError")); });
    })));
    const { box, input } = mountWithFiles();
    await userEvent.type(box, "x");
    await userEvent.upload(input, new File(["x"], "slow.bin"));
    fireEvent.keyDown(box, { key: "Enter", ctrlKey: true });
    expect(await screen.findByText("Still uploading…")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Remove slow.bin" }));
    await waitFor(() => expect(screen.queryByText("slow.bin")).toBeNull());
    expect(aborted).toBe(true);
  });
});

describe("Composer drafts during a send", () => {
  it("keeps a draft stacked while a send is in flight", async () => {
    let release: () => void = () => {};
    const gate = new Promise<void>((r) => { release = r; });
    server.use(http.post("/app/user/api/messages", async () => { await gate; return HttpResponse.json({ channel: "chn_a", seq: 1 }, { status: 201 }); }));
    const { box } = mount();
    await userEvent.type(box, "one");
    fireEvent.keyDown(box, { key: "ArrowDown", ctrlKey: true }); // stack [one]
    await userEvent.type(box, "sending");
    fireEvent.keyDown(box, { key: "Enter", ctrlKey: true });
    await userEvent.clear(box); // (the box keeps its text until the send lands)
    await userEvent.type(box, "two");
    fireEvent.keyDown(box, { key: "ArrowDown", ctrlKey: true }); // stack [one, two] mid-send
    release();
    await waitFor(() => expect(box.value).toBe("two")); // the top pops back
    expect(JSON.parse(sessionStorage.getItem("me-drafts:chn_a")!)).toEqual([{ text: "one", sel: 3 }]);
  });
});
describe("Composer discuss", () => {
  function mountWithRef() {
    const client = new QueryClient();
    const ref: { current: import("./Composer").ComposerHandle | null } = { current: null };
    render(
      <MantineProvider theme={theme}><Notifications />
        <QueryClientProvider client={client}><Composer ref={ref} to="chn_a" label="eng" attachMax={0} /></QueryClientProvider>
      </MantineProvider>,
    );
    return { ref, box: screen.getByRole("textbox", { name: "Message eng" }) as HTMLTextAreaElement };
  }

  it("prefills an empty box, carries replyTo once, then clears it", async () => {
    const bodies: unknown[] = [];
    server.use(http.post("/app/user/api/messages", async ({ request }) => { bodies.push(await request.json()); return HttpResponse.json({ channel: "chn_a", seq: 1 }, { status: 201 }); }));
    const { ref, box } = mountWithRef();
    act(() => ref.current!.discuss("ask_1", "Merge PR #12?"));
    expect(box.value).toBe("re: Merge PR #12?\n");
    expect(screen.getByText(/Replying about: Merge PR #12\?/)).toBeInTheDocument();
    await userEvent.type(box, "not yet");
    fireEvent.keyDown(box, { key: "Enter", ctrlKey: true });
    await waitFor(() => expect(bodies).toHaveLength(1));
    expect(bodies[0]).toEqual({ to: "chn_a", body: "re: Merge PR #12?\nnot yet", replyTo: "ask_1" });
    await waitFor(() => expect(screen.queryByText(/Replying about/)).toBeNull());
    await userEvent.type(box, "next");
    fireEvent.keyDown(box, { key: "Enter", ctrlKey: true });
    await waitFor(() => expect(bodies).toHaveLength(2));
    expect(bodies[1]).toEqual({ to: "chn_a", body: "next" });
  });

  it("never overwrites text already in the box, and ✕ drops the reply", async () => {
    const { ref, box } = mountWithRef();
    await userEvent.type(box, "my own words");
    act(() => ref.current!.discuss("ask_1", "Merge?"));
    expect(box.value).toBe("my own words");
    await userEvent.click(screen.getByRole("button", { name: "Stop replying about Merge?" }));
    expect(screen.queryByText(/Replying about/)).toBeNull();
  });
});
