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
  scope: "Jam-wide", core: "", coreBytes: 0, budget: 4096, over: false, empty: true, showResources: false, leaves: [], resources: [], yaml: "",
  limits: { coreBudget: 4096, maxReadWhen: 160, maxLeaves: 20, maxLeafBytes: 65536, maxResources: 0 }, leafBytes: 0, deliveredBytes: 0, ...p,
});
const dash = (d: Partial<Dashboard> = {}): Dashboard => ({
  stats: { studios: 3, running: 2, waiting: 1, settingUp: 0, idled: 0, attention: 0, projects: 4, agents: 5, users: 6, specs: 7 },
  attention: [{ kind: "broken", why: "kit k v2 does not parse", path: "config/kits/k" }],
  jamContext: panel(), jamNotes: { markdown: "", html: "" }, hasManagers: false, managers: [],
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
    mount(dash({ attention: [], hasManagers: true, managers: [{ name: "m1", connected: false, version: "1.2", studios: 1, capacity: 4, lastSeen: "", strays: ["ghost"], host: null, activities: [] }] }));
    expect(await screen.findByText("Nothing needs attention.")).toBeInTheDocument();
    expect(screen.getByText("disconnected")).toBeInTheDocument();
    expect(screen.getByTitle("ghost")).toHaveTextContent("1");
  });

  it("shows a manager's memory, disk, reclaimable and last cleanup", async () => {
    mount(dash({ hasManagers: true, managers: [{
      name: "m1", connected: true, version: "1.2", studios: 1, capacity: 4, lastSeen: "", strays: [],
      host: { at: "09:31:05", mem: "2.0GiB / 8.0GiB", memPct: 25, disk: "91.0GiB / 100.0GiB", diskPct: 91, reclaimable: "1.5GiB", reclaimableDetail: "images 1.0GiB · build cache 512.0MiB · volumes 0B", lastCleanup: "Oct 28 08:00: freed 3.0GiB (disk above 85%)" },
      activities: [],
    }] }));
    expect(await screen.findByText("2.0GiB / 8.0GiB")).toBeInTheDocument();
    expect(screen.getByText("91.0GiB / 100.0GiB")).toBeInTheDocument();
    expect(screen.getByRole("progressbar", { name: "m1 disk 91% used" })).toBeInTheDocument();
    expect(screen.getByRole("progressbar", { name: "m1 memory 25% used" })).toBeInTheDocument();
    expect(screen.getByTitle("images 1.0GiB · build cache 512.0MiB · volumes 0B")).toHaveTextContent("1.5GiB");
    expect(screen.getByText("Oct 28 08:00: freed 3.0GiB (disk above 85%)")).toBeInTheDocument();
  });

  it("shows a manager's activities, one per line", async () => {
    mount(dash({ hasManagers: true, managers: [{
      name: "m1", connected: true, version: "1.2", studios: 1, capacity: 4, lastSeen: "", strays: [], host: null,
      activities: ["preparing kit web@v2: building step 7/12 (2m)", "cleaning disk: removing unused kit images (10s)"],
    }] }));
    expect(await screen.findByText("Activity")).toBeInTheDocument();
    expect(screen.getByText("preparing kit web@v2: building step 7/12 (2m)")).toBeInTheDocument();
    expect(screen.getByText("cleaning disk: removing unused kit images (10s)")).toBeInTheDocument();
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
    await userEvent.click(await screen.findByRole("button", { name: "Edit" }));
    const box = screen.getByRole("textbox", { name: "Core instructions" });
    await userEvent.clear(box);
    await userEvent.type(box, "new");
    await userEvent.click(screen.getByRole("button", { name: "Save context" }));
    await waitFor(() => expect(saved).toEqual({ core: "new", leaves: [] }));
    expect(await screen.findByText("new")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Edit" }));
    await userEvent.type(screen.getByRole("textbox", { name: "Core instructions" }), " unsaved");
    await userEvent.click(screen.getByRole("button", { name: "Clear context" }));
    await userEvent.click(within(await screen.findByRole("dialog")).getByRole("button", { name: "Clear context" }));
    expect(await screen.findByText("No core instructions.")).toBeInTheDocument();
    expect(screen.queryByRole("textbox")).toBeNull();
  });

  it("keeps the page, and unsaved context edits, when a refresh fails", async () => {
    mount(dash({ jamContext: panel({ yaml: "core: old\n", core: "old", empty: false }) }));
    await userEvent.click(await screen.findByRole("button", { name: "Edit" }));
    await userEvent.type(screen.getByRole("textbox", { name: "Core instructions" }), " unsaved");
    server.use(http.get("/app/admin/api/dashboard", () => new HttpResponse(null, { status: 502 })));
    await act(() => qc.refetchQueries({ queryKey: ["admin", "dashboard"] }));
    expect(await screen.findByText(/Refresh failed/)).toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: "Core instructions" })).toHaveValue("old unsaved");
  });

  it("invalidates the dashboard after a context write (cancelling a poll in flight that could undo it)", async () => {
    mount(dash());
    server.use(http.post("/app/admin/api/jam/context", () => HttpResponse.json(panel({ yaml: "core: new\n", core: "new", empty: false }))));
    const inval = vi.spyOn(qc, "invalidateQueries");
    await userEvent.click(await screen.findByRole("button", { name: "Edit" }));
    await userEvent.type(screen.getByRole("textbox", { name: "Core instructions" }), "new");
    await userEvent.click(screen.getByRole("button", { name: "Save context" }));
    await waitFor(() => expect(inval).toHaveBeenCalledWith({ queryKey: ["admin", "dashboard"] }));
  });

  it("lists the running studios", async () => {
    mount(dash());
    expect(await screen.findByRole("link", { name: "ses_1" })).toHaveAttribute("href", ADMIN + "agents/ses_1");
  });
});
