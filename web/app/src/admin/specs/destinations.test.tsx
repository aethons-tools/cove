import { describe, expect, it } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { server } from "../../test/server";
import { adminBoot, renderAdmin } from "../test/render";
import { ADMIN } from "../paths";
import { dest, destFields } from "./fixtures";

const base = () => server.use(
  http.get("/app/admin/api/tree", () => HttpResponse.json({ nodes: [] })),
  http.get("/app/admin/suggest", () => HttpResponse.json([])),
);
const at = ADMIN + "config/destinations/gh";
const row = { name: "gh", route: "/gh/", upstream: "https://api.github.com", identityIn: "bearer", apply: "bearer", credName: "gh-pat", envKeys: ["GH_HOST"], gitRouted: true, usedBy: 2, managed: null, attn: null };

describe("destinations", () => {
  it("lists them and creates one", async () => {
    base();
    let sent: Record<string, unknown> = {};
    server.use(
      http.get("/app/admin/api/destinations", () => HttpResponse.json({ destinations: [row], methods: ["bearer", "basic"] })),
      http.post("/app/admin/api/destinations", async ({ request }) => { sent = await request.json() as Record<string, unknown>; return HttpResponse.json({ id: "ant", path: "config/destinations/ant" }, { status: 201 }); }),
      http.get("/app/admin/api/destinations/ant", () => HttpResponse.json(dest({ name: "ant" }))),
    );
    const user = userEvent.setup();
    const { router } = renderAdmin(adminBoot, ADMIN + "config/destinations");
    expect(await screen.findByRole("link", { name: "gh" })).toHaveAttribute("href", at);
    expect(screen.getByText("bearer → bearer")).toBeInTheDocument();
    await user.click(screen.getByText("New destination", { selector: "summary" }));
    await user.type(screen.getByLabelText(/^Name/), "ant");
    await user.type(screen.getByLabelText(/^Route \*/), "/anthropic/");
    await user.type(screen.getByLabelText(/^Upstream URL/), "https://api.anthropic.com");
    await user.click(screen.getByRole("button", { name: "Create destination" }));
    await waitFor(() => expect(router.state.location.pathname).toBe(ADMIN + "config/destinations/ant"));
    expect(sent).toMatchObject({ name: "ant", route: "/anthropic/", identityIn: "bearer", apply: "bearer", logHeadersIn: "none", git: "" });
  });

  it("shows a refused create inline", async () => {
    base();
    server.use(
      http.get("/app/admin/api/destinations", () => HttpResponse.json({ destinations: [row], methods: ["bearer"] })),
      http.post("/app/admin/api/destinations", () => HttpResponse.json({ error: 'destination "gh" exists; edit it on its page', code: "conflict" }, { status: 409 })),
    );
    const user = userEvent.setup();
    renderAdmin(adminBoot, ADMIN + "config/destinations");
    await user.click(await screen.findByText("New destination", { selector: "summary" }));
    await user.type(screen.getByLabelText(/^Name/), "gh");
    await user.type(screen.getByLabelText(/^Route \*/), "/gh/");
    await user.type(screen.getByLabelText(/^Upstream URL/), "https://api.github.com");
    await user.click(screen.getByRole("button", { name: "Create destination" }));
    expect(await screen.findByText('destination "gh" exists; edit it on its page')).toBeInTheDocument();
  });

  it("keeps custom offered after switching away from it", async () => {
    base();
    server.use(http.get("/app/admin/api/destinations/gh", () => HttpResponse.json(dest({ fields: destFields({ identityIn: "custom" }), identityInSpec: "header X" }))));
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    await user.click(await screen.findByText("Edit destination", { selector: "summary" }));
    const identity = screen.getByLabelText(/^Identity in/) as HTMLSelectElement;
    await user.selectOptions(identity, "bearer");
    expect([...identity.options].map((o) => o.value)).toContain("custom");
  });

  it("confirms a delete with how many roles use it", async () => {
    base();
    server.use(
      http.get("/app/admin/api/destinations", () => HttpResponse.json({ destinations: [row], methods: [] })),
      http.delete("/app/admin/api/destinations/gh", () => new HttpResponse(null, { status: 204 })),
    );
    const user = userEvent.setup();
    renderAdmin(adminBoot, ADMIN + "config/destinations");
    await user.click(await screen.findByRole("button", { name: "Delete" }));
    expect(await screen.findByText("Delete destination gh? 2 role(s) list it.")).toBeInTheDocument();
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Delete" }));
  });

  it("shows a destination's conflicts, uses and broker", async () => {
    base();
    server.use(http.get("/app/admin/api/destinations/gh", () => HttpResponse.json(dest({
      conflicts: [{ project: "acme", role: "coder", other: "ghe", what: "both set GH_HOST (to different values)" }], conflictRoles: ["acme/coder"],
    }))));
    renderAdmin(adminBoot, at);
    expect(await screen.findByText(/Connector conflict in 1 role/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "ghe" })).toHaveAttribute("href", ADMIN + "config/destinations/ghe");
    expect(screen.getAllByRole("link", { name: "acme/coder" })[0]).toHaveAttribute("href", ADMIN + "config/projects/acme/agents/coder");
    expect(screen.getByText("role mapping")).toBeInTheDocument();
    expect(screen.getByText("GH_HOST")).toBeInTheDocument();
  });

  it("saves its prefilled form", async () => {
    base();
    let sent: Record<string, unknown> = {};
    server.use(
      http.get("/app/admin/api/destinations/gh", () => HttpResponse.json(dest())),
      http.post("/app/admin/api/destinations/gh", async ({ request }) => { sent = await request.json() as Record<string, unknown>; return HttpResponse.json(dest({ fields: destFields({ note: "hi" }) })); }),
    );
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    await user.click(await screen.findByText("Edit destination", { selector: "summary" }));
    await user.type(screen.getByLabelText(/^Note/), "hi");
    await user.click(screen.getByRole("button", { name: "Save destination" }));
    await waitFor(() => expect(sent).toEqual({ route: "/gh/", upstream: "https://api.github.com", identityIn: "bearer", apply: "bearer", credName: "gh-pat",
      env: "GH_HOST={host}", allowPaths: "", note: "hi", logHeadersIn: "none", logHeadersOut: "none", git: "" }));
  });

  it("offers custom only to a destination that uses it", async () => {
    base();
    server.use(http.get("/app/admin/api/destinations/gh", () => HttpResponse.json(dest({ fields: destFields({ identityIn: "custom" }), identityInSpec: "header X-Agent-Token" }))));
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    expect(await screen.findByText("header X-Agent-Token")).toBeInTheDocument();
    await user.click(screen.getByText("Edit destination", { selector: "summary" }));
    const identity = screen.getByLabelText(/^Identity in/) as HTMLSelectElement;
    expect(identity.value).toBe("custom");
    const apply = screen.getByLabelText(/^Apply as/) as HTMLSelectElement;
    expect([...apply.options].map((o) => o.value)).not.toContain("custom");
  });

  it("says when it doesn't exist", async () => {
    base();
    server.use(http.get("/app/admin/api/destinations/gone", () => HttpResponse.json({ error: "x", code: "not_found" }, { status: 404 })));
    renderAdmin(adminBoot, ADMIN + "config/destinations/gone");
    expect(await screen.findByText("Destination not found")).toBeInTheDocument();
  });
});
