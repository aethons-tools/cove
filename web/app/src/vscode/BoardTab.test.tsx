import { afterEach, describe, expect, it, vi } from "vitest";
import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { BoardInfo } from "../api/types";
import { setTransport } from "../api/client";
import { setUploader } from "../composer/upload";
import { setBlobResolver } from "../blobs";
import { setLiveSource } from "../live/useLiveEvents";
import { setFocusSink } from "../push/useFocusReport";
import { setFeedOpener } from "../session/thinking/useThinkingFeed";
import { Bridge, install } from "./bridge";
import { fakeHost, json, type Route } from "./fakeHost";
import { Pane, wirePane } from "./Pane";

// The editor (Excalidraw) is mocked: it shows its props and counts the
// jam-board pushes for its board.
vi.mock("../boards/BoardEditor", async () => {
  const { useEffect, useState } = await import("react");
  return {
    default: function Editor({ id, readOnly }: { id: string; readOnly: boolean }) {
      const [n, setN] = useState(0);
      useEffect(() => {
        const on = (e: Event) => { if ((e as CustomEvent<string>).detail === id) setN((x) => x + 1); };
        window.addEventListener("jam-board", on);
        return () => window.removeEventListener("jam-board", on);
      }, [id]);
      return <div data-testid="editor" data-id={id} data-readonly={String(readOnly)}>{`pushes ${n}`}</div>;
    },
  };
});

const BRD = "brd_0abcdefghjkmnpqrstvwxyz01";
const info: BoardInfo = { id: BRD, title: "Plan", channel: "chn_a", readOnly: true };

let stops: (() => void)[] = [];
afterEach(() => {
  stops.forEach((s) => s());
  stops = [];
  setTransport();
  setUploader();
  setBlobResolver();
  setLiveSource();
  setFocusSink();
  setFeedOpener();
});

function mount(routes: Record<string, Route> = {}) {
  const h = fakeHost(routes);
  const bridge = new Bridge(h.acquireVsCodeApi(), h.win);
  install(bridge, { signIn: () => bridge.post({ type: "signIn" }) });
  stops.push(wirePane(bridge, document, "board"));
  const r = render(<Pane bridge={bridge} name="board" board={BRD} />);
  return { h, ...r };
}
const signedIn = { signedIn: true, target: null, pinned: false, elsewhere: false };

describe("a board tab", () => {
  it("loads the board, posts its title and mounts the editor", async () => {
    const { h } = mount({ [`GET /app/user/api/boards/${BRD}`]: json(info) });
    act(() => { h.state(signedIn); });
    const ed = await screen.findByTestId("editor");
    expect(ed).toHaveAttribute("data-id", BRD);
    expect(ed).toHaveAttribute("data-readonly", "true");
    expect(screen.getByText("Plan")).toBeInTheDocument();
    expect(screen.getByText("view only")).toBeInTheDocument();
    expect(h.posted).toContainEqual({ type: "title", title: "Plan" });
    expect(h.requests.map((r) => r.path)).toEqual([`/app/user/api/boards/${BRD}`]);
  });

  it("a board live event reaches the editor as jam-board", async () => {
    const { h } = mount({ [`GET /app/user/api/boards/${BRD}`]: json(info) });
    act(() => { h.state(signedIn); });
    expect(await screen.findByText("pushes 0")).toBeInTheDocument();
    act(() => { h.live("board", BRD); });
    expect(await screen.findByText("pushes 1")).toBeInTheDocument();
  });

  it("a 404 says the whiteboard isn't available", async () => {
    const { h } = mount();
    act(() => { h.state(signedIn); });
    expect(await screen.findByText("This whiteboard isn't available to you.")).toBeInTheDocument();
    expect(screen.queryByTestId("editor")).toBeNull();
    expect(h.posted.filter((m) => m.type === "title")).toEqual([]);
  });

  it("Open in the web app posts a web intent", async () => {
    const { h } = mount({ [`GET /app/user/api/boards/${BRD}`]: json({ ...info, readOnly: false }) });
    act(() => { h.state(signedIn); });
    await userEvent.click(await screen.findByRole("button", { name: "Open in the web app" }));
    expect(h.posted).toContainEqual({ type: "open", intent: { kind: "web", path: `/app/user/boards/${BRD}` } });
    expect(screen.queryByText("view only")).toBeNull();
  });

  it("signed out shows the board sign-in copy", async () => {
    const { h } = mount({ [`GET /app/user/api/boards/${BRD}`]: json(info) });
    act(() => { h.state({ ...signedIn, signedIn: false }); });
    expect(await screen.findByText("Sign in to Jam to see this whiteboard.")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Sign in" }));
    expect(h.posted).toContainEqual({ type: "signIn" });
    expect(h.requests).toEqual([]);
  });
});
