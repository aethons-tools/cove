import { describe, expect, it } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse, delay } from "msw";
import { server } from "../../test/server";
import { adminBoot, renderAdmin } from "../test/render";
import { ADMIN } from "../paths";
import type { Plan, Tracking } from "../api/types";

const page = (t: Partial<Tracking> = {}): Tracking =>
  ({ source: null, rows: [], drifted: 0, statesErr: false, lastCommit: "", lastApplied: "", canGit: true, plan: null, follow: follow(), ...t });
const follow = (f: Partial<Tracking["follow"]> = {}): Tracking["follow"] => ({ state: "in-sync", head: "abc1234", applied: "abc1234", error: "", checked: "2026-10-31 10:00 UTC", ...f });
const source = { label: "https://github.com/acme/jam@main", repo: "https://github.com/acme/jam", repoLink: "https://github.com/acme/jam", dir: "", ref: "main", commit: "abc1234", commitLink: "", applied: "2026-10-28 10:00 UTC" };
const plan = (p: Partial<Plan> = {}): Plan => ({
  source: "https://github.com/acme/jam@main", commit: "abc1234", digest: "d1", forced: false, hasDrift: false, changes: 1, unchanged: 0,
  ops: [{ action: "create", kind: "user", name: "bob", path: "", file: "users/bob.yml", drift: false, unmanaged: false, lines: [{ op: "+", text: "kind: user" }], tooLarge: false, diff: "" }],
  forget: [], unmanaged: [], kitSources: [], refusals: [], serve: { state: "", keys: [] }, ...p,
});
const rows = [{ kind: "role", name: "app/ic", path: "config/projects/app/agents/ic", commit: "abc1234", applied: "x", state: "drifted" }];

const tree = () => server.use(http.get("/app/admin/api/tree", () => HttpResponse.json({ nodes: [] })));
const at = ADMIN + "config/tracking";

