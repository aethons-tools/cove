import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { server } from "../../test/server";
import { adminBoot, renderAdmin } from "../test/render";
import { ADMIN } from "../paths";
import { kit, kitView } from "./fixtures";
import type { KitDetail } from "../api/types";
import { setBranch } from "../branch/branch";

const at = ADMIN + "config/kits/web";
const api = "/app/admin/api/kits/web";
const base = () => server.use(http.get("/app/admin/api/tree", () => HttpResponse.json({ nodes: [] })));
const serve = (pick: (q: URLSearchParams) => KitDetail) => server.use(http.get(api, ({ request }) => HttpResponse.json(pick(new URL(request.url).searchParams))));
const ok = (k: KitDetail, message: string) => HttpResponse.json({ kit: k, message });

// The config edits show on a branch.
beforeEach(() => setBranch("jam/test"));
afterEach(() => setBranch(""));

describe("a kit's page outside a branch", () => {
  it("keeps Sync and Untrack, hides the config edits, and shows them on a branch", async () => {
    setBranch("");
    base();
    serve(() => kit({ source: { repo: "https://github.com/acme/kits", repoLink: "", dir: "", ref: "main", commit: "abc", commitLink: "" } }));
    renderAdmin(adminBoot, at);
    expect(await screen.findByRole("button", { name: "Sync" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Untrack" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Delete" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Pin" })).toBeNull();
    expect(screen.queryByText("Push new version")).toBeNull();
    expect(screen.queryByRole("heading", { name: "Draft" })).toBeNull();
    act(() => setBranch("jam/me"));
    expect(await screen.findByText("Push new version")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Pin" })).toBeInTheDocument();
    // Back on the followed branch, the edits go at once.
    act(() => setBranch(""));
    expect(screen.queryByText("Push new version")).toBeNull();
    expect(screen.getByRole("button", { name: "Sync" })).toBeInTheDocument();
  });
});

describe("a kit's page", () => {
  it("shows the rail with its links, and pins after confirming", async () => {
    base();
    serve(() => kit());
    server.use(http.post(api + "/pin", () => ok(kit({ current: 1, versions: kit().versions.map((v) => ({ ...v, current: v.n === 1 })) }), "web current is now v1")));
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    const rail = await screen.findByRole("navigation", { name: "Versions" });
    expect(within(rail).getByRole("link", { name: "v1" })).toHaveAttribute("href", at + "?v=1");
    expect(within(rail).getByRole("link", { name: "diff v1" })).toHaveAttribute("href", at + "?v=2&diff=1");
    expect(within(rail).getByRole("link", { name: "diff current" })).toHaveAttribute("href", at + "?v=1&diff=2");
    await user.click(within(rail).getByRole("button", { name: "Pin" }));
    expect(await screen.findByText("Make v1 the current version of web? Roles bound to it raise v1 from now on.")).toBeInTheDocument();
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Pin" }));
    expect(await screen.findByText("web current is now v1")).toBeInTheDocument();
  });

  it("shows a diff with its files, and a changed file's diff", async () => {
    base();
    serve((q) => kit({
      view: kitView({ n: 2 }),
      diff: { from: 1, to: 2, lines: [{ op: "-", text: "prompt: one" }, { op: "+", text: "prompt: two" }], digestChanged: true, tooLarge: false },
      filesDiff: { from: 1, to: 2, rows: [{ change: "modified", path: "a b.txt", label: "a b.txt", mode: "0644", size: "2 B → 5 B" }], more: 0, unchanged: 1 },
      file: q.get("file") ? { path: "a b.txt", diff: true, from: 1, to: 2, lines: [{ op: "+", text: "spaced" }], tooLarge: false, content: "", note: "", missing: false, masked: false, hidden: false } : null,
    }));
    const user = userEvent.setup();
    renderAdmin(adminBoot, at + "?v=2&diff=1");
    expect(await screen.findByText("Diff v1 → v2")).toBeInTheDocument();
    expect(screen.getByText("build digest changed — a new image is built")).toBeInTheDocument();
    expect(screen.getByText("+ prompt: two")).toBeInTheDocument();
    expect(screen.getByText("1 unchanged")).toBeInTheDocument();
    await user.click(screen.getAllByRole("link", { name: "a b.txt" })[0]!); // the changed-files table comes first
    expect(await screen.findByText("+ spaced")).toBeInTheDocument();
  });

  it("opens a file whose name has a space", async () => {
    base();
    let asked = "";
    server.use(http.get(api, ({ request }) => {
      asked = new URL(request.url).searchParams.get("file") ?? "";
      return HttpResponse.json(kit(asked ? { file: { path: asked, diff: false, from: 0, to: 0, lines: [], tooLarge: false, content: "spaced\n", note: "", missing: false, masked: false, hidden: false } } : {}));
    }));
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    await user.click(await screen.findByRole("link", { name: "a b.txt" }));
    await waitFor(() => expect(asked).toBe("a b.txt"));
    expect(await screen.findByText("spaced")).toBeInTheDocument();
  });

  it("shows an invalid version and prefills its push box", async () => {
    base();
    serve(() => kit({ view: kitView({ parseErr: "bogus: unknown field", rawShown: "kind: studio\nbogus: 1\n", masked: true, pushText: "kind: studio\nbogus: 1\n", yaml: "" }) }));
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    expect(await screen.findByText(/Stored config is not a valid studio kit: bogus: unknown field/)).toBeInTheDocument();
    expect(screen.getByText(/Raises from this version fail closed/)).toBeInTheDocument();
    await user.click(screen.getByText("Push new version", { selector: "summary" }));
    expect(screen.getByLabelText(/^Studio kit YAML/)).toHaveValue("kind: studio\nbogus: 1\n");
  });

  it("pushes a new version", async () => {
    base();
    serve(() => kit());
    server.use(http.post(api + "/versions", () => ok(kit({ current: 3 }), "pushed web v3")));
    const user = userEvent.setup();
    renderAdmin(adminBoot, at);
    await user.click(await screen.findByText("Push new version", { selector: "summary" }));
    await user.click(screen.getByRole("button", { name: "Push version" }));
    expect(await screen.findByText("pushed web v3")).toBeInTheDocument();
  });

  it("tracks a source, and offers no git actions without git", async () => {
    base();
    let sent: unknown;
    serve(() => kit());
    server.use(http.post(api + "/source", async ({ request }) => { sent = await request.json(); return ok(kit({ source: { repo: "https://github.com/acme/kits", repoLink: "", dir: "", ref: "main", commit: "abc", commitLink: "" } }), "tracking https://github.com/acme/kits@main: web is now v3"); }));
    const user = userEvent.setup();
    const { unmount } = renderAdmin(adminBoot, at);
    await user.click(await screen.findByText("Track…", { selector: "summary" }));
    await user.type(screen.getByLabelText(/^Source/), "https://github.com/acme/kits@main");
    await user.click(screen.getByRole("button", { name: "Track" }));
    await waitFor(() => expect(sent).toEqual({ source: "https://github.com/acme/kits@main" }));
    expect(await screen.findByRole("button", { name: "Sync" })).toBeInTheDocument();
    unmount();
    serve(() => kit({ canSource: false }));
    renderAdmin(adminBoot, at);
    await screen.findByRole("heading", { name: "web" });
    expect(screen.queryByText("Track…")).toBeNull();
  });

  it("holds Delete for the default kit", async () => {
    base();
    serve(() => kit({ isDefault: true }));
    renderAdmin(adminBoot, at);
    expect(await screen.findByRole("button", { name: "Delete" })).toHaveAttribute("title", "The built-in default kit is re-seeded at serve start");
  });

  it("says when it doesn't exist", async () => {
    base();
    server.use(http.get("/app/admin/api/kits/gone", () => HttpResponse.json({ error: "x", code: "not_found" }, { status: 404 })));
    renderAdmin(adminBoot, ADMIN + "config/kits/gone");
    expect(await screen.findByText("Kit not found")).toBeInTheDocument();
  });
});
