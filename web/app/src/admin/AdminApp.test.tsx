import { describe, expect, it } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import { http, HttpResponse } from "msw";
import { server } from "../test/server";
import { adminBoot, renderAdmin } from "./test/render";
import { ADMIN } from "./paths";

const emptyTree = () => server.use(http.get("/app/admin/api/tree", () => HttpResponse.json({ nodes: [] })));

describe("the admin app", () => {
  it("opens Config on Tracking", async () => {
    emptyTree();
    const { router } = renderAdmin(adminBoot, ADMIN + "config");
    await waitFor(() => expect(router.state.location.pathname).toBe(ADMIN + "config/tracking"));
  });

  it("shows a local admin no Inbox tab, and marks the open tab", async () => {
    emptyTree();
    renderAdmin(adminBoot, ADMIN + "config/users");
    const nav = await screen.findByRole("navigation", { name: "Tabs" });
    expect(within(nav).queryByText("Inbox")).toBeNull();
    expect(within(nav).getByRole("link", { name: "Config" })).toHaveAttribute("aria-current", "page");
    expect(within(nav).getByRole("link", { name: "Agents" })).not.toHaveAttribute("aria-current");
  });

  it("says when there's no such page, linking the Dashboard", async () => {
    renderAdmin(adminBoot, ADMIN + "nonsense");
    expect(await screen.findByRole("heading", { name: "No such page" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Go to the Dashboard" })).toHaveAttribute("href", "/app/admin/");
  });

  it("offers Sign out only when signed in through the browser", async () => {
    const { unmount } = renderAdmin(adminBoot, ADMIN + "nonsense");
    await screen.findByRole("heading", { name: "No such page" });
    screen.getByRole("button", { name: "Account" }).click();
    await screen.findByText("local admin · admin");
    expect(screen.queryByRole("menuitem", { name: "Sign out" })).toBeNull();
    unmount();
    renderAdmin({ ...adminBoot, signOut: true }, ADMIN + "nonsense");
    await screen.findByRole("heading", { name: "No such page" });
    screen.getByRole("button", { name: "Account" }).click();
    expect(await screen.findByRole("menuitem", { name: "Sign out" })).toHaveAttribute("href", "/app/auth/logout");
  });
});