describe("Tracking", () => {
  it("without config.github, says how to set it, with no Track or Untrack", async () => {
    tree();
    server.use(http.get("/app/admin/api/tracking", () => HttpResponse.json(page({ follow: follow({ state: "off", head: "", applied: "", checked: "" }) }))));
    renderAdmin(adminBoot, at);
    expect(await screen.findByText(/No config repo is configured/)).toBeInTheDocument();
    expect(screen.getByText("config.github")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Track" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Untrack" })).toBeNull();
  });

  it("shows the followed repo and what the follower last saw", async () => {
    tree();
    server.use(http.get("/app/admin/api/tracking", () => HttpResponse.json(page({ source, follow: follow({ head: "def5678" }) }))));
    renderAdmin(adminBoot, at);
    expect(await screen.findByText("in sync")).toBeInTheDocument();
    expect(screen.getByText("def5678")).toBeInTheDocument();
    expect(screen.getByText(/checked 2026-10-31 10:00 UTC/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Untrack" })).toBeNull();
  });

  it("asks to confirm the first apply", async () => {
    tree();
    server.use(http.get("/app/admin/api/tracking", () => HttpResponse.json(page({ source: { ...source, commit: "", applied: "" }, follow: follow({ state: "awaiting-confirm", applied: "" }) }))));
    renderAdmin(adminBoot, at);
    expect(await screen.findByText("waiting for you")).toBeInTheDocument();
    expect(screen.getByText(/Diff, review the plan, then apply it/)).toBeInTheDocument();
  });

  it("shows a failed check's error", async () => {
    tree();
    server.use(http.get("/app/admin/api/tracking", () => HttpResponse.json(page({ source, follow: follow({ state: "failed", error: "config plan has refusals: kit x" }) }))));
    renderAdmin(adminBoot, at);
    expect(await screen.findByText("failed")).toBeInTheDocument();
    expect(screen.getByText(/config plan has refusals: kit x/)).toBeInTheDocument();
  });

  it("says when git actions are off", async () => {
    tree();
    server.use(http.get("/app/admin/api/tracking", () => HttpResponse.json(page({ source, canGit: false }))));
    renderAdmin(adminBoot, at);
    expect(await screen.findByText(/Git actions are not available on this serve/)).toBeInTheDocument();
  });

  it("diffs once, with the button busy while planning", async () => {
    tree();
    let diffs = 0;
    server.use(
      http.get("/app/admin/api/tracking", () => HttpResponse.json(page({ source, rows }))),
      http.post("/app/admin/api/tracking/diff", async () => { diffs++; await delay(150); return HttpResponse.json(plan()); }),
    );
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    const diff = await screen.findByRole("button", { name: "Diff" });
    await user.click(diff);
    await user.click(diff);
    expect(diff).toBeDisabled();
    expect(await screen.findByText("users/bob.yml")).toBeInTheDocument();
    expect(diffs).toBe(1);
  });

  it("keeps the plan when an apply is stale", async () => {
    tree();
    server.use(
      http.get("/app/admin/api/tracking", () => HttpResponse.json(page({ source }))),
      http.post("/app/admin/api/tracking/diff", () => HttpResponse.json(plan())),
      http.post("/app/admin/api/tracking/apply", () => HttpResponse.json({ error: "The Jam or the config repo changed since this plan was shown; nothing was applied. Run Diff again and review the new plan.", code: "conflict" }, { status: 409 })),
    );
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    await user.click(await screen.findByRole("button", { name: "Diff" }));
    await user.click(await screen.findByRole("button", { name: "Apply this plan" }));
    expect(await screen.findByText("Apply 1 change(s) to this Jam?")).toBeInTheDocument();
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Apply this plan" }));
    expect(await screen.findByText(/Run Diff again/)).toBeInTheDocument();
    expect(screen.getByText("users/bob.yml")).toBeInTheDocument();
  });

  it("releases a managed object after confirming", async () => {
    tree();
    server.use(
      http.get("/app/admin/api/tracking", () => HttpResponse.json(page({ rows, follow: follow({ state: "off" }) }))),
      http.delete("/app/admin/api/tracking/managed/role/app/ic", () => HttpResponse.json({ tracking: page({ follow: follow({ state: "off" }) }), message: "released role app/ic; it stays in Jam, unmanaged" })),
    );
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    expect(await screen.findByRole("link", { name: "app/ic" })).toHaveAttribute("href", ADMIN + "config/projects/app/agents/ic");
    await user.click(screen.getByRole("button", { name: "Release" }));
    expect(await screen.findByText("Stop managing role app/ic from the config repo? It stays in Jam, unmanaged.")).toBeInTheDocument();
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Release" }));
    expect(await screen.findByText("released role app/ic; it stays in Jam, unmanaged")).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByRole("link", { name: "app/ic" })).toBeNull());
  });

  it("re-plans with force", async () => {
    tree();
    let body: unknown;
    server.use(
      http.get("/app/admin/api/tracking", () => HttpResponse.json(page({ source }))),
      http.post("/app/admin/api/tracking/diff", async ({ request }) => { body = await request.json(); return HttpResponse.json(plan({ hasDrift: true, forced: (body as { force: boolean }).force })); }),
    );
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    await user.click(await screen.findByRole("button", { name: "Diff" }));
    await user.click(await screen.findByRole("checkbox", { name: /Force/ }));
    await waitFor(() => expect(body).toEqual({ force: true }));
  });

  it("offers no Release while following a repo", async () => {
    tree();
    server.use(http.get("/app/admin/api/tracking", () => HttpResponse.json(page({ source, rows }))));
    renderAdmin(adminBoot, at);
    expect(await screen.findByRole("link", { name: "app/ic" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Release" })).toBeNull();
  });

  it("holds Apply while a re-plan runs", async () => {
    tree();
    let calls = 0;
    server.use(
      http.get("/app/admin/api/tracking", () => HttpResponse.json(page({ source, rows }))),
      http.post("/app/admin/api/tracking/diff", async () => { calls++; if (calls > 1) await delay(300); return HttpResponse.json(plan({ hasDrift: true })); }),
    );
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    await user.click(await screen.findByRole("button", { name: "Diff" }));
    await user.click(await screen.findByRole("checkbox", { name: /Force/ }));
    expect(screen.getByRole("button", { name: "Apply this plan" })).toBeDisabled();
    await waitFor(() => expect(screen.getByRole("button", { name: "Apply this plan" })).toBeEnabled());
  });

  it("offers no Apply for a refused plan", async () => {
    tree();
    server.use(
      http.get("/app/admin/api/tracking", () => HttpResponse.json(page({ source }))),
      http.post("/app/admin/api/tracking/diff", () => HttpResponse.json(plan({ refusals: ["kit x is referenced"] }))),
    );
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    await user.click(await screen.findByRole("button", { name: "Diff" }));
    expect(await screen.findByText("kit x is referenced")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Apply this plan" })).toBeNull();
  });
});
