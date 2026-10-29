import { afterEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MantineProvider } from "@mantine/core";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router";
import { http, HttpResponse } from "msw";
import { server } from "../test/server";
import { theme } from "../theme";
import { setBlobResolver } from "../blobs";
import { HostContext, type Host } from "../host";
import type { SessionPane } from "../api/types";
import { PaneBody } from "./PaneTabs";
import { SessionHeader } from "./SessionHeader";
import type { SessionHead } from "../api/types";

const pane = (tab: string, over: Partial<SessionPane> = {}): SessionPane => ({
  id: "ses_1", tab, previews: [], previewsOff: false, boards: [], files: [], asks: [], links: [], status: [], streams: [], eventsOff: false, studio: false, ideOpen: "", ...over,
});
const show = (tab: "previews" | "artifacts", p: SessionPane, host: Host = {}) => {
  server.use(http.get("/app/user/api/sessions/:id/pane/:tab", () => HttpResponse.json(p)));
  return render(
    <MantineProvider theme={theme} env="test">
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <HostContext.Provider value={host}><MemoryRouter>
          <PaneBody id="ses_1" tab={tab} busy={false} onAsk={() => {}} />
        </MemoryRouter></HostContext.Provider>
      </QueryClientProvider>
    </MantineProvider>,
  );
};
const view = { name: "web", url: "https://x.test/p", port: 8080, frame: true };

afterEach(() => setBlobResolver());

describe("PaneBody", () => {
  it("artifacts images resolve through the blob resolver", async () => {
    setBlobResolver(() => Promise.resolve("blob:1"));
    const { container } = show("artifacts", pane("artifacts", {
      boards: [{ id: "brd_1", title: "Plan" }], files: [{ id: "blb_1", name: "pic.png", size: "1 KB", image: true }],
    } as Partial<SessionPane>));
    await waitFor(() => expect(container.querySelectorAll('img[src="blob:1"]').length).toBe(2));
  });

  it("previews call openPreview when the host offers it", async () => {
    const openPreview = vi.fn();
    const { container } = show("previews", pane("previews", { previews: [view] }), { openPreview });
    await screen.findByText(/port 8080/);
    expect(container.querySelector("a[href]")).toBeNull();
    expect(screen.queryByText("View here")).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: "Open" }));
    expect(openPreview).toHaveBeenLastCalledWith("ses_1", view, false);
    await userEvent.click(screen.getByRole("button", { name: "Browser" }));
    expect(openPreview).toHaveBeenLastCalledWith("ses_1", view, true);
  });

  it("without a host, previews are today's links", async () => {
    const { container } = show("previews", pane("previews", { previews: [view] }));
    await screen.findByText(/port 8080/);
    expect(container.querySelector('a[href="https://x.test/p"]')).not.toBeNull();
    expect(screen.getByText("View here")).toBeTruthy();
  });

  it("without onTab the header has no pane tabs", () => {
    const head = {
      id: "ses_1", label: "spider", kind: "standing", ticket: "", role: "r", project: "p", owner: "",
      state: { label: "x", text: "is running", tool: "", class: "busy" }, since: "", openAsks: 0, cost: "", turnCost: "", tokens: "",
      context: "", contextTip: "", uptime: "", mission: "", ssh: "", studio: false, canRelease: false, canRestart: false, canUpgrade: false,
      ops: true, canInterrupt: true, canCompact: false, canClear: false, restartLabel: "Restart", upgradeState: "", channel: "chn_s",
    } as SessionHead;
    render(
      <MantineProvider theme={theme} env="test">
        <QueryClientProvider client={new QueryClient()}><SessionHeader head={head} /></QueryClientProvider>
      </MantineProvider>,
    );
    expect(screen.queryByRole("tablist")).toBeNull();
    expect(screen.getByText("Interrupt")).toBeTruthy();
  });
});
