import { describe, expect, it } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse, delay } from "msw";
import { server } from "../../test/server";
import { adminBoot, renderAdmin } from "../test/render";
import { ADMIN } from "../paths";
import type { AppTokenRow, UserDetail, UserRow } from "../api/types";

const alice: UserRow = { id: "usr_1", name: "alice", logins: ["a@x"], accounts: [{ id: "acc_1", connection: "discord", serviceUid: "42", handle: "ali" }], projects: ["acme"], managed: null };
const detail = (u: Partial<UserRow> = {}, d: Partial<UserDetail> = {}): UserDetail =>
  ({ user: { ...alice, ...u }, removed: false, loginSpec: "a@x", oidcSpec: "", connections: [{ name: "discord", kind: "discord" }], appTokens: [], ...d });

const tree = () => server.use(http.get("/app/admin/api/tree", () => HttpResponse.json({ nodes: [] })));
const err = (status: number, code: string, error: string) => HttpResponse.json({ error, code }, { status });

describe("the users list", () => {
  it("lists users, linking each", async () => {
    tree();
    server.use(http.get("/app/admin/api/users", () => HttpResponse.json({ users: [alice] })));
    renderAdmin(adminBoot, ADMIN + "config/users");
    expect(await screen.findByRole("link", { name: "alice" })).toHaveAttribute("href", ADMIN + "config/users/usr_1");
    expect(screen.getByRole("link", { name: "acme" })).toHaveAttribute("href", ADMIN + "config/projects/acme");
    expect(screen.getByText("@ali")).toBeInTheDocument();
  });

  it("says when there are none", async () => {
    tree();
    server.use(http.get("/app/admin/api/users", () => HttpResponse.json({ users: [] })));
    renderAdmin(adminBoot, ADMIN + "config/users");
    expect(await screen.findByText("No users.")).toBeInTheDocument();
  });

  it("creates a user, opens their page and refreshes the tree", async () => {
    let treeFetches = 0;
    let sent: unknown;
    server.use(
      http.get("/app/admin/api/tree", () => { treeFetches++; return HttpResponse.json({ nodes: [] }); }),
      http.get("/app/admin/api/users", () => HttpResponse.json({ users: [] })),
      http.post("/app/admin/api/users", async ({ request }) => { sent = await request.json(); return HttpResponse.json({ id: "usr_9", path: "config/users/usr_9" }, { status: 201 }); }),
      http.get("/app/admin/api/users/usr_9", () => HttpResponse.json(detail({ id: "usr_9", name: "zed" }))),
    );
    const user = userEvent.setup();
    const { router } = renderAdmin(adminBoot, ADMIN + "config/users");
    await user.click(await screen.findByText("New user"));
    await user.type(screen.getByLabelText(/^Name/), "zed");
    await user.click(screen.getByRole("button", { name: "Create user" }));
    await waitFor(() => expect(router.state.location.pathname).toBe(ADMIN + "config/users/usr_9"));
    expect(sent).toEqual({ name: "zed", logins: "", oidc: "" });
    await waitFor(() => expect(treeFetches).toBeGreaterThan(1));
  });
});

