import { afterEach, describe, expect, it } from "vitest";
import { setTransport, type TransportRequest } from "../api/client";
import { loadElements, saveElements } from "./api";

afterEach(() => setTransport());
const json = (status: number, body: unknown) => ({ status, contentType: "application/json", redirected: false, json: body });

describe("boards api", () => {
  it("loadElements GETs elements since a seq", async () => {
    const seen: TransportRequest[] = [];
    setTransport(async (r) => { seen.push(r); return json(200, { seq: 3, elements: [{ id: "a" }] }); });
    expect(await loadElements("brd_1", 0)).toEqual({ seq: 3, elements: [{ id: "a" }] });
    expect(seen[0]).toMatchObject({ method: "GET", url: "/app/user/boards/brd_1/elements?since=0" });
  });

  it("saveElements POSTs the batch as elements", async () => {
    const seen: TransportRequest[] = [];
    setTransport(async (r) => { seen.push(r); return json(200, { lost: [] }); });
    const els = [{ id: "a", version: 1 }, { id: "b", version: 2 }];
    await saveElements("brd_1", els.map((e) => ({ ...e, json: JSON.stringify(e) })));
    expect(seen[0]).toMatchObject({ method: "POST", url: "/app/user/boards/brd_1/elements" });
    expect((seen[0]!.json as { elements: unknown[] }).elements).toEqual(els);
  });

  it("a refusal names the status", async () => {
    setTransport(async (r) => json(r.method === "POST" ? 409 : 404, { error: "no" }));
    await expect(saveElements("brd_1", [])).rejects.toThrow("save failed (409)");
    await expect(loadElements("brd_1", 0)).rejects.toThrow("load failed (404)");
  });
});
