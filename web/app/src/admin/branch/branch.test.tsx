import { afterEach, describe, expect, it } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { server } from "../../test/server";
import { adminBoot, renderAdmin } from "../test/render";
import { ADMIN } from "../paths";
import type { ConfigBranches } from "../api/types";
import { setBranch } from "./branch";

const list = (over: Partial<ConfigBranches> = {}): ConfigBranches => ({
  followed: "main", defaultName: "jam/local", canPR: true, repoLink: "https://github.com/acme/jam", treeLink: "https://github.com/acme/jam/tree/", prNoun: "pull request",
  branches: [
    { name: "main", head: "a".repeat(40), short: "aaaaaaa", followed: true },
    { name: "jam/alice", head: "b".repeat(40), short: "bbbbbbb", followed: false },
  ], ...over,
});

// at is a config page with an empty tree and projects list, recording the
// branch header each tree request carried.
function serve(seen: (string | null)[] = []) {
  server.use(
    http.get("/app/admin/api/config/branches", () => HttpResponse.json(list())),
    http.get("/app/admin/api/config/branches/pr", () => HttpResponse.json({ url: "" })),
    http.get("/app/admin/api/tree", ({ request }) => {
      seen.push(request.headers.get("X-Jam-Config-Branch"));
      return HttpResponse.json({ nodes: [] }, request.headers.get("X-Jam-Config-Branch") ? { headers: { "X-Jam-Config-Head": "c".repeat(40) } } : undefined);
    }),
    http.get("/app/admin/api/projects", () => HttpResponse.json({ projects: [] })),
  );
  return seen;
}
const at = ADMIN + "config/projects";

afterEach(() => setBranch(""));

describe("the branch bar", () => {
  it("on the followed branch: read-only, and no header", async () => {
    const seen = serve();
    renderAdmin(adminBoot, at);
    expect(await screen.findByText(/what's running \(read-only\)/)).toBeInTheDocument();
    await waitFor(() => expect(seen.length).toBeGreaterThan(0));
    expect(seen.every((h) => h === null)).toBe(true);
  });

  it("Create branch opens it, and config requests then carry the header", async () => {
    const seen = serve();
    let sent: unknown;
    server.use(http.post("/app/admin/api/config/branches", async ({ request }) => {
      sent = await request.json();
      return HttpResponse.json({ branch: { name: "jam/local", head: "a".repeat(40), short: "aaaaaaa", followed: false }, message: "created branch jam/local" });
    }));
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    await user.click(await screen.findByRole("button", { name: "Create branch" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByLabelText("Branch name")).toHaveValue("jam/local");
    await user.click(within(dialog).getByRole("button", { name: "Create" }));
    expect(sent).toEqual({ name: "jam/local" });
    expect(await screen.findByText(/Editing/)).toBeInTheDocument();
    await waitFor(() => expect(seen.at(-1)).toBe("jam/local"));
  });

  it("a refused merge offers a pull request", async () => {
    serve();
    setBranch("jam/alice");
    let prs = 0;
    server.use(
      http.post("/app/admin/api/config/branches/merge", () => HttpResponse.json({ error: "Merge refused: merge conflict in: users/bob.yml. Open a pull request instead.", code: "conflict" }, { status: 409 })),
      http.post("/app/admin/api/config/branches/pr", () => { prs++; return HttpResponse.json({ url: "https://github.com/acme/jam/pull/9" }); }),
    );
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    await user.click(await screen.findByRole("button", { name: "Merge" }));
    await user.click(within(await screen.findByRole("dialog")).getByRole("button", { name: "Merge" }));
    expect(await screen.findByText(/merge conflict in: users\/bob.yml/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Open a pull request" }));
    await waitFor(() => expect(prs).toBe(1));
    expect(await screen.findByRole("link", { name: /pull request/i })).toHaveAttribute("href", "https://github.com/acme/jam/pull/9");
  });

  it("Merge sends the head this tab last read", async () => {
    serve();
    setBranch("jam/alice");
    let sent: unknown;
    server.use(http.post("/app/admin/api/config/branches/merge", async ({ request }) => {
      sent = await request.json();
      return HttpResponse.json({ merge: { head: "ccccccc", fastForward: true, noOp: false }, message: "merged" });
    }));
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    await user.click(await screen.findByRole("button", { name: "Merge" }));
    await user.click(within(await screen.findByRole("dialog")).getByRole("button", { name: "Merge" }));
    await waitFor(() => expect(sent).toEqual({ branch: "jam/alice", head: "c".repeat(40) }));
  });

  it("the branch is per tab (sessionStorage)", () => {
    setBranch("jam/alice");
    expect(sessionStorage.getItem("jam.admin.configBranch")).toBe("jam/alice");
    expect(localStorage.getItem("jam.admin.configBranch")).toBeNull();
  });

  it("says why when the branches can't be listed", async () => {
    serve();
    server.use(http.get("/app/admin/api/config/branches", () => HttpResponse.json({ error: "config repo: fetch https://github.com/acme/jam: auth failed (check config.github.cred)", code: "internal" }, { status: 502 })));
    renderAdmin(adminBoot, at);
    expect(await screen.findByText(/auth failed/)).toBeInTheDocument();
  });

  it("BranchBar shows Read-only without a repo", async () => {
    serve();
    server.use(http.get("/app/admin/api/config/branches", () => HttpResponse.json({ error: "no such API route", code: "not_found" }, { status: 404 })));
    renderAdmin(adminBoot, at);
    const alert = await screen.findByRole("alert");
    expect(within(alert).getByText("Read-only")).toBeInTheDocument();
    expect(within(alert).getByText("This Jam has no config repo. Set config.github, config.gitlab or config.git in the serve config to edit its config on branches.")).toBeInTheDocument();
    expect(screen.queryByText(/can't be listed/)).toBeNull();
    expect(screen.queryByRole("button", { name: "Create branch" })).toBeNull();
  });

  it("on GitLab, links the branch's page and offers a merge request", async () => {
    serve();
    server.use(http.get("/app/admin/api/config/branches", () => HttpResponse.json(list({ repoLink: "https://gitlab.com/a/b", treeLink: "https://gitlab.com/a/b/-/tree/", prNoun: "merge request" }))));
    setBranch("jam/alice");
    renderAdmin(adminBoot, at);
    expect(await screen.findByRole("link", { name: "View the branch" })).toHaveAttribute("href", "https://gitlab.com/a/b/-/tree/jam/alice");
    expect(screen.getByRole("button", { name: "MR" })).toBeInTheDocument();
  });

  it("Back to main leaves the branch", async () => {
    const seen = serve();
    setBranch("jam/alice");
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    await user.click(await screen.findByRole("button", { name: "Back to main" }));
    expect(await screen.findByText(/what's running \(read-only\)/)).toBeInTheDocument();
    await waitFor(() => expect(seen.at(-1)).toBe(null));
  });
});
