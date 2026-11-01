import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { server } from "../../test/server";
import { adminBoot, renderAdmin } from "../test/render";
import { ADMIN } from "../paths";
import { kit } from "./fixtures";
import type { KitRow } from "../api/types";
import { setBranch } from "../branch/branch";

const row = (r: Partial<KitRow> = {}): KitRow => ({ name: "web", current: 2, count: 3, base: "blessed default", egress: 2, usedBy: 1, invalid: false, draft: false, attn: null, ...r });
const base = (canSource = true, kits = [row(), row({ name: "bad", invalid: true, usedBy: 0, draft: true })]) => server.use(
  http.get("/app/admin/api/tree", () => HttpResponse.json({ nodes: [] })),
  http.get("/app/admin/api/kits", () => HttpResponse.json({ kits, canSource })),
);
const at = ADMIN + "config/kits";

// The config edits show on a branch.
beforeEach(() => setBranch("jam/test"));
afterEach(() => setBranch(""));

describe("the kits list", () => {
  it("shows kits, invalid and draft ones too", async () => {
    base();
    renderAdmin(adminBoot, at);
    expect(await screen.findByRole("link", { name: "web" })).toHaveAttribute("href", at + "/web");
    expect(screen.getAllByText("v2").length).toBe(2);
    expect(screen.getByText("invalid")).toBeInTheDocument();
    expect(screen.getByText("draft")).toBeInTheDocument();
    expect(screen.getByText("2 domain(s)")).toBeInTheDocument();
    const del = screen.getAllByRole("button", { name: "Delete" });
    expect(del[0]).toBeDisabled();
    expect(del[0]).toHaveAttribute("title", "Roles use this kit — rebind them first");
  });

  it("creates a kit, or shows why not", async () => {
    base();
    let n = 0;
    server.use(
      http.post("/app/admin/api/kits", () => (++n === 1
        ? HttpResponse.json({ error: 'kit "web" already exists; push a new version on its page', code: "conflict" }, { status: 409 })
        : HttpResponse.json({ id: "web2", path: "config/kits/web2" }, { status: 201 }))),
      http.get("/app/admin/api/kits/web2", () => HttpResponse.json(kit({ name: "web2" }))),
    );
    const user = userEvent.setup();
    const { router } = renderAdmin(adminBoot, at);
    await user.click(await screen.findByText("New kit", { selector: "summary" }));
    await user.type(screen.getAllByLabelText(/^Kit name/)[0]!, "web"); // New kit's comes first
    await user.type(screen.getByLabelText(/^Studio kit YAML/), "kind: studio");
    await user.click(screen.getByRole("button", { name: "Create kit" }));
    expect(await screen.findByText(/already exists; push a new version on its page/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Create kit" }));
    await waitFor(() => expect(router.state.location.pathname).toBe(at + "/web2"));
  });

  it("imports from git, tracking by default", async () => {
    base();
    let sent: unknown;
    server.use(
      http.post("/app/admin/api/kits/import", async ({ request }) => { sent = await request.json(); return HttpResponse.json({ id: "web-dev", path: "config/kits/web-dev", message: "tracking https://github.com/acme/kits@main: web-dev is now v1" }, { status: 201 }); }),
      http.get("/app/admin/api/kits/web-dev", () => HttpResponse.json(kit({ name: "web-dev" }))),
    );
    const user = userEvent.setup();
    const { router } = renderAdmin(adminBoot, at);
    await user.click(await screen.findByText("Import from git", { selector: "summary" }));
    await user.type(screen.getByLabelText(/^Source/), "https://github.com/acme/kits@main");
    await user.click(screen.getByRole("button", { name: "Import" }));
    await waitFor(() => expect(router.state.location.pathname).toBe(at + "/web-dev"));
    expect(sent).toEqual({ source: "https://github.com/acme/kits@main", name: "", track: true });
    expect(await screen.findByText("tracking https://github.com/acme/kits@main: web-dev is now v1")).toBeInTheDocument();
  });

  it("offers no import without git", async () => {
    base(false);
    renderAdmin(adminBoot, at);
    await screen.findByRole("link", { name: "web" });
    expect(screen.queryByText("Import from git")).toBeNull();
  });
});
