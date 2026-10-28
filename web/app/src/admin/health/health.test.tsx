import { describe, expect, it } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { server } from "../../test/server";
import { adminBoot, renderAdmin } from "../test/render";
import { ADMIN } from "../paths";
import type { ChannelLog, Health, SquawkRow } from "../api/types";

const base = () => server.use(
  http.get("/app/admin/api/tree", () => HttpResponse.json({ nodes: [] })),
  http.get("/app/admin/suggest", () => HttpResponse.json([])),
);
const health = (h: Partial<Health> = {}): Health => ({ open: [], resolved: [], silences: "", ...h });

describe("Health", () => {
  it("shows open conditions with their fix, the resolved ones, and Silences", async () => {
    base();
    server.use(http.get("/app/admin/api/health", () => HttpResponse.json(health({
      open: [{ key: "mgr:m1", severity: "critical", summary: "manager m1 is down", fix: "jam manager restart m1", detail: "dial refused", age: "5m", lasted: "" }],
      resolved: [{ key: "kit:k", severity: "warning", summary: "kit k stale", fix: "", detail: "", age: "1h", lasted: "2h" }],
      silences: "http://am.example/#/silences",
    }))));
    renderAdmin(adminBoot, ADMIN + "health");
    expect(await screen.findByText("manager m1 is down")).toBeInTheDocument();
    expect(screen.getByText("critical")).toBeInTheDocument();
    expect(screen.getByText("jam manager restart m1")).toBeInTheDocument();
    expect(screen.getByText(/lasted 2h, resolved 1h ago/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Silences ↗" })).toHaveAttribute("href", "http://am.example/#/silences");
    expect(screen.getByRole("link", { name: "Channel log" })).toHaveAttribute("href", ADMIN + "health/intercom");
  });

  it("says when there's nothing", async () => {
    base();
    server.use(http.get("/app/admin/api/health", () => HttpResponse.json(health())));
    renderAdmin(adminBoot, ADMIN + "health");
    expect(await screen.findByText("Nothing needs attention.")).toBeInTheDocument();
    expect(screen.getByText("None.")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Silences ↗" })).toBeNull();
  });
});

const row = (s: Partial<SquawkRow> = {}): SquawkRow => ({
  at: "2026-10-28 10:00:00", from: "alice", fromId: "human:alice", agentId: "", channel: "", channelId: "", project: "acme", body: "<p>please HOLD</p>", plain: false,
  to: [{ target: "actor:cove-1", external: false }], ...s,
});

describe("the channel log", () => {
  it("reads its filters from the URL, flags a bad date and shows legacy recipients", async () => {
    base();
    let asked = "";
    server.use(http.get("/app/admin/api/intercom", ({ request }) => {
      asked = new URL(request.url).search;
      return HttpResponse.json({ configured: true, legacy: true, filtered: true, badDate: true, rows: [row()] } satisfies ChannelLog);
    }));
    renderAdmin(adminBoot, ADMIN + "health/intercom?log=legacy&since=bad");
    expect(await screen.findByText("Ignored an unparseable date (expected YYYY-MM-DD).")).toBeInTheDocument();
    expect(asked).toContain("log=legacy");
    expect(asked).toContain("since=bad");
    expect(screen.getByRole("combobox", { name: "Log" })).toHaveValue("legacy");
    expect(screen.getByText("actor:cove-1")).toBeInTheDocument();
    expect(screen.getByText("internal")).toBeInTheDocument();
  });

  it("Filter puts the fields in the URL, keeping the log", async () => {
    base();
    server.use(http.get("/app/admin/api/intercom", () => HttpResponse.json({ configured: true, legacy: true, filtered: false, badDate: false, rows: [] } satisfies ChannelLog)));
    const { router } = renderAdmin(adminBoot, ADMIN + "health/intercom?log=legacy");
    await userEvent.type(await screen.findByRole("textbox", { name: "Body contains" }), "x");
    await userEvent.click(screen.getByRole("button", { name: "Filter" }));
    await waitFor(() => expect(new URLSearchParams(router.state.location.search).get("q")).toBe("x"));
    expect(new URLSearchParams(router.state.location.search).get("log")).toBe("legacy");
    await userEvent.click(screen.getByRole("button", { name: "Reset" }));
    await waitFor(() => expect(router.state.location.search).toBe("?log=legacy"));
  });

  it("says when the log isn't configured", async () => {
    base();
    server.use(http.get("/app/admin/api/intercom", () => HttpResponse.json({ configured: false, legacy: false, filtered: false, badDate: false, rows: [] } satisfies ChannelLog)));
    renderAdmin(adminBoot, ADMIN + "health/intercom");
    expect(await screen.findByText("The message log is unavailable.")).toBeInTheDocument();
  });
});