describe("a user's page", () => {
  it("keeps a refused rename's name and shows why", async () => {
    tree();
    server.use(
      http.get("/app/admin/api/users/usr_1", () => HttpResponse.json(detail())),
      http.post("/app/admin/api/users/usr_1/name", () => err(409, "conflict", "name taken")),
    );
    const user = userEvent.setup();
    renderAdmin(adminBoot, ADMIN + "config/users/usr_1");
    const name = await screen.findByLabelText(/^Name/);
    await user.clear(name);
    await user.type(name, "bob");
    await user.click(screen.getByRole("button", { name: "Rename" }));
    expect(await screen.findByText("name taken")).toBeInTheDocument();
    expect(name).toHaveValue("bob");
  });

  it("moves to the id's page after renaming a user opened by name", async () => {
    tree();
    server.use(
      http.get("/app/admin/api/users/alice", () => HttpResponse.json(detail())),
      http.get("/app/admin/api/users/usr_1", () => HttpResponse.json(detail({ name: "alicia" }))),
      http.post("/app/admin/api/users/usr_1/name", () => HttpResponse.json(detail({ name: "alicia" }))),
    );
    const user = userEvent.setup();
    const { router } = renderAdmin(adminBoot, ADMIN + "config/users/alice");
    const name = await screen.findByLabelText(/^Name/);
    await user.clear(name);
    await user.type(name, "alicia");
    await user.click(screen.getByRole("button", { name: "Rename" }));
    await waitFor(() => expect(router.state.location.pathname).toBe(ADMIN + "config/users/usr_1"));
  });

  it("confirms a managed user's rename with the drift note", async () => {
    tree();
    server.use(http.get("/app/admin/api/users/usr_1", () => HttpResponse.json(detail({ managed: { commit: "abc1234", applied: "x" } }))));
    const user = userEvent.setup();
    renderAdmin(adminBoot, ADMIN + "config/users/usr_1");
    expect(await screen.findByText("managed · config @abc1234")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Rename" }));
    expect(await screen.findByText("This will drift from the config repo (managed · config @abc1234). Rename anyway?")).toBeInTheDocument();
  });

  it("is read-only for a removed user", async () => {
    tree();
    server.use(http.get("/app/admin/api/users/usr_1", () => HttpResponse.json(detail({}, { removed: true }))));
    renderAdmin(adminBoot, ADMIN + "config/users/usr_1");
    expect(await screen.findByText(/This user was removed/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Rename" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Remove user" })).toBeNull();
  });

  it("says when the user doesn't exist", async () => {
    tree();
    server.use(http.get("/app/admin/api/users/nobody", () => err(404, "not_found", "no such user")));
    renderAdmin(adminBoot, ADMIN + "config/users/nobody");
    expect(await screen.findByText("User not found")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Back to users" })).toHaveAttribute("href", ADMIN + "config/users");
  });

  it("unlinks an account after confirming", async () => {
    tree();
    server.use(
      http.get("/app/admin/api/users/usr_1", () => HttpResponse.json(detail())),
      http.delete("/app/admin/api/users/usr_1/accounts/acc_1", async () => { await delay(10); return HttpResponse.json(detail({ accounts: [] })); }),
    );
    const user = userEvent.setup();
    renderAdmin(adminBoot, ADMIN + "config/users/usr_1");
    await user.click(await screen.findByRole("button", { name: "Unlink" }));
    expect(await screen.findByText("Unlink this discord account from alice?")).toBeInTheDocument();
    const dialog = screen.getByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: "Unlink" }));
    expect(await screen.findByText("No accounts.")).toBeInTheDocument();
  });

  it("removes the user and goes back to the list", async () => {
    tree();
    server.use(
      http.get("/app/admin/api/users/usr_1", () => HttpResponse.json(detail())),
      http.delete("/app/admin/api/users/usr_1", () => new HttpResponse(null, { status: 204 })),
      http.get("/app/admin/api/users", () => HttpResponse.json({ users: [] })),
    );
    const user = userEvent.setup();
    const { router } = renderAdmin(adminBoot, ADMIN + "config/users/usr_1");
    await user.click(await screen.findByRole("button", { name: "Remove user" }));
    expect(await screen.findByText("Remove user alice? They leave every project; history keeps showing them as removed.")).toBeInTheDocument();
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Remove user" }));
    await waitFor(() => expect(router.state.location.pathname).toBe(ADMIN + "config/users"));
  });

  it("user page lists and revokes app tokens", async () => {
    tree();
    const tok: AppTokenRow = { id: "apt_1", label: "VS Code on mac", created: new Date().toISOString(), lastUsed: new Date().toISOString(),
      expires: new Date(Date.now() + 86_400_000).toISOString(), operator: true };
    let tokens = [tok];
    const deleted: string[] = [];
    server.use(
      http.get("/app/admin/api/users/usr_1", () => HttpResponse.json(detail({}, { appTokens: tokens }))),
      http.delete("/app/admin/api/users/usr_1/app-tokens/apt_1", () => { deleted.push("apt_1"); tokens = []; return new HttpResponse(null, { status: 204 }); }),
    );
    const user = userEvent.setup();
    renderAdmin(adminBoot, ADMIN + "config/users/usr_1");
    expect(await screen.findByText("VS Code on mac")).toBeInTheDocument();
    expect(screen.getByText("operator rights")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Revoke" }));
    await user.click(within(await screen.findByRole("dialog")).getByRole("button", { name: "Revoke" }));
    expect(await screen.findByText("No apps are connected.")).toBeInTheDocument();
    expect(deleted).toEqual(["apt_1"]);
  });
});
