import { describe, expect, it } from "vitest";
import { http, HttpResponse } from "msw";
import { server } from "../test/server";
import { ApiError, apiAt, apiGet, apiPost, interpret, setTransport, shouldRetry, type Raw, type TransportRequest } from "./client";

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

  it("a JSON refusal without error falls back to the status text", async () => {
    server.use(http.get("/app/user/api/rail", () => HttpResponse.json({ code: "x" }, { status: 404, statusText: "Not Found" })));
    await expect(apiGet("rail")).rejects.toMatchObject({ status: 404, code: "x", message: "Not Found" });
  });
});

describe("interpret", () => {
  const raw = (r: Partial<Raw>): Raw => ({ status: 200, contentType: "", redirected: false, ...r });
  it("returns undefined for 204 and the JSON body for 200", () => {
    expect(interpret(raw({ status: 204 }))).toBeUndefined();
    expect(interpret(raw({ contentType: "application/json", json: { a: 1 } }))).toEqual({ a: 1 });
  });
  it("throws the envelope", () => {
    expect(() => interpret(raw({ status: 404, contentType: "application/json", json: { error: "no such", code: "not_found" } })))
      .toThrow(expect.objectContaining({ status: 404, code: "not_found", message: "no such" }));
  });
  it("an envelope without error gives the status text, else empty", () => {
    expect(() => interpret(raw({ status: 404, statusText: "Not Found", contentType: "application/json", json: { code: "x" } })))
      .toThrow(expect.objectContaining({ status: 404, code: "x", message: "Not Found" }));
    expect(() => interpret(raw({ status: 404, contentType: "application/json", json: { code: "x" } })))
      .toThrow(expect.objectContaining({ status: 404, code: "x", message: "" }));
  });
  it.each([
    raw({ status: 401, contentType: "text/html" }),
    raw({ status: 403, contentType: "text/html" }),
    raw({ status: 302 }),
    raw({ status: 0, redirected: true }),
  ])("treats %o as signed out", (r) => {
    expect(() => interpret(r)).toThrow(expect.objectContaining({ code: "signed_out", message: "Your sign-in has expired." }));
  });
  it("treats a 500 as unavailable", () => {
    expect(() => interpret(raw({ status: 500, contentType: "text/plain" })))
      .toThrow(expect.objectContaining({ code: "unavailable", message: "Jam answered 500. Try again shortly." }));
  });
});

describe("setTransport", () => {
  it("routes apiAt calls and restores fetch", async () => {
    const seen: TransportRequest[] = [];
    setTransport(async (r) => { seen.push(r); return { status: 204, contentType: "", redirected: false }; });
    try {
      await apiAt("/app/user/api/").post("messages", { x: 1 });
      expect(seen).toEqual([{ method: "POST", url: "/app/user/api/messages", json: { x: 1 } }]);
    } finally { setTransport(); }
    server.use(http.get("/app/user/api/rail", () => HttpResponse.json({ ok: true })));
    await expect(apiGet("rail")).resolves.toEqual({ ok: true });
    expect(seen).toHaveLength(1);
  });
  it("postForm passes FormData as form", async () => {
    const seen: TransportRequest[] = [];
    setTransport(async (r) => { seen.push(r); return { status: 204, contentType: "", redirected: false }; });
    try {
      const form = new FormData();
      await apiAt("/app/user/api/").postForm("upload", form);
      expect(seen[0]).toEqual({ method: "POST", url: "/app/user/api/upload", form });
    } finally { setTransport(); }
  });
});
