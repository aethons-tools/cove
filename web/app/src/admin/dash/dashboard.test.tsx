import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "@testing-library/react";
import { QueryClient } from "@tanstack/react-query";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { server } from "../../test/server";
import { adminBoot, renderAdmin } from "../test/render";
import { ADMIN } from "../paths";
import type { ContextPanel, Dashboard } from "../api/types";
import { setBranch } from "../branch/branch";

const panel = (p: Partial<ContextPanel> = {}): ContextPanel => ({
  scope: "Jam-wide", core: "", coreBytes: 0, budget: 4096, over: false, empty: true, showResources: false, leaves: [], resources: [], yaml: "", ...p,
});
const dash = (d: Partial<Dashboard> = {}): Dashboard => ({
  stats: { studios: 3, running: 2, waiting: 1, settingUp: 0, idled: 0, attention: 0, projects: 4, agents: 5, users: 6, specs: 7 },
  attention: [{ kind: "broken", why: "kit k v2 does not parse", path: "config/kits/k" }],
  jamContext: panel(), hasManagers: false, managers: [],
  coves: [{ id: "ses_1", name: "", project: "acme", role: "coder", unit: "", status: "running", connector: "", image: "", manager: "", leaseHolder: "", raisedAt: "", lastSeen: "", attn: null }],
  ...d,
});
let qc: QueryClient;
const mount = (d: Dashboard) => {
  qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  server.use(
    http.get("/app/admin/api/tree", () => HttpResponse.json({ nodes: [] })),
    http.get("/app/admin/api/dashboard", () => HttpResponse.json(d)),
  );
  return renderAdmin(adminBoot, ADMIN, qc);
};

// The config edits show on a branch.
beforeEach(() => setBranch("jam/test"));
afterEach(() => setBranch(""));

describe("the dashboard", () => {
  it("shows the tiles, linked to their lists", async () => {
    mount(dash());
    const waiting = await screen.findByRole("link", { name: /1\s*Waiting/ });
    expect(waiting).toHaveAttribute("href", ADMIN + "agents?status=waiting");
    expect(screen.getByRole("link", { name: /2\s*Running/ })).toHaveAttribute("href", ADMIN + "agents?status=running");
    expect(screen.getByRole("link", { name: /6\s*Users/ })).toHaveAttribute("href", ADMIN + "config/users");
    expect(screen.getByRole("link", { name: /7\s*Specs/ })).toHaveAttribute("href", ADMIN + "config/kits");
    expect(screen.getByText("Projects").closest("a")).toBeNull();
  });

  it("lists what needs attention, linked to where it's fixed", async () => {
    mount(dash());
    expect(await screen.findByRole("link", { name: "kit k v2 does not parse" })).toHaveAttribute("href", ADMIN + "config/kits/k");
    expect(screen.getByText("broken")).toBeInTheDocument();
  });

  it("says when nothing needs attention, and shows managers only when configured", async () => {
    mount(dash({ attention: [], hasManagers: true, managers: [{ name: "m1", connected: false, version: "1.2", studios: 1, capacity: 4, lastSeen: "", strays: ["ghost"] }] }));
    expect(await screen.findByText("Nothing needs attention.")).toBeInTheDocument();
    expect(screen.getByText("disconnected")).toBeInTheDocument();
    expect(screen.getByTitle("ghost")).toHaveTextContent("1");
  });

  it("hides Managers without a manager list", async () => {
    mount(dash());
    await screen.findByText("Running agents");
    expect(screen.queryByText("Managers")).toBeNull();
  });

  it("saves the Jam context, and a clear replaces unsaved text", async () => {
    let saved: unknown;
    let now = panel({ yaml: "core: old\n", core: "old", empty: false });
    mount(dash({ jamContext: now }));
    server.use(
      http.get("/app/admin/api/dashboard", () => HttpResponse.json(dash({ jamContext: now }))),
      http.post("/app/admin/api/jam/context", async ({ request }) => { saved = await request.json(); now = panel({ yaml: "core: new\n", core: "new", empty: false }); return HttpResponse.json(now); }),
      http.delete("/app/admin/api/jam/context", () => { now = panel(); return HttpResponse.json(now); }),
    );
    const box = await screen.findByRole("textbox");
    await userEvent.clear(box);
    await userEvent.type(box, "core: new");
    await userEvent.click(screen.getByRole("button", { name: "Save context" }));
    await waitFor(() => expect(saved).toEqual({ yaml: "core: new" }));
    await userEvent.type(await screen.findByRole("textbox"), " unsaved");
    await userEvent.click(screen.getByRole("button", { name: "Clear" }));
    await userEvent.click(within(await screen.findByRole("dialog")).getByRole("button", { name: "Clear" }));
    await waitFor(() => expect(screen.getByRole("textbox")).toHaveValue(""));
  });

  it("keeps the page, and unsaved context edits, when a refresh fails", async () => {
    mount(dash({ jamContext: panel({ yaml: "core: old\n", core: "old", empty: false }) }));
    await userEvent.type(await screen.findByRole("textbox"), " unsaved");
    server.use(http.get("/app/admin/api/dashboard", () => new HttpResponse(null, { status: 502 })));
    await act(() => qc.refetchQueries({ queryKey: ["admin", "dashboard"] }));
    expect(await screen.findByText(/Refresh failed/)).toBeInTheDocument();
    expect(screen.getByRole("textbox")).toHaveValue("core: old\n unsaved");
  });

  it("invalidates the dashboard after a context write (cancelling a poll in flight that could undo it)", async () => {
    mount(dash());
    server.use(http.post("/app/admin/api/jam/context", () => HttpResponse.json(panel({ yaml: "core: new\n", core: "new", empty: false }))));
    const inval = vi.spyOn(qc, "invalidateQueries");
    await userEvent.type(await screen.findByRole("textbox"), "core: new");
    await userEvent.click(screen.getByRole("button", { name: "Save context" }));
    await waitFor(() => expect(inval).toHaveBeenCalledWith({ queryKey: ["admin", "dashboard"] }));
  });

  it("lists the running studios", async () => {
    mount(dash());
    expect(await screen.findByRole("link", { name: "ses_1" })).toHaveAttribute("href", ADMIN + "agents/ses_1");
  });
});
