import { describe, expect, it } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { server } from "./test/server";
import { boot, renderApp } from "./test/render";
import type { ChannelRow, RailResponse } from "./api/types";
import { card } from "./test/card";

const row = (over: Partial<ChannelRow>): ChannelRow => ({
  id: "", label: "", project: "test", kind: "room", needsYou: false, phase: "", unread: 0, openAsks: 0, lastSeq: 1, selected: false, mission: "", session: "", ssh: false, sessionView: "", ...over,
});
const rail: RailResponse = {
  unreadTotal: 2, groups: [], history: [],
  me: [
    row({ id: "chn_a", label: "intercom-spider", kind: "dm", needsYou: true, unread: 2, card: card({ session: "ses_spider", name: "intercom-spider", pill: "needs you", state: "idle" }) }),
    row({ id: "chn_b", label: "eng" }),
  ],
  work: [{ key: "TEST-7", title: "Fix the login", log: "chn_t7", worker: { id: "ses_t7", label: "test-7", state: "working" } }],
  agents: [{ session: "ses_spider", ssh: false, card: card({ session: "ses_spider", name: "intercom-spider", mission: "SPA slice 1" }) }],
};

describe("the app shell", () => {
  it("shows the brand, the user and the rail's Me, Work, and Agents", async () => {
    server.use(http.get("/app/user/api/rail", () => HttpResponse.json(rail)));
    renderApp(boot);
    expect(screen.getByText("Acme Jam")).toBeInTheDocument();
    const me = await screen.findByRole("region", { name: "Me" });
    expect(within(me).getByRole("link", { name: /eng/ })).toHaveAttribute("href", "/app/user/c/chn_b");
    expect(within(me).getByRole("link", { name: /intercom-spider/ })).toHaveAttribute("href", "/app/user/c/chn_a");
    expect(within(me).getByLabelText("2 unread")).toBeInTheDocument();
    expect(within(me).getByText("needs you")).toBeInTheDocument();
    const work = screen.getByRole("region", { name: "Work" });
    expect(within(work).getByRole("link", { name: /TEST-7/ })).toHaveAttribute("href", "/app/user/c/chn_t7");
    const agents = screen.getByRole("region", { name: "Agents" });
    expect(within(agents).getByRole("link", { name: /intercom-spider/ })).toHaveAttribute("href", "/app/user/s/ses_spider");
    expect(within(agents).getByText("SPA slice 1")).toBeInTheDocument();
  });

  it("opens a Work row's worker in its session view", async () => {
    server.use(http.get("/app/user/api/rail", () => HttpResponse.json(rail)));
    const { router } = renderApp(boot);
    await userEvent.click(await screen.findByRole("button", { name: "Open test-7's session view" }));
    expect(router.state.location.pathname).toBe("/app/user/s/ses_t7");
  });

  it("saves a reordered Me and shows the new order at once", async () => {
    const saved: unknown[] = [];
    let release = () => {};
    const gate = new Promise<void>((r) => { release = r; });
    let me = rail.me;
    server.use(
      http.get("/app/user/api/rail", () => HttpResponse.json({ ...rail, me })),
      http.put("/app/user/api/rail/order", async ({ request }) => {
        saved.push(await request.json());
        await gate;
        me = [rail.me[1]!, rail.me[0]!];
        return new HttpResponse(null, { status: 204 });
      }),
    );
    renderApp(boot);
    const section = await screen.findByRole("region", { name: "Me" });
    const hrefs = () => within(section).getAllByRole("link").map((a) => a.getAttribute("href"));
    fireEvent.keyDown(within(section).getByRole("button", { name: "Move eng" }), { key: "ArrowUp" });
    await waitFor(() => expect(saved).toEqual([{ section: "me", ids: ["chn_b", "chn_a"] }]));
    expect(hrefs()).toEqual(["/app/user/c/chn_b", "/app/user/c/chn_a"]); // before the save answers
    release();
    await waitFor(() => expect(hrefs()).toEqual(["/app/user/c/chn_b", "/app/user/c/chn_a"]));
  });

  it("shows an empty state for a member with no channels", async () => {
    server.use(http.get("/app/user/api/rail", () => HttpResponse.json({ groups: [], me: [], work: [], agents: [], history: [], unreadTotal: 0 })));
    renderApp(boot);
    expect(await screen.findByText(/No conversations yet/)).toBeInTheDocument();
  });

  it("asks to sign in again when the sign-in has expired", async () => {
    server.use(http.get("/app/user/api/rail", () => new HttpResponse("<html/>", { status: 403, headers: { "Content-Type": "text/html" } })));
    renderApp(boot);
    expect(await screen.findByText("Your sign-in has expired.")).toBeInTheDocument();
    // Back through the gate at the same page: it sends a login redirect with
    // return_to, or its own "Sign in again" (via logout) when only one token expired.
    expect(screen.getByRole("link", { name: "Sign in again" })).toHaveAttribute("href", "/app/user/");
  });

  it("offers admin tabs and sign out from the bootstrap", async () => {
    server.use(http.get("/app/user/api/rail", () => HttpResponse.json(rail)));
    renderApp({ ...boot, admin: true, roles: "member · admin", tabs: [...boot.tabs, { key: "projects", label: "Projects", href: "/app/admin/projects", badge: 3, badgeRed: true, badgeTitle: "3 need attention" }] });
    expect(screen.getByRole("link", { name: /Projects/ })).toHaveAttribute("href", "/app/admin/projects");
    expect(screen.getByLabelText("3 need attention")).toBeInTheDocument();
  });
});
