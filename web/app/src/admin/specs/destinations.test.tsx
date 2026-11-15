import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { server } from "../../test/server";
import { adminBoot, renderAdmin } from "../test/render";
import { ADMIN } from "../paths";
import { dest, destFields, endpoint } from "./fixtures";
import { setBranch } from "../branch/branch";

const base = () => server.use(
  http.get("/app/admin/api/tree", () => HttpResponse.json({ nodes: [] })),
  http.get("/app/admin/suggest", () => HttpResponse.json([])),
);
const at = ADMIN + "config/destinations/gh";
const row = { name: "gh", endpoints: 2, addresses: ["/gh/", "/gh-git/"], upstreams: ["https://api.github.com"], credName: "gh-pat", envKeys: ["GH_HOST"], gitRouted: true, usedBy: 2, attn: null };

const card$ = async () => (await screen.findByRole("heading", { name: "Destination" })).closest<HTMLElement>(".mantine-Card-root")!;

// The config edits show on a branch.
beforeEach(() => setBranch("jam/test"));
afterEach(() => setBranch(""));

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
    expect(screen.getByText("/gh-git/")).toBeInTheDocument();
    await user.click(screen.getByText("New destination", { selector: "summary" }));
    await user.type(screen.getByLabelText(/^Name/), "ant");
    await user.type(screen.getByLabelText(/^Route \*/), "/anthropic/");
    await user.type(screen.getByLabelText(/^Upstream URL/), "https://api.anthropic.com");
    await user.click(screen.getByRole("button", { name: "Create destination" }));
    await waitFor(() => expect(router.state.location.pathname).toBe(ADMIN + "config/destinations/ant"));
    expect(sent).toEqual({ name: "ant", credName: "", note: "", env: [], endpoints: [{ name: "", subdomain: "", route: "/anthropic/", upstream: "https://api.anthropic.com",
      identityIn: "bearer", identityInSpec: null, apply: "bearer", applySpec: null, allowPaths: [], git: false, logHeadersIn: "none", logHeadersOut: "none" }] });
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

  it("shows a destination's conflicts and uses", async () => {
    base();
    server.use(http.get("/app/admin/api/destinations/gh", () => HttpResponse.json(dest({
      conflicts: [{ project: "acme", role: "coder", other: "ghe", what: "both set GH_HOST (to different values)" }], conflictRoles: ["acme/coder"],
    }))));
    renderAdmin(adminBoot, at);
    expect(await screen.findByText(/Connector conflict in 1 role/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "ghe" })).toHaveAttribute("href", ADMIN + "config/destinations/ghe");
    expect(screen.getAllByRole("link", { name: "acme/coder" })[0]).toHaveAttribute("href", ADMIN + "config/projects/acme/agents/coder");
    expect(screen.getByText("role mapping")).toBeInTheDocument();
  });

  it("shows the card read-only until Edit, and Cancel drops the edits", async () => {
    base();
    server.use(http.get("/app/admin/api/destinations/gh", () => HttpResponse.json(dest({ fields: destFields({ note: "GitHub for acme" }) }))));
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    const card = await card$();
    expect(within(card).getByText("gh-pat")).toBeInTheDocument();
    expect(within(card).getByText("GitHub for acme")).toBeInTheDocument();
    expect(within(card).getByText("/gh/")).toBeInTheDocument();
    expect(within(card).getByText("GH_HOST")).toBeInTheDocument();
    expect(within(card).queryByRole("textbox")).toBeNull();
    expect(within(card).queryByText("+ new endpoint…")).toBeNull();
    expect(within(card).getByLabelText("1 / 10 endpoints")).toBeInTheDocument();
    await user.click(within(card).getByRole("button", { name: "Edit" }));
    await user.type(within(card).getByLabelText("Note"), "!");
    await user.click(within(card).getByRole("button", { name: "Cancel" }));
    expect(within(card).getByText("GitHub for acme")).toBeInTheDocument();
    expect(within(card).queryByLabelText("Note")).toBeNull();
  });

  it("saves its fields unchanged", async () => {
    base();
    let sent: Record<string, unknown> = {};
    const custom = endpoint({ name: "rest", identityIn: "custom", identityInSpec: { header: "Authorization", prefixes: ["Bearer ", "token "], encoding: "raw" },
      apply: "custom", applySpec: { header: "Authorization", template: "token {cred}", encoding: "raw", basicUser: "" }, allowPaths: ["/repos/*", "/user"] });
    const fields = destFields({ note: "hi", endpoints: [custom, endpoint({ name: "git", route: "/gh-git/", upstream: "https://github.com", git: true })] });
    server.use(
      http.get("/app/admin/api/destinations/gh", () => HttpResponse.json(dest({ fields }))),
      http.post("/app/admin/api/destinations/gh", async ({ request }) => { sent = await request.json() as Record<string, unknown>; return HttpResponse.json(dest({ fields })); }),
    );
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    const card = await card$();
    await user.click(within(card).getByRole("button", { name: "Edit" }));
    await user.click(within(card).getByRole("button", { name: "Save destination" }));
    await waitFor(() => expect(sent).toEqual(fields));
    expect(await within(card).findByRole("button", { name: "Edit" })).toBeInTheDocument();
  });

  it("adds and removes endpoints", async () => {
    base();
    let sent: { endpoints?: { name: string }[] } = {};
    const git = endpoint({ name: "git", route: "/gh-git/", upstream: "https://github.com", git: true });
    server.use(
      http.get("/app/admin/api/destinations/gh", () => HttpResponse.json(dest({ fields: destFields({ endpoints: [endpoint(), git] }) }))),
      http.post("/app/admin/api/destinations/gh", async ({ request }) => { sent = await request.json() as typeof sent; return HttpResponse.json(dest()); }),
    );
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    const card = await card$();
    await user.click(within(card).getByRole("button", { name: "Edit" }));
    await user.click(within(card).getByRole("button", { name: "Remove endpoint 1" }));
    await user.click(within(card).getByText("+ new endpoint…"));
    await user.keyboard("graphql");
    await user.tab();
    await user.keyboard("gh");
    await user.tab();
    await user.keyboard("/api/graphql");
    await user.tab();
    await user.keyboard("https://api.github.com/graphql{Escape}");
    expect(within(card).getByLabelText("2 / 10 endpoints")).toBeInTheDocument();
    await user.click(within(card).getByRole("button", { name: "Save destination" }));
    await waitFor(() => expect(sent.endpoints).toEqual([git, endpoint({ name: "graphql", subdomain: "gh", route: "/api/graphql", upstream: "https://api.github.com/graphql" })]));
  });

  it("offers no new endpoint at the limit", async () => {
    base();
    const ten = Array.from({ length: 10 }, (_, i) => endpoint({ name: `e${i}`, route: `/e${i}/` }));
    server.use(http.get("/app/admin/api/destinations/gh", () => HttpResponse.json(dest({ fields: destFields({ endpoints: ten }) }))));
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    const card = await card$();
    await user.click(within(card).getByRole("button", { name: "Edit" }));
    expect(within(card).getByLabelText("10 / 10 endpoints")).toBeInTheDocument();
    expect(within(card).queryByText("+ new endpoint…")).toBeNull();
  });

  it("shows a preset's meaning read-only and edits an expression", async () => {
    base();
    let sent: { endpoints?: unknown[] } = {};
    server.use(
      http.get("/app/admin/api/destinations/gh", () => HttpResponse.json(dest())),
      http.post("/app/admin/api/destinations/gh", async ({ request }) => { sent = await request.json() as typeof sent; return HttpResponse.json(dest()); }),
    );
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    const card = await card$();
    await user.click(within(card).getByRole("button", { name: "Details, row 1" }));
    expect(within(card).getByText("Bearer {cred}")).toBeInTheDocument();
    expect(within(card).getAllByText("(preset bearer)", { exact: false })).toHaveLength(2);
    await user.click(within(card).getByRole("button", { name: "Edit" }));
    expect(within(card).queryByLabelText("Template, with {cred} once, gh")).toBeNull();
    await user.click(within(card).getByRole("button", { name: /^Apply as, row 1/ }));
    await user.selectOptions(within(card).getByLabelText("Apply as, row 1"), "expression");
    const template = within(card).getByLabelText("Template, with {cred} once, gh");
    expect(template).toHaveValue("Bearer {cred}");
    await user.clear(template);
    await user.type(template, "token {{cred");
    expect(within(card).getByText("The template needs {cred} exactly once")).toBeInTheDocument();
    expect(within(card).getByText("1 problem to fix before saving")).toBeInTheDocument();
    expect(within(card).getByRole("button", { name: "Save destination" })).toBeDisabled();
    await user.type(template, "}");
    expect(within(card).getByText("Ready to save")).toBeInTheDocument();
    await user.click(within(card).getByRole("button", { name: "Save destination" }));
    await waitFor(() => expect(sent.endpoints).toEqual([endpoint({ apply: "custom", applySpec: { header: "Authorization", template: "token {cred}", encoding: "raw", basicUser: "" } })]));
  });

  it("tags an endpoint that logs headers", async () => {
    base();
    const logging = endpoint({ name: "git", route: "/gh-git/", logHeadersOut: "changed" });
    server.use(http.get("/app/admin/api/destinations/gh", () => HttpResponse.json(dest({ fields: destFields({ endpoints: [endpoint(), logging] }) }))));
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    const card = await card$();
    expect(within(card).getAllByText("logging")).toHaveLength(1);
    await user.click(within(card).getByRole("button", { name: "Edit" }));
    await user.click(within(card).getByRole("button", { name: "Details, row 1" }));
    await user.selectOptions(within(card).getByLabelText("Log incoming headers, gh"), "examined");
    expect(within(card).getAllByText("logging")).toHaveLength(2);
  });

  it("shows problems at their fields and keeps Save off", async () => {
    base();
    server.use(http.get("/app/admin/api/destinations/gh", () => HttpResponse.json(dest())));
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    const card = await card$();
    await user.click(within(card).getByRole("button", { name: "Edit" }));
    await user.click(within(card).getByText("+ new endpoint…"));
    await user.keyboard("gh{Escape}");
    expect(within(card).getByText("Name already used")).toBeInTheDocument();
    expect(within(card).getByText("Give a subdomain, a route or both")).toBeInTheDocument();
    expect(within(card).getByText("Required")).toBeInTheDocument();
    expect(within(card).getByText("3 problems to fix before saving")).toBeInTheDocument();
    expect(within(card).getByRole("button", { name: "Save destination" })).toBeDisabled();
    await user.click(within(card).getByRole("button", { name: "Remove endpoint 2" }));
    await user.click(within(card).getByRole("button", { name: /^Value, row 1/ }));
    await user.clear(within(card).getByLabelText("Value, row 1"));
    await user.type(within(card).getByLabelText("Value, row 1"), "{{host:rest}{Escape}");
    expect(within(card).getByText("{host:rest} names no endpoint")).toBeInTheDocument();
  });

  it("shows the server's field errors at their fields", async () => {
    base();
    server.use(
      http.get("/app/admin/api/destinations/gh", () => HttpResponse.json(dest())),
      http.post("/app/admin/api/destinations/gh", () => HttpResponse.json({ error: "x", code: "invalid", errors: [
        { field: "endpoints[0].subdomain", message: 'subdomain "gh": this Jam has no destinations domain' },
        { field: "endpoints[0].logHeadersIn", message: "log_headers_in must be none, examined or all" },
      ] }, { status: 400 })),
    );
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    const card = await card$();
    await user.click(within(card).getByRole("button", { name: "Edit" }));
    await user.click(within(card).getByRole("button", { name: /^Subdomain, row 1/ }));
    await user.keyboard("gh{Escape}");
    await user.click(within(card).getByRole("button", { name: "Save destination" }));
    expect(await within(card).findByText('Subdomain "gh": this Jam has no destinations domain')).toBeInTheDocument();
    // The detail opens to show its problem.
    expect(within(card).getByText("Log_headers_in must be none, examined or all")).toBeInTheDocument();
    expect(within(card).getByText("2 problems to fix before saving")).toBeInTheDocument();
  });

  it("says when it doesn't exist", async () => {
    base();
    server.use(http.get("/app/admin/api/destinations/gone", () => HttpResponse.json({ error: "x", code: "not_found" }, { status: 404 })));
    renderAdmin(adminBoot, ADMIN + "config/destinations/gone");
    expect(await screen.findByText("Destination not found")).toBeInTheDocument();
  });
});
