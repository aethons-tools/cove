import { afterEach, describe, expect, it } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { server } from "../../test/server";
import { adminBoot, renderAdmin } from "../test/render";
import { ADMIN } from "../paths";
import type { TreeNode } from "../api/types";

const n = (path: string, label: string, children: TreeNode[] = [], extra: Partial<TreeNode> = {}): TreeNode =>
  ({ path, label, group: children.length > 0, badge: 0, badgeRed: false, badgeTitle: "", children, ...extra });

const nodes: TreeNode[] = [
  n("config/tracking", "Tracking"),
  n("config/projects", "Projects", [
    n("config/projects/acme", "acme", [
      n("config/projects/acme/members", "Members", [], { group: true }),
      n("config/projects/acme/agents", "Agents", [n("config/projects/acme/agents/coder", "coder", [], { badge: 1, badgeRed: true, badgeTitle: "1 broken" })], { badge: 1, badgeRed: true }),
    ], { badge: 1, badgeRed: true }),
  ], { badge: 1, badgeRed: true }),
  n("config/users", "Users", [n("config/users/usr_1", "alice")]),
  n("config/kits", "Kits", [], { group: true }),
];

const serveTree = () => server.use(http.get("/app/admin/api/tree", () => HttpResponse.json({ nodes })));
const tree = () => screen.getByRole("navigation", { name: "Config" });

describe("the Config tree", () => {
  afterEach(() => localStorage.clear());

  it("opens the current page's branch and marks it", async () => {
    serveTree();
    renderAdmin(adminBoot, ADMIN + "config/projects/acme/agents/coder");
    const coder = await screen.findByRole("link", { name: /^coder/ });
    expect(coder).toHaveAttribute("aria-current", "page");
    expect(within(coder).getByLabelText("1 broken")).toHaveTextContent("1");
    expect(within(tree()).queryByRole("link", { name: "alice" })).toBeNull();
  });

  it("expands a group without navigating, and remembers it", async () => {
    serveTree();
    const user = userEvent.setup();
    const { router, unmount } = renderAdmin(adminBoot, ADMIN + "config/tracking");
    await user.click(await screen.findByRole("button", { name: "Expand Users" }));
    expect(await screen.findByRole("link", { name: "alice" })).toBeInTheDocument();
    expect(router.state.location.pathname).toBe(ADMIN + "config/tracking");
    unmount();
    renderAdmin(adminBoot, ADMIN + "config/tracking");
    expect(await screen.findByRole("link", { name: "alice" })).toBeInTheDocument();
  });

  it("filters", async () => {
    serveTree();
    const user = userEvent.setup();
    renderAdmin(adminBoot, ADMIN + "config/tracking");
    await screen.findByRole("link", { name: /^Projects/ });
    await user.type(screen.getByPlaceholderText("Filter"), "ali");
    expect(await screen.findByRole("link", { name: "alice" })).toBeInTheDocument();
    await waitFor(() => expect(within(tree()).queryByRole("link", { name: /^Projects/ })).toBeNull());
  });

  it("navigates to a group's page", async () => {
    serveTree();
    const user = userEvent.setup();
    const { router } = renderAdmin(adminBoot, ADMIN + "config/tracking");
    await user.click(await screen.findByRole("link", { name: "Kits" }));
    await waitFor(() => expect(router.state.location.pathname).toBe(ADMIN + "config/kits"));
    expect(screen.getByRole("link", { name: "Open it in the current admin" })).toHaveAttribute("href", "/app/admin/kits");
  });

  it("still renders for a path that names nothing", async () => {
    serveTree();
    renderAdmin(adminBoot, ADMIN + "config/projects/gone");
    expect(await screen.findByRole("link", { name: /^Projects/ })).toBeInTheDocument();
    expect(screen.getByText("This page hasn't moved yet")).toBeInTheDocument();
  });

  it("marks only the exact node current (names differing in case)", async () => {
    server.use(http.get("/app/admin/api/tree", () => HttpResponse.json({ nodes: [
      n("config/projects", "Projects", [n("config/projects/Acme", "Acme"), n("config/projects/acme", "acme")]),
    ] })));
    renderAdmin(adminBoot, ADMIN + "config/projects/acme");
    await screen.findByRole("link", { name: "Acme" });
    expect(within(tree()).getByRole("link", { name: "acme" })).toHaveAttribute("aria-current", "page");
    expect(within(tree()).getByRole("link", { name: "Acme" })).not.toHaveAttribute("aria-current");
  });

  it("shows why the tree can't load", async () => {
    server.use(http.get("/app/admin/api/tree", () => HttpResponse.json({ error: "boom", code: "internal" }, { status: 500 })));
    renderAdmin(adminBoot, ADMIN + "config/tracking");
    expect(await screen.findByText(/boom/)).toBeInTheDocument();
  });
});
