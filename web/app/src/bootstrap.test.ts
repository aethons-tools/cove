import { describe, expect, it } from "vitest";
import { http, HttpResponse } from "msw";
import { server } from "./test/server";
import { loadBootstrap } from "./bootstrap";

const boot = { brand: "Acme Jam", user: { id: "usr_1", name: "alice", initial: "A" }, roles: "member", admin: false,
  signOut: true, tabs: [], attachMax: 0, pushKey: "", boards: false };

describe("loadBootstrap", () => {
  it("reads the injected block without a request", async () => {
    const el = document.createElement("script");
    el.id = "jam-bootstrap"; el.type = "application/json"; el.textContent = JSON.stringify(boot);
    document.head.append(el);
    await expect(loadBootstrap()).resolves.toEqual(boot); // MSW errors on any unhandled request
  });

  it("falls back to the API (the dev server's index has none)", async () => {
    server.use(http.get("/app/user/api/bootstrap", () => HttpResponse.json(boot)));
    await expect(loadBootstrap()).resolves.toEqual(boot);
  });
});
