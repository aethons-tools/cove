import { describe, expect, it } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { server } from "../../test/server";
import { adminBoot, renderAdmin } from "../test/render";
import { ADMIN } from "../paths";
import type { SearchResult } from "../api/types";
import { Highlight } from "./highlight";

const result = (r: Partial<SearchResult> = {}): SearchResult => ({ q: "", tooShort: false, total: 0, groups: [], jump: "", ...r });
const base = (answer: (q: string) => SearchResult) => server.use(
  http.get("/app/admin/api/tree", () => HttpResponse.json({ nodes: [] })),
  http.get("/app/admin/api/dashboard", () => new HttpResponse(null, { status: 500 })),
  http.get("/app/admin/api/users", () => HttpResponse.json({ users: [] })),
  http.get("/app/admin/api/users/:id", () => new HttpResponse(null, { status: 404 })),
  http.get("/app/admin/api/search", ({ request }) => HttpResponse.json(answer(new URL(request.url).searchParams.get("q") ?? ""))),
);
const aliceHit = { title: "alice", sub: "acme", path: "config/users/usr_1", exact: true };

describe("search", () => {
  it("/ focuses the title-bar box, but not while typing elsewhere", async () => {
    base(() => result());
    renderAdmin(adminBoot, ADMIN + "nonsense");
    const box = await screen.findByRole("searchbox", { name: "Search" });
    await userEvent.keyboard("/");
    expect(box).toHaveFocus();
    expect(box).toHaveValue("");
    const ta = document.createElement("textarea");
    document.body.appendChild(ta);
    ta.focus();
    await userEvent.keyboard("/");
    expect(ta).toHaveFocus();
    expect(ta).toHaveValue("/");
    ta.remove();
  });

  it("Enter with one exact match jumps there, replacing the history entry", async () => {
    base((q) => result({ q, total: 1, jump: "config/users/usr_1", groups: [{ key: "users", name: "Users", count: 1, more: "", hits: [aliceHit] }] }));
    const { router } = renderAdmin(adminBoot, ADMIN + "nonsense");
    await userEvent.type(await screen.findByRole("searchbox", { name: "Search" }), "alice{Enter}");
    await waitFor(() => expect(router.state.location.pathname).toBe(ADMIN + "config/users/usr_1"));
    expect(router.state.historyAction).toBe("REPLACE");
  });

  it("Enter with several shows the results, dropping go", async () => {
    base((q) => result({ q, total: 2, groups: [
      { key: "users", name: "Users", count: 1, more: "", hits: [{ ...aliceHit, title: "acme" }] },
      { key: "projects", name: "Projects", count: 1, more: "", hits: [{ title: "acme", sub: "", path: "config/projects/acme", exact: true }] },
      { key: "squawks", name: "Squawks", count: 12, more: "health/intercom?q=acme", hits: [{ title: "acme deploy", sub: "", path: "health/intercom?q=acme", exact: false }] },
    ] }));
    const { router } = renderAdmin(adminBoot, ADMIN + "nonsense");
    await userEvent.type(await screen.findByRole("searchbox", { name: "Search" }), "acme{Enter}");
    expect(await screen.findByText("2 match(es) for")).toBeInTheDocument();
    await waitFor(() => expect(router.state.location.search).toBe("?q=acme"));
    expect(screen.getByRole("link", { name: "all 12 in Intercom" })).toHaveAttribute("href", ADMIN + "health/intercom?q=acme");
    const hrefs = screen.getAllByRole("link", { name: /^acme/ }).map((a) => a.getAttribute("href"));
    expect(hrefs).toEqual(expect.arrayContaining([ADMIN + "config/users/usr_1", ADMIN + "config/projects/acme"]));
  });

  it("asks for 2 characters", async () => {
    base((q) => result({ q, tooShort: true }));
    renderAdmin(adminBoot, ADMIN + "search?q=a");
    expect(await screen.findByText("Type at least 2 characters.")).toBeInTheDocument();
  });

  it("highlights the query case-insensitively, as text", () => {
    const { container } = render(<Highlight text="<b>Alice</b> and alice" q="ALI" />);
    expect([...container.querySelectorAll("mark")].map((m) => m.textContent)).toEqual(["Ali", "ali"]);
    expect(container.querySelector("b")).toBeNull();
    expect(container.textContent).toBe("<b>Alice</b> and alice");
  });
});
