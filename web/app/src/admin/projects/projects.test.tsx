import { describe, expect, it } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { QueryClient } from "@tanstack/react-query";
import { server } from "../../test/server";
import { adminBoot, renderAdmin } from "../test/render";
import { ADMIN } from "../paths";
import { project } from "./fixtures";

const tree = () => server.use(http.get("/app/admin/api/tree", () => HttpResponse.json({ nodes: [] })));
const at = ADMIN + "config/projects/acme";

describe("the projects list", () => {
  it("lists projects and creates one", async () => {
    tree();
    server.use(
      http.get("/app/admin/api/projects", () => HttpResponse.json({ projects: [{ name: "acme", members: 2, roles: 1, rooms: 0, managed: null }] })),
      http.post("/app/admin/api/projects", () => HttpResponse.json({ id: "beta", path: "config/projects/beta" }, { status: 201 })),
      http.get("/app/admin/api/projects/beta", () => HttpResponse.json(project({ name: "beta" }))),
    );
    const user = userEvent.setup();
    const { router } = renderAdmin(adminBoot, ADMIN + "config/projects");
    expect(await screen.findByRole("link", { name: "acme" })).toHaveAttribute("href", at);
    await user.click(screen.getByText("New project"));
    await user.type(screen.getByLabelText(/^Name/), "beta");
    await user.click(screen.getByRole("button", { name: "Create project" }));
    await waitFor(() => expect(router.state.location.pathname).toBe(ADMIN + "config/projects/beta"));
  });
});

describe("a project's overview", () => {
  it("renames and follows the new name", async () => {
    tree();
    server.use(
      http.get("/app/admin/api/projects/acme", () => HttpResponse.json(project())),
      http.post("/app/admin/api/projects/acme/rename", () => HttpResponse.json({ id: "acme2", path: "config/projects/acme2" })),
      http.get("/app/admin/api/projects/acme2", () => HttpResponse.json(project({ name: "acme2" }))),
    );
    const user = userEvent.setup();
    const { router } = renderAdmin(adminBoot, at);
    const input = await screen.findByLabelText("New name");
    await user.clear(input);
    await user.type(input, "acme2");
    await user.click(screen.getByRole("button", { name: "Rename" }));
    await waitFor(() => expect(router.state.location.pathname).toBe(ADMIN + "config/projects/acme2"));
  });

  it("holds Delete while the project is in use", async () => {
    tree();
    server.use(http.get("/app/admin/api/projects/acme", () => HttpResponse.json(project({ inUseBy: "role acme/coder" }))));
    renderAdmin(adminBoot, at);
    const del = await screen.findByRole("button", { name: "Delete" });
    expect(del).toBeDisabled();
    expect(del).toHaveAttribute("title", "Still referenced by role acme/coder — remove its roles, agents and grants first");
  });

  it("deletes after confirming and goes to the list", async () => {
    tree();
    server.use(
      http.get("/app/admin/api/projects/acme", () => HttpResponse.json(project())),
      http.delete("/app/admin/api/projects/acme", () => new HttpResponse(null, { status: 204 })),
      http.get("/app/admin/api/projects", () => HttpResponse.json({ projects: [] })),
    );
    const user = userEvent.setup();
    const { router } = renderAdmin(adminBoot, at);
    await user.click(await screen.findByRole("button", { name: "Delete" }));
    expect(await screen.findByText("Delete project acme? Its roster, escalation and chat service go with it; its channels are archived (history stays readable) and its name is freed.")).toBeInTheDocument();
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Delete" }));
    await waitFor(() => expect(router.state.location.pathname).toBe(ADMIN + "config/projects"));
  });

  it("sets the chat service", async () => {
    tree();
    let sent: unknown;
    server.use(
      http.get("/app/admin/api/projects/acme", () => HttpResponse.json(project())),
      http.post("/app/admin/api/projects/acme/chat-service", async ({ request }) => { sent = await request.json(); return HttpResponse.json(project({ chatService: "disc" })); }),
    );
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    await user.selectOptions(await screen.findByLabelText("Chat service"), "disc");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(sent).toEqual({ service: "disc" }));
    expect(await screen.findByText("chat: disc")).toBeInTheDocument();
  });

  it("shows the server's chat service after a refetch", async () => {
    tree();
    let n = 0;
    server.use(http.get("/app/admin/api/projects/acme", () => HttpResponse.json(project({ chatService: ++n === 1 ? "" : "disc" }))));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    renderAdmin(adminBoot, at, client);
    expect(await screen.findByLabelText("Chat service")).toHaveValue("");
    await client.refetchQueries({ queryKey: ["admin", "project", "acme"] });
    await waitFor(() => expect(screen.getByLabelText("Chat service")).toHaveValue("disc"));
  });

  it("edits and clears the agent context", async () => {
    tree();
    let sent: unknown;
    const withCtx = project({ context: { ...project().context, empty: false, core: "Be kind.", coreBytes: 8, yaml: "core: Be kind.\n" } });
    server.use(
      http.get("/app/admin/api/projects/acme", () => HttpResponse.json(withCtx)),
      http.post("/app/admin/api/projects/acme/context", async ({ request }) => { sent = await request.json(); return HttpResponse.json(withCtx); }),
      http.delete("/app/admin/api/projects/acme/context", () => HttpResponse.json(project())),
    );
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    expect(await screen.findByText("Be kind.")).toBeInTheDocument();
    await user.click(screen.getByText("Edit agent context"));
    await user.click(screen.getByRole("button", { name: "Save context" }));
    await waitFor(() => expect(sent).toEqual({ yaml: "core: Be kind.\n" }));
    await user.click(screen.getByRole("button", { name: "Clear" }));
    expect(await screen.findByText("Remove this project agent context?")).toBeInTheDocument();
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Clear" }));
    expect(await screen.findByText("None — agents get no project section.")).toBeInTheDocument();
  });

  it("says when the project doesn't exist", async () => {
    tree();
    server.use(http.get("/app/admin/api/projects/gone", () => HttpResponse.json({ error: "no such project", code: "not_found" }, { status: 404 })));
    renderAdmin(adminBoot, ADMIN + "config/projects/gone");
    expect(await screen.findByText("Project not found")).toBeInTheDocument();
  });
});

describe("TypeAhead", () => {
  it("completes the last token of a list", async () => {
    tree();
    server.use(
      http.get("/app/admin/api/projects/acme", () => HttpResponse.json(project({ members: [] }))),
      http.get("/app/admin/suggest", ({ request }) => HttpResponse.json(new URL(request.url).searchParams.get("kind") === "users" ? ["alice", "bob"] : [])),
    );
    const user = userEvent.setup();
    renderAdmin(adminBoot, at + "/members");
    await user.click(await screen.findByText("Add member", { selector: "summary" }));
    const input = screen.getByLabelText(/^User/);
    await user.type(input, "bo");
    await user.click(await screen.findByRole("option", { name: "bob" }));
    expect(input).toHaveValue("bob");
  });
});
