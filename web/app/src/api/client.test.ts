import { describe, expect, it } from "vitest";
import { http, HttpResponse } from "msw";
import { server } from "../test/server";
import { ApiError, apiGet } from "./client";

describe("apiGet", () => {
  it("returns the JSON body", async () => {
    server.use(http.get("/app/user/api/rail", () => HttpResponse.json({ groups: [], unreadTotal: 0 })));
    await expect(apiGet("rail")).resolves.toEqual({ groups: [], unreadTotal: 0 });
  });

  it("throws the API's error envelope", async () => {
    server.use(http.get("/app/user/api/rail", () => HttpResponse.json({ error: "no", code: "forbidden" }, { status: 403 })));
    await expect(apiGet("rail")).rejects.toMatchObject({ status: 403, code: "forbidden", message: "no" });
  });

  it("treats an HTML 403 from the gate as signed out", async () => {
    server.use(http.get("/app/user/api/rail", () => new HttpResponse("<html>sign in</html>", { status: 403, headers: { "Content-Type": "text/html" } })));
    await expect(apiGet("rail")).rejects.toMatchObject({ code: "signed_out" });
  });

  it("treats a non-JSON 502 as unavailable, not signed out", async () => {
    server.use(http.get("/app/user/api/rail", () => new HttpResponse("bad gateway", { status: 502, headers: { "Content-Type": "text/plain" } })));
    const err = await apiGet("rail").catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).code).toBe("unavailable");
  });
});
