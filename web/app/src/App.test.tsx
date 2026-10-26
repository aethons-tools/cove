import { describe, expect, it } from "vitest";
import { screen } from "@testing-library/react";
import { http, HttpResponse } from "msw";
import { server } from "./test/server";
import { boot, renderApp } from "./test/render";
import type { RailResponse } from "./api/types";

const rail: RailResponse = {
  unreadTotal: 2,
  groups: [
    { bucket: "needs-you", title: "Needs you", rows: [{ id: "chn_a", label: "intercom-spider", project: "test", kind: "session", needsYou: true, phase: "waiting", unread: 2, openAsks: 1, lastSeq: 9, selected: false, mission: "SPA slice 1" }] },
    { bucket: "channels", title: "Channels", rows: [{ id: "chn_b", label: "eng", project: "test", kind: "room", needsYou: false, phase: "", unread: 0, openAsks: 0, lastSeq: 3, selected: false, mission: "" }] },
  ],
};

describe("the app shell", () => {
  it("shows the brand, the user and the rail's groups and unread", async () => {
    server.use(http.get("/app/user/api/rail", () => HttpResponse.json(rail)));
    renderApp(boot);
    expect(screen.getByText("Acme Jam")).toBeInTheDocument();
    expect(await screen.findByText("intercom-spider")).toBeInTheDocument();
    expect(screen.getByText("Needs you")).toBeInTheDocument();
    expect(screen.getByText("eng")).toBeInTheDocument();
    expect(screen.getByText("SPA slice 1")).toBeInTheDocument();
    expect(screen.getByLabelText("2 unread")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /eng/ })).toHaveAttribute("href", "/app/next/c/chn_b");
  });

  it("shows an empty state for a member with no channels", async () => {
    server.use(http.get("/app/user/api/rail", () => HttpResponse.json({ groups: [], unreadTotal: 0 })));
    renderApp(boot);
    expect(await screen.findByText(/No conversations yet/)).toBeInTheDocument();
  });

  it("asks to sign in again when the sign-in has expired", async () => {
    server.use(http.get("/app/user/api/rail", () => new HttpResponse("<html/>", { status: 403, headers: { "Content-Type": "text/html" } })));
    renderApp(boot);
    expect(await screen.findByText("Your sign-in has expired.")).toBeInTheDocument();
    // Back through the gate at the same page: it sends a login redirect with
    // return_to, or its own "Sign in again" (via logout) when only one token expired.
    expect(screen.getByRole("link", { name: "Sign in again" })).toHaveAttribute("href", "/app/next/");
  });

  it("offers admin tabs and sign out from the bootstrap", async () => {
    server.use(http.get("/app/user/api/rail", () => HttpResponse.json(rail)));
    renderApp({ ...boot, admin: true, roles: "member · admin", tabs: [...boot.tabs, { key: "projects", label: "Projects", href: "/app/admin/projects", badge: 3, badgeRed: true, badgeTitle: "3 need attention" }] });
    expect(screen.getByRole("link", { name: /Projects/ })).toHaveAttribute("href", "/app/admin/projects");
    expect(screen.getByLabelText("3 need attention")).toBeInTheDocument();
  });
});
