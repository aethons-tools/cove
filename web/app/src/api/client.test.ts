import { describe, expect, it } from "vitest";
import { http, HttpResponse } from "msw";
import { server } from "../test/server";
import { ApiError, apiGet, apiPost, shouldRetry } from "./client";

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

  it("treats the gate's login redirect as signed out", async () => {
    server.use(http.get("/app/user/api/rail", () => new HttpResponse(null, { status: 302, headers: { Location: "/app/auth/login?return_to=%2Fapp%2Fuser%2Fapi%2Frail" } })));
    await expect(apiGet("rail")).rejects.toMatchObject({ code: "signed_out" });
  });

  it("treats a non-JSON 502 as unavailable, not signed out", async () => {
    server.use(http.get("/app/user/api/rail", () => new HttpResponse("bad gateway", { status: 502, headers: { "Content-Type": "text/plain" } })));
    const err = await apiGet("rail").catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).code).toBe("unavailable");
  });
});

describe("shouldRetry", () => {
  it("never retries an expired sign-in or a refusal", () => {
    expect(shouldRetry(0, new ApiError(302, "signed_out", "x"))).toBe(false);
    expect(shouldRetry(0, new ApiError(403, "forbidden", "x"))).toBe(false);
    expect(shouldRetry(0, new ApiError(404, "not_found", "x"))).toBe(false);
  });
  it("retries an outage a few times", () => {
    expect(shouldRetry(0, new ApiError(502, "unavailable", "x"))).toBe(true);
    expect(shouldRetry(0, new ApiError(0, "unavailable", "x"))).toBe(true);
    expect(shouldRetry(3, new ApiError(502, "unavailable", "x"))).toBe(false);
  });
});
describe("apiPost", () => {
  it("sends JSON and resolves undefined on 204", async () => {
    let got: { ct: string | null; body: unknown } | undefined;
    server.use(http.post("/app/user/api/conversations/chn_a/read", async ({ request }) => {
      got = { ct: request.headers.get("Content-Type"), body: await request.json() };
      return new HttpResponse(null, { status: 204 });
    }));
    await expect(apiPost("conversations/chn_a/read", { seq: 7 })).resolves.toBeUndefined();
    expect(got).toEqual({ ct: "application/json", body: { seq: 7 } });
  });

  it("throws the envelope on refusal", async () => {
    server.use(http.post("/app/user/api/conversations/chn_a/leave", () => HttpResponse.json({ error: "this channel's members are fixed", code: "conflict" }, { status: 409 })));
    await expect(apiPost("conversations/chn_a/leave", {})).rejects.toMatchObject({ status: 409, code: "conflict" });
  });
});
