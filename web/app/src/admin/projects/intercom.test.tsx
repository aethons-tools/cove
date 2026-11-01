import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { server } from "../../test/server";
import { adminBoot, renderAdmin } from "../test/render";
import { ADMIN } from "../paths";
import { project } from "./fixtures";
import type { SquawkRow } from "../api/types";
import { setBranch } from "../branch/branch";

const tree = () => server.use(http.get("/app/admin/api/tree", () => HttpResponse.json({ nodes: [] })));
const at = ADMIN + "config/projects/acme/intercom";
const sq = (s: Partial<SquawkRow>): SquawkRow => ({ at: "Oct 28 10:00", from: "alice", fromId: "usr_1", agentId: "", channel: "eng", channelId: "chn_1", project: "acme", body: "", plain: false, to: [], ...s });

// The config edits show on a branch.
beforeEach(() => setBranch("jam/test"));
afterEach(() => setBranch(""));

describe("project intercom", () => {
  it("adds and removes rooms", async () => {
    tree();
    let sent: unknown;
    server.use(
      http.get("/app/admin/api/projects/acme", ({ request }) => HttpResponse.json(project({ rooms: new URL(request.url).searchParams.get("section") === "intercom" ? [] : [] }))),
      http.get("/app/admin/suggest", () => HttpResponse.json(["discord"])),
      http.post("/app/admin/api/projects/acme/rooms", async ({ request }) => { sent = await request.json(); return HttpResponse.json(project({ rooms: [{ name: "eng", connection: "disc", kind: "discord", ref: "9", postOnly: true }] })); }),
      http.delete("/app/admin/api/projects/acme/rooms/eng", () => HttpResponse.json(project({ rooms: [] }))),
    );
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    expect(await screen.findByText("No rooms.")).toBeInTheDocument();
    await user.click(screen.getByText("Add room", { selector: "summary" }));
    await user.type(screen.getByLabelText(/^Name/), "eng");
    await user.type(screen.getByLabelText(/^Connection/), "disc");
    await user.type(screen.getByLabelText(/^Ref/), "9");
    await user.click(screen.getByRole("button", { name: "Add room" }));
    await waitFor(() => expect(sent).toEqual({ name: "eng", connection: "disc", ref: "9" }));
    expect(await screen.findByText("post-only")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Remove" }));
    expect(await screen.findByText("Remove room eng from acme?")).toBeInTheDocument();
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Remove" }));
    expect(await screen.findByText("No rooms.")).toBeInTheDocument();
  });

  it("shows recent messages: markdown as HTML, plain as text", async () => {
    tree();
    server.use(http.get("/app/admin/api/projects/acme", () => HttpResponse.json(project({ squawks: [
      sq({ body: "<p><strong>bold</strong></p>" }),
      sq({ body: "&lt;script&gt;alert(1)&lt;/script&gt;", plain: true, from: "bob" }), // squawkrender escapes a plain body
    ] }))));
    renderAdmin(adminBoot, at);
    expect((await screen.findByText("bold")).tagName).toBe("STRONG");
    expect(screen.getByText("<script>alert(1)</script>")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Full log" })).toHaveAttribute("href", ADMIN + "health/intercom?project=acme");
  });

  it("says when the log is unavailable", async () => {
    tree();
    server.use(http.get("/app/admin/api/projects/acme", () => HttpResponse.json(project({ logConfigured: false }))));
    renderAdmin(adminBoot, at);
    expect(await screen.findByText("The message log is unavailable.")).toBeInTheDocument();
  });
});
